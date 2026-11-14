// Package azure implements the shared storage contract using Azure Blob Storage.
package azure

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"

	"github.com/1k-off/reksicherung/internal/reksicherung/storage"
)

const (
	defaultMaxObjectSize      int64 = 1 << 30
	defaultMaxListPageSize    int32 = 1000
	maxObjectKeyBytes               = 1024
	maxContinuationTokenBytes       = 2048
	defaultEndpointSuffix           = "core.windows.net"
)

// Config contains explicit Azure connection settings. Credentials are supplied
// here only; the client does not use the ambient Azure credential chain unless
// service-principal fields are provided (ClientSecretCredential).
type Config struct {
	AccountName    string
	Container      string
	EndpointSuffix string

	AccountKey   string
	TenantID     string
	ClientID     string
	ClientSecret string

	MaxObjectSize   int64
	MaxListPageSize int32
}

type blobItem struct {
	Key          string
	Size         int64
	LastModified time.Time
}

type blobBackend interface {
	List(ctx context.Context, prefix, marker string, limit int32) (items []blobItem, nextMarker string, err error)
	Download(ctx context.Context, key string) (body io.ReadCloser, size int64, err error)
	Stat(ctx context.Context, key string) (size int64, mod time.Time, err error)
}

// Client implements storage.Store.
type Client struct {
	backend         blobBackend
	maxObjectSize   int64
	maxListPageSize int32
}

var _ storage.Store = (*Client)(nil)

// New constructs an Azure Blob client using account key or service principal.
func New(config Config) (*Client, error) {
	backend, err := newSDKBackend(config)
	if err != nil {
		return nil, err
	}
	return newClient(backend, config.MaxObjectSize, config.MaxListPageSize)
}

func newClient(backend blobBackend, maxObjectSize int64, maxListPageSize int32) (*Client, error) {
	if backend == nil {
		return nil, fmt.Errorf("storage azure: %w", storage.ErrInvalidInput)
	}
	if maxObjectSize == 0 {
		maxObjectSize = defaultMaxObjectSize
	}
	if maxObjectSize < 1 {
		return nil, fmt.Errorf("storage azure: %w", storage.ErrInvalidInput)
	}
	if maxListPageSize == 0 {
		maxListPageSize = defaultMaxListPageSize
	}
	if maxListPageSize < 1 || maxListPageSize > defaultMaxListPageSize {
		return nil, fmt.Errorf("storage azure: %w", storage.ErrInvalidInput)
	}
	return &Client{backend: backend, maxObjectSize: maxObjectSize, maxListPageSize: maxListPageSize}, nil
}

func newSDKBackend(config Config) (blobBackend, error) {
	if !validAccountName(config.AccountName) || !validContainer(config.Container) {
		return nil, fmt.Errorf("storage azure: %w", storage.ErrInvalidInput)
	}
	suffix := config.EndpointSuffix
	if suffix == "" {
		suffix = defaultEndpointSuffix
	}
	if !validEndpointSuffix(suffix) {
		return nil, fmt.Errorf("storage azure: %w", storage.ErrInvalidInput)
	}

	hasKey := strings.TrimSpace(config.AccountKey) != ""
	hasSP := config.TenantID != "" || config.ClientID != "" || config.ClientSecret != ""
	completeSP := config.TenantID != "" && config.ClientID != "" && config.ClientSecret != ""
	serviceURL := fmt.Sprintf("https://%s.blob.%s/", config.AccountName, suffix)

	var service *azblob.Client
	switch {
	case hasKey && hasSP:
		return nil, fmt.Errorf("storage azure: %w", storage.ErrInvalidInput)
	case hasKey:
		cred, err := azblob.NewSharedKeyCredential(config.AccountName, config.AccountKey)
		if err != nil {
			return nil, fmt.Errorf("storage azure: %w", storage.ErrInvalidInput)
		}
		service, err = azblob.NewClientWithSharedKeyCredential(serviceURL, cred, nil)
		if err != nil {
			return nil, fmt.Errorf("storage azure: %w", storage.ErrInvalidInput)
		}
	case completeSP:
		cred, err := azidentity.NewClientSecretCredential(config.TenantID, config.ClientID, config.ClientSecret, nil)
		if err != nil {
			return nil, fmt.Errorf("storage azure: %w", storage.ErrInvalidInput)
		}
		service, err = azblob.NewClient(serviceURL, cred, nil)
		if err != nil {
			return nil, fmt.Errorf("storage azure: %w", storage.ErrInvalidInput)
		}
	default:
		return nil, fmt.Errorf("storage azure: %w", storage.ErrInvalidInput)
	}

	return &sdkBackend{container: service.ServiceClient().NewContainerClient(config.Container)}, nil
}

type sdkBackend struct {
	container *container.Client
}

func (b *sdkBackend) List(ctx context.Context, prefix, marker string, limit int32) ([]blobItem, string, error) {
	pager := b.container.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{
		Prefix:     optionalString(prefix),
		Marker:     optionalString(marker),
		MaxResults: &limit,
	})
	if !pager.More() {
		return nil, "", nil
	}
	page, err := pager.NextPage(ctx)
	if err != nil {
		return nil, "", err
	}
	items := make([]blobItem, 0, len(page.Segment.BlobItems))
	for _, item := range page.Segment.BlobItems {
		if item == nil || item.Name == nil {
			return nil, "", fmt.Errorf("storage azure: list response contains invalid object metadata")
		}
		entry := blobItem{Key: *item.Name}
		if item.Properties != nil {
			if item.Properties.ContentLength != nil {
				entry.Size = *item.Properties.ContentLength
			}
			if item.Properties.LastModified != nil {
				entry.LastModified = *item.Properties.LastModified
			}
		}
		items = append(items, entry)
	}
	next := ""
	if page.NextMarker != nil {
		next = *page.NextMarker
	}
	return items, next, nil
}

