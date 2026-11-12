package artifact

import (
	"context"
	"errors"
	"io"
	"path"
	"strings"
)

const maxListedCandidates = 32

// Candidate is metadata for one selectable archive member. It never includes
// file bytes.
type Candidate struct {
	Path     string
	Size     int64
	KindHint Kind
}

// CandidateList is a bounded set of terminal archive members.
type CandidateList struct {
	Candidates []Candidate
	Truncated  bool
}

// AmbiguousError is returned when multiple terminal .bak/.bacpac members exist
// and no MemberPath was supplied. Callers may inspect Candidates for AI or
// operator selection.
type AmbiguousError struct {
	Candidates []Candidate
	Truncated  bool
}

func (err *AmbiguousError) Error() string {
	return ErrAmbiguousArchive.Error()
}

func (err *AmbiguousError) Is(target error) bool {
	return target == ErrAmbiguousArchive
}

// ResolveOptions controls archive member selection.
type ResolveOptions struct {
	// MemberPath, when set, must match an exact archive member path (as returned
	// by ListCandidates). Only that member is extracted.
	MemberPath string
}

// Resolve consumes source and returns exactly one terminal database artifact.
// It is equivalent to ResolveWith with empty options.
func Resolve(ctx context.Context, source io.ReadCloser, sourceName string, limits Limits) (io.ReadCloser, Kind, error) {
	return ResolveWith(ctx, source, sourceName, limits, ResolveOptions{})
}

// ResolveWith is Resolve with optional exact member selection.
func ResolveWith(ctx context.Context, source io.ReadCloser, sourceName string, limits Limits, opts ResolveOptions) (io.ReadCloser, Kind, error) {
	if source == nil {
		return nil, "", ErrInvalidConfig
	}
	if ctx == nil {
		_ = source.Close()
		return nil, "", ErrInvalidConfig
	}
	defer source.Close()
	if err := validateLimits(&limits); err != nil {
		return nil, "", err
	}
	if opts.MemberPath != "" {
		if err := validateMemberPath(opts.MemberPath); err != nil {
			return nil, "", err
		}
		if !isTerminalArtifactName(opts.MemberPath) {
			return nil, "", ErrInvalidArchive
		}
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
			if opts.MemberPath != "" && opts.MemberPath != currentName && path.Base(opts.MemberPath) != path.Base(currentName) {
				return nil, "", ErrNoArtifact
			}
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
			next, nextName, err = manager.extractZIP(current, opts.MemberPath)
		case archiveTar:
			next, nextName, err = manager.extractTar(current, false, opts.MemberPath)
		case archiveTarGzip:
			next, nextName, err = manager.extractTar(current, true, opts.MemberPath)
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
		// Preferred member applies only at the first ambiguous archive level.
		opts.MemberPath = ""
	}
}

// ListCandidates inspects source and returns terminal .bak/.bacpac members at
// the first archive level that contains them. Wrapper-only levels are unwrapped
// when unique. Results are metadata only (no bytes).
func ListCandidates(ctx context.Context, source io.ReadCloser, sourceName string, limits Limits) (CandidateList, error) {
	if source == nil {
		return CandidateList{}, ErrInvalidConfig
	}
	if ctx == nil {
		_ = source.Close()
		return CandidateList{}, ErrInvalidConfig
	}
	defer source.Close()
	if err := validateLimits(&limits); err != nil {
		return CandidateList{}, err
	}

	manager := &tempManager{ctx: ctx, limits: limits, files: make(map[string]*tempFile)}
	defer manager.cleanup()

	current, err := manager.newFile(limits.MaxInputBytes)
	if err != nil {
		return CandidateList{}, err
	}
	if err := copyToTemp(ctx, source, current, limits.MaxInputBytes); err != nil {
		return CandidateList{}, err
	}
	if err := current.closeWriter(); err != nil {
		return CandidateList{}, ErrInvalidArchive
	}

	currentName := sourceName
	for depth := 0; ; depth++ {
		if err := ctx.Err(); err != nil {
			return CandidateList{}, err
		}
		archiveType, finalKind, err := manager.detect(current, currentName)
		if err != nil {
			return CandidateList{}, err
		}
		if archiveType == archiveNone {
			return CandidateList{Candidates: []Candidate{{
				Path:     currentName,
				Size:     current.size,
				KindHint: finalKind,
			}}}, nil
		}
		if depth >= limits.MaxNestingDepth {
			return CandidateList{}, ErrNestingTooDeep
		}

		var listed CandidateList
		switch archiveType {
		case archiveZIP:
			listed, err = manager.listZIPCandidates(current)
		case archiveTar:
			listed, err = manager.listTarCandidates(current, false)
		case archiveTarGzip:
			listed, err = manager.listTarCandidates(current, true)
		default:
			err = ErrUnsupported
		}
		if err != nil {
			return CandidateList{}, err
		}
		if len(listed.Candidates) > 0 {
			return listed, nil
		}

		// No terminals: unwrap a unique wrapper candidate and continue.
		var next *tempFile
		var nextName string
		switch archiveType {
		case archiveZIP:
			next, nextName, err = manager.extractZIP(current, "")
		case archiveTar:
			next, nextName, err = manager.extractTar(current, false, "")
		case archiveTarGzip:
			next, nextName, err = manager.extractTar(current, true, "")
		}
		if err != nil {
			return CandidateList{}, err
		}
		if err := current.remove(); err != nil {
			_ = next.remove()
			return CandidateList{}, ErrInvalidArchive
		}
		current, currentName = next, nextName
		if err := current.closeWriter(); err != nil {
			return CandidateList{}, ErrInvalidArchive
		}
	}
}

func validateLimits(limits *Limits) error {
	if limits.MaxInputBytes <= 0 || limits.MaxExpandedBytes <= 0 || limits.MaxTemporaryBytes <= 0 {
		return ErrInvalidConfig
	}
	if limits.MaxNestingDepth == 0 {
		limits.MaxNestingDepth = defaultMaxNestingDepth
	}
	if limits.MaxEntries == 0 {
		limits.MaxEntries = defaultMaxEntries
	}
	if limits.MaxNestingDepth < 1 || limits.MaxNestingDepth > 32 ||
		limits.MaxEntries < 1 || limits.MaxEntries > 100000 {
		return ErrInvalidConfig
	}
	return nil
}

func kindHintForName(name string) Kind {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".bacpac"):
		return KindBACPAC
	case strings.HasSuffix(lower, ".bak"):
		return KindSQLBackup
	default:
		return ""
	}
}

func truncateCandidates(candidates []Candidate) CandidateList {
	if len(candidates) <= maxListedCandidates {
		return CandidateList{Candidates: candidates}
	}
	return CandidateList{
		Candidates: append([]Candidate(nil), candidates[:maxListedCandidates]...),
		Truncated:  true,
	}
}

func newAmbiguousError(candidates []Candidate) error {
	listed := truncateCandidates(candidates)
	return &AmbiguousError{Candidates: listed.Candidates, Truncated: listed.Truncated}
}

func AsAmbiguous(err error) (*AmbiguousError, bool) {
	var ambiguous *AmbiguousError
	if errors.As(err, &ambiguous) {
		return ambiguous, true
	}
	return nil, false
}
