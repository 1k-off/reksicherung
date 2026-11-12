// Package openbao resolves secret references from OpenBao KV v2 mounts.
package openbao

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
)

const (
	requestTimeout = 10 * time.Second
	maxResponse    = 1 << 20
	// PlaintextEnv allows http:// to non-loopback OpenBao endpoints (compose lab).
	PlaintextEnv = "REKSICHERUNG_OPENBAO_PLAINTEXT"
)

var (
	// ErrNotFound means that the OpenBao path or requested key does not exist.
	ErrNotFound = errors.New("openbao: secret or key not found")
	// ErrRequest means the request failed or OpenBao returned an unsuccessful
	// response. Details from remote responses are deliberately not included.
	ErrRequest = errors.New("openbao: request failed")
	// ErrInvalidResponse means OpenBao returned an unexpected or invalid KV v2
	// response.
	ErrInvalidResponse = errors.New("openbao: invalid response")
)

// Provider resolves OpenBao KV v2 references using a token and bounded HTTP
// requests. Create instances with New.
type Provider struct {
	endpoint *url.URL
	token    string
	client   *http.Client
}

// New creates a provider for an OpenBao base URL and token.
//
// HTTPS is required except for loopback addresses (including localhost), which
// allows local development and test servers. Set REKSICHERUNG_OPENBAO_PLAINTEXT=1
// to allow http:// to Docker Compose service names (lab only). Requests have a
// fixed timeout, disable redirects, and limit response bodies to 1 MiB.
func New(endpoint, token string) (*Provider, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, errors.New("openbao: invalid endpoint")
	}
	if parsed.Scheme == "http" && !HTTPEndpointAllowed(parsed.Hostname()) {
		return nil, errors.New("openbao: HTTPS required for non-loopback endpoint")
	}
	if !validToken(token) {
		return nil, errors.New("openbao: invalid token")
	}
	parsed.Path = ""
	parsed.RawPath = ""

	return &Provider{
		endpoint: parsed,
		token:    token,
		client: &http.Client{
			Timeout: requestTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Resolve retrieves a string value from an OpenBao KV v2 secret. It validates
// the reference even when the caller constructed it directly.
//
// Errors intentionally omit server response bodies, request URLs, tokens, and
// secret values. The returned byte slice is owned by the caller.
func (p *Provider) Resolve(ctx context.Context, reference secrets.Reference) ([]byte, error) {
	if p == nil || p.endpoint == nil || p.client == nil || !reference.Valid() {
		return nil, secrets.ErrInvalidReference
	}

	requestURL := *p.endpoint
	requestURL.Path = "/v1/" + reference.Mount + "/data/" + reference.Path
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, ErrRequest
	}
	request.Header.Set("X-Vault-Token", p.token)

	response, err := p.client.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("openbao: request canceled: %w", ctxErr)
		}
		return nil, ErrRequest
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w (HTTP %d)", ErrRequest, response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil || len(body) > maxResponse {
		return nil, ErrInvalidResponse
	}

	var envelope struct {
		Data struct {
			Data map[string]json.RawMessage `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Data.Data == nil {
		return nil, ErrInvalidResponse
	}
	rawValue, ok := envelope.Data.Data[reference.Key]
	if !ok {
		return nil, ErrNotFound
	}

	var value string
	if err := json.Unmarshal(rawValue, &value); err != nil {
		return nil, ErrInvalidResponse
	}
	return []byte(value), nil
}

// HTTPEndpointAllowed reports whether plaintext HTTP may target host.
func HTTPEndpointAllowed(host string) bool {
	return isLoopback(host) || os.Getenv(PlaintextEnv) == "1"
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validToken(token string) bool {
	if token == "" || strings.TrimSpace(token) != token {
		return false
	}
	for _, char := range token {
		if char < '!' || char > '~' {
			return false
		}
	}
	return true
}
