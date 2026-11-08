// Package podman implements isolated restore containers using the Podman CLI.
package podman

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
)

const maxExecOutput = 4 << 20
const maxContainerProcesses = 1024
const quotaProbeLimit = "8m"

// Runtime manages short-lived, network-isolated containers through Podman.
type Runtime struct {
	executable string
	runner     commandRunner
	timeoutMu  sync.Mutex
	timeouts   map[string]*time.Timer
}

type commandRunner interface {
	Run(context.Context, string, []string, io.Reader, io.Writer) error
}

type stderrCommandRunner interface {
	RunWithStderr(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error
}

// New creates a Podman runtime that executes the given Podman CLI binary.
// An empty executable uses "podman" from PATH.
func New(executable string) *Runtime {
	if executable == "" {
		executable = "podman"
	}
	return &Runtime{executable: executable, runner: execRunner{}}
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, executable string, args []string, stdin io.Reader, stdout io.Writer) error {
	return (execRunner{}).RunWithStderr(ctx, executable, args, stdin, stdout, io.Discard)
}

func (execRunner) RunWithStderr(ctx context.Context, executable string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// Start creates and starts a restore container with no network, published
// ports, host mounts, or caller-controlled entrypoint. Environment values are
// supplied through a short-lived mode-0600 file rather than command arguments.
func (r *Runtime) Start(ctx context.Context, spec container.Spec) (container.Container, error) {
	if err := container.ValidateSpec(spec); err != nil {
		return container.Container{}, err
	}
	if err := ctx.Err(); err != nil {
		return container.Container{}, err
	}
	lifetimeDeadline := time.Now().Add(spec.Timeout)
	startCtx, cancel := context.WithDeadline(ctx, lifetimeDeadline)
	defer cancel()

	name := container.ContainerName(spec.RunID)
	createToken, err := newCreateToken()
	if err != nil {
		return container.Container{}, fmt.Errorf("generate restore container ownership token: %w", err)
	}
	args := []string{
		"create",
		"--name", name,
		"--label", "reksicherung.managed=true",
		"--label", "reksicherung.run-id=" + spec.RunID,
		"--label", "reksicherung.create-token=" + createToken,
		"--network", "none",
		"--cpus", strconv.FormatFloat(spec.CPUs, 'f', -1, 64),
		"--memory", strconv.FormatInt(spec.MemoryBytes, 10),
		"--storage-opt", "size=" + strconv.FormatInt(spec.DiskBytes, 10),
		"--image-volume", "ignore",
		"--pids-limit", strconv.Itoa(maxContainerProcesses),
	}
	envFile, err := writeEnvironmentFile(spec.Environment)
	if err != nil {
		return container.Container{}, fmt.Errorf("prepare container environment: %w", err)
	}
	args = append(args, "--env-file", envFile)
	args = append(args, spec.Image)

	output := &limitedBuffer{limit: 4096}
	createErr := r.runner.Run(startCtx, r.executable, args, nil, output)
	removeEnvErr := os.Remove(envFile)
	if createErr != nil {
		if errors.Is(removeEnvErr, os.ErrNotExist) {
			removeEnvErr = nil
		}
		cleanupErr := r.cleanupPartialByName(startCtx, name, createToken)
		return container.Container{}, errors.Join(
			fmt.Errorf("podman create failed: %w", createErr),
			wrapIfError(removeEnvErr, "remove temporary environment file"),
			wrapIfError(cleanupErr, "remove partially created restore container"),
		)
	}
	if removeEnvErr != nil && !errors.Is(removeEnvErr, os.ErrNotExist) {
		cleanupErr := r.cleanupPartialByName(startCtx, name, createToken)
		return container.Container{}, errors.Join(
			fmt.Errorf("remove temporary environment file: %w", removeEnvErr),
			wrapIfError(cleanupErr, "remove newly created container"),
		)
	}
	if output.exceeded {
		cleanupErr := r.cleanupPartialByName(startCtx, name, createToken)
		return container.Container{}, errors.Join(
			errors.New("podman create returned more than 4096 bytes"),
			wrapIfError(cleanupErr, "remove newly created restore container"),
		)
	}

	id := strings.TrimSpace(output.String())
	if !validContainerID(id) {
		cleanupErr := r.cleanupPartialByName(startCtx, name, createToken)
		return container.Container{}, errors.Join(
			errors.New("podman create returned an invalid container ID"),
			wrapIfError(cleanupErr, "remove newly created restore container"),
		)
	}
	if err := r.verifyDiskQuota(startCtx, spec); err != nil {
		cleanupErr := r.cleanupByID(startCtx, id)
		return container.Container{}, errors.Join(
			fmt.Errorf("verify Podman writable-layer disk quota: %w", err),
			wrapIfError(cleanupErr, "remove unverified restore container"),
		)
	}
	if err := r.runner.Run(startCtx, r.executable, []string{"start", id}, nil, io.Discard); err != nil {
		removeErr := r.cleanupByID(startCtx, id)
		if removeErr != nil {
			return container.Container{}, errors.Join(
				fmt.Errorf("podman start failed: %w", err),
				fmt.Errorf("remove failed container: %w", removeErr),
			)
		}
		return container.Container{}, fmt.Errorf("podman start failed: %w", err)
	}

	if err := startCtx.Err(); err != nil {
		cleanupErr := r.cleanupByID(startCtx, id)
		return container.Container{}, errors.Join(
			fmt.Errorf("restore container startup exceeded its lifetime: %w", err),
			wrapIfError(cleanupErr, "remove expired restore container"),
		)
	}
	if !spec.RetainForReview {
		r.scheduleExpiry(id, time.Until(lifetimeDeadline))
	}
	return container.Container{ID: id, Name: name, RunID: spec.RunID}, nil
}

// Remove forcibly removes a container, tolerating an already-removed resource.
func (r *Runtime) Remove(ctx context.Context, target container.Container) error {
	if !validContainerHandle(target) {
		return errors.New("invalid container reference")
	}
	if err := r.verifyManaged(ctx, target); err != nil {
		if errors.Is(err, container.ErrNotFound) {
			return nil
		}
		return err
	}
	if err := r.removeByID(ctx, target.ID); err != nil {
		return err
	}
	r.cancelExpiry(target.ID)
	return nil
}

// Exec executes a command as an argv vector inside a container. It does not
// invoke a shell, and its output is limited to four MiB.
func (r *Runtime) Exec(ctx context.Context, target container.Container, command []string) ([]byte, error) {
	if !validContainerHandle(target) {
		return nil, errors.New("invalid container reference")
	}
	if err := validateCommand(command); err != nil {
		return nil, err
	}
	if err := r.verifyManaged(ctx, target); err != nil {
		return nil, err
	}

	output := &limitedBuffer{limit: maxExecOutput}
	args := append([]string{"exec", target.ID}, command...)
	if err := r.runner.Run(ctx, r.executable, args, nil, output); err != nil {
		if errors.Is(err, errOutputLimit) {
			return nil, fmt.Errorf("container command output exceeds %d bytes", maxExecOutput)
		}
		return nil, fmt.Errorf("podman exec failed: %w", err)
	}
	if output.exceeded {
		return nil, fmt.Errorf("container command output exceeds %d bytes", maxExecOutput)
	}
	return output.Bytes(), nil
}

// ExecWithEnvironment runs a command with a short-lived, mode-0600 environment
// file. Secret values are not placed in Podman's argv or the container config.
func (r *Runtime) ExecWithEnvironment(ctx context.Context, target container.Container, command []string, environment []container.EnvironmentVariable) ([]byte, error) {
	if !validContainerHandle(target) {
		return nil, errors.New("invalid container reference")
	}
	if err := validateCommand(command); err != nil {
		return nil, err
	}
	if err := validateEnvironment(environment); err != nil {
		return nil, err
	}
	if err := r.verifyManaged(ctx, target); err != nil {
		return nil, err
	}
	if len(environment) == 0 {
		output := &limitedBuffer{limit: maxExecOutput}
		args := append([]string{"exec", target.ID}, command...)
		if err := r.runner.Run(ctx, r.executable, args, nil, output); err != nil {
			if errors.Is(err, errOutputLimit) {
				return nil, fmt.Errorf("container command output exceeds %d bytes", maxExecOutput)
			}
			return nil, fmt.Errorf("podman exec failed: %w", err)
		}
		if output.exceeded {
			return nil, fmt.Errorf("container command output exceeds %d bytes", maxExecOutput)
		}
		return output.Bytes(), nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	envFile, err := writeEnvironmentFile(environment)
	if err != nil {
		return nil, fmt.Errorf("prepare command environment: %w", err)
	}
	output := &limitedBuffer{limit: maxExecOutput}
	args := append([]string{"exec", "--env-file", envFile, target.ID}, command...)
	runErr := r.runner.Run(ctx, r.executable, args, nil, output)
	removeErr := os.Remove(envFile)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	if errors.Is(runErr, errOutputLimit) {
		runErr = fmt.Errorf("container command output exceeds %d bytes", maxExecOutput)
	} else if runErr != nil {
		runErr = fmt.Errorf("podman exec failed: %w", runErr)
	}
	if runErr != nil || removeErr != nil {
		return nil, errors.Join(runErr, wrapIfError(removeErr, "remove temporary command environment file"))
	}
	if output.exceeded {
		return nil, fmt.Errorf("container command output exceeds %d bytes", maxExecOutput)
	}
	return output.Bytes(), nil
}

func (r *Runtime) verifyManaged(ctx context.Context, target container.Container) error {
	existsErr := r.runner.Run(ctx, r.executable, []string{"container", "exists", target.ID}, nil, io.Discard)
	if existsErr != nil {
		var exitError interface{ ExitCode() int }
		if errors.As(existsErr, &exitError) && exitError.ExitCode() == 1 {
			return container.ErrNotFound
		}
		return fmt.Errorf("verify restore container exists: %w", existsErr)
	}

	labels, err := r.inspectLabels(ctx, target.ID)
	if err != nil {
		return err
	}
	if labels["reksicherung.managed"] != "true" {
		return errors.New("container is not managed by Reksicherung")
	}
	if labels["reksicherung.run-id"] != target.RunID {
		return errors.New("container ownership does not match the requested run")
	}
	return nil
}

func (r *Runtime) inspectLabels(ctx context.Context, id string) (map[string]string, error) {
	output := &limitedBuffer{limit: 64 << 10}
	if err := r.runner.Run(ctx, r.executable, []string{
		"inspect", "--format", "{{json .Config.Labels}}", id,
	}, nil, output); err != nil {
		return nil, fmt.Errorf("inspect restore container ownership: %w", err)
	}
	if output.exceeded {
		return nil, errors.New("restore container labels exceed inspection limit")
	}
	var labels map[string]string
	if err := json.Unmarshal(output.Bytes(), &labels); err != nil {
		return nil, errors.New("Podman returned invalid restore container labels")
	}
	return labels, nil
}

func (r *Runtime) cleanupPartialByName(ctx context.Context, name, createToken string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), container.StopTimeout)
	defer cancel()
	if err := r.runner.Run(cleanupCtx, r.executable, []string{"container", "exists", name}, nil, io.Discard); err != nil {
		var exitError interface{ ExitCode() int }
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return nil
		}
		return fmt.Errorf("check partially created container: %w", err)
	}
	labels, err := r.inspectLabels(cleanupCtx, name)
	if err != nil {
		return err
	}
	if labels["reksicherung.managed"] != "true" || labels["reksicherung.create-token"] != createToken {
		return nil
	}
	return r.removeByName(cleanupCtx, name)
}

