package input

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

const (
	dbUserRef     = "openbao://secret/reksicherung/test-db#username"
	dbPasswordRef = "openbao://secret/reksicherung/test-db#password"
	healthRef     = "openbao://secret/reksicherung/healthchecks#url"
)

func TestDecodeRejectsUnknownAndPlaintextCredentialFields(t *testing.T) {
	const sentinel = "PLAINTEXT_SECRET_SENTINEL"
	inputYAML := `project:
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
	_, err := Decode(strings.NewReader(inputYAML))
	if err == nil {
		t.Fatal("Decode() unexpectedly accepted a plaintext credential field")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatal("Decode() error disclosed the plaintext credential")
	}
}

func TestDecodeRequiresSingleStrictYAMLDocument(t *testing.T) {
	for name, inputYAML := range map[string]string{
		"empty":     "",
		"unknown":   "unsupported: true\n",
		"documents": "project: {name: demo}\n---\nproject: {name: other}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(strings.NewReader(inputYAML)); err == nil {
				t.Fatal("Decode() unexpectedly succeeded")
			}
		})
	}
}

func TestDecodeRejectsUnboundedExecutionFields(t *testing.T) {
	for _, test := range []struct {
		name string
		yaml string
	}{
		{
			name: "scripts",
			yaml: `project: {name: test}
storage: {type: s3, bucket: backups}
schedules: {cron: "0 5 * * *"}
archive: {includes: [/var/lib/app]}
scripts: {before: "echo TEST_ONLY_SENTINEL"}
`,
		},
		{
			name: "database args",
			yaml: `project: {name: test}
storage: {type: s3, bucket: backups}
schedules: {cron: "0 5 * * *"}
databases:
  - name: main
    type: mssql
    host: localhost
    database: app
    username_ref: ` + dbUserRef + `
    password_ref: ` + dbPasswordRef + `
    args: "/p:Secret=TEST_ONLY_SENTINEL"
`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Decode(strings.NewReader(test.yaml))
			if err == nil {
				t.Fatal("Decode() accepted unsupported executable configuration")
			}
			if strings.Contains(err.Error(), "TEST_ONLY_SENTINEL") {
				t.Fatal("Decode() error disclosed input value")
			}
		})
	}
}

func TestValidateAcceptsWindowsArchivePathsAndHooks(t *testing.T) {
	in := simpleInput()
	in.Archive = &Archive{Includes: []string{`C:\backups\app.bacpac`, `C:/backups/other.bak`}}
	in.Hooks = Hooks{
		RunBefore: StringList{`sqlpackage /Action:Export /TargetFile:C:\backups\app.bacpac`},
		RunAfter:  StringList{`del C:\backups\app.bacpac`},
	}
	if err := in.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	decoded, err := Decode(strings.NewReader(`project: {name: win-app}
storage: {type: s3, bucket: backups}
schedules: {cron: "0 5 * * *", healthcheck_url_ref: ` + healthRef + `}
archive:
  includes: ["C:/backups/app.bacpac"]
hooks:
  run_before: "echo before"
  run_after:
    - "echo after-1"
    - "echo after-2"
`))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded Validate() error = %v", err)
	}
	if got := []string(decoded.Hooks.RunBefore); len(got) != 1 || got[0] != "echo before" {
		t.Fatalf("run_before = %#v", decoded.Hooks.RunBefore)
	}
	if got := []string(decoded.Hooks.RunAfter); len(got) != 2 {
		t.Fatalf("run_after = %#v", decoded.Hooks.RunAfter)
	}
}

func TestValidationRejectsUnsafeNamesPathsAndReferences(t *testing.T) {
	tests := map[string]func(*Input){
		"project traversal":   func(in *Input) { in.Project.Name = "../bad" },
		"storage traversal":   func(in *Input) { in.Storage.PathPrefix = "safe/../escape" },
		"archive traversal":   func(in *Input) { in.Archive = &Archive{Includes: []string{"/var/../etc"}} },
		"invalid secret ref":  func(in *Input) { in.Storage.AccessKeyIDRef = "not-a-reference" },
		"empty schedule keep": func(in *Input) { in.Schedules.Keep = -1 },
		"relative archive":    func(in *Input) { in.Archive = &Archive{Includes: []string{"var/lib/app"}} },
		"empty hook":          func(in *Input) { in.Hooks.RunBefore = StringList{" "} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			in := simpleInput()
			mutate(&in)
			if err := in.Validate(); err == nil {
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
	if decodeErr == nil {
		t.Fatal("Decode() unexpectedly succeeded")
	}
	combined := decodeErr.Error() + logOutput.String()
	if strings.Contains(combined, sentinel) {
		t.Fatal("plaintext credential sentinel was disclosed by error or log output")
	}
}

func TestDecodeInvalidSecretReferenceDoesNotEchoInput(t *testing.T) {
	const sentinel = "SECRET_SENTINEL_REFERENCE"
	in := simpleInput()
	in.Storage.AccessKeyIDRef = sentinel
	err := in.Validate()
	if err == nil {
		t.Fatal("Validate() accepted an invalid secret reference")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatal("Validate() error disclosed the invalid reference value")
	}
}

func TestBinderPlaceholderIsStableAndSorted(t *testing.T) {
	b := NewBinder()
	first := b.Placeholder("storage_access_key_id", "openbao://secret/a#k")
	second := b.Placeholder("encryption_password", "openbao://secret/b#p")
	again := b.Placeholder("storage_access_key_id", "openbao://secret/a#k")
	if first != "$REKSICHERUNG_STORAGE_ACCESS_KEY_ID" || again != first {
		t.Fatalf("placeholder = %q / %q", first, again)
	}
	if second != "$REKSICHERUNG_ENCRYPTION_PASSWORD" {
		t.Fatalf("encryption placeholder = %q", second)
	}
	got := b.Bindings()
	if len(got) != 2 {
		t.Fatalf("bindings = %#v", got)
	}
	if got[0].EnvironmentVariable != "REKSICHERUNG_ENCRYPTION_PASSWORD" {
		t.Fatalf("bindings not sorted: %#v", got)
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
