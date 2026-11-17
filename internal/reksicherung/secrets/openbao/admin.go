package openbao

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
)

// HealthStatus is a subset of OpenBao /v1/sys/health.
type HealthStatus struct {
	Initialized bool   `json:"initialized"`
	Sealed      bool   `json:"sealed"`
	Standby     bool   `json:"standby"`
	Version     string `json:"version"`
}

// AppRoleCredentials are returned once when ensuring a backup AppRole.
type AppRoleCredentials struct {
	RoleName string
	RoleID   string
	SecretID string
	Policy   string
}

// Health reports OpenBao seal/init state. HTTP 429/472/473 still decode as
// success for sealed/uninit/standby bring-up checks.
func (p *Provider) Health(ctx context.Context) (HealthStatus, error) {
	if p == nil || p.endpoint == nil || p.client == nil {
		return HealthStatus{}, ErrRequest
	}
	body, status, err := p.do(ctx, http.MethodGet, "/v1/sys/health", nil, false)
	if err != nil {
		return HealthStatus{}, err
	}
	switch status {
	case http.StatusOK, http.StatusTooManyRequests, 472, 473:
	default:
		return HealthStatus{}, fmt.Errorf("%w (HTTP %d)", ErrRequest, status)
	}
	var health HealthStatus
	if err := json.Unmarshal(body, &health); err != nil {
		return HealthStatus{}, ErrInvalidResponse
	}
	return health, nil
}

// KVGet reads all string fields from a KV v2 secret path (no key fragment).
func (p *Provider) KVGet(ctx context.Context, mount, secretPath string) (map[string]string, error) {
	if err := validateMountPath(mount, secretPath); err != nil {
		return nil, err
	}
	body, status, err := p.do(ctx, http.MethodGet, "/v1/"+mount+"/data/"+secretPath, nil, true)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w (HTTP %d)", ErrRequest, status)
	}
	var envelope struct {
		Data struct {
			Data map[string]json.RawMessage `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Data.Data == nil {
		return nil, ErrInvalidResponse
	}
	out := make(map[string]string, len(envelope.Data.Data))
	for key, raw := range envelope.Data.Data {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, ErrInvalidResponse
		}
		out[key] = value
	}
	return out, nil
}

// KVPut writes string fields to a KV v2 secret path (creates or updates).
func (p *Provider) KVPut(ctx context.Context, mount, secretPath string, data map[string]string) error {
	if err := validateMountPath(mount, secretPath); err != nil {
		return err
	}
	if len(data) == 0 {
		return errors.New("openbao: kv put requires at least one field")
	}
	payload, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		return ErrRequest
	}
	_, status, err := p.do(ctx, http.MethodPost, "/v1/"+mount+"/data/"+secretPath, payload, true)
	if err != nil {
		return err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return fmt.Errorf("%w (HTTP %d)", ErrRequest, status)
	}
	return nil
}

// KVList lists keys under a KV v2 metadata path. Trailing slash is optional.
func (p *Provider) KVList(ctx context.Context, mount, secretPath string) ([]string, error) {
	if err := validateMountPath(mount, secretPath); err != nil {
		return nil, err
	}
	listPath := strings.TrimSuffix(secretPath, "/")
	body, status, err := p.do(ctx, "LIST", "/v1/"+mount+"/metadata/"+listPath, nil, true)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w (HTTP %d)", ErrRequest, status)
	}
	var envelope struct {
		Data struct {
			Keys []string `json:"keys"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, ErrInvalidResponse
	}
	return append([]string(nil), envelope.Data.Keys...), nil
}

// EnsureBackupAppRole creates/updates a per-source policy + AppRole and returns
// role_id plus a newly generated secret_id. Role name: backup-<source_id>.
func (p *Provider) EnsureBackupAppRole(ctx context.Context, sourceID string, includeDefaults bool) (AppRoleCredentials, error) {
	if !secrets.ValidSourceID(sourceID) {
		return AppRoleCredentials{}, secrets.ErrInvalidSourceID
	}
	sourcePath, err := secrets.SourcePath(sourceID)
	if err != nil {
		return AppRoleCredentials{}, err
	}
	roleName := "backup-" + sourceID
	policyName := roleName
	policy := backupSourcePolicy(sourcePath, includeDefaults)
	if err := p.writePolicy(ctx, policyName, policy); err != nil {
		return AppRoleCredentials{}, err
	}
	if err := p.writeAppRole(ctx, roleName, policyName); err != nil {
		return AppRoleCredentials{}, err
	}
	roleID, err := p.readAppRoleID(ctx, roleName)
	if err != nil {
		return AppRoleCredentials{}, err
	}
	secretID, err := p.generateSecretID(ctx, roleName)
	if err != nil {
		return AppRoleCredentials{}, err
	}
	return AppRoleCredentials{
		RoleName: roleName,
		RoleID:   roleID,
		SecretID: secretID,
		Policy:   policyName,
	}, nil
}

// AppRoleLogin exchanges role_id/secret_id for a short-lived client token.
func (p *Provider) AppRoleLogin(ctx context.Context, roleID, secretID string) (string, error) {
	if !validToken(roleID) || !validToken(secretID) {
		return "", errors.New("openbao: invalid approle credentials")
	}
	payload, err := json.Marshal(map[string]string{
		"role_id":   roleID,
		"secret_id": secretID,
	})
	if err != nil {
		return "", ErrRequest
	}
	// Login does not require an existing client token.
	body, status, err := p.do(ctx, http.MethodPost, "/v1/auth/approle/login", payload, false)
	if err != nil {
		return "", err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return "", fmt.Errorf("%w (HTTP %d)", ErrRequest, status)
	}
	var envelope struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Auth.ClientToken == "" {
		return "", ErrInvalidResponse
	}
	return envelope.Auth.ClientToken, nil
}

