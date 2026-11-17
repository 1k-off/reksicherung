package openbao

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthAndKVAndAppRoleFlow(t *testing.T) {
	var sawPolicy, sawRole, sawRoleID, sawSecretID, sawLogin bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/sys/health":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"initialized":true,"sealed":false,"standby":false,"version":"2.0.0"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/secret/data/reksicherung/sources/demo-app":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"data":{"STORAGE_TYPE":"s3","S3_BUCKET":"b"}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/secret/data/reksicherung/sources/demo-app":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "LIST" && r.URL.Path == "/v1/secret/metadata/reksicherung/sources":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"keys":["demo-app/"]}}`))
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1/sys/policies/acl/"):
			sawPolicy = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/approle/role/backup-demo-app":
			sawRole = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/auth/approle/role/backup-demo-app/role-id":
			sawRoleID = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"role_id":"role-demo"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/approle/role/backup-demo-app/secret-id":
			sawSecretID = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"secret_id":"secret-demo"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/approle/login":
			sawLogin = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"auth":{"client_token":"client-demo-token"}}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	provider, err := New(server.URL, "admin-token")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx := context.Background()

	health, err := provider.Health(ctx)
	if err != nil || health.Sealed || !health.Initialized {
		t.Fatalf("Health() = %#v, err=%v", health, err)
	}

	data, err := provider.KVGet(ctx, "secret", "reksicherung/sources/demo-app")
	if err != nil || data["STORAGE_TYPE"] != "s3" {
		t.Fatalf("KVGet() = %#v, err=%v", data, err)
	}
	if err := provider.KVPut(ctx, "secret", "reksicherung/sources/demo-app", map[string]string{"S3_BUCKET": "b"}); err != nil {
		t.Fatalf("KVPut() error = %v", err)
	}
	keys, err := provider.KVList(ctx, "secret", "reksicherung/sources")
	if err != nil || len(keys) != 1 || keys[0] != "demo-app/" {
		t.Fatalf("KVList() = %#v, err=%v", keys, err)
	}

	creds, err := provider.EnsureBackupAppRole(ctx, "demo-app", true)
	if err != nil {
		t.Fatalf("EnsureBackupAppRole() error = %v", err)
	}
	if !sawPolicy || !sawRole || !sawRoleID || !sawSecretID {
		t.Fatalf("approle provisioning calls missing: policy=%v role=%v roleID=%v secretID=%v", sawPolicy, sawRole, sawRoleID, sawSecretID)
	}
	if creds.RoleID != "role-demo" || creds.SecretID != "secret-demo" || creds.RoleName != "backup-demo-app" {
		t.Fatalf("creds = %#v", creds)
	}

	token, err := provider.AppRoleLogin(ctx, "role-demo", "secret-demo")
	if err != nil || token != "client-demo-token" || !sawLogin {
		t.Fatalf("AppRoleLogin() = %q err=%v sawLogin=%v", token, err, sawLogin)
	}
}

func TestEnsureBackupAppRoleRejectsBadSourceID(t *testing.T) {
	provider, err := New("http://127.0.0.1:8200", "token")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := provider.EnsureBackupAppRole(context.Background(), "../bad", false); err == nil {
		t.Fatal("EnsureBackupAppRole() accepted invalid source id")
	}
}

func TestBackupSourcePolicyIncludesDefaults(t *testing.T) {
	policy := backupSourcePolicy("reksicherung/sources/demo-app", true)
	if !strings.Contains(policy, `path "secret/data/reksicherung/sources/demo-app"`) {
		t.Fatalf("policy missing source path: %s", policy)
	}
	if !strings.Contains(policy, `path "secret/data/reksicherung/defaults"`) {
		t.Fatalf("policy missing defaults: %s", policy)
	}
	var _ = json.RawMessage(nil)
}
