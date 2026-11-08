// Package restic selects a snapshot and restores it into a configured path
// inside an already-created, isolated restore container.
//
// The package does not create containers, configure storage, or orchestrate
// checks. A caller supplies a command runner that executes argv without a
// shell, injects environment values without placing them in argv, enforces
// bounded output, and honors context cancellation. See README.md for the
// production adapter contract and current S3-first constraints.
package restic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	// SelectionLatest selects the newest snapshot by its recorded snapshot time.
	SelectionLatest = "latest"
	// SelectionID selects the snapshot matching the full snapshot ID.
	SelectionID = "id"

	defaultExecutable = "restic"
	maxOutputBytes    = 16 << 20
)

var (
	// ErrInvalidConfig indicates an invalid client configuration.
	ErrInvalidConfig = errors.New("restic: invalid configuration")
	// ErrInvalidSelection indicates an invalid snapshot selection request.
	ErrInvalidSelection = errors.New("restic: invalid snapshot selection")
	// ErrSnapshotNotFound indicates that the requested snapshot is not present.
	ErrSnapshotNotFound = errors.New("restic: snapshot not found")
	// ErrInvalidSnapshotOutput indicates that Restic returned malformed or
	// unexpected snapshot JSON.
	ErrInvalidSnapshotOutput = errors.New("restic: invalid snapshot output")
	// ErrOutputLimit indicates that a command produced more output than allowed.
	ErrOutputLimit = errors.New("restic: command output exceeds limit")
	// ErrCommandFailed indicates a failed Restic command. Underlying runner
	// errors and command output are deliberately not included.
	ErrCommandFailed = errors.New("restic: command failed")
)

var snapshotIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// EnvironmentVariable is passed to the runner separately from argv. Values
// may contain credentials and must never be logged or included in errors.
type EnvironmentVariable struct {
	Name  string
	Value string
}

// Invocation describes one command execution. Args contains discrete argv
// elements, never a shell command string. The runner must cap stdout to
// MaxOutputBytes and must not expose Environment values in diagnostics.
type Invocation struct {
	Executable     string
	Args           []string
	Environment    []EnvironmentVariable
	MaxOutputBytes int64
}

// Runner executes one command in the restore container. It must not invoke a
// shell, must honor context cancellation, and must return no more than the
// requested number of stdout bytes. The returned error is treated as opaque
// and is never propagated to callers.
//
// Implementations should inject Environment values only into the Restic
// process environment or through short-lived, permission-restricted secret
// files. They must not pass secret values through argv, logs, error messages,
// or persistent container configuration. stderr should not be returned as
// stdout; implementations should retain only bounded, sanitized diagnostics
// internally, if any.
type Runner interface {
	Run(ctx context.Context, invocation Invocation) ([]byte, error)
}

// Config contains the Restic repository and the destination configured for
// this restore container. TargetPath must be a descendant of TargetRoot; the
// request cannot override either path.
type Config struct {
	Executable     string
	Repository     string
	TargetRoot     string
	TargetPath     string
	Timeout        time.Duration
	MaxOutputBytes int64
	Environment    []EnvironmentVariable
}

// Selection identifies a snapshot using either Policy "latest" or Policy
// "id" with a full, 64-character snapshot ID.
type Selection struct {
	Policy string
	ID     string
}

// Snapshot contains the identifying metadata needed to report a restore.
type Snapshot struct {
	ID   string
	Time time.Time
}

// RestoreResult records which snapshot Restic restored and the configured
// destination. It contains no command output or credentials.
type RestoreResult struct {
	Snapshot Snapshot
	Target   string
}

// Client runs bounded Restic commands through a caller-provided runner.
type Client struct {
	config Config
	runner Runner
}

// New validates configuration and returns a Restic client.
func New(config Config, runner Runner) (*Client, error) {
	if runner == nil || !validConfig(config) {
		return nil, ErrInvalidConfig
	}
	if config.Executable == "" {
		config.Executable = defaultExecutable
	}
	if config.MaxOutputBytes == 0 {
		config.MaxOutputBytes = 1 << 20
	}
	config.Environment = append([]EnvironmentVariable(nil), config.Environment...)
	return &Client{config: config, runner: runner}, nil
}

