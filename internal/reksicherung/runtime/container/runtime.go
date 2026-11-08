// Package container defines the narrow container lifecycle used by restore
// workflows. Implementations must not expose a restore container to external
// networks or host ports by default.
package container

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
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

// Spec describes a short-lived, isolated restore container.
type Spec struct {
	RunID       string
	Image       string
	CPUs        float64
	MemoryBytes int64
	Environment []EnvironmentVariable
}

// Container identifies a runtime-managed container without exposing its
// environment or other configuration.
type Container struct {
	ID   string
	Name string
}

// Runtime starts and removes restore containers. Implementations should make
// Remove idempotent so interrupted workflow cleanup can be retried safely.
type Runtime interface {
	Start(context.Context, Spec) (Container, error)
	Remove(context.Context, string) error
	Exec(context.Context, string, []string) ([]byte, error)
}

// ValidateSpec checks fields that are shared by container implementations.
func ValidateSpec(spec Spec) error {
	if !validRunID(spec.RunID) {
		return fmt.Errorf("%w: run ID must be a stable ASCII identifier", ErrInvalidSpec)
	}
	if spec.Image == "" || strings.HasPrefix(spec.Image, "-") || strings.TrimSpace(spec.Image) != spec.Image || strings.ContainsAny(spec.Image, "\x00\r\n\t ") {
		return fmt.Errorf("%w: image reference is empty or contains whitespace", ErrInvalidSpec)
	}
	if math.IsNaN(spec.CPUs) || math.IsInf(spec.CPUs, 0) || spec.CPUs <= 0 {
		return fmt.Errorf("%w: CPU limit must be a finite positive number", ErrInvalidSpec)
	}
	if spec.MemoryBytes < 64*1024*1024 {
		return fmt.Errorf("%w: memory limit must be at least 64 MiB", ErrInvalidSpec)
	}
	seen := make(map[string]struct{}, len(spec.Environment))
	for _, variable := range spec.Environment {
		if !validEnvironmentName(variable.Name) {
			return fmt.Errorf("%w: invalid environment variable name %q", ErrInvalidSpec, variable.Name)
		}
		if strings.ContainsAny(variable.Value, "\x00\r\n") {
			return fmt.Errorf("%w: environment variable %q contains a forbidden character", ErrInvalidSpec, variable.Name)
		}
		if _, exists := seen[variable.Name]; exists {
			return fmt.Errorf("%w: duplicate environment variable %q", ErrInvalidSpec, variable.Name)
		}
		seen[variable.Name] = struct{}{}
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
