// Package podman adapts the Restic restore client to a managed Podman
// container. Environment values are supplied through the runtime's temporary
// environment file and are not included in argv or persistent container
// configuration.
package podman

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/1k-off/reksicherung/internal/reksicherung/restore/restic"
	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
)

const maxOutputBytes = 4 << 20

// commandRuntime is the subset of the container runtime required by Runner.
type commandRuntime interface {
	ExecWithEnvironment(context.Context, container.Container, []string, []container.EnvironmentVariable) ([]byte, error)
}

type streamCommandRuntime interface {
	ExecStreamWithEnvironment(context.Context, container.Container, []string, []container.EnvironmentVariable) (io.ReadCloser, error)
}

// Runner executes Restic inside one already-started restore container.
type Runner struct {
	runtime commandRuntime
	target  container.Container
}

// New binds a Restic runner to a managed container ID.
func New(runtime commandRuntime, target container.Container) (*Runner, error) {
	if runtime == nil || target.ID == "" || target.RunID == "" ||
		target.Name != container.ContainerName(target.RunID) {
		return nil, errors.New("restic Podman runner requires a runtime and valid container handle")
	}
	return &Runner{runtime: runtime, target: target}, nil
}

// Run executes the requested program as an argv vector and rejects requests
// whose output limit exceeds the container runtime's hard limit.
func (runner *Runner) Run(ctx context.Context, invocation restic.Invocation) ([]byte, error) {
	if invocation.Executable == "" || invocation.MaxOutputBytes <= 0 || invocation.MaxOutputBytes > maxOutputBytes {
		return nil, restic.ErrInvalidConfig
	}
	command := make([]string, 1, len(invocation.Args)+1)
	command[0] = invocation.Executable
	command = append(command, invocation.Args...)

	environment := make([]container.EnvironmentVariable, len(invocation.Environment))
	for i, variable := range invocation.Environment {
		environment[i] = container.EnvironmentVariable{Name: variable.Name, Value: variable.Value}
	}
	output, err := runner.runtime.ExecWithEnvironment(ctx, runner.target, command, environment)
	if err != nil {
		// Do not forward runtime diagnostics; they may contain command details.
		return nil, fmt.Errorf("execute Restic in restore container: %w", restic.ErrCommandFailed)
	}
	if int64(len(output)) > invocation.MaxOutputBytes {
		return nil, restic.ErrOutputLimit
	}
	return output, nil
}

// Stream executes a Restic command whose stdout is a large archive payload.
// The runtime must stream it without buffering, and the caller must close the
// returned reader to cancel the command and release its environment file.
func (runner *Runner) Stream(ctx context.Context, invocation restic.Invocation) (io.ReadCloser, error) {
	streamRuntime, ok := runner.runtime.(streamCommandRuntime)
	if !ok || invocation.Executable == "" || invocation.MaxOutputBytes <= 0 || invocation.MaxOutputBytes > maxOutputBytes {
		return nil, restic.ErrInvalidConfig
	}
	command := make([]string, 1, len(invocation.Args)+1)
	command[0] = invocation.Executable
	command = append(command, invocation.Args...)

	environment := make([]container.EnvironmentVariable, len(invocation.Environment))
	for index, variable := range invocation.Environment {
		environment[index] = container.EnvironmentVariable{Name: variable.Name, Value: variable.Value}
	}
	stream, err := streamRuntime.ExecStreamWithEnvironment(ctx, runner.target, command, environment)
	if err != nil {
		return nil, restic.ErrCommandFailed
	}
	return stream, nil
}
