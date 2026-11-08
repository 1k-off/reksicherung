package gobackup

import (
	"bytes"
	"log"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	dbUserRef     = "openbao://secret/reksicherung/test-db#username"
	dbPasswordRef = "openbao://secret/reksicherung/test-db#password"
	s3AccessRef   = "openbao://secret/reksicherung/object-store#access-key"
	s3SecretRef   = "openbao://secret/reksicherung/object-store#secret-key"
	healthRef     = "openbao://secret/reksicherung/healthchecks#url"
)

func TestDecodeRejectsUnknownAndPlaintextCredentialFields(t *testing.T) {
	const sentinel = "PLAINTEXT_SECRET_SENTINEL"
	input := `project:
  name: test
storage:
  type: s3
  bucket: backups
  secret_access_key: ` + sentinel + `
schedules:
  cron: "0 5 * * *"
  healthcheck_url_ref: ` + healthRef + `
archive:
  includes: [/var/lib/app]
`
	_, err := Decode(strings.NewReader(input))
	if err == nil {
		t.Fatal("Decode() unexpectedly accepted a plaintext credential field")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatal("Decode() error disclosed the plaintext credential")
	}
}

func TestDecodeRequiresSingleStrictYAMLDocument(t *testing.T) {
	for name, input := range map[string]string{
		"empty":     "",
		"unknown":   "unsupported: true\n",
		"documents": "project: {name: demo}\n---\nproject: {name: other}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(strings.NewReader(input)); err == nil {
				t.Fatal("Decode() unexpectedly succeeded")
			}
		})
	}
}

func TestGenerateSimpleDefaultsS3AndMSSQLMapping(t *testing.T) {
	input, err := Decode(strings.NewReader(`project:
  name: my-app
storage:
  type: minio
  bucket: backups-general
  endpoint: https://objects.example.test
  region: fsn1
  path_prefix: my-app
  access_key_id_ref: ` + s3AccessRef + `
  secret_access_key_ref: ` + s3SecretRef + `
schedules:
  cron: "0 5 * * *"
  healthcheck_url_ref: ` + healthRef + `
databases:
  - name: main
    type: MSSQL
    host: sqlserver
    port: 1433
    database: appdb
    username_ref: ` + dbUserRef + `
    password_ref: ` + dbPasswordRef + `
    ignore_schema_check: true
`))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	result, err := Generate(input)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	doc := decodeOutput(t, result.YAML)
	web := mapAt(t, doc, "web")
	if web["host"] != "127.0.0.1" || integerAt(t, web, "port") != 2703 {
		t.Fatalf("web defaults = %#v", web)
	}
	models := mapAt(t, doc, "models")
	model := mapAt(t, models, "my_app_default")
	if got := mapAt(t, model, "schedule")["cron"]; got != "0 5 * * *" {
		t.Errorf("schedule.cron = %#v", got)
	}
	storage := mapAt(t, mapAt(t, model, "storages"), "primary")
	assertEqual(t, storage, "type", "minio")
	assertEqual(t, storage, "bucket", "backups-general")
	assertEqual(t, storage, "region", "fsn1")
	assertEqual(t, storage, "endpoint", "https://objects.example.test")
	assertEqual(t, storage, "path", "my-app")
	assertEqual(t, storage, "keep", 14)
	assertEqual(t, storage, "access_key_id", "$REKSICHERUNG_STORAGE_ACCESS_KEY_ID")
	assertEqual(t, storage, "secret_access_key", "$REKSICHERUNG_STORAGE_SECRET_ACCESS_KEY")

	db := mapAt(t, mapAt(t, model, "databases"), "main")
	assertEqual(t, db, "type", "mssql")
	assertEqual(t, db, "port", 1433)
	assertEqual(t, db, "trustServerCertificate", true)
	assertEqual(t, db, "args", "/p:VerifyExtraction=false")
	assertEqual(t, db, "username", "$REKSICHERUNG_DATABASES_MAIN_USERNAME")
	assertEqual(t, db, "password", "$REKSICHERUNG_DATABASES_MAIN_PASSWORD")

	notifier := mapAt(t, mapAt(t, model, "notifiers"), "default")
	assertEqual(t, notifier, "type", "healthchecks")
	assertEqual(t, notifier, "url", "$REKSICHERUNG_SCHEDULES_HEALTHCHECK_URL")
	assertEqual(t, notifier, "on_success", true)
	assertEqual(t, notifier, "on_failure", true)
	assertBindings(t, result.Bindings, map[string]string{
		"REKSICHERUNG_DATABASES_MAIN_PASSWORD":   dbPasswordRef,
		"REKSICHERUNG_DATABASES_MAIN_USERNAME":   dbUserRef,
		"REKSICHERUNG_SCHEDULES_HEALTHCHECK_URL": healthRef,
		"REKSICHERUNG_STORAGE_ACCESS_KEY_ID":     s3AccessRef,
		"REKSICHERUNG_STORAGE_SECRET_ACCESS_KEY": s3SecretRef,
	})
}

