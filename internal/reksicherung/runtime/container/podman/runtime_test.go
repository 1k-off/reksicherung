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
	"sync"
	"testing"
	"time"

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
	defer runtime.cancelExpiry(testContainerID)
	if got.ID != testContainerID || got.Name != container.ContainerName(spec.RunID) {
		t.Fatalf("Start() = %#v, want ID %q and name %q", got, testContainerID, container.ContainerName(spec.RunID))
	}
	if len(runner.calls) != 4 {
		t.Fatalf("Podman call count = %d, want create, two quota probes, and start", len(runner.calls))
	}
	if len(runner.deadlines) != 1 || time.Until(runner.deadlines[0]) > spec.Timeout || time.Until(runner.deadlines[0]) <= 0 {
		t.Fatalf("Podman create deadline = %#v, want a deadline bounded by %s", runner.deadlines, spec.Timeout)
	}
	create := runner.calls[0]
	if create.args[0] != "create" {
		t.Fatalf("first Podman command = %q, want create", create.args[0])
	}
	assertArgPair(t, create.args, "--network", "none")
	assertArgPair(t, create.args, "--cpus", "2")
	assertArgPair(t, create.args, "--memory", "2147483648")
	assertArgPair(t, create.args, "--storage-opt", "size=4294967296")
	assertArgPair(t, create.args, "--image-volume", "ignore")
	assertArgPair(t, create.args, "--pids-limit", "1024")
	assertArgPair(t, create.args, "--label", "reksicherung.managed=true")
	assertArgPair(t, create.args, "--label", "reksicherung.run-id=run-123")
	assertNoArgContains(t, create.args, secret)
	assertNoArgPair(t, create.args, "--publish")
	assertNoArgPair(t, create.args, "--volume")
	assertArgPair(t, runner.calls[1].args, "--network", "none")
	assertArgPair(t, runner.calls[1].args, "--storage-opt", "size=8m")
	assertArgPair(t, runner.calls[1].args, "--pids-limit", "32")
	assertArgPair(t, runner.calls[1].args, "count=4", "status=none")
	assertNoArgPair(t, runner.calls[1].args, "--volume")
	assertNoArgPair(t, runner.calls[1].args, "--mount")
	assertArgPair(t, runner.calls[2].args, "count=9", "status=none")

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
	if got := runner.calls[3].args; len(got) != 2 || got[0] != "start" || got[1] != testContainerID {
		t.Fatalf("start args = %#v, want start plus container ID", got)
	}
}

func TestStartDoesNotScheduleExpiryForHumanReviewRetention(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{outputs: map[string]string{"create": testContainerID}}
	runtime := &Runtime{executable: "podman", runner: runner}
	spec := testSpec()
	spec.RetainForReview = true
	if _, err := runtime.Start(context.Background(), spec); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if len(runtime.timeouts) != 0 {
		t.Fatalf("expiry timers = %#v, want none for a held run", runtime.timeouts)
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
		if len(runner.calls) != 3 || runner.calls[0].args[0] != "create" ||
			runner.calls[2].args[0] == "rm" {
			t.Fatalf("calls = %#v, pre-existing resource must not be removed", runner.calls)
		}
	})

	t.Run("partial create with matching ownership token is removed", func(t *testing.T) {
		t.Parallel()
		runner := &fakeRunner{
			fail:          map[string]error{"create": errors.New("connection interrupted")},
			partialCreate: true,
		}
		runtime := &Runtime{executable: "podman", runner: runner}
		_, err := runtime.Start(context.Background(), testSpec())
		if err == nil || !strings.Contains(err.Error(), "podman create failed") {
			t.Fatalf("Start() error = %v, want create failure", err)
		}
		if len(runner.calls) != 4 || runner.calls[3].args[0] != "rm" {
			t.Fatalf("calls = %#v, want ownership-confirmed partial-container cleanup", runner.calls)
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
		if len(runner.calls) != 5 || runner.calls[4].args[0] != "rm" {
			t.Fatalf("calls = %#v, want create, quota probes, start, cleanup", runner.calls)
		}
	})
}

func TestStartFailsClosedWhenPodmanIgnoresDiskQuota(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{
		outputs:         map[string]string{"create": testContainerID},
		ignoreDiskQuota: true,
	}
	runtime := &Runtime{executable: "podman", runner: runner}
	_, err := runtime.Start(context.Background(), testSpec())
	if !errors.Is(err, ErrDiskQuotaUnsupported) {
		t.Fatalf("Start() error = %v, want ErrDiskQuotaUnsupported", err)
	}
	if len(runner.calls) != 4 || runner.calls[3].args[0] != "rm" {
		t.Fatalf("calls = %#v, want create, control probe, quota probe, cleanup", runner.calls)
	}
}