// Restore preflights the Restic executable, discovers snapshots, selects one,
// and restores it into the configured target path. The timeout applies to the
// complete operation, including preflight and snapshot discovery.
func (client *Client) Restore(ctx context.Context, selection Selection) (RestoreResult, error) {
	if ctx == nil {
		return RestoreResult{}, errors.New("restic: context must not be nil")
	}
	if !validSelection(selection) {
		return RestoreResult{}, ErrInvalidSelection
	}

	requestContext, cancel := context.WithTimeout(ctx, client.config.Timeout)
	defer cancel()

	if _, err := client.run(requestContext, nil, "version"); err != nil {
		return RestoreResult{}, fmt.Errorf("restic preflight: %w", err)
	}
	snapshotOutput, err := client.run(requestContext, client.config.Environment,
		"--repo", client.config.Repository, "snapshots", "--json")
	if err != nil {
		return RestoreResult{}, fmt.Errorf("restic snapshot listing: %w", err)
	}
	snapshots, err := parseSnapshots(snapshotOutput)
	if err != nil {
		return RestoreResult{}, err
	}
	selected, err := selectSnapshot(snapshots, selection)
	if err != nil {
		return RestoreResult{}, err
	}

	// Restic's restore command does not overwrite existing files unless
	// --overwrite is supplied. We intentionally never enable overwrite or
	// deletion behavior; the configured target is the only restore destination.
	if _, err := client.run(requestContext, client.config.Environment,
		"--repo", client.config.Repository, "restore", selected.ID,
		"--target", client.config.TargetPath); err != nil {
		return RestoreResult{}, fmt.Errorf("restic restore: %w", err)
	}
	return RestoreResult{Snapshot: selected, Target: client.config.TargetPath}, nil
}