func TestGenerateEnhancedArchiveEncryptionScriptsAndNotifiers(t *testing.T) {
	input := Input{
		Project: Project{Name: "service-name"},
		Mode:    ModeEnhanced,
		Storage: Storage{
			Type:       "s3",
			Bucket:     "backup-bucket",
			PathPrefix: "project",
		},
		Schedules: Schedules{
			Daily:   &Schedule{Cron: "0 1 * * *", HealthcheckURLRef: healthRef},
			Weekly:  &Schedule{Cron: "0 2 * * 0", HealthcheckURLRef: healthRef},
			Monthly: &Schedule{Cron: "0 3 1 * *", HealthcheckURLRef: healthRef},
		},
		Archive: &Archive{
			Includes: []string{"/var/lib/app", "/etc/app.yml"},
			Excludes: []string{"/var/lib/app/tmp"},
		},
		Encryption: Encryption{Enabled: true, PasswordRef: "openbao://secret/reksicherung/encryption#password"},
		Notifiers:  Notifiers{OnSuccess: boolPointer(false)},
		Scripts:    Scripts{Before: "  stop-app  ", After: " start-app\n"},
	}
	if err := input.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	result, err := Generate(input)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	doc := decodeOutput(t, result.YAML)
	models := mapAt(t, doc, "models")
	for tier, wantKeep := range map[string]int{"daily": 7, "weekly": 4, "monthly": 12} {
		model := mapAt(t, models, "service_name_"+tier)
		storageName := "s3_" + tier
		storage := mapAt(t, mapAt(t, model, "storages"), storageName)
		assertEqual(t, model, "default_storage", storageName)
		assertEqual(t, storage, "path", "project/"+tier)
		assertEqual(t, storage, "keep", wantKeep)
		archive := mapAt(t, model, "archive")
		assertEqual(t, archive, "includes", []any{"/var/lib/app", "/etc/app.yml"})
		assertEqual(t, archive, "excludes", []any{"/var/lib/app/tmp"})
		assertEqual(t, mapAt(t, model, "compress_with"), "type", "tgz")
		assertEqual(t, model, "before_script", "stop-app")
		assertEqual(t, model, "after_script", "start-app")
		encrypt := mapAt(t, model, "encrypt_with")
		assertEqual(t, encrypt, "type", "openssl")
		assertEqual(t, encrypt, "password", "$REKSICHERUNG_ENCRYPTION_PASSWORD")
		assertEqual(t, encrypt, "salt", true)
		assertEqual(t, encrypt, "base64", false)
		notify := mapAt(t, mapAt(t, model, "notifiers"), tier)
		assertEqual(t, notify, "url", "$REKSICHERUNG_SCHEDULES_"+strings.ToUpper(tier)+"_HEALTHCHECK_URL")
		assertEqual(t, notify, "on_success", false)
		assertEqual(t, notify, "on_failure", true)
	}
}

func TestGenerateSFTPAndCredentialBindings(t *testing.T) {
	input := Input{
		Project: Project{Name: "sftp-project"},
		Storage: Storage{
			Type:        "sftp",
			Host:        "backup.example.test",
			Port:        2222,
			PathPrefix:  "archives",
			UsernameRef: "openbao://secret/reksicherung/sftp#username",
			PasswordRef: "openbao://secret/reksicherung/sftp#password",
		},
		Schedules: Schedules{Cron: "0 0 * * *", HealthcheckURLRef: healthRef},
		Archive:   &Archive{Includes: []string{"/var/lib/app"}, CompressType: "tgz"},
	}
	result, err := Generate(input)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	model := mapAt(t, mapAt(t, decodeOutput(t, result.YAML), "models"), "sftp_project_default")
	storage := mapAt(t, mapAt(t, model, "storages"), "primary")
	assertEqual(t, storage, "type", "sftp")
	assertEqual(t, storage, "host", "backup.example.test")
	assertEqual(t, storage, "port", 2222)
	assertEqual(t, storage, "username", "$REKSICHERUNG_STORAGE_USERNAME")
	assertEqual(t, storage, "password", "$REKSICHERUNG_STORAGE_PASSWORD")
	assertBindings(t, result.Bindings, map[string]string{
		"REKSICHERUNG_SCHEDULES_HEALTHCHECK_URL": healthRef,
		"REKSICHERUNG_STORAGE_USERNAME":          "openbao://secret/reksicherung/sftp#username",
		"REKSICHERUNG_STORAGE_PASSWORD":          "openbao://secret/reksicherung/sftp#password",
	})
}