func TestStartFailsClosedWhenQuotaProbeFailureIsAmbiguous(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{
		outputs:               map[string]string{"create": testContainerID},
		ambiguousQuotaFailure: true,
	}
	runtime := &Runtime{executable: "podman", runner: runner}
	_, err := runtime.Start(context.Background(), testSpec())
	if err == nil || !strings.Contains(err.Error(), "without an explicit quota-exceeded result") {
		t.Fatalf("Start() error = %v, want ambiguous quota failure", err)
	}
	if len(runner.calls) != 4 || runner.calls[3].args[0] != "rm" {
		t.Fatalf("calls = %#v, want cleanup after ambiguous quota probe", runner.calls)
	}
}

func TestExecUsesArgumentVectorAndBoundsOutput(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		runner := &fakeRunner{outputs: map[string]string{"exec": "database list\n"}}
		runtime := &Runtime{executable: "podman", runner: runner}
		got, err := runtime.Exec(context.Background(), testContainerRef(), []string{"sqlcmd", "-Q", "SELECT 1"})
		if err != nil {
			t.Fatalf("Exec() error = %v", err)
		}
		if string(got) != "database list\n" {
			t.Fatalf("Exec() output = %q", got)
		}
		want := []string{"exec", testContainerID, "sqlcmd", "-Q", "SELECT 1"}
		if !equalStrings(runner.calls[2].args, want) {
			t.Fatalf("Podman args = %#v, want %#v", runner.calls[2].args, want)
		}
	})

	t.Run("output too large", func(t *testing.T) {
		t.Parallel()
		runner := &fakeRunner{writeOutput: bytes.Repeat([]byte("x"), maxExecOutput+1)}
		runtime := &Runtime{executable: "podman", runner: runner}
		if _, err := runtime.Exec(context.Background(), testContainerRef(), []string{"command"}); err == nil {
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
				target := testContainerRef()
				target.ID = test.id
				if _, err := runtime.Exec(context.Background(), target, test.command); err == nil {
					t.Fatal("Exec() error = nil, want validation error")
				}
			})
		}
		if len(runner.calls) != 0 {
			t.Fatalf("invalid requests invoked Podman: %#v", runner.calls)
		}
	})
}

func TestExecWithEnvironmentKeepsValuesOutOfArgumentsAndRemovesFile(t *testing.T) {
	t.Parallel()

	const secret = "sentinel-command-secret"
	runner := &fakeRunner{outputs: map[string]string{"exec": "ok"}}
	runtime := &Runtime{executable: "podman", runner: runner}
	output, err := runtime.ExecWithEnvironment(context.Background(), testContainerRef(),
		[]string{"restic", "snapshots"}, []container.EnvironmentVariable{
			{Name: "RESTIC_PASSWORD", Value: secret},
		})
	if err != nil {
		t.Fatalf("ExecWithEnvironment() error = %v", err)
	}
	if string(output) != "ok" {
		t.Fatalf("output = %q, want ok", output)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("Podman calls = %d, want existence, ownership, and exec", len(runner.calls))
	}
	args := runner.calls[2].args
	if !equalStrings(args[:2], []string{"exec", "--env-file"}) ||
		!equalStrings(args[len(args)-3:], []string{testContainerID, "restic", "snapshots"}) {
		t.Fatalf("Podman args = %#v", args)
	}
	assertNoArgContains(t, args, secret)
	envPath := valueAfter(t, args, "--env-file")
	if _, err := os.Stat(envPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("environment file remains after command: %v", err)
	}
	if !strings.Contains(runner.envFileContents, "RESTIC_PASSWORD="+secret+"\n") {
		t.Fatal("temporary command environment did not contain expected variable")
	}
}

