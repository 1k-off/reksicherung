// Package artifact resolves plain SQL backups and database artifacts nested in
// ZIP, tar, or gzip-compressed tar archives. It never extracts archive paths to
// disk; only one validated regular-file member is streamed to a private temp
// file at each nesting level.
package artifact

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// Kind identifies the final database artifact.
type Kind string

const (
	KindSQLBackup Kind = "bak"
	KindBACPAC    Kind = "bacpac"
)

const (
	defaultMaxNestingDepth = 8
	defaultMaxEntries      = 10000
)

var (
	ErrInvalidConfig    = errors.New("invalid artifact resolver configuration")
	ErrUnsupported      = errors.New("unsupported or unrecognized backup artifact")
	ErrInvalidArchive   = errors.New("invalid or unsafe backup archive")
	ErrAmbiguousArchive = errors.New("backup archive contains multiple candidate artifacts")
	ErrNoArtifact       = errors.New("backup archive contains no supported database artifact")
	ErrLimitExceeded    = errors.New("backup artifact exceeded a configured limit")
	ErrNestingTooDeep   = errors.New("backup archive nesting exceeds the configured limit")
)

// Limits bounds the initial input, decompressed data, total temporary disk
// usage, nesting depth, and entry count. MaxTemporaryBytes includes all
// simultaneously staged files, such as a ZIP plus the member being extracted.
type Limits struct {
	MaxInputBytes     int64
	MaxExpandedBytes  int64
	MaxTemporaryBytes int64
	MaxNestingDepth   int
	MaxEntries        int
	TempDir           string
}

// Resolve consumes source and returns a private, temporary file containing
// exactly one terminal .bak or BACPAC artifact. Supported wrappers are ZIP,
// tar, gzip-compressed tar, and arbitrary nesting of those formats. Member
// paths are inspected but never materialized, preventing traversal and link
// attacks. The returned reader owns and removes its temporary file on Close.
func Resolve(ctx context.Context, source io.ReadCloser, sourceName string, limits Limits) (io.ReadCloser, Kind, error) {
	if source == nil {
		return nil, "", ErrInvalidConfig
	}
	if ctx == nil {
		_ = source.Close()
		return nil, "", ErrInvalidConfig
	}
	defer source.Close()
	if limits.MaxInputBytes <= 0 || limits.MaxExpandedBytes <= 0 || limits.MaxTemporaryBytes <= 0 {
		return nil, "", ErrInvalidConfig
	}
	if limits.MaxNestingDepth == 0 {
		limits.MaxNestingDepth = defaultMaxNestingDepth
	}
	if limits.MaxEntries == 0 {
		limits.MaxEntries = defaultMaxEntries
	}
	if limits.MaxNestingDepth < 1 || limits.MaxNestingDepth > 32 ||
		limits.MaxEntries < 1 || limits.MaxEntries > 100000 {
		return nil, "", ErrInvalidConfig
	}

	manager := &tempManager{ctx: ctx, limits: limits, files: make(map[string]*tempFile)}
	keep := false
	defer func() {
		if !keep {
			manager.cleanup()
		}
	}()

	current, err := manager.newFile(limits.MaxInputBytes)
	if err != nil {
		return nil, "", err
	}
	if err := copyToTemp(ctx, source, current, limits.MaxInputBytes); err != nil {
		return nil, "", err
	}
	if err := current.closeWriter(); err != nil {
		return nil, "", ErrInvalidArchive
	}

	currentName := sourceName
	for depth := 0; ; depth++ {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		archiveType, finalKind, err := manager.detect(current, currentName)
		if err != nil {
			return nil, "", err
		}
		if archiveType == archiveNone {
			reader, err := current.open()
			if err != nil {
				return nil, "", ErrInvalidArchive
			}
			keep = true
			return &resolvedFile{File: reader, temp: current}, finalKind, nil
		}
		if depth >= limits.MaxNestingDepth {
			return nil, "", ErrNestingTooDeep
		}

		var next *tempFile
		var nextName string
		switch archiveType {
		case archiveZIP:
			next, nextName, err = manager.extractZIP(current)
		case archiveTar:
			next, nextName, err = manager.extractTar(current, false)
		case archiveTarGzip:
			next, nextName, err = manager.extractTar(current, true)
		default:
			err = ErrUnsupported
		}
		if err != nil {
			return nil, "", err
		}
		if err := current.remove(); err != nil {
			_ = next.remove()
			return nil, "", ErrInvalidArchive
		}
		current, currentName = next, nextName
		if err := current.closeWriter(); err != nil {
			return nil, "", ErrInvalidArchive
		}
	}
}

type archiveType uint8

const (
	archiveNone archiveType = iota
	archiveZIP
	archiveTar
	archiveTarGzip
)

type tempManager struct {
	ctx       context.Context
	limits    Limits
	tempBytes int64
	expanded  int64
	files     map[string]*tempFile
}

