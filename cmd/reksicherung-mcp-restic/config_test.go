package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	resticservice "github.com/1k-off/reksicherung/internal/reksicherung/mcp/restic"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
)

func TestLoadConfigRequiresReferencesAndIgnoresArbitraryEnvironment(t *testing.T) {
	values := validResticEnvironment()
	values["AWS_ACCESS_KEY_ID"] = "must-not-be-read"
	values["RESTIC_PASSWORD"] = "also-must-not-be-read"
	values["HOME"] = "must-not-be-read"
	var accessed []string
	config, err := loadConfig(func(name string) string {
		accessed = append(accessed, name)
		return values[name]
	})
	if err != nil {
		t.Fatalf("loadConfig(valid) error = %v", err)
	}
	if config.repository != values[repositoryEnv] || config.passwordRef != values[passwordRefEnv] {
		t.Fatalf("config did not retain non-secret configuration and secret reference")
	}
	if !reflect.DeepEqual(config.environment, []string{"AWS_DEFAULT_REGION=us-east-1"}) {
		t.Fatalf("non-secret child environment = %q", config.environment)
	}
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "RESTIC_PASSWORD", "HOME"} {
		if contains(accessed, name) {
			t.Errorf("loadConfig read unsupported process variable %q", name)
		}
	}
	for _, forbidden := range []string{"must-not-be-read", "also-must-not-be-read"} {
		if strings.Contains(strings.Join(config.environment, "\n"), forbidden) {
			t.Errorf("arbitrary environment value passed through: %q", forbidden)
		}
	}

	for _, test := range []struct {
		name   string
		change func(map[string]string)
	}{
		{name: "missing repository password reference", change: func(env map[string]string) { delete(env, passwordRefEnv) }},
		{name: "literal password instead of reference", change: func(env map[string]string) { env[passwordRefEnv] = "literal-password" }},
		{name: "malformed backend reference", change: func(env map[string]string) {
			env["REKSICHERUNG_RESTIC_AWS_ACCESS_KEY_ID_REF"] = "openbao://missing-key"
		}},
		{name: "remote plaintext OpenBao", change: func(env map[string]string) { env[openBaoAddrEnv] = "http://bao.example.invalid" }},
		{name: "OpenBao endpoint userinfo", change: func(env map[string]string) { env[openBaoAddrEnv] = "https://user:password@bao.example.invalid" }},
		{name: "repository URL credentials", change: func(env map[string]string) {
			env[repositoryEnv] = "s3:https://user:password@s3.example.invalid/archive"
		}},
		{name: "repository secret query", change: func(env map[string]string) {
			env[repositoryEnv] = "s3:https://s3.example.invalid/archive?token=private"
		}},
		{name: "sftp password", change: func(env map[string]string) { env[repositoryEnv] = "sftp:backup:private@host:/repo" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := cloneEnvironment(values)
			test.change(env)
			_, err := loadConfig(func(name string) string { return env[name] })
			if err == nil {
				t.Fatal("loadConfig() succeeded with invalid configuration")
			}
			for _, secret := range []string{"private", "literal-password", "protected-bootstrap-token"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("configuration error leaked secret material: %v", err)
				}
			}
		})
	}
}

func TestValidRepositoryAcceptsCommonResticForms(t *testing.T) {
	for _, repository := range []string{
		"/var/backups/restic",
		"sftp:backup@host.example.invalid:/srv/restic",
		"s3:https://s3.example.invalid/bucket?region=us-east-1",
		"b2:bucket-name:repository",
		"azure:container:/repository",
	} {
		if !validRepository(repository) {
			t.Errorf("validRepository(%q) = false", repository)
		}
	}
}