func newCreateToken() (string, error) {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}
	return hex.EncodeToString(token), nil
}

func validateEnvironment(environment []container.EnvironmentVariable) error {
	return container.ValidateEnvironment(environment)
}

func validateCommand(command []string) error {
	if len(command) == 0 || len(command) > container.MaxCommandArguments {
		return errors.New("container command must contain between 1 and 256 arguments")
	}
	totalBytes := 0
	for _, argument := range command {
		if argument == "" || strings.ContainsRune(argument, '\x00') {
			return errors.New("container command contains an empty argument or NUL byte")
		}
		totalBytes += len(argument) + 1
		if totalBytes > container.MaxCommandBytes {
			return fmt.Errorf("container command exceeds %d bytes", container.MaxCommandBytes)
		}
	}
	return nil
}

func (r *Runtime) removeByID(ctx context.Context, id string) error {
	err := r.runner.Run(ctx, r.executable, []string{
		"rm", "--ignore", "--force", "--time", "10", id,
	}, nil, io.Discard)
	if err != nil {
		return fmt.Errorf("podman remove failed: %w", err)
	}
	return nil
}

func (r *Runtime) removeByName(ctx context.Context, name string) error {
	err := r.runner.Run(ctx, r.executable, []string{
		"rm", "--ignore", "--force", "--time", "10", name,
	}, nil, io.Discard)
	if err != nil {
		return fmt.Errorf("podman remove failed: %w", err)
	}
	return nil
}

