package podman

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
)

const (
	artifactDirectory  = "/var/opt/mssql/backup"
	maxArtifactBytes   = int64(1 << 40)
	maxArtifactNameLen = 128
)

var ErrArtifactTooLarge = errors.New("artifact exceeds transfer byte limit")

// Transfer streams one artifact into the SQL Server backup directory without
// staging its contents on the host. Existing files with the same name are
// replaced only after the complete stream has been received. maxBytes must be
// positive and no greater than 1 TiB. The runtime takes ownership of source and
// closes it when the transfer ends or its context is canceled; source.Close
// must unblock a pending Read.
func (r *Runtime) Transfer(ctx context.Context, target container.Container, name string, source io.ReadCloser, maxBytes int64) error {
	if source == nil {
		return errors.New("artifact source must not be nil")
	}
	var closeOnce sync.Once
	closeSource := func() {
		closeOnce.Do(func() { _ = source.Close() })
	}
	defer closeSource()

	if !validContainerHandle(target) {
		return errors.New("invalid container reference")
	}
	if !validArtifactName(name) {
		return errors.New("invalid artifact name")
	}
	if maxBytes <= 0 || maxBytes > maxArtifactBytes {
		return fmt.Errorf("artifact byte limit must be between 1 and %d", maxArtifactBytes)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.verifyManaged(ctx, target); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			closeSource()
		case <-done:
		}
	}()
	defer close(done)

	tempName, err := temporaryArtifactName()
	if err != nil {
		return fmt.Errorf("generate temporary artifact name: %w", err)
	}
	tempPath := artifactDirectory + "/" + tempName
	destination := artifactDirectory + "/" + name
	if err := r.runner.Run(ctx, r.executable, []string{
		"exec", target.ID, "mkdir", "-p", "--", artifactDirectory,
	}, nil, io.Discard); err != nil {
		return fmt.Errorf("prepare in-container artifact directory: %w", err)
	}

	input := &boundedReader{source: source, remaining: maxBytes}
	streamErr := r.runner.Run(ctx, r.executable, []string{
		"exec", "--interactive", target.ID, "tee", "--", tempPath,
	}, input, io.Discard)
	if contextErr := ctx.Err(); contextErr != nil {
		streamErr = contextErr
	} else if input.exceeded {
		streamErr = ErrArtifactTooLarge
	}
	if streamErr != nil {
		cleanupErr := r.removeTemporaryArtifact(ctx, target.ID, tempPath)
		if errors.Is(streamErr, ErrArtifactTooLarge) {
			streamErr = fmt.Errorf("%w: limit is %s bytes", ErrArtifactTooLarge, strconv.FormatInt(maxBytes, 10))
		} else {
			streamErr = fmt.Errorf("stream artifact into container: %w", streamErr)
		}
		return errors.Join(streamErr, wrapIfError(cleanupErr, "remove partial in-container artifact"))
	}

	if err := r.runner.Run(ctx, r.executable, []string{
		"exec", target.ID, "mv", "-fT", "--", tempPath, destination,
	}, nil, io.Discard); err != nil {
		cleanupErr := r.removeTemporaryArtifact(ctx, target.ID, tempPath)
		return errors.Join(
			fmt.Errorf("publish in-container artifact: %w", err),
			wrapIfError(cleanupErr, "remove temporary in-container artifact"),
		)
	}
	return nil
}

func (r *Runtime) removeTemporaryArtifact(ctx context.Context, id, path string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), container.StopTimeout)
	defer cancel()
	if err := r.runner.Run(cleanupCtx, r.executable, []string{
		"exec", id, "rm", "-f", "--", path,
	}, nil, io.Discard); err != nil {
		return fmt.Errorf("remove temporary artifact: %w", err)
	}
	return nil
}

func temporaryArtifactName() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return ".reksicherung-upload-" + hex.EncodeToString(random), nil
}

func validArtifactName(name string) bool {
	return len(name) > 0 && len(name) <= maxArtifactNameLen &&
		name != "." && name != ".." &&
		!strings.HasPrefix(name, ".reksicherung-upload-") &&
		validRunSuffix(name)
}

type boundedReader struct {
	source    io.Reader
	remaining int64
	exceeded  bool
}

func (r *boundedReader) Read(buffer []byte) (int, error) {
	if r.remaining > 0 {
		if int64(len(buffer)) > r.remaining {
			buffer = buffer[:int(r.remaining)]
		}
		read, err := r.source.Read(buffer)
		r.remaining -= int64(read)
		return read, err
	}

	var probe [1]byte
	read, err := io.ReadFull(r.source, probe[:])
	if read > 0 {
		r.exceeded = true
		return 0, ErrArtifactTooLarge
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return 0, io.EOF
	}
	return 0, err
}