// VerifyBackupAppRole logs in and proves read access to sources/<source_id>.
// ErrNotFound on the source path still counts as success (auth worked; KV empty).
func (p *Provider) VerifyBackupAppRole(ctx context.Context, sourceID, roleID, secretID string) error {
	if !secrets.ValidSourceID(sourceID) {
		return secrets.ErrInvalidSourceID
	}
	token, err := p.AppRoleLogin(ctx, roleID, secretID)
	if err != nil {
		return err
	}
	scoped, err := New(p.endpoint.String(), token)
	if err != nil {
		return err
	}
	sourcePath, err := secrets.SourcePath(sourceID)
	if err != nil {
		return err
	}
	if _, err := scoped.KVGet(ctx, secrets.DefaultMount, sourcePath); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

func (p *Provider) writePolicy(ctx context.Context, name, policyHCL string) error {
	if !validPolicyName(name) {
		return errors.New("openbao: invalid policy name")
	}
	payload, err := json.Marshal(map[string]string{"policy": policyHCL})
	if err != nil {
		return ErrRequest
	}
	_, status, err := p.do(ctx, http.MethodPut, "/v1/sys/policies/acl/"+name, payload, true)
	if err != nil {
		return err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return fmt.Errorf("%w (HTTP %d)", ErrRequest, status)
	}
	return nil
}

func (p *Provider) writeAppRole(ctx context.Context, roleName, policyName string) error {
	if !validPolicyName(roleName) || !validPolicyName(policyName) {
		return errors.New("openbao: invalid approle name")
	}
	payload, err := json.Marshal(map[string]any{
		"token_policies": []string{policyName},
		"token_ttl":      "1h",
		"token_max_ttl":  "4h",
	})
	if err != nil {
		return ErrRequest
	}
	_, status, err := p.do(ctx, http.MethodPost, "/v1/auth/approle/role/"+roleName, payload, true)
	if err != nil {
		return err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return fmt.Errorf("%w (HTTP %d)", ErrRequest, status)
	}
	return nil
}

func (p *Provider) readAppRoleID(ctx context.Context, roleName string) (string, error) {
	body, status, err := p.do(ctx, http.MethodGet, "/v1/auth/approle/role/"+roleName+"/role-id", nil, true)
	if err != nil {
		return "", err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return "", fmt.Errorf("%w (HTTP %d)", ErrRequest, status)
	}
	var envelope struct {
		Data struct {
			RoleID string `json:"role_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Data.RoleID == "" {
		return "", ErrInvalidResponse
	}
	return envelope.Data.RoleID, nil
}

func (p *Provider) generateSecretID(ctx context.Context, roleName string) (string, error) {
	body, status, err := p.do(ctx, http.MethodPost, "/v1/auth/approle/role/"+roleName+"/secret-id", []byte("{}"), true)
	if err != nil {
		return "", err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return "", fmt.Errorf("%w (HTTP %d)", ErrRequest, status)
	}
	var envelope struct {
		Data struct {
			SecretID string `json:"secret_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Data.SecretID == "" {
		return "", ErrInvalidResponse
	}
	return envelope.Data.SecretID, nil
}

func backupSourcePolicy(sourcePath string, includeDefaults bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "path \"secret/data/%s\" {\n  capabilities = [\"read\"]\n}\n", sourcePath)
	fmt.Fprintf(&b, "path \"secret/metadata/%s\" {\n  capabilities = [\"read\"]\n}\n", sourcePath)
	if includeDefaults {
		fmt.Fprintf(&b, "path \"secret/data/%s\" {\n  capabilities = [\"read\"]\n}\n", secrets.DefaultsPath)
		fmt.Fprintf(&b, "path \"secret/metadata/%s\" {\n  capabilities = [\"read\"]\n}\n", secrets.DefaultsPath)
	}
	return b.String()
}

func validateMountPath(mount, secretPath string) error {
	if !validPathSegment(mount) {
		return errors.New("openbao: invalid mount")
	}
	if secretPath == "" || strings.Contains(secretPath, "..") || strings.HasPrefix(secretPath, "/") {
		return errors.New("openbao: invalid secret path")
	}
	for _, segment := range strings.Split(secretPath, "/") {
		if segment == "" || segment == "." || segment == ".." || !validPathSegment(segment) {
			return errors.New("openbao: invalid secret path")
		}
	}
	return nil
}

func validPathSegment(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func validPolicyName(value string) bool {
	return validPathSegment(value) && !strings.Contains(value, ".")
}

func (p *Provider) do(ctx context.Context, method, path string, payload []byte, withToken bool) ([]byte, int, error) {
	if p == nil || p.endpoint == nil || p.client == nil {
		return nil, 0, ErrRequest
	}
	if withToken && !validToken(p.token) {
		return nil, 0, errors.New("openbao: invalid token")
	}
	requestURL := *p.endpoint
	requestURL.Path = path
	var bodyReader io.Reader
	if payload != nil {
		bodyReader = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), bodyReader)
	if err != nil {
		return nil, 0, ErrRequest
	}
	if withToken {
		request.Header.Set("X-Vault-Token", p.token)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := p.client.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, 0, fmt.Errorf("openbao: request canceled: %w", ctxErr)
		}
		return nil, 0, ErrRequest
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil || len(body) > maxResponse {
		return nil, response.StatusCode, ErrInvalidResponse
	}
	return body, response.StatusCode, nil
}