func (r *Runtime) scheduleExpiry(id string, timeout time.Duration) {
	r.timeoutMu.Lock()
	defer r.timeoutMu.Unlock()
	if r.timeouts == nil {
		r.timeouts = make(map[string]*time.Timer)
	}
	r.timeouts[id] = time.AfterFunc(timeout, func() {
		expiryCtx, cancel := context.WithTimeout(context.Background(), container.StopTimeout)
		defer cancel()
		if err := r.removeByID(expiryCtx, id); err != nil {
			log.Printf("[PODMAN]: timed out container %s could not be removed: %v", id, err)
		}
		r.cancelExpiry(id)
	})
}

func (r *Runtime) cancelExpiry(id string) {
	r.timeoutMu.Lock()
	defer r.timeoutMu.Unlock()
	if timer, exists := r.timeouts[id]; exists {
		timer.Stop()
		delete(r.timeouts, id)
	}
}

func (r *Runtime) cleanupByID(ctx context.Context, id string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), container.StopTimeout)
	defer cancel()
	return r.removeByID(cleanupCtx, id)
}

func (r *Runtime) cleanupByName(ctx context.Context, name string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), container.StopTimeout)
	defer cancel()
	return r.removeByName(cleanupCtx, name)
}

func wrapIfError(err error, message string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}