func TestResolvedSecretsReachRunnerAndAreNotDiagnostics(t *testing.T) {
	env := validResticEnvironment()
	for name, reference := range map[string]string{
		"REKSICHERUNG_RESTIC_AWS_ACCESS_KEY_ID_REF":     "openbao://secret/restic/aws#access-key",
		"REKSICHERUNG_RESTIC_AWS_SECRET_ACCESS_KEY_REF": "openbao://secret/restic/aws#secret-key",
		"REKSICHERUNG_RESTIC_AWS_SESSION_TOKEN_REF":     "openbao://secret/restic/aws#session-token",
		"REKSICHERUNG_RESTIC_B2_ACCOUNT_ID_REF":         "openbao://secret/restic/b2#account-id",
		"REKSICHERUNG_RESTIC_B2_ACCOUNT_KEY_REF":        "openbao://secret/restic/b2#account-key",
		"REKSICHERUNG_RESTIC_AZURE_ACCOUNT_NAME_REF":    "openbao://secret/restic/azure#account-name",
		"REKSICHERUNG_RESTIC_AZURE_ACCOUNT_KEY_REF":     "openbao://secret/restic/azure#account-key",
	} {
		env[name] = reference
	}
	config, err := loadConfig(func(name string) string { return env[name] })
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	values := map[string]string{
		"openbao://secret/restic/repository#password": "repo-password-private",
		"openbao://secret/restic/aws#access-key":      "aws-access-private",
		"openbao://secret/restic/aws#secret-key":      "aws-secret-private",
		"openbao://secret/restic/aws#session-token":   "aws-session-private",
		"openbao://secret/restic/b2#account-id":       "b2-id-private",
		"openbao://secret/restic/b2#account-key":      "b2-key-private",
		"openbao://secret/restic/azure#account-name":  "azure-name-private",
		"openbao://secret/restic/azure#account-key":   "azure-key-private",
	}
	provider := &recordingSecretProvider{values: values}
	runner := &recordingResticRunner{}
	instance, err := newResticService(context.Background(), config, provider, runner)
	if err != nil {
		t.Fatalf("newResticService() error = %v", err)
	}
	if _, err := instance.ListSnapshots(context.Background(), 1); err != nil {
		t.Fatalf("ListSnapshots() error = %v", err)
	}
	if !reflect.DeepEqual(provider.references, []string{
		"openbao://secret/restic/repository#password",
		"openbao://secret/restic/aws#access-key",
		"openbao://secret/restic/aws#secret-key",
		"openbao://secret/restic/aws#session-token",
		"openbao://secret/restic/b2#account-id",
		"openbao://secret/restic/b2#account-key",
		"openbao://secret/restic/azure#account-name",
		"openbao://secret/restic/azure#account-key",
	}) {
		t.Fatalf("resolved references = %v", provider.references)
	}
	wantEnvironment := []string{
		"RESTIC_REPOSITORY=" + env[repositoryEnv],
		"RESTIC_PASSWORD=repo-password-private",
		"AWS_DEFAULT_REGION=us-east-1",
		"AWS_ACCESS_KEY_ID=aws-access-private",
		"AWS_SECRET_ACCESS_KEY=aws-secret-private",
		"AWS_SESSION_TOKEN=aws-session-private",
		"B2_ACCOUNT_ID=b2-id-private",
		"B2_ACCOUNT_KEY=b2-key-private",
		"AZURE_ACCOUNT_NAME=azure-name-private",
		"AZURE_ACCOUNT_KEY=azure-key-private",
	}
	if !reflect.DeepEqual(runner.environment, wantEnvironment) {
		t.Fatalf("runner environment = %q, want %q", runner.environment, wantEnvironment)
	}
	for _, buffer := range provider.buffers {
		for _, value := range buffer {
			if value != 0 {
				t.Fatal("resolved secret byte buffer was not cleared")
			}
		}
	}

	failingProvider := &recordingSecretProvider{err: errors.New("provider failed with repo-password-private")}
	_, err = newResticService(context.Background(), config, failingProvider, runner)
	if err == nil || strings.Contains(err.Error(), "repo-password-private") {
		t.Fatalf("secret-resolution diagnostic is missing or leaked secret: %v", err)
	}
}

func validResticEnvironment() map[string]string {
	return map[string]string{
		repositoryEnv:        "s3:s3.example.invalid/archive",
		passwordRefEnv:       "openbao://secret/restic/repository#password",
		openBaoAddrEnv:       "https://bao.example.invalid",
		openBaoTokenEnv:      "protected-bootstrap-token",
		"AWS_DEFAULT_REGION": "us-east-1",
	}
}

func cloneEnvironment(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for name, value := range source {
		clone[name] = value
	}
	return clone
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

type recordingSecretProvider struct {
	values     map[string]string
	err        error
	references []string
	buffers    [][]byte
}

func (provider *recordingSecretProvider) Resolve(_ context.Context, reference secrets.Reference) ([]byte, error) {
	raw := "openbao://" + reference.Mount + "/" + reference.Path + "#" + reference.Key
	provider.references = append(provider.references, raw)
	if provider.err != nil {
		return nil, provider.err
	}
	value, ok := provider.values[raw]
	if !ok {
		return nil, errors.New("unexpected secret reference")
	}
	buffer := []byte(value)
	provider.buffers = append(provider.buffers, buffer)
	return buffer, nil
}

type recordingResticRunner struct {
	environment []string
}

func (runner *recordingResticRunner) Run(_ context.Context, _ string, _ []string, environment []string) ([]byte, error) {
	runner.environment = append([]string(nil), environment...)
	return []byte("[]"), nil
}

var _ resticservice.Runner = (*recordingResticRunner)(nil)