func (client *Client) run(ctx context.Context, environment []EnvironmentVariable, args ...string) ([]byte, error) {
	output, err := client.runner.Run(ctx, Invocation{
		Executable:     client.config.Executable,
		Args:           append([]string(nil), args...),
		Environment:    append([]EnvironmentVariable(nil), environment...),
		MaxOutputBytes: client.config.MaxOutputBytes,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrCommandFailed
	}
	if int64(len(output)) > client.config.MaxOutputBytes {
		return nil, ErrOutputLimit
	}
	return output, nil
}

func validConfig(config Config) bool {
	if config.Executable != "" && (strings.TrimSpace(config.Executable) != config.Executable ||
		strings.ContainsAny(config.Executable, "\x00\r\n\t ") || strings.HasPrefix(config.Executable, "-")) {
		return false
	}
	if !validRepository(config.Repository) || !validTarget(config.TargetRoot, config.TargetPath) {
		return false
	}
	if config.Timeout <= 0 || config.Timeout > 24*time.Hour {
		return false
	}
	if config.MaxOutputBytes < 0 || config.MaxOutputBytes > maxOutputBytes {
		return false
	}
	seen := make(map[string]struct{}, len(config.Environment))
	for _, variable := range config.Environment {
		if !validEnvironmentName(variable.Name) || !allowedEnvironmentName(variable.Name) ||
			strings.ContainsRune(variable.Value, '\x00') {
			return false
		}
		if _, exists := seen[variable.Name]; exists {
			return false
		}
		seen[variable.Name] = struct{}{}
	}
	return true
}

func allowedEnvironmentName(name string) bool {
	switch name {
	case "RESTIC_PASSWORD", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN", "AWS_DEFAULT_REGION", "AWS_REGION",
		"AWS_S3_FORCE_PATH_STYLE":
		return true
	default:
		return false
	}
}

func validRepository(repository string) bool {
	if repository == "" || len(repository) > 4096 || strings.TrimSpace(repository) != repository ||
		strings.HasPrefix(repository, "-") || strings.ContainsAny(repository, "\x00\r\n\t ") ||
		!strings.HasPrefix(repository, "s3:") {
		return false
	}
	// The URL-form S3 repository syntax may contain an endpoint but must never
	// carry embedded credentials. Credential-bearing values belong in env.
	reference := strings.TrimPrefix(repository, "s3:")
	if strings.Contains(reference, "://") {
		endpoint, err := url.Parse(reference)
		if err != nil || (endpoint.Scheme != "https" && endpoint.Scheme != "http") ||
			endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" ||
			endpoint.Fragment != "" || endpoint.Path == "" {
			return false
		}
	} else if strings.ContainsAny(reference, "?#") {
		return false
	}
	return reference != ""
}

func validTarget(root, target string) bool {
	if !validAbsoluteContainerPath(root) || !validAbsoluteContainerPath(target) || root == target {
		return false
	}
	return strings.HasPrefix(target, root+"/")
}

func validAbsoluteContainerPath(value string) bool {
	if value == "" || !path.IsAbs(value) || path.Clean(value) != value ||
		strings.ContainsAny(value, "\\\x00\r\n\t") {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	for _, component := range strings.Split(value, "/") {
		if component == "." || component == ".." {
			return false
		}
	}
	return true
}

func validEnvironmentName(name string) bool {
	if name == "" {
		return false
	}
	for index, character := range name {
		alphaNumeric := character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9'
		if index == 0 && !((character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') || character == '_') {
			return false
		}
		if !alphaNumeric && character != '_' {
			return false
		}
	}
	return true
}

func validSelection(selection Selection) bool {
	switch selection.Policy {
	case SelectionLatest:
		return selection.ID == ""
	case SelectionID:
		return snapshotIDPattern.MatchString(selection.ID)
	default:
		return false
	}
}

type resticSnapshot struct {
	ID             string   `json:"id"`
	ShortID        string   `json:"short_id"`
	Time           string   `json:"time"`
	Tree           string   `json:"tree"`
	Paths          []string `json:"paths"`
	Hostname       string   `json:"hostname"`
	Username       string   `json:"username"`
	UID            *uint32  `json:"uid"`
	GID            *uint32  `json:"gid"`
	Tags           []string `json:"tags"`
	ProgramVersion string   `json:"program_version"`
}

func parseSnapshots(data []byte) ([]Snapshot, error) {
	if len(data) == 0 || !hasUniqueJSONKeys(data) {
		return nil, ErrInvalidSnapshotOutput
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var encoded []resticSnapshot
	if err := decoder.Decode(&encoded); err != nil {
		return nil, ErrInvalidSnapshotOutput
	}
	if err := ensureJSONEOF(decoder); err != nil || encoded == nil {
		return nil, ErrInvalidSnapshotOutput
	}

	snapshots := make([]Snapshot, 0, len(encoded))
	seenIDs := make(map[string]struct{}, len(encoded))
	for _, item := range encoded {
		if !snapshotIDPattern.MatchString(item.ID) || item.Time == "" {
			return nil, ErrInvalidSnapshotOutput
		}
		id := strings.ToLower(item.ID)
		if _, exists := seenIDs[id]; exists {
			return nil, ErrInvalidSnapshotOutput
		}
		seenIDs[id] = struct{}{}
		parsedTime, err := time.Parse(time.RFC3339Nano, item.Time)
		if err != nil {
			return nil, ErrInvalidSnapshotOutput
		}
		snapshots = append(snapshots, Snapshot{ID: id, Time: parsedTime})
	}
	return snapshots, nil
}

func selectSnapshot(snapshots []Snapshot, selection Selection) (Snapshot, error) {
	if selection.Policy == SelectionID {
		id := strings.ToLower(selection.ID)
		for _, snapshot := range snapshots {
			if snapshot.ID == id {
				return snapshot, nil
			}
		}
		return Snapshot{}, ErrSnapshotNotFound
	}
	if len(snapshots) == 0 {
		return Snapshot{}, ErrSnapshotNotFound
	}
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].Time.Equal(snapshots[j].Time) {
			return snapshots[i].ID < snapshots[j].ID
		}
		return snapshots[i].Time.After(snapshots[j].Time)
	})
	return snapshots[0], nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrInvalidSnapshotOutput
	}
	return nil
}

// hasUniqueJSONKeys rejects duplicate object keys before decoding. The standard
// encoding/json decoder otherwise accepts duplicates and keeps the last value.
func hasUniqueJSONKeys(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if !consumeJSONValue(decoder) {
		return false
	}
	var extra any
	return errors.Is(decoder.Decode(&extra), io.EOF)
}

func consumeJSONValue(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return true
	}

	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return false
			}
			key, ok := keyToken.(string)
			if !ok {
				return false
			}
			if _, exists := seen[key]; exists {
				return false
			}
			seen[key] = struct{}{}
			if !consumeJSONValue(decoder) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && closing == json.Delim('}')
	case '[':
		for decoder.More() {
			if !consumeJSONValue(decoder) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && closing == json.Delim(']')
	default:
		return false
	}
}