func TestExecWithEnvironmentRejectsInvalidRequestsBeforePodman(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	runtime := &Runtime{executable: "podman", runner: runner}
	tests := []struct {
		name        string
		command     []string
		environment []container.EnvironmentVariable
	}{
		{name: "empty command"},
		{name: "NUL argument", command: []string{"restic\x00"}},
		{name: "invalid env name", command: []string{"restic"}, environment: []container.EnvironmentVariable{{Name: "BAD=NAME", Value: "x"}}},
		{name: "newline env", command: []string{"restic"}, environment: []container.EnvironmentVariable{{Name: "TOKEN", Value: "first\nsecond"}}},
		{name: "duplicate env", command: []string{"restic"}, environment: []container.EnvironmentVariable{{Name: "TOKEN", Value: "a"}, {Name: "TOKEN", Value: "b"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := runtime.ExecWithEnvironment(context.Background(), testContainerRef(), test.command, test.environment); err == nil {
				t.Fatal("ExecWithEnvironment() error = nil, want validation error")
			}
		})
	}
	if len(runner.calls) != 0 {
		t.Fatalf("invalid requests invoked Podman: %#v", runner.calls)
	}
}

func TestTransferStreamsIntoControlledPathAndPublishesOnlyAfterEOF(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	runtime := &Runtime{executable: "podman", runner: runner}
	content := []byte("small backup stream")
	if err := runtime.Transfer(context.Background(), testContainerRef(), "database.bak", io.NopCloser(bytes.NewReader(content)), int64(len(content))); err != nil {
		t.Fatalf("Transfer() error = %v", err)
	}
	if len(runner.calls) != 5 {
		t.Fatalf("Podman call count = %d, want ownership checks, mkdir, stream, publish", len(runner.calls))
	}
	if !equalStrings(runner.calls[2].args, []string{"exec", testContainerID, "mkdir", "-p", "--", artifactDirectory}) {
		t.Fatalf("directory command = %#v", runner.calls[2].args)
	}
	stream := runner.calls[3].args
	if len(stream) != 6 || stream[0] != "exec" || stream[1] != "--interactive" ||
		stream[2] != testContainerID || stream[3] != "tee" || stream[4] != "--" {
		t.Fatalf("stream command = %#v", stream)
	}
	tempPath := stream[5]
	if !strings.HasPrefix(tempPath, artifactDirectory+"/.reksicherung-upload-") {
		t.Fatalf("temporary path = %q, want controlled backup directory", tempPath)
	}
	if !bytes.Equal(runner.stdinData.Bytes(), content) {
		t.Fatalf("streamed bytes = %q, want %q", runner.stdinData.Bytes(), content)
	}
	wantPublish := []string{"exec", testContainerID, "mv", "-fT", "--", tempPath, artifactDirectory + "/database.bak"}
	if !equalStrings(runner.calls[4].args, wantPublish) {
		t.Fatalf("publish command = %#v, want %#v", runner.calls[4].args, wantPublish)
	}
	for _, recorded := range runner.calls {
		assertNoArgPair(t, recorded.args, "--volume")
		assertNoArgPair(t, recorded.args, "--mount")
	}
}

func TestTransferRejectsOversizeAndCleansPartialInContainerFile(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	runtime := &Runtime{executable: "podman", runner: runner}
	err := runtime.Transfer(context.Background(), testContainerRef(), "database.bak", io.NopCloser(strings.NewReader("12345")), 4)
	if !errors.Is(err, ErrArtifactTooLarge) {
		t.Fatalf("Transfer() error = %v, want ErrArtifactTooLarge", err)
	}
	if len(runner.calls) != 5 || runner.calls[4].args[0] != "exec" ||
		!equalStrings(runner.calls[4].args[2:], []string{"rm", "-f", "--", runner.calls[3].args[5]}) {
		t.Fatalf("calls = %#v, want directory, bounded stream, and partial-file cleanup", runner.calls)
	}
	if got := runner.stdinData.String(); got != "1234" {
		t.Fatalf("bytes sent to container = %q, want exactly the configured limit", got)
	}
}

func TestTransferRejectsUnsafeRequestsWithoutCallingPodman(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	runtime := &Runtime{executable: "podman", runner: runner}
	for _, test := range []struct {
		name string
		id   string
		file string
		max  int64
		src  io.ReadCloser
	}{
		{name: "bad ID", id: "../host", file: "backup.bak", max: 10, src: io.NopCloser(strings.NewReader("x"))},
		{name: "traversal", id: testContainerID, file: "../backup.bak", max: 10, src: io.NopCloser(strings.NewReader("x"))},
		{name: "option-like name", id: testContainerID, file: "--volume", max: 10, src: io.NopCloser(strings.NewReader("x"))},
		{name: "zero limit", id: testContainerID, file: "backup.bak", max: 0, src: io.NopCloser(strings.NewReader("x"))},
		{name: "limit above hard cap", id: testContainerID, file: "backup.bak", max: maxArtifactBytes + 1, src: io.NopCloser(strings.NewReader("x"))},
		{name: "nil source", id: testContainerID, file: "backup.bak", max: 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := testContainerRef()
			target.ID = test.id
			if err := runtime.Transfer(context.Background(), target, test.file, test.src, test.max); err == nil {
				t.Fatal("Transfer() error = nil, want validation error")
			}
		})
	}
	if len(runner.calls) != 0 {
		t.Fatalf("invalid requests invoked Podman: %#v", runner.calls)
	}
}