type tempFile struct {
	manager  *tempManager
	file     *os.File
	path     string
	size     int64
	maxBytes int64
	removed  bool
}

func (manager *tempManager) newFile(maxBytes int64) (*tempFile, error) {
	file, err := os.CreateTemp(manager.limits.TempDir, "reksicherung-artifact-*")
	if err != nil {
		return nil, ErrInvalidConfig
	}
	result := &tempFile{manager: manager, file: file, path: file.Name(), maxBytes: maxBytes}
	manager.files[result.path] = result
	return result, nil
}

func (file *tempFile) Write(data []byte) (int, error) {
	if err := file.manager.ctx.Err(); err != nil {
		return 0, err
	}
	if file.file == nil || file.removed {
		return 0, os.ErrClosed
	}
	remainingFile := file.maxBytes - file.size
	remainingTotal := file.manager.limits.MaxTemporaryBytes - file.manager.tempBytes
	allowed := remainingFile
	if remainingTotal < allowed {
		allowed = remainingTotal
	}
	if allowed <= 0 {
		return 0, ErrLimitExceeded
	}
	if int64(len(data)) > allowed {
		written, err := file.file.Write(data[:allowed])
		file.size += int64(written)
		file.manager.tempBytes += int64(written)
		if err != nil {
			return written, err
		}
		return written, ErrLimitExceeded
	}
	written, err := file.file.Write(data)
	file.size += int64(written)
	file.manager.tempBytes += int64(written)
	return written, err
}

func (file *tempFile) closeWriter() error {
	if file.file == nil {
		return nil
	}
	err := file.file.Close()
	file.file = nil
	return err
}

func (file *tempFile) open() (*os.File, error) {
	if file.removed {
		return nil, os.ErrNotExist
	}
	return os.Open(file.path)
}

func (file *tempFile) remove() error {
	if file.removed {
		return nil
	}
	closeErr := file.closeWriter()
	removeErr := os.Remove(file.path)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	file.manager.tempBytes -= file.size
	delete(file.manager.files, file.path)
	file.removed = true
	return errors.Join(closeErr, removeErr)
}

func (manager *tempManager) cleanup() {
	for _, file := range manager.files {
		_ = file.remove()
	}
}

func (manager *tempManager) detect(file *tempFile, name string) (archiveType, Kind, error) {
	lower := strings.ToLower(name)
	extension := path.Ext(lower)
	reader, err := file.open()
	if err != nil {
		return archiveNone, "", ErrInvalidArchive
	}
	defer reader.Close()

	signature := make([]byte, 512)
	n, readErr := reader.Read(signature)
	signature = signature[:n]
	isZIP := hasZIPSignature(signature)
	isGzip := n >= 2 && signature[0] == 0x1f && signature[1] == 0x8b
	isTar := n >= 262 && string(signature[257:262]) == "ustar"

	if isZIP {
		isBACPAC, err := manager.isBACPAC(file)
		if err != nil {
			return archiveNone, "", err
		}
		if isBACPAC {
			return archiveNone, KindBACPAC, nil
		}
		return archiveZIP, "", nil
	}
	if isGzip {
		return archiveTarGzip, "", nil
	}
	if isTar || extension == ".tar" {
		return archiveTar, "", nil
	}
	if errors.Is(readErr, io.EOF) || readErr == nil {
		switch extension {
		case ".bak":
			return archiveNone, KindSQLBackup, nil
		case ".bacpac":
			return archiveNone, KindBACPAC, nil
		}
		if isArchiveName(lower) {
			return archiveNone, "", ErrInvalidArchive
		}
		// File naming is only a hint. A non-archive payload with an unfamiliar
		// extension is passed to SQL Server, whose HEADERONLY check validates it.
		return archiveNone, KindSQLBackup, nil
	}
	return archiveNone, "", ErrInvalidArchive
}

