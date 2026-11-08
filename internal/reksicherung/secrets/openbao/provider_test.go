package openbao

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
)

func TestResolveKVV2Secret(t *testing.T) {
	const secretValue = "secret-material-must-not-leak"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("request method = %q, want GET", r.Method)
		}
		if r.URL.RequestURI() != "/v1/secret/data/folder%20name/a%3Fb%23c" {
			t.Errorf("request URI = %q", r.URL.RequestURI())
		}
		if got := r.Header.Get("X-Vault-Token"); got != "test-token" {
			t.Errorf("X-Vault-Token = %q, want configured token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":{"data":{"password":%q},"metadata":{"version":1}}}`, secretValue)
	}))
	defer server.Close()

	provider, err := New(server.URL, "test-token")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	got, err := provider.Resolve(context.Background(), secrets.Reference{
		Provider: "openbao",
		Mount:    "secret",
		Path:     "folder name/a?b#c",
		Key:      "password",
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if string(got) != secretValue {
		t.Fatalf("Resolve() = %q, want configured secret value", got)
	}
}

func TestResolveResponseHandling(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErr    error
	}{
		{
			name:       "missing key",
			statusCode: http.StatusOK,
			body:       `{"data":{"data":{"other":"unrelated-secret"}}}`,
			wantErr:    ErrNotFound,
		},
		{
			name:       "missing KV v2 envelope",
			statusCode: http.StatusOK,
			body:       `{"data":{"password":"secret-material"}}`,
			wantErr:    ErrInvalidResponse,
		},
		{
			name:       "malformed JSON",
			statusCode: http.StatusOK,
			body:       `{"data":{"data":{"password":"secret-material"`,
			wantErr:    ErrInvalidResponse,
		},
		{
			name:       "non-string secret",
			statusCode: http.StatusOK,
			body:       `{"data":{"data":{"password":{"value":"secret-material"}}}}`,
			wantErr:    ErrInvalidResponse,
		},
		{
			name:       "authentication error",
			statusCode: http.StatusForbidden,
			body:       `{"errors":["token=test-token secret-material"]}`,
			wantErr:    ErrRequest,
		},
		{
			name:       "internal HTTP error",
			statusCode: http.StatusInternalServerError,
			body:       `{"errors":["secret-material"]}`,
			wantErr:    ErrRequest,
		},
		{
			name:       "path not found",
			statusCode: http.StatusNotFound,
			body:       `{"errors":["secret-material"]}`,
			wantErr:    ErrNotFound,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.statusCode)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			provider, err := New(server.URL, "test-token")
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			_, err = provider.Resolve(context.Background(), testReference())
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Resolve() error = %v, want %v", err, test.wantErr)
			}
			if strings.Contains(err.Error(), "secret-material") || strings.Contains(err.Error(), "test-token") {
				t.Errorf("Resolve() error exposed sensitive material: %q", err)
			}
		})
	}
}

func TestResolveRejectsInvalidReference(t *testing.T) {
	provider, err := New("https://bao.example", "test-token")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = provider.Resolve(context.Background(), secrets.Reference{
		Provider: "openbao",
		Mount:    "secret",
		Path:     "../private",
		Key:      "password",
	})
	if !errors.Is(err, secrets.ErrInvalidReference) {
		t.Fatalf("Resolve() error = %v, want %v", err, secrets.ErrInvalidReference)
	}
}

func TestResolveRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"data":{"data":{"password":"%s"}}}`, strings.Repeat("x", maxResponse))
	}))
	defer server.Close()
	provider, err := New(server.URL, "test-token")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = provider.Resolve(context.Background(), testReference())
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("Resolve() error = %v, want %v", err, ErrInvalidResponse)
	}
}

func TestResolveCanceledContext(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	provider, err := New(server.URL, "test-token")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, resolveErr := provider.Resolve(ctx, testReference())
		result <- resolveErr
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive request")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Resolve() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Resolve() did not return after context cancellation")
	}
}

func TestNewEndpointValidation(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		token    string
		wantErr  bool
	}{
		{name: "loopback HTTP allowed", endpoint: "http://127.0.0.1:8200", token: "token"},
		{name: "localhost HTTP allowed", endpoint: "http://localhost:8200", token: "token"},
		{name: "remote HTTPS allowed", endpoint: "https://bao.example:8200", token: "token"},
		{name: "remote HTTP rejected", endpoint: "http://bao.example:8200", token: "token", wantErr: true},
		{name: "endpoint credentials rejected", endpoint: "https://user@bao.example", token: "token", wantErr: true},
		{name: "endpoint query rejected", endpoint: "https://bao.example?token=secret", token: "token", wantErr: true},
		{name: "empty token rejected", endpoint: "https://bao.example", token: "", wantErr: true},
		{name: "whitespace token rejected", endpoint: "https://bao.example", token: " token", wantErr: true},
		{name: "header injection rejected", endpoint: "https://bao.example", token: "token\r\nX-Injected: yes", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.endpoint, test.token)
			if (err != nil) != test.wantErr {
				t.Fatalf("New() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func testReference() secrets.Reference {
	return secrets.Reference{
		Provider: "openbao",
		Mount:    "secret",
		Path:     "reksicherung/test",
		Key:      "password",
	}
}