func TestRemoveUsesIdempotentPodmanFlags(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	runtime := &Runtime{executable: "podman", runner: runner}
	if err := runtime.Remove(context.Background(), testContainerRef()); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	want := []string{"rm", "--ignore", "--force", "--time", "10", testContainerID}
	if !equalStrings(runner.calls[2].args, want) {
		t.Fatalf("Podman args = %#v, want %#v", runner.calls[2].args, want)
	}
}

func TestRuntimeRejectsContainersWithoutManagedLabel(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{outputs: map[string]string{"inspect": "{}"}}
	runtime := &Runtime{executable: "podman", runner: runner}
	if _, err := runtime.Exec(context.Background(), testContainerRef(), []string{"id"}); err == nil ||
		!strings.Contains(err.Error(), "not managed by Reksicherung") {
		t.Fatalf("Exec() error = %v, want ownership error", err)
	}
	if len(runner.calls) != 2 || runner.calls[0].args[1] != "exists" ||
		runner.calls[1].args[0] != "inspect" {
		t.Fatalf("unowned container was acted on: %#v", runner.calls)
	}
}

func TestRuntimeRejectsContainerFromDifferentRun(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	runtime := &Runtime{executable: "podman", runner: runner}
	target := testContainerRef()
	target.RunID = "another-run"
	target.Name = container.ContainerName(target.RunID)
	if _, err := runtime.Exec(context.Background(), target, []string{"id"}); err == nil ||
		!strings.Contains(err.Error(), "ownership does not match") {
		t.Fatalf("Exec() error = %v, want run ownership error", err)
	}
	if len(runner.calls) != 2 || runner.calls[1].args[0] != "inspect" {
		t.Fatalf("mismatched container was acted on: %#v", runner.calls)
	}
}

func TestRemoveTreatsMissingContainerAsAlreadyRemoved(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{fail: map[string]error{"container": exitCodeError(1)}}
	runtime := &Runtime{executable: "podman", runner: runner}
	if err := runtime.Remove(context.Background(), testContainerRef()); err != nil {
		t.Fatalf("Remove() error = %v, want idempotent success", err)
	}
	if len(runner.calls) != 1 || runner.calls[0].args[1] != "exists" {
		t.Fatalf("missing container should not be removed by ID: %#v", runner.calls)
	}
}

func TestConfiguredLifetimeRemovesContainer(t *testing.T) {
	runner := &expiryRunner{calls: make(chan []string, 1)}
	runtime := &Runtime{executable: "podman", runner: runner}
	runtime.scheduleExpiry(testContainerID, 5*time.Millisecond)

	select {
	case args := <-runner.calls:
		want := []string{"rm", "--ignore", "--force", "--time", "10", testContainerID}
		if !equalStrings(args, want) {
			t.Fatalf("expiry removal args = %#v, want %#v", args, want)
		}
	case <-time.After(time.Second):
		t.Fatal("configured lifetime did not remove the container")
	}
}

func TestTransferHonorsCanceledContextBeforeCallingPodman(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	runtime := &Runtime{executable: "podman", runner: runner}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := &trackedReadCloser{Reader: strings.NewReader("data")}
	if err := runtime.Transfer(ctx, testContainerRef(), "database.bak", source, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("Transfer() error = %v, want context.Canceled", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("canceled transfer invoked Podman: %#v", runner.calls)
	}
	if !source.closed {
		t.Fatal("Transfer() did not close the owned source on early cancellation")
	}
}

func TestTransferCleansPartialFileWhenSourceIsCanceled(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	runtime := &Runtime{executable: "podman", runner: runner}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &cancelingSource{cancel: cancel}
	err := runtime.Transfer(ctx, testContainerRef(), "database.bak", source, 100)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Transfer() error = %v, want context.Canceled", err)
	}
	if len(runner.calls) != 5 || runner.calls[4].args[2] != "rm" {
		t.Fatalf("calls = %#v, want ownership checks, directory, partial stream, and cleanup", runner.calls)
	}
}

func TestTransferClosesBlockedSourceOnCancellation(t *testing.T) {
	t.Parallel()

	runtime := &Runtime{executable: "podman", runner: &fakeRunner{}}
	ctx, cancel := context.WithCancel(context.Background())
	source := &blockingReadCloser{started: make(chan struct{}), closed: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		result <- runtime.Transfer(ctx, testContainerRef(), "database.bak", source, 100)
	}()

	select {
	case <-source.started:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("transfer did not begin reading source")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Transfer() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Transfer() remained blocked after cancellation")
	}
	select {
	case <-source.closed:
	default:
		t.Fatal("Transfer() did not close the blocked source")
	}
}