func (manager *tempManager) extractZIP(parent *tempFile) (*tempFile, string, error) {
	reader, err := parent.open()
	if err != nil {
		return nil, "", ErrInvalidArchive
	}
	defer reader.Close()
	info, err := reader.Stat()
	if err != nil {
		return nil, "", ErrInvalidArchive
	}
	zipReader, err := zip.NewReader(reader, info.Size())
	if err != nil {
		return nil, "", ErrInvalidArchive
	}
	if len(zipReader.File) > manager.limits.MaxEntries {
		return nil, "", ErrLimitExceeded
	}

	candidates := make([]*zip.File, 0, 1)
	var declaredExpanded int64
	for _, member := range zipReader.File {
		if err := validateMemberPath(member.Name); err != nil {
			return nil, "", err
		}
		mode := member.Mode()
		if mode&os.ModeSymlink != 0 || (!member.FileInfo().IsDir() && !mode.IsRegular() && mode.Type() != 0) {
			return nil, "", ErrInvalidArchive
		}
		if member.Flags&1 != 0 {
			return nil, "", ErrUnsupported
		}
		if member.UncompressedSize64 > uint64(manager.limits.MaxExpandedBytes) ||
			declaredExpanded > manager.limits.MaxExpandedBytes-int64(member.UncompressedSize64) {
			return nil, "", ErrLimitExceeded
		}
		declaredExpanded += int64(member.UncompressedSize64)
		if !member.FileInfo().IsDir() && isCandidateName(member.Name) {
			candidates = append(candidates, member)
		}
	}
	if declaredExpanded > manager.limits.MaxExpandedBytes-manager.expanded {
		return nil, "", ErrLimitExceeded
	}
	if len(candidates) > 1 {
		return nil, "", ErrAmbiguousArchive
	}
	if len(candidates) == 0 {
		return nil, "", ErrNoArtifact
	}

	member := candidates[0]
	if member.UncompressedSize64 > uint64(manager.limits.MaxExpandedBytes-manager.expanded) ||
		member.UncompressedSize64 > uint64(manager.limits.MaxTemporaryBytes) {
		return nil, "", ErrLimitExceeded
	}
	source, err := member.Open()
	if err != nil {
		return nil, "", ErrInvalidArchive
	}
	defer source.Close()
	output, err := manager.newFile(int64(member.UncompressedSize64))
	if err != nil {
		return nil, "", err
	}
	counted := &expandedReader{manager: manager, reader: source}
	copied, copyErr := io.Copy(output, counted)
	if copyErr != nil {
		_ = output.remove()
		if errors.Is(copyErr, ErrLimitExceeded) {
			return nil, "", ErrLimitExceeded
		}
		return nil, "", ErrInvalidArchive
	}
	if copied != int64(member.UncompressedSize64) {
		_ = output.remove()
		return nil, "", ErrInvalidArchive
	}
	if err := output.closeWriter(); err != nil {
		_ = output.remove()
		return nil, "", ErrInvalidArchive
	}
	return output, member.Name, nil
}

func (manager *tempManager) extractTar(parent *tempFile, compressed bool) (*tempFile, string, error) {
	file, err := parent.open()
	if err != nil {
		return nil, "", ErrInvalidArchive
	}
	defer file.Close()

	var source io.Reader = file
	var gzipReader *gzip.Reader
	if compressed {
		buffered := bufio.NewReader(file)
		gzipReader, err = gzip.NewReader(buffered)
		if err != nil {
			return nil, "", ErrInvalidArchive
		}
		defer gzipReader.Close()
		source = gzipReader
	}
	counted := &expandedReader{manager: manager, reader: source}
	tarReader := tar.NewReader(counted)
	var selected *tempFile
	var selectedName string
	entryCount := 0
	var declaredExpanded int64
	for {
		if err := manager.ctx.Err(); err != nil {
			if selected != nil {
				_ = selected.remove()
			}
			return nil, "", err
		}
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if selected != nil {
				_ = selected.remove()
			}
			return nil, "", ErrInvalidArchive
		}
		entryCount++
		if entryCount > manager.limits.MaxEntries {
			if selected != nil {
				_ = selected.remove()
			}
			return nil, "", ErrLimitExceeded
		}
		if err := validateMemberPath(header.Name); err != nil {
			if selected != nil {
				_ = selected.remove()
			}
			return nil, "", err
		}
		if header.Size < 0 || header.Size > manager.limits.MaxExpandedBytes ||
			declaredExpanded > manager.limits.MaxExpandedBytes-header.Size {
			if selected != nil {
				_ = selected.remove()
			}
			return nil, "", ErrLimitExceeded
		}
		declaredExpanded += header.Size
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 {
				if selected != nil {
					_ = selected.remove()
				}
				return nil, "", ErrInvalidArchive
			}
		case tar.TypeReg, tar.TypeRegA:
			if !isCandidateName(header.Name) {
				continue
			}
			if selected != nil {
				_ = selected.remove()
				return nil, "", ErrAmbiguousArchive
			}
			if header.Size > manager.limits.MaxExpandedBytes-manager.expanded ||
				header.Size > manager.limits.MaxTemporaryBytes {
				return nil, "", ErrLimitExceeded
			}
			selected, err = manager.newFile(header.Size)
			if err != nil {
				return nil, "", err
			}
			if _, err := io.CopyN(selected, tarReader, header.Size); err != nil {
				_ = selected.remove()
				return nil, "", ErrInvalidArchive
			}
			selectedName = header.Name
		default:
			if selected != nil {
				_ = selected.remove()
			}
			return nil, "", ErrInvalidArchive
		}
	}
	if _, err := io.Copy(io.Discard, counted); err != nil {
		if selected != nil {
			_ = selected.remove()
		}
		if errors.Is(err, ErrLimitExceeded) {
			return nil, "", ErrLimitExceeded
		}
		return nil, "", ErrInvalidArchive
	}
	if selected == nil {
		return nil, "", ErrNoArtifact
	}
	if err := selected.closeWriter(); err != nil {
		_ = selected.remove()
		return nil, "", ErrInvalidArchive
	}
	return selected, selectedName, nil
}

