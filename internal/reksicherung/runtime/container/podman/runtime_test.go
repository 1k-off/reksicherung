package podman

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
)

const testContainerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestStartCreatesNetworkIsolatedContainerAndRemovesSecretFile(t *testing.T) {
	t.Parallel()

	const secret = "test-only-secret-value"
	runner := &fakeRunner{
		outputs: map[string]string{"create": testContainerID},
	}
	runtime := &Runtime{executable: "podman", runner: runner}
	spec := testSpec()
	spec.Environment = []container.EnvironmentVariable{
		{Name: "MSSQL_SA_PASSWORD", Value: secret},
		{Name: "ACCEPT_EULA", Value: "Y"},
	}

	got, err := runtime.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got.ID != testContainerID || got.Name != container.ContainerName(spec.RunID) {
		t.Fatalf("Start() = %#v, want ID %q and name %q", got, testContainerID, container.ContainerName(spec.RunID))
	}
	if len(runner.calls) != 2 {
		t.Fatalf("Podman call count = %d, want 2", len(runner.calls))
	}
	create := runner.calls[0]
	if create.args[0] != "create" {
		t.Fatalf("first Podman command = %q, want create", create.args[0])
	}
	assertArgPair(t, create.args, "--network", "none")
	assertArgPair(t, create.args, "--cpus", "2")
	assertArgPair(t, create.args, "--memory", "2147483648")
	assertArgPair(t, create.args, "--pids-limit", "1024")
	assertArgPair(t, create.args, "--label", "reksicherung.managed=true")
	assertArgPair(t, create.args, "--label", "reksicherung.run-id=run-123")
	assertNoArgContains(t, create.args, secret)
	assertNoArgPair(t, create.args, "--publish")
	assertNoArgPair(t, create.args, "--volume")

	envPath := valueAfter(t, create.args, "--env-file")
	if _, err := os.Stat(envPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("environment file still exists after create: stat err = %v", err)
	}
	if !strings.Contains(runner.envFileContents, "MSSQL_SA_PASSWORD="+secret+"\n") {
		t.Fatal("environment file did not contain expected SQL Server password")
	}
	if strings.Contains(fmt.Sprint(runner.calls), secret) {
		t.Fatal("secret appeared in recorded Podman arguments")
	}
	if got := runner.calls[1].args; len(got) != 2 || got[0] != "start" || got[1] != testContainerID {
		t.Fatalf("start args = %#v, want start plus container ID", got)
	}
}

func TestStartFailureAttemptsToRemovePartialContainer(t *testing.T) {
	t.Parallel()

	t.Run("create conflict leaves existing container alone", func(t *testing.T) {
		t.Parallel()
		runner := &fakeRunner{fail: map[string]error{"create": errors.New("container name already in use")}}
		runtime := &Runtime{executable: "podman", runner: runner}

		_, err := runtime.Start(context.Background(), testSpec())
		if err == nil || !strings.Contains(err.Error(), "podman create failed") {
			t.Fatalf("Start() error = %v, want create failure", err)
		}
		if len(runner.calls) != 1 || runner.calls[0].args[0] != "create" {
			t.Fatalf("calls = %#v, want create only; existing resource must not be removed", runner.calls)
		}
	})

	t.Run("start failed", func(t *testing.T) {
		t.Parallel()
		runner := &fakeRunner{
			outputs: map[string]string{"create": testContainerID},
			fail:    map[string]error{"start": errors.New("exit status 125")},
		}
		runtime := &Runtime{executable: "podman", runner: runner}

		_, err := runtime.Start(context.Background(), testSpec())
		if err == nil || !strings.Contains(err.Error(), "podman start failed") {
			t.Fatalf("Start() error = %v, want start failure", err)
		}
		if len(runner.calls) != 3 || runner.calls[2].args[0] != "rm" {
			t.Fatalf("calls = %#v, want create, start, cleanup", runner.calls)
		}
	})
}

