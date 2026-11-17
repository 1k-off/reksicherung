package resolve

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/1k-off/reksicherung/internal/reksicherung/configgen/input"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
)

type fakeProvider map[string]string

func (f fakeProvider) Resolve(_ context.Context, reference secrets.Reference) ([]byte, error) {
	key := reference.Mount + "/" + reference.Path + "#" + reference.Key
	value, ok := f[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return []byte(value), nil
}

func TestUnsafeYAMLSubstitutesPlaceholders(t *testing.T) {
	safe := []byte(`
version: "1"
default:
  env:
    AWS_ACCESS_KEY_ID: $REKSICHERUNG_STORAGE_ACCESS_KEY_ID
    AWS_SECRET_ACCESS_KEY: $REKSICHERUNG_STORAGE_SECRET_ACCESS_KEY
app:
  backup:
    send-before:
      - url: $REKSICHERUNG_SCHEDULES_HEALTHCHECK_URL/start
`)
	bindings := []input.Binding{
		{EnvironmentVariable: "REKSICHERUNG_STORAGE_ACCESS_KEY_ID", SecretReference: "openbao://secret/store#access"},
		{EnvironmentVariable: "REKSICHERUNG_STORAGE_SECRET_ACCESS_KEY", SecretReference: "openbao://secret/store#secret"},
		{EnvironmentVariable: "REKSICHERUNG_SCHEDULES_HEALTHCHECK_URL", SecretReference: "openbao://secret/hc#url"},
	}
	provider := fakeProvider{
		"secret/store#access": "AKIA_TEST",
		"secret/store#secret": "secret-value",
		"secret/hc#url":       "https://hc.example/check",
	}
	got, err := UnsafeYAML(context.Background(), safe, bindings, provider)
	if err != nil {
		t.Fatalf("UnsafeYAML() error = %v", err)
	}
	out := string(got)
	for _, want := range []string{"AKIA_TEST", "secret-value", "https://hc.example/check/start"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "$REKSICHERUNG_") {
		t.Fatalf("placeholders remain:\n%s", out)
	}
}

func TestUnsafeYAMLFailsClosedOnMissingSecret(t *testing.T) {
	safe := []byte("password: $REKSICHERUNG_ENCRYPTION_PASSWORD\n")
	bindings := []input.Binding{
		{EnvironmentVariable: "REKSICHERUNG_ENCRYPTION_PASSWORD", SecretReference: "openbao://secret/enc#password"},
	}
	_, err := UnsafeYAML(context.Background(), safe, bindings, fakeProvider{})
	if err == nil {
		t.Fatal("UnsafeYAML() unexpectedly succeeded")
	}
}
