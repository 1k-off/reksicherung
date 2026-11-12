// Package restic provides bounded, read-only snapshot discovery for Restic
// repositories. It deliberately exposes snapshot metadata, never file contents.
package restic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"time"
)

var (
	ErrInvalidInput = errors.New("restic snapshot discovery: invalid input")
	ErrNotFound     = errors.New("restic snapshot discovery: snapshot not found")
	ErrFailed       = errors.New("restic snapshot discovery failed")
	ErrTimeout      = errors.New("restic snapshot discovery timed out")
	ErrCanceled     = errors.New("restic snapshot discovery canceled")
)

const (
	defaultTimeout   = 15 * time.Second
	maxTimeout       = 2 * time.Minute
	defaultMaxCount  = 50
	hardMaxCount     = 100
	maxCommandOutput = 4 << 20
	maxHostnameBytes = 32
	maxTagBytes      = 8
	maxTags          = 4
)

var snapshotIDPattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

// Runner executes an argument vector with an explicit environment and returns
// bounded stdout. Implementations must not invoke a shell.
type Runner interface {
	Run(context.Context, string, []string, []string) ([]byte, error)
}

// Options configures one repository and bounds command execution and results.
type Options struct {
	Binary       string
	Repository   string
	Password     string
	Environment  []string
	Timeout      time.Duration
	MaxSnapshots int
}

