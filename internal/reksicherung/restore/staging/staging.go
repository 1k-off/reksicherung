// Package staging stores validated backup artifacts in an operator-managed
// directory shared between provider and SQL Server MCP services.
package staging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	KindBAK    = "bak"
	KindBACPAC = "bacpac"
	idBytes    = 16
)

var (
	ErrInvalidConfig = errors.New("invalid artifact staging configuration")
	ErrInvalidID     = errors.New("invalid staged artifact ID")
	ErrLimit         = errors.New("staged artifact exceeds configured limit")
	ErrStageFailed   = errors.New("artifact staging failed")
)

// Store is scoped to one pre-created operator-managed directory. It never
// changes directory permissions and never removes artifacts implicitly.
type Store struct {
	directory string
	maxBytes  int64
}

type Artifact struct {
	ID   string
	Kind string
	Size int64
}

// New validates an existing stage directory. The operator is responsible for
// mounting it into the provider and SQL Server MCP services with suitable
// ownership and permissions.
func New(directory string, maxBytes int64) (*Store, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || maxBytes < 1 {
		return nil, ErrInvalidConfig
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidConfig
	}
	return &Store{directory: directory, maxBytes: maxBytes}, nil
}

// Save copies one already-resolved database artifact to a random opaque ID.
// kind must be bak or bacpac. The source is always closed.
func (store *Store) Save(ctx context.Context, source io.ReadCloser, kind string) (_ Artifact, returnErr error) {
	if source == nil {
		return Artifact{}, ErrInvalidConfig
	}
	defer func() {
		if err := source.Close(); err != nil && returnErr == nil {
			returnErr = ErrStageFailed
		}
	}()
	if store == nil || ctx == nil || (kind != KindBAK && kind != KindBACPAC) {
		return Artifact{}, ErrInvalidConfig
	}
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}

	file, err := os.CreateTemp(store.directory, ".reksicherung-stage-*.tmp")
	if err != nil {
		return Artifact{}, ErrStageFailed
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(0640); err != nil {
		_ = file.Close()
		return Artifact{}, ErrStageFailed
	}
	written, copyErr := io.Copy(file, &limitedReader{ctx: ctx, source: source, remaining: store.maxBytes})
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written == 0 {
		if err := ctx.Err(); err != nil {
			return Artifact{}, err
		}
		if errors.Is(copyErr, ErrLimit) {
			return Artifact{}, ErrLimit
		}
		return Artifact{}, ErrStageFailed
	}
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}

	for attempts := 0; attempts < 3; attempts++ {
		id, err := newID()
		if err != nil {
			return Artifact{}, ErrStageFailed
		}
		finalPath := filepath.Join(store.directory, id+"."+kind)
		if err := os.Link(tempPath, finalPath); err == nil {
			return Artifact{ID: id, Kind: kind, Size: written}, nil
		} else if !errors.Is(err, os.ErrExist) {
			return Artifact{}, ErrStageFailed
		}
	}
	return Artifact{}, ErrStageFailed
}

// Open opens one artifact by opaque ID and validated kind. It rejects links,
// directories, non-canonical files, and files larger than the store limit.
func (store *Store) Open(id, kind string) (*os.File, Artifact, error) {
	if store == nil || !validID(id) || (kind != KindBAK && kind != KindBACPAC) {
		return nil, Artifact{}, ErrInvalidID
	}
	name := id + "." + kind
	path := filepath.Join(store.directory, name)
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > store.maxBytes {
		return nil, Artifact{}, ErrInvalidID
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, Artifact{}, ErrInvalidID
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || after.Size() != before.Size() {
		_ = file.Close()
		return nil, Artifact{}, ErrInvalidID
	}
	return file, Artifact{ID: id, Kind: kind, Size: after.Size()}, nil
}

func newID() (string, error) {
	var data [idBytes]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func validID(value string) bool {
	if len(value) != idBytes*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

type limitedReader struct {
	ctx       context.Context
	source    io.Reader
	remaining int64
}

func (reader *limitedReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	if reader.remaining < 0 {
		return 0, ErrLimit
	}
	if int64(len(buffer)) > reader.remaining+1 {
		buffer = buffer[:reader.remaining+1]
	}
	n, err := reader.source.Read(buffer)
	reader.remaining -= int64(n)
	if reader.remaining < 0 {
		return n, ErrLimit
	}
	return n, err
}