func (manager *tempManager) countExpanded(n int) error {
	if n <= 0 {
		return nil
	}
	if int64(n) > manager.limits.MaxExpandedBytes-manager.expanded {
		return ErrLimitExceeded
	}
	manager.expanded += int64(n)
	return nil
}

type expandedReader struct {
	manager *tempManager
	reader  io.Reader
}

func (reader *expandedReader) Read(buffer []byte) (int, error) {
	if err := reader.manager.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := reader.reader.Read(buffer)
	if countErr := reader.manager.countExpanded(n); countErr != nil {
		return 0, countErr
	}
	return n, err
}

func copyToTemp(ctx context.Context, source io.Reader, destination *tempFile, maxBytes int64) error {
	buffer := make([]byte, 64*1024)
	var copied int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := source.Read(buffer)
		if n > 0 {
			if int64(n) > maxBytes-copied {
				return ErrLimitExceeded
			}
			written, writeErr := destination.Write(buffer[:n])
			copied += int64(written)
			if writeErr != nil {
				if errors.Is(writeErr, ErrLimitExceeded) {
					return ErrLimitExceeded
				}
				return ErrInvalidArchive
			}
			if written != n {
				return ErrInvalidArchive
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

func validateMemberPath(name string) error {
	if name == "" || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") ||
		(len(name) >= 2 && isASCIILetter(name[0]) && name[1] == ':') {
		return ErrInvalidArchive
	}
	for _, component := range strings.Split(name, "/") {
		if component == ".." {
			return ErrInvalidArchive
		}
	}
	return nil
}

func isCandidateName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".bak") || strings.HasSuffix(lower, ".bacpac") ||
		strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".tar") ||
		strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz") ||
		strings.HasSuffix(lower, ".gz")
}

func isArchiveName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".tar") ||
		strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz") ||
		strings.HasSuffix(lower, ".gz")
}

func hasZIPSignature(signature []byte) bool {
	return len(signature) >= 4 &&
		(string(signature[:4]) == "PK\x03\x04" || string(signature[:4]) == "PK\x05\x06" ||
			string(signature[:4]) == "PK\x07\x08")
}

func (manager *tempManager) isBACPAC(file *tempFile) (bool, error) {
	reader, err := file.open()
	if err != nil {
		return false, ErrInvalidArchive
	}
	defer reader.Close()
	info, err := reader.Stat()
	if err != nil {
		return false, ErrInvalidArchive
	}
	zipReader, err := zip.NewReader(reader, info.Size())
	if err != nil {
		return false, nil
	}
	foundModel, foundOrigin := false, false
	var declaredExpanded int64
	seenNames := make(map[string]struct{}, len(zipReader.File))
	if len(zipReader.File) > manager.limits.MaxEntries {
		return false, ErrLimitExceeded
	}
	for _, member := range zipReader.File {
		if err := validateMemberPath(member.Name); err != nil {
			return false, err
		}
		nameKey := strings.ToLower(member.Name)
		if _, exists := seenNames[nameKey]; exists {
			return false, ErrInvalidArchive
		}
		seenNames[nameKey] = struct{}{}
		mode := member.Mode()
		if mode&os.ModeSymlink != 0 || (!member.FileInfo().IsDir() && !mode.IsRegular() && mode.Type() != 0) {
			return false, ErrInvalidArchive
		}
		if member.Flags&1 != 0 {
			return false, ErrUnsupported
		}
		if member.FileInfo().IsDir() && member.UncompressedSize64 != 0 {
			return false, ErrInvalidArchive
		}
		if member.UncompressedSize64 > uint64(manager.limits.MaxExpandedBytes) ||
			declaredExpanded > manager.limits.MaxExpandedBytes-int64(member.UncompressedSize64) {
			return false, ErrLimitExceeded
		}
		declaredExpanded += int64(member.UncompressedSize64)
		switch strings.ToLower(member.Name) {
		case "model.xml":
			foundModel = true
		case "origin.xml":
			foundOrigin = true
		}
	}
	return foundModel && foundOrigin, nil
}

func isASCIILetter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

type resolvedFile struct {
	*os.File
	temp *tempFile
}

func (file *resolvedFile) Close() error {
	closeErr := file.File.Close()
	removeErr := file.temp.remove()
	if closeErr != nil || removeErr != nil {
		return fmt.Errorf("close resolved artifact: %w", errors.Join(closeErr, removeErr))
	}
	return nil
}
