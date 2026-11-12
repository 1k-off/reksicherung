// Package gobackup discovers GoBackup archive objects using the shared storage
// abstraction. It does not implement GoBackup's restore process.
package gobackup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/1k-off/reksicherung/internal/reksicherung/storage"
)

const (
	defaultPageSize           int32 = 100
	maxPageSize               int32 = 1000
	defaultMaxBackups               = 10000
	maxBackups                      = 100000
	defaultMaxPages                 = 100
	maxPages                        = 1000
	defaultMaxObjectBytes           = int64(1 << 30)
	maxObjectKeyBytes               = 1024
	maxContinuationTokenBytes       = 2048
)

var (
	// ErrInvalidConfig indicates that the service configuration is incomplete
	// or outside its safety bounds.
	ErrInvalidConfig = errors.New("gobackup: invalid configuration")
	// ErrInvalidBackupID indicates that a selection is not a safe relative key
	// under the configured prefix.
	ErrInvalidBackupID = errors.New("gobackup: invalid backup ID")
	// ErrBackupLimit indicates that listing exceeded its configured object or
	// page limit and could not establish a complete inventory.
	ErrBackupLimit = errors.New("gobackup: backup listing limit exceeded")
	// ErrPaginationLoop indicates that the storage provider repeated a
	// continuation token.
	ErrPaginationLoop = errors.New("gobackup: pagination token repeated")
	// ErrObjectTooLarge indicates that a streamed archive exceeded its limit.
	ErrObjectTooLarge = errors.New("gobackup: object exceeds size limit")
)

// Config bounds discovery and retrieval. Prefix is required and must be a
// safe relative directory prefix ending in "/".
type Config struct {
	Prefix string

	// PageSize bounds each Store.List call. Zero selects 100; the maximum is 1000.
	PageSize int32
	// MaxBackups bounds the total number of listed objects, including directory
	// markers. Zero selects 10,000; the maximum is 100,000.
	MaxBackups int
	// MaxPages independently bounds Store.List calls. Zero selects 100;
	// the maximum is 1,000.
	MaxPages int
	// MaxObjectBytes bounds bytes returned by Open. Zero selects 1 GiB.
	MaxObjectBytes int64
}

// Backup contains an object's key relative to the configured prefix and its
// storage metadata. ID is the stable selection input accepted by Select/Open.
type Backup struct {
	ID           string
	Size         int64
	LastModified time.Time
}

// Service provides bounded GoBackup archive discovery and streaming access.
type Service struct {
	store  storage.Store
	config Config
}

// New creates a service with explicit prefix scoping and finite operational
// limits.
func New(store storage.Store, config Config) (*Service, error) {
	if store == nil || !validPrefix(config.Prefix) {
		return nil, ErrInvalidConfig
	}
	if config.PageSize == 0 {
		config.PageSize = defaultPageSize
	}
	if config.MaxBackups == 0 {
		config.MaxBackups = defaultMaxBackups
	}
	if config.MaxPages == 0 {
		config.MaxPages = defaultMaxPages
	}
	if config.MaxObjectBytes == 0 {
		config.MaxObjectBytes = defaultMaxObjectBytes
	}
	if config.PageSize < 1 || config.PageSize > maxPageSize ||
		config.MaxBackups < 1 || config.MaxBackups > maxBackups ||
		config.MaxPages < 1 || config.MaxPages > maxPages ||
		config.MaxObjectBytes < 1 {
		return nil, ErrInvalidConfig
	}
	return &Service{store: store, config: config}, nil
}

// List returns all discoverable backups under the configured prefix, ordered
// by LastModified descending and then ID ascending. It returns an error rather
// than a partial list if any listing limit is reached.
func (service *Service) List(ctx context.Context) ([]Backup, error) {
	objects := make([]Backup, 0)
	seenKeys := make(map[string]struct{})
	seenTokens := map[string]struct{}{"": {}}
	token := ""

	for pageNumber := 0; ; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("gobackup: list canceled: %w", err)
		}
		if pageNumber >= service.config.MaxPages {
			return nil, ErrBackupLimit
		}
		page, err := service.store.List(ctx, storage.ListOptions{
			Prefix:            service.config.Prefix,
			ContinuationToken: token,
			Limit:             service.config.PageSize,
		})
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("gobackup: list canceled: %w", ctxErr)
		}
		if err != nil {
			return nil, fmt.Errorf("gobackup: list page %d: %w", pageNumber+1, err)
		}
		if len(page.Objects) > int(service.config.PageSize) {
			return nil, fmt.Errorf("gobackup: storage returned more than one page")
		}
		for _, object := range page.Objects {
			if !validListedKey(object.Key, service.config.Prefix) {
				return nil, fmt.Errorf("gobackup: storage returned an unsafe or out-of-prefix key")
			}
			if _, exists := seenKeys[object.Key]; exists {
				return nil, fmt.Errorf("gobackup: storage returned a duplicate object key")
			}
			if len(seenKeys) >= service.config.MaxBackups {
				return nil, ErrBackupLimit
			}
			seenKeys[object.Key] = struct{}{}
			if object.Size < 0 {
				return nil, fmt.Errorf("gobackup: storage returned invalid object size")
			}
			if strings.HasSuffix(object.Key, "/") {
				continue
			}
			relativeID := strings.TrimPrefix(object.Key, service.config.Prefix)
			// GoBackup writes operational state under .gobackup-state/; it is not
			// a restoreable archive and must not win Latest() by mtime.
			if strings.HasPrefix(relativeID, ".gobackup-state/") {
				continue
			}
			objects = append(objects, Backup{
				ID:           relativeID,
				Size:         object.Size,
				LastModified: object.LastModified,
			})
		}
		if page.ContinuationToken == "" {
			break
		}
		if len(page.ContinuationToken) > maxContinuationTokenBytes || !utf8.ValidString(page.ContinuationToken) {
			return nil, fmt.Errorf("gobackup: storage returned an invalid continuation token")
		}
		for _, character := range page.ContinuationToken {
			if unicode.IsControl(character) {
				return nil, fmt.Errorf("gobackup: storage returned an invalid continuation token")
			}
		}
		if _, exists := seenTokens[page.ContinuationToken]; exists {
			return nil, ErrPaginationLoop
		}
		seenTokens[page.ContinuationToken] = struct{}{}
		token = page.ContinuationToken
	}

	sort.Slice(objects, func(left, right int) bool {
		if !objects[left].LastModified.Equal(objects[right].LastModified) {
			return objects[left].LastModified.After(objects[right].LastModified)
		}
		return objects[left].ID < objects[right].ID
	})
	return objects, nil
}