func (b *sdkBackend) Download(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	resp, err := b.container.NewBlobClient(key).DownloadStream(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	size := int64(-1)
	if resp.ContentLength != nil {
		size = *resp.ContentLength
	}
	return resp.Body, size, nil
}

func (b *sdkBackend) Stat(ctx context.Context, key string) (int64, time.Time, error) {
	props, err := b.container.NewBlobClient(key).GetProperties(ctx, nil)
	if err != nil {
		return 0, time.Time{}, err
	}
	size := int64(0)
	if props.ContentLength != nil {
		size = *props.ContentLength
	}
	mod := time.Time{}
	if props.LastModified != nil {
		mod = *props.LastModified
	}
	return size, mod, nil
}

// List returns one bounded page of objects under a relative prefix.
func (client *Client) List(ctx context.Context, options storage.ListOptions) (storage.ListPage, error) {
	if err := ctx.Err(); err != nil {
		return storage.ListPage{}, fmt.Errorf("storage azure: list canceled: %w", err)
	}
	if !validPrefix(options.Prefix) || !validContinuationToken(options.ContinuationToken) ||
		options.Limit < 1 || options.Limit > client.maxListPageSize {
		return storage.ListPage{}, fmt.Errorf("storage azure: %w", storage.ErrInvalidInput)
	}
	items, next, err := client.backend.List(ctx, options.Prefix, options.ContinuationToken, options.Limit)
	if err != nil {
		return storage.ListPage{}, translateError(ctx, "list", err)
	}
	if len(items) > int(options.Limit) {
		return storage.ListPage{}, fmt.Errorf("storage azure: list response exceeds requested page size")
	}
	page := storage.ListPage{Objects: make([]storage.Object, 0, len(items))}
	for _, item := range items {
		if !validKey(item.Key) || !strings.HasPrefix(item.Key, options.Prefix) || item.Size < 0 {
			return storage.ListPage{}, fmt.Errorf("storage azure: list response contains invalid object metadata")
		}
		page.Objects = append(page.Objects, storage.Object{
			Key: item.Key, Size: item.Size, LastModified: item.LastModified,
		})
	}
	if next != "" {
		if !validContinuationToken(next) {
			return storage.ListPage{}, fmt.Errorf("storage azure: list response contains invalid continuation token")
		}
		page.ContinuationToken = next
	}
	return page, nil
}

// Get opens a streaming object read bounded by MaxObjectSize.
func (client *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("storage azure: get canceled: %w", err)
	}
	if !validKey(key) {
		return nil, fmt.Errorf("storage azure: %w", storage.ErrInvalidInput)
	}
	body, size, err := client.backend.Download(ctx, key)
	if err != nil {
		return nil, translateError(ctx, "get", err)
	}
	if size >= 0 && size > client.maxObjectSize {
		if body != nil {
			_ = body.Close()
		}
		return nil, storage.ErrObjectTooLarge
	}
	return &boundedReadCloser{body: body, remaining: client.maxObjectSize}, nil
}

// Stat returns metadata for one object without reading its contents.
func (client *Client) Stat(ctx context.Context, key string) (storage.Object, error) {
	if err := ctx.Err(); err != nil {
		return storage.Object{}, fmt.Errorf("storage azure: stat canceled: %w", err)
	}
	if !validKey(key) {
		return storage.Object{}, fmt.Errorf("storage azure: %w", storage.ErrInvalidInput)
	}
	size, mod, err := client.backend.Stat(ctx, key)
	if err != nil {
		return storage.Object{}, translateError(ctx, "stat", err)
	}
	if size < 0 {
		return storage.Object{}, fmt.Errorf("storage azure: stat response contains invalid object metadata")
	}
	return storage.Object{Key: key, Size: size, LastModified: mod}, nil
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
	if reader.body == nil {
		return nil
	}
	return reader.body.Close()
}

func translateError(ctx context.Context, operation string, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return fmt.Errorf("storage azure: %s canceled: %w", operation, ctxErr)
	}
	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) && bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ContainerNotFound) {
		return storage.ErrObjectNotFound
	}
	// Head/get may surface 404 without typed bloberror in some paths.
	if errors.As(err, &respErr) && respErr.StatusCode == 404 {
		return storage.ErrObjectNotFound
	}
	return fmt.Errorf("storage azure: %s failed", operation)
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func validAccountName(value string) bool {
	if len(value) < 3 || len(value) > 24 {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func validContainer(value string) bool {
	if len(value) < 3 || len(value) > 63 || strings.Contains(value, "..") {
		return false
	}
	if value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func validEndpointSuffix(value string) bool {
	if value == "" || len(value) > 253 || strings.Contains(value, "..") || strings.ContainsAny(value, "/:\\ ") {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func validPrefix(value string) bool {
	if strings.HasPrefix(value, "/") || strings.Contains(value, `\`) || len(value) > maxObjectKeyBytes {
		return false
	}
	if strings.Contains(value, "..") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return utf8.ValidString(value)
}

func validKey(value string) bool {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, `\`) ||
		strings.Contains(value, "..") || len(value) > maxObjectKeyBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validContinuationToken(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > maxContinuationTokenBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
