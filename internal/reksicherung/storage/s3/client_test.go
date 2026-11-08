package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/storage"
)

const (
	testAccessKey = "dummy-access-key"
	testSecretKey = "dummy-secret-key"
	testBucket    = "backup-bucket"
)

func newTestClient(t *testing.T, endpoint string, maxSize int64) *Client {
	t.Helper()
	client, err := New(Config{
		Endpoint:        endpoint,
		Region:          "us-east-1",
		Bucket:          testBucket,
		AccessKeyID:     testAccessKey,
		SecretAccessKey: testSecretKey,
		MaxObjectSize:   maxSize,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

func TestNewValidatesEndpointAndExplicitConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		config Config
	}{
		{name: "plaintext remote", config: validConfig("http://storage.example.com")},
		{name: "query", config: validConfig("https://storage.example.com/?x=1")},
		{name: "userinfo", config: validConfig("https://user:password@storage.example.com")},
		{name: "traversal endpoint path", config: validConfig("https://storage.example.com/../bucket")},
		{name: "invalid port", config: validConfig("https://storage.example.com:99999")},
		{name: "missing access key", config: Config{Endpoint: "https://storage.example.com", Region: "us-east-1", Bucket: testBucket, SecretAccessKey: testSecretKey}},
		{name: "missing secret", config: Config{Endpoint: "https://storage.example.com", Region: "us-east-1", Bucket: testBucket, AccessKeyID: testAccessKey}},
		{name: "invalid bucket", config: Config{Endpoint: "https://storage.example.com", Region: "us-east-1", Bucket: "../bucket", AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey}},
		{name: "invalid object size", config: Config{Endpoint: "https://storage.example.com", Region: "us-east-1", Bucket: testBucket, AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey, MaxObjectSize: -1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if client, err := New(test.config); err == nil {
				t.Fatalf("New() = %v, want error", client)
			}
		})
	}

	for _, endpoint := range []string{
		"http://localhost:9000",
		"http://127.0.0.1:9000",
		"http://[::1]:9000",
		"https://storage.example.com",
	} {
		t.Run("allowed "+endpoint, func(t *testing.T) {
			config := validConfig(endpoint)
			if _, err := New(config); err != nil {
				t.Fatalf("New() error = %v", err)
			}
		})
	}
}

func validConfig(endpoint string) Config {
	return Config{
		Endpoint:        endpoint,
		Region:          "us-east-1",
		Bucket:          testBucket,
		AccessKeyID:     testAccessKey,
		SecretAccessKey: testSecretKey,
	}
}

func TestClientListGetAndStat(t *testing.T) {
	const objectKey = "snapshots/weekly.bak"
	modified := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Errorf("request missing SigV4 authorization header")
		}
		if !strings.Contains(request.Header.Get("Authorization"), testAccessKey) {
			t.Errorf("request was not signed with the explicitly configured access key")
		}
		switch {
		case request.Method == http.MethodGet && request.URL.Query().Get("list-type") != "2":
			if request.URL.Path != "/"+testBucket+"/"+objectKey {
				t.Errorf("GET path = %q", request.URL.Path)
			}
			writer.Header().Set("Content-Length", "6")
			_, _ = io.WriteString(writer, "backup")
		case request.Method == http.MethodHead:
			writer.Header().Set("Content-Length", "6")
			writer.Header().Set("Last-Modified", modified.Format(http.TimeFormat))
			writer.WriteHeader(http.StatusOK)
		default:
			if request.Method != http.MethodGet || request.URL.Query().Get("list-type") != "2" {
				t.Errorf("list request = %s %s", request.Method, request.URL.String())
			}
			writer.Header().Set("Content-Type", "application/xml")
			_, _ = fmt.Fprintf(writer, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>%s</Name><Prefix>snapshots/</Prefix><KeyCount>1</KeyCount><MaxKeys>2</MaxKeys><IsTruncated>false</IsTruncated><Contents><Key>%s</Key><LastModified>%s</LastModified><ETag>"etag"</ETag><Size>6</Size><StorageClass>STANDARD</StorageClass></Contents></ListBucketResult>`, testBucket, objectKey, modified.Format(time.RFC3339))
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, 32)

	page, err := client.List(context.Background(), storage.ListOptions{Prefix: "snapshots/", Limit: 2})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page.Objects) != 1 || page.Objects[0].Key != objectKey || page.Objects[0].Size != 6 ||
		!page.Objects[0].LastModified.Equal(modified) || page.ContinuationToken != "" {
		t.Fatalf("List() = %#v", page)
	}

	reader, err := client.Get(context.Background(), objectKey)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	content, err := io.ReadAll(reader)
	if closeErr := reader.Close(); closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}
	if err != nil || string(content) != "backup" {
		t.Fatalf("Get() content/error = %q, %v", content, err)
	}

	object, err := client.Stat(context.Background(), objectKey)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if object.Key != objectKey || object.Size != 6 || !object.LastModified.Equal(modified) {
		t.Fatalf("Stat() = %#v", object)
	}
}

func TestListUsesCallerPageBoundsAndOpaqueContinuation(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Query().Get("max-keys") != "1" {
			t.Errorf("request = %s %s", request.Method, request.URL.String())
		}
		switch requests.Add(1) {
		case 1:
			if request.URL.Query().Get("continuation-token") != "" {
				t.Errorf("first page token = %q", request.URL.Query().Get("continuation-token"))
			}
			_, _ = io.WriteString(writer, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>opaque+token</NextContinuationToken><Contents><Key>one.bak</Key><Size>1</Size></Contents></ListBucketResult>`)
		case 2:
			if request.URL.Query().Get("continuation-token") != "opaque+token" {
				t.Errorf("next page token = %q", request.URL.Query().Get("continuation-token"))
			}
			_, _ = io.WriteString(writer, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>two.bak</Key><Size>2</Size></Contents></ListBucketResult>`)
		default:
			t.Errorf("unexpected extra list request")
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, 32)

	first, err := client.List(context.Background(), storage.ListOptions{Limit: 1})
	if err != nil {
		t.Fatalf("first List() error = %v", err)
	}
	if first.ContinuationToken != "opaque+token" {
		t.Fatalf("first continuation token = %q", first.ContinuationToken)
	}
	second, err := client.List(context.Background(), storage.ListOptions{Limit: 1, ContinuationToken: first.ContinuationToken})
	if err != nil {
		t.Fatalf("second List() error = %v", err)
	}
	if len(second.Objects) != 1 || second.Objects[0].Key != "two.bak" || second.ContinuationToken != "" {
		t.Fatalf("second page = %#v", second)
	}
}

func TestGetRejectsOversizeAndBoundsUnknownLengthStream(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantOnOpen bool
	}{
		{
			name: "declared size",
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Length", "6")
				_, _ = io.WriteString(writer, "123456")
			},
			wantOnOpen: true,
		},
		{
			name: "stream exceeds limit",
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				writer.(http.Flusher).Flush()
				_, _ = io.WriteString(writer, "123456")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			client := newTestClient(t, server.URL, 5)
			reader, err := client.Get(context.Background(), "backup.bak")
			if test.wantOnOpen {
				if !errors.Is(err, storage.ErrObjectTooLarge) || reader != nil {
					t.Fatalf("Get() = (%v, %v), want oversize error", reader, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			defer reader.Close()
			buffer := make([]byte, 8)
			_, err = reader.Read(buffer)
			if err != nil {
				t.Fatalf("first Read() error = %v", err)
			}
			_, err = reader.Read(buffer)
			if !errors.Is(err, storage.ErrObjectTooLarge) {
				t.Fatalf("second Read() error = %v, want size limit error", err)
			}
		})
	}
}

func TestBoundedReaderDoesNotTreatTemporaryNoProgressAsEOF(t *testing.T) {
	reader := &boundedReadCloser{body: &zeroThenDataReader{}, remaining: 0}
	buffer := make([]byte, 1)
	if n, err := reader.Read(buffer); n != 0 || err != nil {
		t.Fatalf("first Read() = (%d, %v), want temporary no progress", n, err)
	}
	if _, err := reader.Read(buffer); !errors.Is(err, storage.ErrObjectTooLarge) {
		t.Fatalf("second Read() error = %v, want size limit error", err)
	}
}

type zeroThenDataReader struct {
	calls int
}

func (reader *zeroThenDataReader) Read(buffer []byte) (int, error) {
	reader.calls++
	if reader.calls == 1 {
		return 0, nil
	}
	if reader.calls == 2 {
		return copy(buffer, "x"), nil
	}
	return 0, io.EOF
}

func (*zeroThenDataReader) Close() error {
	return nil
}

func TestOperationsHonorContextCancellation(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		<-request.Context().Done()
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, 32)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.List(canceled, storage.ListOptions{Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("List() error = %v, want context.Canceled", err)
	}
	if _, err := client.Get(canceled, "backup.bak"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get() error = %v, want context.Canceled", err)
	}
	if _, err := client.Stat(canceled, "backup.bak"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stat() error = %v, want context.Canceled", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("pre-canceled requests sent = %d, want 0", requests.Load())
	}

	inflight, cancelInflight := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.Stat(inflight, "backup.bak")
		done <- err
	}()
	deadline := time.After(2 * time.Second)
	for requests.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("request did not reach test server")
		case <-time.After(time.Millisecond):
		}
	}
	cancelInflight()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("in-flight Stat() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stat() did not return after cancellation")
	}
}

func TestNotFoundErrorsAreProviderNeutralAndSanitized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(writer, `<Error><Code>NoSuchKey</Code><Message>access key `+testAccessKey+` secret `+testSecretKey+`</Message></Error>`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, 32)
	_, err := client.Stat(context.Background(), "missing.bak")
	if !errors.Is(err, storage.ErrObjectNotFound) {
		t.Fatalf("Stat() error = %v, want ErrObjectNotFound", err)
	}
	if strings.Contains(err.Error(), testAccessKey) || strings.Contains(err.Error(), testSecretKey) {
		t.Fatalf("error disclosed credentials: %v", err)
	}
}

func TestListRejectsUnsafeReturnedKeysAndInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>../outside.bak</Key><Size>1</Size></Contents></ListBucketResult>`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, 32)
	if _, err := client.List(context.Background(), storage.ListOptions{Limit: 1}); err == nil {
		t.Fatal("List() accepted unsafe key in service response")
	}
	for _, options := range []storage.ListOptions{
		{Prefix: "../private", Limit: 1},
		{Prefix: "/absolute", Limit: 1},
		{Prefix: "safe", Limit: 0},
		{Prefix: "safe", Limit: 1001},
		{Prefix: "safe", Limit: 1, ContinuationToken: strings.Repeat("x", maxContinuationTokenBytes+1)},
	} {
		if _, err := client.List(context.Background(), options); !errors.Is(err, storage.ErrInvalidInput) {
			t.Errorf("List(%#v) error = %v, want ErrInvalidInput", options, err)
		}
	}
}

func TestListRejectsServerResponseLargerThanRequestedPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>one.bak</Key><Size>1</Size></Contents><Contents><Key>two.bak</Key><Size>2</Size></Contents></ListBucketResult>`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, 32)
	if _, err := client.List(context.Background(), storage.ListOptions{Limit: 1}); err == nil {
		t.Fatal("List() accepted more entries than its requested page limit")
	}
}

func TestListFailsClosedOnMalformedPageOrOutOfPrefixObjects(t *testing.T) {
	tests := []struct {
		name    string
		prefix  string
		payload string
	}{
		{name: "missing truncation state", payload: `<ListBucketResult><Contents><Key>backup.bak</Key><Size>1</Size></Contents></ListBucketResult>`},
		{name: "key outside requested prefix", prefix: "expected/", payload: `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>other/backup.bak</Key><Size>1</Size></Contents></ListBucketResult>`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(writer, test.payload)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, 32)
			if _, err := client.List(context.Background(), storage.ListOptions{Prefix: test.prefix, Limit: 1}); err == nil {
				t.Fatal("List() accepted malformed or out-of-prefix response")
			}
		})
	}
}

func TestKeysAndPrefixesAreSafeRelativePaths(t *testing.T) {
	for _, key := range []string{"backup.bak", "year/backup.bak", "数据库/backup.bak"} {
		if !validKey(key) {
			t.Errorf("validKey(%q) = false", key)
		}
	}
	for _, key := range []string{"", "/absolute", "../parent", "a/../b", "a//b", "a\\b", "a\nb", "a/."} {
		if validKey(key) {
			t.Errorf("validKey(%q) = true", key)
		}
	}
	for _, prefix := range []string{"", "backup", "year/", "year/2025/"} {
		if !validPrefix(prefix) {
			t.Errorf("validPrefix(%q) = false", prefix)
		}
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetRequests.Add(1)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	client := newTestClient(t, redirect.URL, 32)
	_, err := client.Stat(context.Background(), "backup.bak")
	if err == nil {
		t.Fatal("Stat() succeeded after redirect")
	}
	if targetRequests.Load() != 0 {
		t.Fatalf("redirect target requests = %d, want 0", targetRequests.Load())
	}
}

func TestValidateEndpointRejectsEncodedAmbiguousPaths(t *testing.T) {
	for _, endpoint := range []string{
		"http://127.0.0.1:9000/a%2fb",
		"http://127.0.0.1:9000/a/../b",
		"http://127.0.0.1:9000/a\\b",
		"http://127.0.0.1:9000//bucket",
	} {
		if _, err := validateEndpoint(endpoint); err == nil {
			t.Errorf("validateEndpoint(%q) succeeded", endpoint)
		}
	}
}

func TestTLSCertificateVerificationIsEnabled(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, 32)
	if _, err := client.Stat(context.Background(), "backup.bak"); err == nil {
		t.Fatal("Stat() succeeded with an untrusted TLS certificate")
	}
	if requests.Load() != 0 {
		t.Fatalf("requests reaching untrusted TLS server = %d, want 0", requests.Load())
	}
}

func TestURLParsingDoesNotAcceptRemoteHTTPByHostnameSuffix(t *testing.T) {
	for _, endpoint := range []string{"http://localhost.example:9000", "http://127.0.0.2.example"} {
		if _, err := validateEndpoint(endpoint); err == nil {
			t.Errorf("validateEndpoint(%q) succeeded", endpoint)
		}
	}
}
