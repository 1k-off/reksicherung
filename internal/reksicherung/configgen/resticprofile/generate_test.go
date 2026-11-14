package resticprofile

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/1k-off/reksicherung/internal/reksicherung/configgen/input"
	"gopkg.in/yaml.v3"
)

const (
	s3AccessRef   = "openbao://secret/reksicherung/object-store#access-key"
	s3SecretRef   = "openbao://secret/reksicherung/object-store#secret-key"
	healthRef     = "openbao://secret/reksicherung/healthchecks#url"
	encryptionRef = "openbao://secret/reksicherung/encryption#password"
)

func TestGenerateSimpleArchiveS3(t *testing.T) {
	in, err := input.Decode(strings.NewReader(`project:
  name: my-app
storage:
  type: minio
  bucket: backups-general
  endpoint: https://objects.example.test
  path_prefix: my-app
  access_key_id_ref: ` + s3AccessRef + `
  secret_access_key_ref: ` + s3SecretRef + `
schedules:
  cron: "0 5 * * *"
  healthcheck_url_ref: ` + healthRef + `
archive:
  includes: [/var/lib/app]
  excludes: [/var/lib/app/tmp]
encryption:
  enabled: true
  password_ref: ` + encryptionRef + `
`))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	result, err := Generate(in)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	doc := decodeOutput(t, result.YAML)
	assertEqual(t, doc, "version", "1")
	env := mapAt(t, mapAt(t, doc, "default"), "env")
	assertEqual(t, env, "AWS_ACCESS_KEY_ID", "$REKSICHERUNG_STORAGE_ACCESS_KEY_ID")
	assertEqual(t, env, "AWS_SECRET_ACCESS_KEY", "$REKSICHERUNG_STORAGE_SECRET_ACCESS_KEY")
	assertEqual(t, env, "RESTIC_PASSWORD", "$REKSICHERUNG_ENCRYPTION_PASSWORD")

	profile := mapAt(t, doc, "my_app")
	assertEqual(t, profile, "inherit", "default")
	assertEqual(t, profile, "initialize", true)
	assertEqual(t, profile, "repository", "s3:https://objects.example.test/backups-general/my-app")
	backup := mapAt(t, profile, "backup")
	assertEqual(t, backup, "source", []any{"/var/lib/app"})
	assertEqual(t, backup, "exclude", []any{"/var/lib/app/tmp"})
	assertEqual(t, backup, "schedule", "0 5 * * *")
	assertEqual(t, backup, "check-after", true)
	sendBefore := backup["send-before"].([]any)[0].(map[string]any)
	assertEqual(t, sendBefore, "url", "$REKSICHERUNG_SCHEDULES_HEALTHCHECK_URL/start")
	forget := mapAt(t, profile, "forget")
	assertEqual(t, forget, "keep-last", 14)
	assertEqual(t, forget, "prune", true)

	assertBindings(t, result.Bindings, map[string]string{
		"REKSICHERUNG_ENCRYPTION_PASSWORD":       encryptionRef,
		"REKSICHERUNG_SCHEDULES_HEALTHCHECK_URL": healthRef,
		"REKSICHERUNG_STORAGE_ACCESS_KEY_ID":     s3AccessRef,
		"REKSICHERUNG_STORAGE_SECRET_ACCESS_KEY": s3SecretRef,
	})
	for _, binding := range result.Bindings {
		if strings.Contains(binding.SecretReference, "PLAIN") || binding.SecretReference == "" {
			t.Fatalf("unexpected binding %#v", binding)
		}
	}
}

func TestGenerateEnhancedProfiles(t *testing.T) {
	in := archiveInput()
	in.Mode = input.ModeEnhanced
	in.Schedules = input.Schedules{
		Daily:   &input.Schedule{Cron: "0 1 * * *", HealthcheckURLRef: healthRef},
		Weekly:  &input.Schedule{Cron: "0 2 * * 0", HealthcheckURLRef: healthRef},
		Monthly: &input.Schedule{Cron: "0 3 1 * *", HealthcheckURLRef: healthRef},
	}
	result, err := Generate(in)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	doc := decodeOutput(t, result.YAML)
	for _, tier := range []struct {
		name string
		keep int
	}{
		{"daily", 7},
		{"weekly", 4},
		{"monthly", 12},
	} {
		profile := mapAt(t, doc, "service_name-"+tier.name)
		assertEqual(t, profile, "repository", "s3:s3.amazonaws.com/backup-bucket/project/"+tier.name)
		assertEqual(t, mapAt(t, profile, "forget"), "keep-last", tier.keep)
	}
}