func testSpec() container.Spec {
	return container.Spec{
		RunID:       "run-123",
		Image:       "mcr.microsoft.com/mssql/server:2022-latest",
		CPUs:        2,
		MemoryBytes: 2 * 1024 * 1024 * 1024,
		DiskBytes:   4 * 1024 * 1024 * 1024,
		Timeout:     time.Hour,
	}
}

func testContainerRef() container.Container {
	return container.Container{
		ID:    testContainerID,
		Name:  container.ContainerName("run-123"),
		RunID: "run-123",
	}
}

type cancelingSource struct {
	cancel context.CancelFunc
	sent   bool
}

func (r *cancelingSource) Read(buffer []byte) (int, error) {
	if !r.sent {
		r.sent = true
		read := copy(buffer, "partial")
		r.cancel()
		return read, nil
	}
	return 0, context.Canceled
}

func (r *cancelingSource) Close() error { return nil }

type trackedReadCloser struct {
	io.Reader
	closed bool
}

func (source *trackedReadCloser) Close() error {
	source.closed = true
	return nil
}

type blockingReadCloser struct {
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (source *blockingReadCloser) Read([]byte) (int, error) {
	source.once.Do(func() { close(source.started) })
	<-source.closed
	return 0, io.ErrClosedPipe
}

func (source *blockingReadCloser) Close() error {
	select {
	case <-source.closed:
	default:
		close(source.closed)
	}
	return nil
}

type expiryRunner struct {
	calls chan []string
}

func (r *expiryRunner) Run(_ context.Context, _ string, args []string, _ io.Reader, _ io.Writer) error {
	r.calls <- append([]string(nil), args...)
	return nil
}

type call struct {
	args []string
}

type fakeRunner struct {
	calls                 []call
	outputs               map[string]string
	fail                  map[string]error
	writeOutput           []byte
	envFileContents       string
	stdinData             bytes.Buffer
	ignoreDiskQuota       bool
	ambiguousQuotaFailure bool
	partialCreate         bool
	createToken           string
	createRunID           string
	deadlines             []time.Time
}

type exitCodeError int

func (err exitCodeError) Error() string { return "container does not exist" }
func (err exitCodeError) ExitCode() int { return int(err) }

func (r *fakeRunner) Run(ctx context.Context, _ string, args []string, stdin io.Reader, stdout io.Writer) error {
	argsCopy := append([]string(nil), args...)
	r.calls = append(r.calls, call{args: argsCopy})
	if stdin != nil {
		if _, err := io.Copy(&r.stdinData, stdin); err != nil {
			return err
		}
	}
	if args[0] == "create" {
		deadline, ok := ctx.Deadline()
		if !ok {
			return errors.New("create context has no deadline")
		}
		r.deadlines = append(r.deadlines, deadline)
		for _, arg := range args {
			if strings.HasPrefix(arg, "reksicherung.run-id=") {
				r.createRunID = strings.TrimPrefix(arg, "reksicherung.run-id=")
			}
			if r.partialCreate && strings.HasPrefix(arg, "reksicherung.create-token=") {
				r.createToken = strings.TrimPrefix(arg, "reksicherung.create-token=")
			}
		}
	}
	if containsArg(args, "--env-file") {
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
		if args[0] == "create" && r.partialCreate {
			return err
		}
		return err
	}
	if args[0] == "run" {
		if containsArg(args, "count=4") {
			return nil
		}
		if r.ambiguousQuotaFailure {
			return errors.New("filesystem is full")
		}
		if r.ignoreDiskQuota {
			return nil
		}
		return errors.New("quota enforcement reached")
	}
	output := r.outputs[args[0]]
	if args[0] == "inspect" && output == "" {
		runID := r.createRunID
		if runID == "" {
			runID = "run-123"
		}
		output = `{"reksicherung.managed":"true","reksicherung.run-id":"` + runID +
			`","reksicherung.create-token":"` + r.createToken + `"}`
	}
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

func (r *fakeRunner) RunWithStderr(ctx context.Context, executable string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	err := r.Run(ctx, executable, args, stdin, stdout)
	if err != nil && containsArg(args, "count=9") && !r.ambiguousQuotaFailure {
		if _, writeErr := io.WriteString(stderr, "dd: error writing output: Disk quota exceeded\n"); writeErr != nil {
			return writeErr
		}
	}
	return err
}

func containsArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
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
