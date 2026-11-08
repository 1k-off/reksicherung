// Package s3 implements the shared storage contract using an S3-compatible
// object store.
package s3

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/1k-off/reksicherung/internal/reksicherung/storage"
)

const (
	defaultMaxObjectSize      int64 = 1 << 30
	defaultMaxListPageSize    int32 = 1000
	maxObjectKeyBytes               = 1024
	maxContinuationTokenBytes       = 2048
)

// Config contains the minimum connection settings. Credentials are supplied
// explicitly; the implementation never consults environment variables,
// profiles, instance metadata, or another default credential chain.
type Config struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string

	// MaxObjectSize bounds every Get stream. Zero selects a 1 GiB default.
	MaxObjectSize int64
	// MaxListPageSize bounds caller-selected page sizes. Zero selects 1000.
	MaxListPageSize int32
}

// Client implements storage.Store.
type Client struct {
	api             *s3.Client
	bucket          string
	maxObjectSize   int64
	maxListPageSize int32
}

var _ storage.Store = (*Client)(nil)

// New constructs a client with TLS verification enabled for HTTPS endpoints
// and explicit static credentials only. Plaintext HTTP is permitted only for
// localhost or an IP address that is itself loopback.
func New(config Config) (*Client, error) {
	endpoint, err := validateEndpoint(config.Endpoint)
	if err != nil {
		return nil, err
	}
	if config.Region == "" || strings.TrimSpace(config.Region) != config.Region {
		return nil, fmt.Errorf("storage s3: %w", storage.ErrInvalidInput)
	}
	if !validBucket(config.Bucket) || !validCredential(config.AccessKeyID) || !validCredential(config.SecretAccessKey) {
		return nil, fmt.Errorf("storage s3: %w", storage.ErrInvalidInput)
	}
	if config.MaxObjectSize == 0 {
		config.MaxObjectSize = defaultMaxObjectSize
	}
	if config.MaxObjectSize < 1 {
		return nil, fmt.Errorf("storage s3: %w", storage.ErrInvalidInput)
	}
	if config.MaxListPageSize == 0 {
		config.MaxListPageSize = defaultMaxListPageSize
	}
	if config.MaxListPageSize < 1 || config.MaxListPageSize > defaultMaxListPageSize {
		return nil, fmt.Errorf("storage s3: %w", storage.ErrInvalidInput)
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			ForceAttemptHTTP2:     true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// Never forward signed requests or credentials to a redirect target.
			return http.ErrUseLastResponse
		},
	}
	awsConfig := aws.Config{
		Region:      config.Region,
		Credentials: aws.NewCredentialsCache(staticCredentials{accessKeyID: config.AccessKeyID, secretAccessKey: config.SecretAccessKey}),
		HTTPClient:  httpClient,
	}
	api := s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(endpoint.String())
		options.UsePathStyle = true
	})

	return &Client{
		api:             api,
		bucket:          config.Bucket,
		maxObjectSize:   config.MaxObjectSize,
		maxListPageSize: config.MaxListPageSize,
	}, nil
}

// List returns one bounded page of objects under a relative prefix.
func (client *Client) List(ctx context.Context, options storage.ListOptions) (storage.ListPage, error) {
	if err := ctx.Err(); err != nil {
		return storage.ListPage{}, fmt.Errorf("storage s3: list canceled: %w", err)
	}
	if !validPrefix(options.Prefix) || !validContinuationToken(options.ContinuationToken) ||
		options.Limit < 1 || options.Limit > client.maxListPageSize {
		return storage.ListPage{}, fmt.Errorf("storage s3: %w", storage.ErrInvalidInput)
	}

	output, err := client.api.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:            aws.String(client.bucket),
		Prefix:            optionalString(options.Prefix),
		ContinuationToken: optionalString(options.ContinuationToken),
		MaxKeys:           aws.Int32(options.Limit),
	})
	if err != nil {
		return storage.ListPage{}, translateError(ctx, "list", err)
	}
	if len(output.Contents) > int(options.Limit) {
		return storage.ListPage{}, fmt.Errorf("storage s3: list response exceeds requested page size")
	}

	page := storage.ListPage{Objects: make([]storage.Object, 0, len(output.Contents))}
	for _, item := range output.Contents {
		if item.Key == nil || !validKey(*item.Key) || !strings.HasPrefix(*item.Key, options.Prefix) ||
			item.Size == nil || *item.Size < 0 {
			return storage.ListPage{}, fmt.Errorf("storage s3: list response contains invalid object metadata")
		}
		object := storage.Object{Key: *item.Key, Size: *item.Size}
		if item.LastModified != nil {
			object.LastModified = *item.LastModified
		}
		page.Objects = append(page.Objects, object)
	}
	if output.IsTruncated == nil {
		return storage.ListPage{}, fmt.Errorf("storage s3: list response is missing truncation status")
	}
	if *output.IsTruncated {
		if output.NextContinuationToken == nil || !validContinuationToken(*output.NextContinuationToken) ||
			*output.NextContinuationToken == "" {
			return storage.ListPage{}, fmt.Errorf("storage s3: list response contains invalid continuation token")
		}
		page.ContinuationToken = *output.NextContinuationToken
	}
	return page, nil
}