func TestGenerateIsDeterministicAndYAMLParses(t *testing.T) {
	input := simpleInput()
	first, err := Generate(input)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	second, err := Generate(input)
	if err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}
	if !bytes.Equal(first.YAML, second.YAML) {
		t.Fatal("Generate() returned different YAML for identical input")
	}
	if !reflect.DeepEqual(first.Bindings, second.Bindings) {
		t.Fatal("Generate() returned different bindings for identical input")
	}
	_ = decodeOutput(t, first.YAML)
}

func TestValidationRejectsUnsafeNamesPathsAndReferences(t *testing.T) {
	tests := map[string]func(*Input){
		"project traversal":   func(in *Input) { in.Project.Name = "../bad" },
		"storage traversal":   func(in *Input) { in.Storage.PathPrefix = "safe/../escape" },
		"archive traversal":   func(in *Input) { in.Archive = &Archive{Includes: []string{"/var/../etc"}} },
		"invalid secret ref":  func(in *Input) { in.Storage.AccessKeyIDRef = "not-a-reference" },
		"empty schedule keep": func(in *Input) { in.Schedules.Keep = -1 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := simpleInput()
			mutate(&input)
			if err := input.Validate(); err == nil {
				t.Fatal("Validate() unexpectedly succeeded")
			}
		})
	}
}

func TestStrictSecretHandlingDoesNotLeakSentinel(t *testing.T) {
	const sentinel = "DO_NOT_LEAK_THIS_SECRET"
	inputYAML := `project:
  name: secure-project
storage:
  type: s3
  bucket: backups
  secret_access_key: ` + sentinel + `
schedules:
  cron: "0 5 * * *"
  healthcheck_url_ref: ` + healthRef + `
archive:
  includes: [/var/lib/app]
`
	var logOutput bytes.Buffer
	originalOutput := log.Writer()
	log.SetOutput(&logOutput)
	defer log.SetOutput(originalOutput)

	_, decodeErr := Decode(strings.NewReader(inputYAML))
	validResult, generateErr := Generate(simpleInput())
	if decodeErr == nil || generateErr != nil {
		t.Fatalf("Decode() err = %v; Generate() err = %v", decodeErr, generateErr)
	}
	combined := decodeErr.Error() + string(validResult.YAML) + logOutput.String()
	for _, binding := range validResult.Bindings {
		combined += binding.EnvironmentVariable + binding.SecretReference
	}
	if strings.Contains(combined, sentinel) {
		t.Fatal("plaintext credential sentinel was disclosed by error, YAML, binding, or log output")
	}
}

func TestDecodeInvalidSecretReferenceDoesNotEchoInput(t *testing.T) {
	const sentinel = "SECRET_SENTINEL_REFERENCE"
	input := simpleInput()
	input.Storage.AccessKeyIDRef = sentinel
	err := input.Validate()
	if err == nil {
		t.Fatal("Validate() accepted an invalid secret reference")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatal("Validate() error disclosed the invalid reference value")
	}
}

func simpleInput() Input {
	return Input{
		Project: Project{Name: "simple-project"},
		Storage: Storage{Type: "s3", Bucket: "backups"},
		Schedules: Schedules{
			Cron:              "0 5 * * *",
			HealthcheckURLRef: healthRef,
		},
		Archive: &Archive{Includes: []string{"/var/lib/app"}},
	}
}

func decodeOutput(t *testing.T, output []byte) map[string]any {
	t.Helper()
	var document map[string]any
	if err := yaml.Unmarshal(output, &document); err != nil {
		t.Fatalf("generated YAML did not parse: %v\n%s", err, output)
	}
	return document
}

func mapAt(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key]
	if !ok {
		t.Fatalf("missing key %q in %#v", key, parent)
	}
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%q has type %T, want map[string]any", key, value)
	}
	return result
}

func integerAt(t *testing.T, parent map[string]any, key string) int {
	t.Helper()
	value, ok := parent[key].(int)
	if !ok {
		t.Fatalf("%q has type %T, want int", key, parent[key])
	}
	return value
}

func assertEqual(t *testing.T, object map[string]any, key string, want any) {
	t.Helper()
	got, ok := object[key]
	if !ok {
		t.Fatalf("missing key %q in %#v", key, object)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %#v, want %#v", key, got, want)
	}
}

func assertBindings(t *testing.T, got []Binding, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d bindings, want %d: %#v", len(got), len(want), got)
	}
	for _, binding := range got {
		if want[binding.EnvironmentVariable] != binding.SecretReference {
			t.Errorf("binding %s = %q, want %q", binding.EnvironmentVariable, binding.SecretReference, want[binding.EnvironmentVariable])
		}
	}
}

func boolPointer(value bool) *bool {
	return &value
}