func TestValidateForProviderRejectsUnsupported(t *testing.T) {
	cases := map[string]func(*input.Input){
		"databases": func(in *input.Input) {
			in.Databases = []input.Database{{
				Name: "main", Type: "mssql", Host: "db", Database: "app",
				UsernameRef: "openbao://secret/reksicherung/db#user",
				PasswordRef: "openbao://secret/reksicherung/db#pass",
			}}
		},
		"web":            func(in *input.Input) { in.Web.Port = 2703 },
		"sftp":           func(in *input.Input) { in.Storage = input.Storage{Type: "sftp", Host: "backup.example.test", UsernameRef: "openbao://secret/reksicherung/sftp#user"} },
		"compress_type":  func(in *input.Input) { in.Archive.CompressType = "tgz" },
		"missing archive": func(in *input.Input) { in.Archive = nil },
		"missing password": func(in *input.Input) {
			in.Encryption = input.Encryption{}
		},
		"openssl salt": func(in *input.Input) {
			v := true
			in.Encryption.Salt = &v
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := archiveInput()
			mutate(&in)
			err := ValidateForProvider(in)
			if err == nil {
				t.Fatal("ValidateForProvider() unexpectedly succeeded")
			}
			if !strings.HasPrefix(err.Error(), "resticprofile:") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	in := archiveInput()
	first, err := Generate(in)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	second, err := Generate(in)
	if err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}
	if !bytes.Equal(first.YAML, second.YAML) {
		t.Fatal("Generate() returned different YAML for identical input")
	}
	if !reflect.DeepEqual(first.Bindings, second.Bindings) {
		t.Fatal("Generate() returned different bindings for identical input")
	}
}

func TestGenerateAzureAccountKey(t *testing.T) {
	in := archiveInput()
	in.Storage = input.Storage{
		Type:          "azure",
		Account:       "mystorageacct",
		Container:     "backups",
		PathPrefix:    "project",
		AccountKeyRef: "openbao://secret/reksicherung/azure#key",
	}
	result, err := Generate(in)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	doc := decodeOutput(t, result.YAML)
	env := mapAt(t, mapAt(t, doc, "default"), "env")
	assertEqual(t, env, "AZURE_ACCOUNT_NAME", "mystorageacct")
	assertEqual(t, env, "AZURE_ACCOUNT_KEY", "$REKSICHERUNG_STORAGE_ACCOUNT_KEY")
	profile := mapAt(t, doc, "service_name")
	assertEqual(t, profile, "repository", "azure:backups:/project/")
}

func TestGenerateAzureAccountSAS(t *testing.T) {
	in := archiveInput()
	in.Storage = input.Storage{
		Type:          "azure",
		Account:       "mystorageacct",
		Container:     "shop",
		PathPrefix:    "project",
		AccountSASRef: "openbao://secret/reksicherung/azure#sas",
	}
	result, err := Generate(in)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	env := mapAt(t, mapAt(t, decodeOutput(t, result.YAML), "default"), "env")
	assertEqual(t, env, "AZURE_ACCOUNT_NAME", "mystorageacct")
	assertEqual(t, env, "AZURE_ACCOUNT_SAS", "$REKSICHERUNG_STORAGE_ACCOUNT_SAS")
	if _, ok := env["AZURE_ACCOUNT_KEY"]; ok {
		t.Fatal("unexpected AZURE_ACCOUNT_KEY with SAS")
	}
	assertBindings(t, result.Bindings, map[string]string{
		"REKSICHERUNG_ENCRYPTION_PASSWORD":       encryptionRef,
		"REKSICHERUNG_SCHEDULES_HEALTHCHECK_URL": healthRef,
		"REKSICHERUNG_STORAGE_ACCOUNT_SAS":       "openbao://secret/reksicherung/azure#sas",
	})
}

func TestValidateForProviderRejectsAzureServicePrincipal(t *testing.T) {
	in := archiveInput()
	in.Storage = input.Storage{
		Type:            "azure",
		Account:         "mystorageacct",
		Container:       "backups",
		TenantIDRef:     "openbao://secret/reksicherung/azure#tenant",
		ClientIDRef:     "openbao://secret/reksicherung/azure#client",
		ClientSecretRef: "openbao://secret/reksicherung/azure#secret",
	}
	if err := ValidateForProvider(in); err == nil {
		t.Fatal("ValidateForProvider() accepted azure SP")
	}
}

func TestRepositoryURLWithoutEndpoint(t *testing.T) {
	repo, err := repositoryURL(input.Storage{Type: "s3", Bucket: "backups", PathPrefix: "app"}, "")
	if err != nil {
		t.Fatalf("repositoryURL() error = %v", err)
	}
	if repo != "s3:s3.amazonaws.com/backups/app" {
		t.Fatalf("repository = %q", repo)
	}
}

func archiveInput() input.Input {
	return input.Input{
		Project: input.Project{Name: "service-name"},
		Storage: input.Storage{
			Type:       "s3",
			Bucket:     "backup-bucket",
			PathPrefix: "project",
		},
		Schedules: input.Schedules{
			Cron:              "0 5 * * *",
			HealthcheckURLRef: healthRef,
		},
		Archive:    &input.Archive{Includes: []string{"/var/lib/app"}},
		Encryption: input.Encryption{Enabled: true, PasswordRef: encryptionRef},
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

func assertBindings(t *testing.T, got []input.Binding, want map[string]string) {
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