// Get opens a streaming object read. The stream is bounded by MaxObjectSize;
// the object is rejected before returning a reader when its declared size
// already exceeds the limit.
func (client *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("storage s3: get canceled: %w", err)
	}
	if !validKey(key) {
		return nil, fmt.Errorf("storage s3: %w", storage.ErrInvalidInput)
	}
	output, err := client.api.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(client.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, translateError(ctx, "get", err)
	}
	if output.ContentLength != nil && (*output.ContentLength < 0 || *output.ContentLength > client.maxObjectSize) {
		_ = output.Body.Close()
		return nil, storage.ErrObjectTooLarge
	}
	return &boundedReadCloser{body: output.Body, remaining: client.maxObjectSize}, nil
}

// Stat returns metadata for one object without reading its contents.
func (client *Client) Stat(ctx context.Context, key string) (storage.Object, error) {
	if err := ctx.Err(); err != nil {
		return storage.Object{}, fmt.Errorf("storage s3: stat canceled: %w", err)
	}
	if !validKey(key) {
		return storage.Object{}, fmt.Errorf("storage s3: %w", storage.ErrInvalidInput)
	}
	output, err := client.api.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(client.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return storage.Object{}, translateError(ctx, "stat", err)
	}
	if output.ContentLength != nil && *output.ContentLength < 0 {
		return storage.Object{}, fmt.Errorf("storage s3: stat response contains invalid object metadata")
	}
	object := storage.Object{Key: key}
	if output.ContentLength != nil {
		object.Size = *output.ContentLength
	}
	if output.LastModified != nil {
		object.LastModified = *output.LastModified
	}
	return object, nil
}

type staticCredentials struct {
	accessKeyID     string
	secretAccessKey string
}

func (provider staticCredentials) Retrieve(context.Context) (aws.Credentials, error) {
	return aws.Credentials{
		AccessKeyID:     provider.accessKeyID,
		SecretAccessKey: provider.secretAccessKey,
		Source:          "explicit storage configuration",
	}, nil
}

type boundedReadCloser struct {
	body      io.ReadCloser
	remaining int64
	checked   bool
}

func (reader *boundedReadCloser) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if reader.remaining == 0 {
		if reader.checked {
			return 0, io.EOF
		}
		var probe [1]byte
		n, err := reader.body.Read(probe[:])
		if n > 0 {
			reader.checked = true
			return 0, storage.ErrObjectTooLarge
		}
		if errors.Is(err, io.EOF) {
			reader.checked = true
			return 0, err
		}
		if err != nil {
			return 0, err
		}
		return 0, nil
	}
	if int64(len(buffer)) > reader.remaining {
		buffer = buffer[:reader.remaining]
	}
	n, err := reader.body.Read(buffer)
	reader.remaining -= int64(n)
	return n, err
}

func (reader *boundedReadCloser) Close() error {
	return reader.body.Close()
}

func translateError(ctx context.Context, operation string, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("storage s3: %s canceled: %w", operation, ctxErr)
	}
	var apiError smithy.APIError
	if errors.As(err, &apiError) && (apiError.ErrorCode() == "NoSuchKey" || apiError.ErrorCode() == "NotFound") {
		return storage.ErrObjectNotFound
	}
	// Do not return SDK or server error text: it is outside our trust boundary
	// and could contain sensitive request or response data.
	return fmt.Errorf("storage s3: %s failed", operation)
}

func validateEndpoint(raw string) (*url.URL, error) {
	invalid := func() (*url.URL, error) {
		return nil, fmt.Errorf("storage s3: invalid endpoint")
	}
	if raw == "" || strings.TrimSpace(raw) != raw {
		return invalid()
	}
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Opaque != "" || endpoint.User != nil || endpoint.Hostname() == "" ||
		endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || endpoint.RawPath != "" {
		return invalid()
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return invalid()
	}
	if port := endpoint.Port(); port != "" {
		portNumber, parseErr := strconv.Atoi(port)
		if parseErr != nil || portNumber < 1 || portNumber > 65535 {
			return invalid()
		}
	}
	if endpoint.Scheme == "http" && !isLoopbackHost(endpoint.Hostname()) {
		return invalid()
	}
	if !validEndpointPath(endpoint.Path) {
		return invalid()
	}
	return endpoint, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validEndpointPath(value string) bool {
	if value == "" || value == "/" {
		return true
	}
	if !strings.HasPrefix(value, "/") || strings.Contains(value, "//") || strings.Contains(value, `\`) {
		return false
	}
	for _, segment := range strings.Split(strings.TrimPrefix(value, "/"), "/") {
		if segment == "" { // permit a single trailing slash
			continue
		}
		if segment == "." || segment == ".." {
			return false
		}
		for _, character := range segment {
			if unicode.IsControl(character) {
				return false
			}
		}
	}
	return utf8.ValidString(value)
}

func validBucket(value string) bool {
	if len(value) < 3 || len(value) > 63 || value[0] == '.' || value[len(value)-1] == '.' ||
		strings.Contains(value, "..") {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') ||
			character == '-' || character == '.') {
			return false
		}
	}
	if net.ParseIP(value) != nil {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
	}
	return true
}

func validCredential(value string) bool {
	if value == "" || len(value) > 1024 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validKey(value string) bool {
	if value == "" || len(value) > maxObjectKeyBytes || !utf8.ValidString(value) ||
		strings.HasPrefix(value, "/") || strings.Contains(value, `\`) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func validPrefix(value string) bool {
	if value == "" {
		return true
	}
	trimmed := strings.TrimSuffix(value, "/")
	if !validKey(trimmed) {
		return false
	}
	return true
}

func validContinuationToken(value string) bool {
	if len(value) > maxContinuationTokenBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return aws.String(value)
}
