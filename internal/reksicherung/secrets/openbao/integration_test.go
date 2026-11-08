//go:build integration

package openbao

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
)

func TestOpenBaoKVV2Integration(t *testing.T) {
	address := os.Getenv("OPENBAO_TEST_ADDR")
	token := os.Getenv("OPENBAO_TEST_TOKEN")
	if address == "" || token == "" {
		t.Skip("set OPENBAO_TEST_ADDR and OPENBAO_TEST_TOKEN to run against a local dev OpenBao")
	}
	if !isLoopbackEndpoint(address) {
		t.Fatal("OPENBAO_TEST_ADDR must point to a loopback OpenBao endpoint")
	}
	provider, err := New(address, token)
	if err != nil {
		t.Fatal("could not configure OpenBao test provider")
	}

	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		t.Fatal("could not generate an integration-test identifier")
	}
	id := hex.EncodeToString(random)
	secretPath := "reksicherung/integration/" + id
	dummyValue := "integration-dummy-" + id
	endpoint, err := url.Parse(address)
	if err != nil {
		t.Fatal("invalid OpenBao test address")
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelCleanup()
	defer func() {
		cleanupURL := *endpoint
		cleanupURL.Path = "/v1/secret/metadata/" + secretPath
		request, requestErr := http.NewRequestWithContext(cleanupCtx, http.MethodDelete, cleanupURL.String(), nil)
		if requestErr != nil {
			t.Error("could not prepare integration secret cleanup")
			return
		}
		request.Header.Set("X-Vault-Token", token)
		response, requestErr := client.Do(request)
		if requestErr != nil {
			t.Error("could not clean up integration secret")
			return
		}
		defer response.Body.Close()
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			t.Error("OpenBao did not confirm integration secret cleanup")
		}
	}()

	writeURL := *endpoint
	writeURL.Path = "/v1/secret/data/" + secretPath
	payload, err := json.Marshal(struct {
		Data map[string]string `json:"data"`
	}{Data: map[string]string{"dummy": dummyValue}})
	if err != nil {
		t.Fatal("could not encode integration secret")
	}
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelWrite()
	request, err := http.NewRequestWithContext(writeCtx, http.MethodPut, writeURL.String(), bytes.NewReader(payload))
	if err != nil {
		t.Fatal("could not prepare integration secret write")
	}
	request.Header.Set("X-Vault-Token", token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("could not write dummy secret to local OpenBao")
	}
	response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		t.Fatalf("OpenBao rejected integration secret write (HTTP %d)", response.StatusCode)
	}

	got, err := provider.Resolve(context.Background(), secrets.Reference{
		Provider: "openbao",
		Mount:    "secret",
		Path:     secretPath,
		Key:      "dummy",
	})
	if err != nil {
		t.Fatal("could not resolve dummy secret from local OpenBao")
	}
	if !bytes.Equal(got, []byte(dummyValue)) {
		t.Fatal("resolved dummy secret did not match the value written")
	}
}

func isLoopbackEndpoint(address string) bool {
	endpoint, err := url.Parse(address)
	if err != nil || endpoint.Host == "" || endpoint.User != nil ||
		(endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return false
	}
	host := endpoint.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