func writeEnvironmentFile(environment []container.EnvironmentVariable) (string, error) {
	if err := validateEnvironment(environment); err != nil {
		return "", fmt.Errorf("invalid environment: %w", err)
	}
	file, err := os.CreateTemp("", "reksicherung-podman-env-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	cleanup := func(cause error) (string, error) {
		closeErr := file.Close()
		removeErr := os.Remove(path)
		return "", errors.Join(cause, closeErr, removeErr)
	}
	if err := file.Chmod(0o600); err != nil {
		return cleanup(err)
	}
	writer := bufio.NewWriter(file)
	for _, variable := range environment {
		if _, err := fmt.Fprintf(writer, "%s=%s\n", variable.Name, variable.Value); err != nil {
			return cleanup(err)
		}
	}
	if err := writer.Flush(); err != nil {
		return cleanup(err)
	}
	if err := file.Sync(); err != nil {
		return cleanup(err)
	}
	if err := file.Close(); err != nil {
		removeErr := os.Remove(path)
		return "", errors.Join(err, removeErr)
	}
	return path, nil
}

func validContainerID(id string) bool {
	return len(id) >= 12 && len(id) <= 64 && isHex(id)
}

func validContainerHandle(target container.Container) bool {
	return validContainerID(target.ID) && validRunSuffix(target.RunID) &&
		target.Name == container.ContainerName(target.RunID)
}

func validRunSuffix(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i, r := range id {
		alphaNumeric := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if i == 0 && !alphaNumeric {
			return false
		}
		if !alphaNumeric && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

func isHex(value string) bool {
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return value != ""
}

var errOutputLimit = errors.New("command output limit exceeded")

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.exceeded = true
		return 0, errOutputLimit
	}
	if len(p) > remaining {
		_, _ = b.Buffer.Write(p[:remaining])
		b.exceeded = true
		return remaining, errOutputLimit
	}
	return b.Buffer.Write(p)
}
