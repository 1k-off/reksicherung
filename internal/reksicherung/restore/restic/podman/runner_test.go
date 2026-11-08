package podman

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/1k-off/reksicherung/internal/reksicherung/restore/restic"
	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
)

func TestRunnerExecutesArgumentVectorWithSeparateEnvironment(t *testing.T) {
	t.Parallel()

	const secret = "sentinel-restic-password"
	runtime := &fakeRuntime{output: []byte("restic 0.17.0\n")}
	target := container.Container{
		ID: "0123456789abcdef", Name: container.ContainerName("run-123"), RunID: "run-123",
	}
	runner, err := New(runtime, target)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	got, err := runner.Run(context.Background(), restic.Invocation{
		Executable:     "restic",
		Args:           []string{"--repo", "s3:https://objects.example/bucket/repo", "version"},
		Environment:    []restic.EnvironmentVariable{{Name: "RESTIC_PASSWORD", Value: secret}},
		MaxOutputBytes: 1024,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if string(got) != string(runtime.output) {
		t.Fatalf("Run() output = %q, want %q", got, runtime.output)
	}
	if runtime.target != target {
		t.Fatalf("container handle = %#v", runtime.target)
	}
	if len(runtime.command) != 4 || runtime.command[0] != "restic" ||
		runtime.command[1] != "--repo" || runtime.command[3] != "version" {
		t.Fatalf("command = %#v", runtime.command)
	}
	if len(runtime.environment) != 1 || runtime.environment[0].Name != "RESTIC_PASSWORD" ||
		runtime.environment[0].Value != secret {
		t.Fatalf("environment = %#v", runtime.environment)
	}
	for _, argument := range runtime.command {
		if argument == secret {
			t.Fatal("secret was included in argv")
		}
	}
}

func TestRunnerStreamsBackupOutputWithSeparateEnvironment(t *testing.T) {
	t.Parallel()

	const secret = "streamed-restic-password"
	runtime := &fakeRuntime{streamOutput: "native backup contents"}
	target := container.Container{
		ID: "0123456789abcdef", Name: container.ContainerName("run-123"), RunID: "run-123",
	}
	runner, err := New(runtime, target)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	stream, err := runner.Stream(context.Background(), restic.Invocation{
		Executable:     "restic",
		Args:           []string{"--repo", "s3:bucket/repo", "dump", strings.Repeat("a", 64), "/backup/db.bak"},
		Environment:    []restic.EnvironmentVariable{{Name: "RESTIC_PASSWORD", Value: secret}},
		MaxOutputBytes: 1024,
	})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer stream.Close()
	output, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll(stream) error = %v", err)
	}
	if string(output) != runtime.streamOutput || runtime.target != target {
		t.Fatalf("stream output=%q target=%#v", output, runtime.target)
	}
	if len(runtime.command) != 6 || runtime.command[0] != "restic" ||
		runtime.command[3] != "dump" || runtime.command[5] != "/backup/db.bak" {
		t.Fatalf("stream command = %#v", runtime.command)
	}
	if len(runtime.environment) != 1 || runtime.environment[0] != (container.EnvironmentVariable{Name: "RESTIC_PASSWORD", Value: secret}) {
		t.Fatalf("stream environment = %#v", runtime.environment)
	}
	for _, argument := range runtime.command {
		if argument == secret {
			t.Fatal("Restic password appeared in command argv")
		}
	}
}

func TestRunnerEnforcesOutputLimitAndSanitizesRuntimeError(t *testing.T) {
	t.Parallel()

	t.Run("output limit", func(t *testing.T) {
		runtime := &fakeRuntime{output: make([]byte, 9)}
		runner, _ := New(runtime, container.Container{
			ID: "container", Name: container.ContainerName("run"), RunID: "run",
		})
		_, err := runner.Run(context.Background(), restic.Invocation{
			Executable: "restic", MaxOutputBytes: 8,
		})
		if !errors.Is(err, restic.ErrOutputLimit) {
			t.Fatalf("Run() error = %v, want ErrOutputLimit", err)
		}
	})

	t.Run("runtime failure", func(t *testing.T) {
		const secret = "do-not-leak-this"
		runtime := &fakeRuntime{err: errors.New(secret)}
		runner, _ := New(runtime, container.Container{
			ID: "container", Name: container.ContainerName("run"), RunID: "run",
		})
		_, err := runner.Run(context.Background(), restic.Invocation{
			Executable: "restic", MaxOutputBytes: 8,
		})
		if !errors.Is(err, restic.ErrCommandFailed) {
			t.Fatalf("Run() error = %v, want ErrCommandFailed", err)
		}
		if err != nil && contains(err.Error(), secret) {
			t.Fatalf("runtime error leaked secret: %v", err)
		}
	})
}

func TestNewRejectsMissingRuntimeOrContainer(t *testing.T) {
	t.Parallel()
	if _, err := New(nil, container.Container{
		ID: "container", Name: container.ContainerName("run"), RunID: "run",
	}); err == nil {
		t.Fatal("New(nil, ...) error = nil")
	}
	if _, err := New(&fakeRuntime{}, container.Container{}); err == nil {
		t.Fatal("New(..., empty handle) error = nil")
	}
}

type fakeRuntime struct {
	target       container.Container
	command      []string
	environment  []container.EnvironmentVariable
	output       []byte
	streamOutput string
	err          error
}

func (runtime *fakeRuntime) ExecWithEnvironment(_ context.Context, target container.Container, command []string, environment []container.EnvironmentVariable) ([]byte, error) {
	runtime.target = target
	runtime.command = append([]string(nil), command...)
	runtime.environment = append([]container.EnvironmentVariable(nil), environment...)
	return runtime.output, runtime.err
}

func (runtime *fakeRuntime) ExecStreamWithEnvironment(_ context.Context, target container.Container, command []string, environment []container.EnvironmentVariable) (io.ReadCloser, error) {
	runtime.target = target
	runtime.command = append([]string(nil), command...)
	runtime.environment = append([]container.EnvironmentVariable(nil), environment...)
	if runtime.err != nil {
		return nil, runtime.err
	}
	return io.NopCloser(strings.NewReader(runtime.streamOutput)), nil
}

func contains(value, substring string) bool {
	for i := 0; i+len(substring) <= len(value); i++ {
		if value[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}