func TestExecUsesArgumentVectorAndBoundsOutput(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		runner := &fakeRunner{outputs: map[string]string{"exec": "database list\n"}}
		runtime := &Runtime{executable: "podman", runner: runner}
		got, err := runtime.Exec(context.Background(), testContainerID, []string{"sqlcmd", "-Q", "SELECT 1"})
		if err != nil {
			t.Fatalf("Exec() error = %v", err)
		}
		if string(got) != "database list\n" {
			t.Fatalf("Exec() output = %q", got)
		}
		want := []string{"exec", testContainerID, "sqlcmd", "-Q", "SELECT 1"}
		if !equalStrings(runner.calls[0].args, want) {
			t.Fatalf("Podman args = %#v, want %#v", runner.calls[0].args, want)
		}
	})

	t.Run("output too large", func(t *testing.T) {
		t.Parallel()
		runner := &fakeRunner{writeOutput: bytes.Repeat([]byte("x"), maxExecOutput+1)}
		runtime := &Runtime{executable: "podman", runner: runner}
		if _, err := runtime.Exec(context.Background(), testContainerID, []string{"command"}); err == nil {
			t.Fatal("Exec() error = nil, want output limit error")
		}
	})

	t.Run("rejects invalid input", func(t *testing.T) {
		t.Parallel()
		runner := &fakeRunner{}
		runtime := &Runtime{executable: "podman", runner: runner}
		for _, test := range []struct {
			name    string
			id      string
			command []string
		}{
			{name: "invalid ID", id: "../host", command: []string{"command"}},
			{name: "empty command", id: testContainerID},
			{name: "NUL argument", id: testContainerID, command: []string{"bad\x00arg"}},
		} {
			t.Run(test.name, func(t *testing.T) {
				if _, err := runtime.Exec(context.Background(), test.id, test.command); err == nil {
					t.Fatal("Exec() error = nil, want validation error")
				}
			})
		}
		if len(runner.calls) != 0 {
			t.Fatalf("invalid requests invoked Podman: %#v", runner.calls)
		}
	})
}

func TestRemoveUsesIdempotentPodmanFlags(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	runtime := &Runtime{executable: "podman", runner: runner}
	if err := runtime.Remove(context.Background(), testContainerID); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	want := []string{"rm", "--ignore", "--force", "--time", "10", testContainerID}
	if !equalStrings(runner.calls[0].args, want) {
		t.Fatalf("Podman args = %#v, want %#v", runner.calls[0].args, want)
	}
}

func testSpec() container.Spec {
	return container.Spec{
		RunID:       "run-123",
		Image:       "mcr.microsoft.com/mssql/server:2022-latest",
		CPUs:        2,
		MemoryBytes: 2 * 1024 * 1024 * 1024,
	}
}

type call struct {
	args []string
}

type fakeRunner struct {
	calls           []call
	outputs         map[string]string
	fail            map[string]error
	writeOutput     []byte
	envFileContents string
}

func (r *fakeRunner) Run(_ context.Context, _ string, args []string, stdout io.Writer) error {
	argsCopy := append([]string(nil), args...)
	r.calls = append(r.calls, call{args: argsCopy})
	if args[0] == "create" {
		path := valueAfterTest(args, "--env-file")
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		r.envFileContents = string(data)
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if goruntime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			return fmt.Errorf("environment file mode = %o, want 600", info.Mode().Perm())
		}
	}
	if err := r.fail[args[0]]; err != nil {
		return err
	}
	output := r.outputs[args[0]]
	if len(r.writeOutput) > 0 {
		_, err := stdout.Write(r.writeOutput)
		return err
	}
	if output != "" {
		_, err := io.WriteString(stdout, output)
		return err
	}
	return nil
}

func assertArgPair(t *testing.T, args []string, key, value string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return
		}
	}
	t.Fatalf("argument pair %s %s missing from %#v", key, value, args)
}

func assertNoArgPair(t *testing.T, args []string, key string) {
	t.Helper()
	for _, arg := range args {
		if arg == key {
			t.Fatalf("unexpected argument %q in %#v", key, args)
		}
	}
}

func assertNoArgContains(t *testing.T, args []string, value string) {
	t.Helper()
	for _, arg := range args {
		if strings.Contains(arg, value) {
			t.Fatalf("argument %q contains forbidden value", arg)
		}
	}
}

func valueAfter(t *testing.T, args []string, key string) string {
	t.Helper()
	return valueAfterTest(args, key)
}

func valueAfterTest(args []string, key string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key {
			return args[i+1]
		}
	}
	return ""
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
