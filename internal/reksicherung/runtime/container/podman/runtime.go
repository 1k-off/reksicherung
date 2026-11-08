// Package podman implements isolated restore containers using the Podman CLI.
package podman

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
)

const maxExecOutput = 4 << 20
const maxContainerProcesses = 1024

// Runtime manages short-lived, network-isolated containers through Podman.
type Runtime struct {
	executable string
	runner     commandRunner
}

type commandRunner interface {
	Run(context.Context, string, []string, io.Writer) error
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

func (execRunner) Run(ctx context.Context, executable string, args []string, stdout io.Writer) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard
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

	name := container.ContainerName(spec.RunID)
	args := []string{
		"create",
		"--name", name,
		"--label", "reksicherung.managed=true",
		"--label", "reksicherung.run-id=" + spec.RunID,
		"--network", "none",
		"--cpus", strconv.FormatFloat(spec.CPUs, 'f', -1, 64),
		"--memory", strconv.FormatInt(spec.MemoryBytes, 10),
		"--pids-limit", strconv.Itoa(maxContainerProcesses),
	}
	envFile, err := writeEnvironmentFile(spec.Environment)
	if err != nil {
		return container.Container{}, fmt.Errorf("prepare container environment: %w", err)
	}
	args = append(args, "--env-file", envFile)
	args = append(args, spec.Image)

	var output bytes.Buffer
	createErr := r.runner.Run(ctx, r.executable, args, &output)
	removeEnvErr := os.Remove(envFile)
	if createErr != nil {
		if errors.Is(removeEnvErr, os.ErrNotExist) {
			removeEnvErr = nil
		}
		return container.Container{}, errors.Join(
			fmt.Errorf("podman create failed: %w", createErr),
			wrapIfError(removeEnvErr, "remove temporary environment file"),
		)
	}
	if removeEnvErr != nil && !errors.Is(removeEnvErr, os.ErrNotExist) {
		cleanupErr := r.cleanupByName(ctx, name)
		return container.Container{}, errors.Join(
			fmt.Errorf("remove temporary environment file: %w", removeEnvErr),
			wrapIfError(cleanupErr, "remove newly created container"),
		)
	}

	id := strings.TrimSpace(output.String())
	if !validContainerID(id) {
		_ = r.cleanupByName(ctx, name)
		return container.Container{}, errors.New("podman create returned an invalid container ID")
	}
	if err := r.runner.Run(ctx, r.executable, []string{"start", id}, io.Discard); err != nil {
		removeErr := r.cleanupByID(ctx, id)
		if removeErr != nil {
			return container.Container{}, errors.Join(
				fmt.Errorf("podman start failed: %w", err),
				fmt.Errorf("remove failed container: %w", removeErr),
			)
		}
		return container.Container{}, fmt.Errorf("podman start failed: %w", err)
	}

	return container.Container{ID: id, Name: name}, nil
}

// Remove forcibly removes a container, tolerating an already-removed resource.
func (r *Runtime) Remove(ctx context.Context, id string) error {
	if !validContainerReference(id) {
		return errors.New("invalid container reference")
	}
	return r.removeByID(ctx, id)
}

// Exec executes a command as an argv vector inside a container. It does not
// invoke a shell, and its output is limited to four MiB.
func (r *Runtime) Exec(ctx context.Context, id string, command []string) ([]byte, error) {
	if !validContainerReference(id) {
		return nil, errors.New("invalid container reference")
	}
	if len(command) == 0 {
		return nil, errors.New("container command must not be empty")
	}
	for _, arg := range command {
		if strings.ContainsRune(arg, '\x00') || !utf8.ValidString(arg) {
			return nil, errors.New("container command contains an invalid argument")
		}
	}

	output := &limitedBuffer{limit: maxExecOutput}
	args := append([]string{"exec", id}, command...)
	if err := r.runner.Run(ctx, r.executable, args, output); err != nil {
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

func (r *Runtime) removeByID(ctx context.Context, id string) error {
	err := r.runner.Run(ctx, r.executable, []string{
		"rm", "--ignore", "--force", "--time", "10", id,
	}, io.Discard)
	if err != nil {
		return fmt.Errorf("podman remove failed: %w", err)
	}
	return nil
}

func (r *Runtime) removeByName(ctx context.Context, name string) error {
	err := r.runner.Run(ctx, r.executable, []string{
		"rm", "--ignore", "--force", "--time", "10", name,
	}, io.Discard)
	if err != nil {
		return fmt.Errorf("podman remove failed: %w", err)
	}
	return nil
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

func validContainerReference(id string) bool {
	if validContainerID(id) {
		return true
	}
	return strings.HasPrefix(id, "reksicherung-") &&
		len(id) <= len("reksicherung-")+128 &&
		validRunSuffix(strings.TrimPrefix(id, "reksicherung-"))
}

func validRunSuffix(id string) bool {
	if id == "" {
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
