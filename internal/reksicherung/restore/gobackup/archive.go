// Package gobackup streams BACPAC files from GoBackup tar archives and imports
// them into an isolated SQL Server container with SqlPackage.
//
// Only plain tar and gzip-compressed tar (.tar.gz/.tgz) archives are supported.
// Encrypted and split archives are intentionally unsupported. The package does
// not extract archive paths or write BACPAC data to the host filesystem.
package gobackup

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// Format identifies the archive encoding.
type Format string

const (
	// FormatTar is an uncompressed tar archive.
	FormatTar Format = "tar"
	// FormatTarGzip is a gzip-compressed tar archive (.tar.gz or .tgz).
	FormatTarGzip Format = "tar.gz"
)

var (
	// ErrUnsupportedFormat indicates an unsupported archive format or compression.
	ErrUnsupportedFormat = errors.New("unsupported GoBackup archive format")
	// ErrLimitExceeded indicates that an archive or BACPAC limit was exceeded.
	ErrLimitExceeded = errors.New("archive size limit exceeded")
	// ErrUnsafePath indicates that an archive member has an unsafe or malformed path.
	ErrUnsafePath = errors.New("unsafe archive member path")
	// ErrNoBACPAC indicates that the archive contains no BACPAC member.
	ErrNoBACPAC = errors.New("archive contains no BACPAC member")
	// ErrMultipleBACPAC indicates that the archive contains more than one BACPAC member.
	ErrMultipleBACPAC = errors.New("archive contains multiple BACPAC members")
)

// Limits are maximum encoded archive, decoded archive, and BACPAC sizes in bytes.
// All values must be positive.
type Limits struct {
	MaxCompressedBytes   int64
	MaxUncompressedBytes int64
	MaxBACPACBytes       int64
}

// ExtractBACPAC streams the sole .bacpac member from archive to dst.
//
// It validates every member path and type, and consumes the complete archive
// before returning so byte limits and gzip checksums are verified. On an error
// discovered after streaming starts (for example, a duplicate BACPAC later in
// the archive), dst may already contain a partial or complete BACPAC.
func ExtractBACPAC(ctx context.Context, archive io.Reader, dst io.Writer, format Format, limits Limits) (result error) {
	if ctx == nil {
		return errors.New("context must not be nil")
	}
	if archive == nil {
		return errors.New("archive reader must not be nil")
	}
	if dst == nil {
		return errors.New("BACPAC writer must not be nil")
	}
	if limits.MaxCompressedBytes <= 0 || limits.MaxUncompressedBytes <= 0 || limits.MaxBACPACBytes <= 0 {
		return errors.New("all archive limits must be positive")
	}
	if format != FormatTar && format != FormatTarGzip {
		return fmt.Errorf("%w: %q", ErrUnsupportedFormat, format)
	}

	compressed := &budgetReader{ctx: ctx, reader: archive, limit: limits.MaxCompressedBytes, label: "compressed archive"}
	buffered := bufio.NewReader(compressed)
	var uncompressed io.Reader
	var gzipReader *gzip.Reader
	if format == FormatTarGzip {
		reader, err := gzip.NewReader(buffered)
		if err != nil {
			return fmt.Errorf("open gzip archive: %w", err)
		}
		gzipReader = reader
		defer func() {
			if err := gzipReader.Close(); err != nil {
				result = errors.Join(result, fmt.Errorf("close gzip archive: %w", err))
			}
		}()
		uncompressed = &budgetReader{ctx: ctx, reader: gzipReader, limit: limits.MaxUncompressedBytes, label: "uncompressed archive"}
	} else {
		if err := rejectKnownCompression(buffered); err != nil {
			return err
		}
		uncompressed = &budgetReader{ctx: ctx, reader: buffered, limit: limits.MaxUncompressedBytes, label: "uncompressed archive"}
	}

	tarReader := tar.NewReader(uncompressed)
	bacpacCount := 0
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar archive: %w", err)
		}
		if err := validateMemberPath(header.Name); err != nil {
			return err
		}
		if header.Size < 0 {
			return fmt.Errorf("invalid size for archive member %q: %d", header.Name, header.Size)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 {
				return fmt.Errorf("directory archive member %q has nonzero size %d", header.Name, header.Size)
			}
		case tar.TypeReg, tar.TypeRegA:
			if strings.EqualFold(path.Ext(header.Name), ".bacpac") {
				bacpacCount++
				if bacpacCount > 1 {
					return ErrMultipleBACPAC
				}
				if header.Size > limits.MaxBACPACBytes {
					return fmt.Errorf("%w: BACPAC is %d bytes (maximum %d)", ErrLimitExceeded, header.Size, limits.MaxBACPACBytes)
				}
				if _, err := io.CopyN(dst, tarReader, header.Size); err != nil {
					return fmt.Errorf("stream BACPAC member %q: %w", header.Name, err)
				}
			}
		default:
			return fmt.Errorf("unsupported non-regular archive member %q (type %d)", header.Name, header.Typeflag)
		}
	}

	// tar.Reader stops at the end-of-archive marker. Drain the underlying stream
	// to enforce whole-archive limits and, for gzip, verify its trailer checksum.
	if _, err := io.Copy(io.Discard, uncompressed); err != nil {
		return fmt.Errorf("finish archive stream: %w", err)
	}
	if bacpacCount == 0 {
		return ErrNoBACPAC
	}
	return nil
}

func validateMemberPath(name string) error {
	if name == "" || strings.ContainsRune(name, '\\') || strings.ContainsRune(name, '\x00') ||
		path.IsAbs(name) || (len(name) >= 2 && isASCIILetter(name[0]) && name[1] == ':') {
		return fmt.Errorf("%w: %q", ErrUnsafePath, name)
	}
	for _, component := range strings.Split(name, "/") {
		if component == ".." {
			return fmt.Errorf("%w: %q", ErrUnsafePath, name)
		}
	}
	return nil
}

func isASCIILetter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func rejectKnownCompression(reader *bufio.Reader) error {
	signature, _ := reader.Peek(6)
	if len(signature) >= 2 && signature[0] == 0x1f && signature[1] == 0x8b ||
		len(signature) >= 2 && signature[0] == 'P' && signature[1] == 'K' ||
		len(signature) >= 3 && string(signature[:3]) == "BZh" ||
		len(signature) >= 6 && string(signature[:6]) == "\xfd7zXZ\x00" ||
		len(signature) >= 4 && string(signature[:4]) == "\x28\xb5\x2f\xfd" {
		return fmt.Errorf("%w: detected compression is not plain tar", ErrUnsupportedFormat)
	}
	return nil
}

type budgetReader struct {
	ctx    context.Context
	reader io.Reader
	limit  int64
	read   int64
	label  string
}

func (reader *budgetReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	remaining := reader.limit - reader.read
	if remaining == 0 {
		var probe [1]byte
		n, err := reader.reader.Read(probe[:])
		if n > 0 {
			return 0, fmt.Errorf("%w: %s exceeds %d bytes", ErrLimitExceeded, reader.label, reader.limit)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, io.EOF
			}
			return 0, err
		}
		return 0, nil
	}
	if int64(len(buffer)) > remaining {
		buffer = buffer[:remaining]
	}
	n, err := reader.reader.Read(buffer)
	reader.read += int64(n)
	return n, err
}
