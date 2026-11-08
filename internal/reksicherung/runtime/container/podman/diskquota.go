package podman

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
)

var ErrDiskQuotaUnsupported = errors.New("Podman writable-layer disk quota is not enforced")

// verifyDiskQuota performs a small write probe in an ephemeral, networkless
// container. Podman versions and storage drivers have been observed to accept
// --storage-opt while silently ignoring it, so merely passing the option is
// not enough to safely start a restore.
func (r *Runtime) verifyDiskQuota(ctx context.Context, spec container.Spec) error {
	baseArgs := []string{
		"run", "--rm",
		"--network", "none",
		"--cpus", strconv.FormatFloat(spec.CPUs, 'f', -1, 64),
		"--memory", strconv.FormatInt(spec.MemoryBytes, 10),
		"--storage-opt", "size=" + quotaProbeLimit,
		"--image-volume", "ignore",
		"--pids-limit", "32",
		"--entrypoint", "dd",
		spec.Image,
	}
	controlArgs := append(append([]string(nil), baseArgs...),
		"if=/dev/zero", "of=/tmp/reksicherung-quota-control", "bs=1M", "count=4", "status=none")
	if err := r.runner.Run(ctx, r.executable, controlArgs, nil, io.Discard); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return fmt.Errorf("disk quota probe could not complete its in-limit write: %w", err)
	}

	overLimitArgs := append(append([]string(nil), baseArgs...),
		"if=/dev/zero", "of=/tmp/reksicherung-quota-probe", "bs=1M", "count=9", "status=none")
	runner, ok := r.runner.(stderrCommandRunner)
	if !ok {
		return errors.New("Podman runner cannot report the disk quota probe failure reason")
	}
	stderr := &limitedBuffer{
		limit: 4096,
	}
	err := runner.RunWithStderr(ctx, r.executable, overLimitArgs, nil, io.Discard, stderr)
	if err == nil {
		return ErrDiskQuotaUnsupported
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if stderr.exceeded || !strings.Contains(stderr.String(), "Disk quota exceeded") {
		return fmt.Errorf("disk quota probe failed without an explicit quota-exceeded result: %w", err)
	}
	return nil
}