// Latest returns the deterministically newest backup, or storage.ErrObjectNotFound
// when the configured prefix contains no archive objects.
func (service *Service) Latest(ctx context.Context) (Backup, error) {
	backups, err := service.List(ctx)
	if err != nil {
		return Backup{}, err
	}
	if len(backups) == 0 {
		return Backup{}, storage.ErrObjectNotFound
	}
	return backups[0], nil
}

// Select looks up one explicit backup ID (the object key relative to Prefix)
// and returns metadata without reading object contents.
func (service *Service) Select(ctx context.Context, id string) (Backup, error) {
	key, err := service.objectKey(id)
	if err != nil {
		return Backup{}, err
	}
	if err := ctx.Err(); err != nil {
		return Backup{}, fmt.Errorf("gobackup: select canceled: %w", err)
	}
	object, err := service.store.Stat(ctx, key)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Backup{}, fmt.Errorf("gobackup: select canceled: %w", ctxErr)
	}
	if err != nil {
		return Backup{}, fmt.Errorf("gobackup: select backup: %w", err)
	}
	if object.Key != key || object.Size < 0 {
		return Backup{}, fmt.Errorf("gobackup: storage returned invalid object metadata")
	}
	return Backup{ID: id, Size: object.Size, LastModified: object.LastModified}, nil
}

// Open streams the exact object identified by a relative backup ID. The
// returned reader never emits more than MaxObjectBytes and detects an
// over-limit stream when the consumer reads beyond that boundary.
func (service *Service) Open(ctx context.Context, id string) (io.ReadCloser, error) {
	key, err := service.objectKey(id)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("gobackup: open canceled: %w", err)
	}
	reader, err := service.store.Get(ctx, key)
	if ctxErr := ctx.Err(); ctxErr != nil {
		if reader != nil {
			_ = reader.Close()
		}
		return nil, fmt.Errorf("gobackup: open canceled: %w", ctxErr)
	}
	if err != nil {
		return nil, fmt.Errorf("gobackup: open backup: %w", err)
	}
	if reader == nil {
		return nil, fmt.Errorf("gobackup: storage returned an empty reader")
	}
	return &boundedReader{ctx: ctx, reader: reader, remaining: service.config.MaxObjectBytes}, nil
}

func (service *Service) objectKey(id string) (string, error) {
	if !validRelativeID(id) || len(service.config.Prefix)+len(id) > maxObjectKeyBytes {
		return "", ErrInvalidBackupID
	}
	return service.config.Prefix + id, nil
}

func validPrefix(prefix string) bool {
	if prefix == "" || len(prefix) > maxObjectKeyBytes || !utf8.ValidString(prefix) ||
		!strings.HasSuffix(prefix, "/") || strings.HasPrefix(prefix, "/") ||
		strings.Contains(prefix, `\`) || hasDrivePrefix(prefix) {
		return false
	}
	return validSegments(strings.TrimSuffix(prefix, "/"))
}

func validRelativeID(id string) bool {
	return id != "" && len(id) <= maxObjectKeyBytes && !strings.HasSuffix(id, "/") &&
		!strings.HasPrefix(id, "/") && !strings.Contains(id, `\`) && !hasDrivePrefix(id) &&
		utf8.ValidString(id) &&
		validSegments(id)
}

func hasDrivePrefix(value string) bool {
	return len(value) >= 2 &&
		((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) &&
		value[1] == ':'
}

func validListedKey(key, prefix string) bool {
	if len(key) == 0 || len(key) > maxObjectKeyBytes || !utf8.ValidString(key) ||
		!strings.HasPrefix(key, prefix) || strings.HasPrefix(key, "/") || strings.Contains(key, `\`) {
		return false
	}
	if strings.HasSuffix(key, "/") {
		key = strings.TrimSuffix(key, "/")
	}
	return validSegments(key)
}

func validSegments(value string) bool {
	if value == "" {
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

type boundedReader struct {
	ctx       context.Context
	reader    io.ReadCloser
	remaining int64
	checked   bool
	tooLarge  bool
}

func (reader *boundedReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	if reader.tooLarge {
		return 0, ErrObjectTooLarge
	}
	if reader.remaining == 0 {
		if reader.checked {
			return 0, io.EOF
		}
		var probe [1]byte
		n, err := reader.reader.Read(probe[:])
		if n > 0 {
			reader.checked = true
			reader.tooLarge = true
			return 0, ErrObjectTooLarge
		}
		if errors.Is(err, io.EOF) {
			reader.checked = true
			return 0, io.EOF
		}
		return 0, err
	}
	if int64(len(buffer)) > reader.remaining {
		buffer = buffer[:reader.remaining]
	}
	n, err := reader.reader.Read(buffer)
	reader.remaining -= int64(n)
	return n, err
}

func (reader *boundedReader) Close() error {
	return reader.reader.Close()
}
