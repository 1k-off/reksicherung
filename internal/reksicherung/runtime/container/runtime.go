// Package container defines the narrow container lifecycle used by restore
// workflows. Implementations must not expose a restore container to external
// networks or host ports by default.
package container

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
)

const (
	MinContainerDiskBytes   int64   = 1 << 30
	MaxContainerDiskBytes   int64   = 1 << 40
	MaxContainerMemory      int64   = 1 << 40
	MaxContainerCPUs        float64 = 64
	MaxEnvironmentBytes             = 64 << 10
	MaxEnvironmentVariables         = 64
	MaxCommandBytes                 = 64 << 10
	MaxCommandArguments             = 256
)

var (
	// ErrNotFound indicates that the requested container does not exist.
	ErrNotFound = errors.New("container not found")
	// ErrInvalidSpec indicates that a container request violates runtime limits.
	ErrInvalidSpec = errors.New("invalid container specification")
)

// EnvironmentVariable is an environment value passed to a container at create
// time. Values are intentionally excluded from String and diagnostic output.
type EnvironmentVariable struct {
	Name  string
	Value string
}

// Spec describes a short-lived, isolated restore container. All resource
// limits, including the maximum lifetime, are required and validated.
type Spec struct {
	RunID           string
	Image           string
	CPUs            float64
	MemoryBytes     int64
	DiskBytes       int64
	Timeout         time.Duration
	RetainForReview bool
	Environment     []EnvironmentVariable
}

// Container identifies a runtime-managed container without exposing its
// environment or other configuration.
type Container struct {
	ID    string
	Name  string
	RunID string
}

// Runtime starts and removes restore containers and streams bounded artifacts
// into them. Implementations should make Remove idempotent so interrupted
// workflow cleanup can be retried safely.
type Runtime interface {
	Start(context.Context, Spec) (Container, error)
	Remove(context.Context, Container) error
	Exec(context.Context, Container, []string) ([]byte, error)
	ExecWithEnvironment(context.Context, Container, []string, []EnvironmentVariable) ([]byte, error)
	Transfer(ctx context.Context, target Container, name string, source io.ReadCloser, maxBytes int64) error
}

// ValidateSpec checks fields that are shared by container implementations.
func ValidateSpec(spec Spec) error {
	if !validRunID(spec.RunID) {
		return fmt.Errorf("%w: run ID must be a stable ASCII identifier", ErrInvalidSpec)
	}
	if spec.Image == "" || strings.HasPrefix(spec.Image, "-") || strings.TrimSpace(spec.Image) != spec.Image || strings.ContainsAny(spec.Image, "\x00\r\n\t ") {
		return fmt.Errorf("%w: image reference is empty or contains whitespace", ErrInvalidSpec)
	}
	if math.IsNaN(spec.CPUs) || math.IsInf(spec.CPUs, 0) || spec.CPUs < 0.25 || spec.CPUs > MaxContainerCPUs {
		return fmt.Errorf("%w: CPU limit must be between 0.25 and %g", ErrInvalidSpec, MaxContainerCPUs)
	}
	if spec.MemoryBytes < 64*1024*1024 || spec.MemoryBytes > MaxContainerMemory {
		return fmt.Errorf("%w: memory limit must be between 64 MiB and 1 TiB", ErrInvalidSpec)
	}
	if spec.DiskBytes < MinContainerDiskBytes || spec.DiskBytes > MaxContainerDiskBytes {
		return fmt.Errorf("%w: writable-layer disk limit must be between 1 GiB and 1 TiB", ErrInvalidSpec)
	}
	if spec.Timeout < time.Minute || spec.Timeout > 24*time.Hour {
		return fmt.Errorf("%w: container lifetime must be between 1 minute and 24 hours", ErrInvalidSpec)
	}
	if err := ValidateEnvironment(spec.Environment); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSpec, err)
	}
	return nil
}

// ValidateEnvironment checks that environment variables can be represented
// safely in a bounded Podman environment file.
func ValidateEnvironment(environment []EnvironmentVariable) error {
	if len(environment) > MaxEnvironmentVariables {
		return fmt.Errorf("environment has more than %d variables", MaxEnvironmentVariables)
	}
	seen := make(map[string]struct{}, len(environment))
	totalBytes := 0
	for _, variable := range environment {
		if !validEnvironmentName(variable.Name) {
			return fmt.Errorf("invalid environment variable name %q", variable.Name)
		}
		if strings.ContainsAny(variable.Value, "\x00\r\n") {
			return fmt.Errorf("environment variable %q contains a forbidden character", variable.Name)
		}
		if _, exists := seen[variable.Name]; exists {
			return fmt.Errorf("duplicate environment variable %q", variable.Name)
		}
		seen[variable.Name] = struct{}{}
		totalBytes += len(variable.Name) + len(variable.Value) + 2
		if totalBytes > MaxEnvironmentBytes {
			return fmt.Errorf("environment exceeds %d bytes", MaxEnvironmentBytes)
		}
	}
	return nil
}

// ContainerName creates a stable human-readable name from a validated run ID.
func ContainerName(runID string) string {
	return "reksicherung-" + runID
}

// StopTimeout is the maximum graceful shutdown interval used during removal.
const StopTimeout = 10 * time.Second

func validRunID(id string) bool {
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

func validEnvironmentName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		alphaNumeric := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if i == 0 && !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_') {
			return false
		}
		if !alphaNumeric && r != '_' {
			return false
		}
	}
	return true
}