// Snapshot contains only identifiers and descriptive snapshot metadata.
// Paths and all repository contents are intentionally omitted.
type Snapshot struct {
	ID       string    `json:"id"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Tags     []string  `json:"tags"`
}

// SnapshotList is bounded and indicates if additional snapshots were present.
type SnapshotList struct {
	Snapshots []Snapshot `json:"snapshots"`
	Truncated bool       `json:"truncated"`
}

// Service exposes only snapshot discovery and explicit ID selection metadata.
type Service struct {
	runner  Runner
	options Options
}

// New creates a service using the operating system process runner.
func New(options Options) (*Service, error) {
	return NewWithRunner(execRunner{}, options)
}

// NewWithRunner creates a service with an injectable command runner.
func NewWithRunner(runner Runner, options Options) (*Service, error) {
	if runner == nil || !validEnvironmentValue(options.Repository, 4096) ||
		!validEnvironmentValue(options.Password, 16*1024) {
		return nil, ErrInvalidInput
	}
	if options.Binary == "" {
		options.Binary = "restic"
	}
	if !validEnvironmentValue(options.Binary, 4096) {
		return nil, ErrInvalidInput
	}
	if options.Timeout == 0 {
		options.Timeout = defaultTimeout
	}
	if options.MaxSnapshots == 0 {
		options.MaxSnapshots = defaultMaxCount
	}
	if options.Timeout < time.Millisecond || options.Timeout > maxTimeout ||
		options.MaxSnapshots < 1 || options.MaxSnapshots > hardMaxCount {
		return nil, ErrInvalidInput
	}
	seenEnvironment := make(map[string]struct{}, len(options.Environment))
	for _, entry := range options.Environment {
		name, valid := validEnvironmentEntry(entry)
		if !valid {
			return nil, ErrInvalidInput
		}
		if _, exists := seenEnvironment[name]; exists {
			return nil, ErrInvalidInput
		}
		seenEnvironment[name] = struct{}{}
	}
	options.Environment = append([]string(nil), options.Environment...)
	return &Service{runner: runner, options: options}, nil
}

// ListSnapshots returns at most limit snapshots from the configured
// repository, newest first. Restic's --latest is per host/path group and can
// return far more than limit; list all (bounded by MaxSnapshots+1) and truncate.
func (s *Service) ListSnapshots(ctx context.Context, limit int) (SnapshotList, error) {
	if ctx == nil || limit < 1 || limit > s.options.MaxSnapshots {
		return SnapshotList{}, ErrInvalidInput
	}
	output, err := s.run(ctx, []string{"snapshots", "--json"})
	if err != nil {
		return SnapshotList{}, err
	}
	records, err := decodeSnapshotRecords(output)
	if err != nil {
		return SnapshotList{}, ErrFailed
	}
	if len(records) > s.options.MaxSnapshots+1 {
		return SnapshotList{}, ErrFailed
	}
	snapshots, err := snapshotMetadata(records)
	if err != nil {
		return SnapshotList{}, ErrFailed
	}
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].Time.After(snapshots[j].Time)
	})
	truncated := len(snapshots) > limit
	if truncated {
		snapshots = snapshots[:limit]
	}
	return SnapshotList{Snapshots: snapshots, Truncated: truncated}, nil
}

// SelectSnapshot returns metadata only for one explicitly selected full
// snapshot ID. It never retrieves files or initiates a restore.
func (s *Service) SelectSnapshot(ctx context.Context, id string) (Snapshot, error) {
	if ctx == nil || !snapshotIDPattern.MatchString(id) {
		return Snapshot{}, ErrInvalidInput
	}
	output, err := s.run(ctx, []string{"snapshots", "--json", id})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Snapshot{}, ErrNotFound
		}
		return Snapshot{}, err
	}
	records, err := decodeSnapshotRecords(output)
	if err != nil {
		return Snapshot{}, ErrFailed
	}
	if len(records) != 1 || records[0].ID != id {
		return Snapshot{}, ErrNotFound
	}
	snapshots, err := snapshotMetadata(records)
	if err != nil {
		return Snapshot{}, ErrFailed
	}
	return snapshots[0], nil
}

func (s *Service) run(ctx context.Context, args []string) ([]byte, error) {
	runCtx, cancel := context.WithTimeout(ctx, s.options.Timeout)
	defer cancel()

	env := make([]string, 0, 2+len(s.options.Environment))
	env = append(env,
		"RESTIC_REPOSITORY="+s.options.Repository,
		"RESTIC_PASSWORD="+s.options.Password,
	)
	env = append(env, s.options.Environment...)
	output, err := s.runner.Run(runCtx, s.options.Binary, args, env)
	if err == nil {
		if len(output) > maxCommandOutput {
			return nil, ErrFailed
		}
		return output, nil
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return nil, ErrTimeout
	}
	if errors.Is(runCtx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return nil, ErrCanceled
	}
	return nil, ErrFailed
}

func validEnvironmentValue(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] == 0 {
			return false
		}
	}
	return true
}

type snapshotRecord struct {
	ID             string          `json:"id"`
	ShortID        string          `json:"short_id"`
	Time           time.Time       `json:"time"`
	Hostname       string          `json:"hostname"`
	Username       string          `json:"username"`
	UID            json.RawMessage `json:"uid"`
	GID            json.RawMessage `json:"gid"`
	Paths          []string        `json:"paths"`
	Tags           []string        `json:"tags"`
	Tree           string          `json:"tree"`
	Parent         string          `json:"parent"`
	Original       string          `json:"original"`
	ProgramVersion string          `json:"program_version"`
	Summary        json.RawMessage `json:"summary"`
	Excludes       []string        `json:"excludes"`
	Includes       []string        `json:"includes"`
	StructType     string          `json:"struct_type"`
}

func decodeSnapshotRecords(data []byte) ([]snapshotRecord, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var records []snapshotRecord
	if err := decoder.Decode(&records); err != nil {
		return nil, err
	}
	if records == nil {
		return nil, errors.New("snapshot response must be a JSON array")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("unexpected trailing JSON value")
		}
		return nil, err
	}
	return records, nil
}

func snapshotMetadata(records []snapshotRecord) ([]Snapshot, error) {
	result := make([]Snapshot, 0, len(records))
	for _, record := range records {
		if !snapshotIDPattern.MatchString(record.ID) || record.Time.IsZero() ||
			len(record.Hostname) > maxHostnameBytes || len(record.Tags) > maxTags {
			return nil, ErrFailed
		}
		tags := make([]string, len(record.Tags))
		for i, tag := range record.Tags {
			if len(tag) > maxTagBytes {
				return nil, ErrFailed
			}
			tags[i] = tag
		}
		result = append(result, Snapshot{
			ID: record.ID, Time: record.Time, Hostname: record.Hostname, Tags: tags,
		})
	}
	return result, nil
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, binary string, args, env []string) ([]byte, error) {
	if ctx == nil {
		return nil, ErrInvalidInput
	}
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = env
	var output boundedBuffer
	output.limit = maxCommandOutput
	command.Stdout = &output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		if output.exceeded {
			return nil, ErrFailed
		}
		return nil, err
	}
	return output.Bytes(), nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	if len(data) > buffer.limit-buffer.Len() {
		buffer.exceeded = true
		return 0, errors.New("restic output limit exceeded")
	}
	return buffer.Buffer.Write(data)
}

func validEnvironmentEntry(entry string) (string, bool) {
	for i := 0; i < len(entry); i++ {
		if entry[i] == '=' {
			if i == 0 || len(entry)-i-1 > 16*1024 {
				return "", false
			}
			if !validEnvironmentValue(entry[i+1:], 16*1024) {
				return "", false
			}
			name := entry[:i]
			switch name {
			case "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
				"AWS_DEFAULT_REGION", "AWS_REGION", "B2_ACCOUNT_ID", "B2_ACCOUNT_KEY",
				"AZURE_ACCOUNT_NAME", "AZURE_ACCOUNT_KEY",
				"RESTIC_CACHE_DIR", "XDG_CACHE_HOME":
				return name, true
			default:
				return "", false
			}
		}
		if !(entry[i] == '_' || entry[i] >= 'A' && entry[i] <= 'Z' ||
			entry[i] >= 'a' && entry[i] <= 'z' ||
			i > 0 && entry[i] >= '0' && entry[i] <= '9') {
			return "", false
		}
	}
	return "", false
}
