package container

import (
	"errors"
	"math"
	"testing"
)

func TestValidateSpecAcceptsValidRestoreContainer(t *testing.T) {
	t.Parallel()

	spec := Spec{
		RunID:       "run-123",
		Image:       "mcr.microsoft.com/mssql/server:2022-latest",
		CPUs:        2,
		MemoryBytes: 2 * 1024 * 1024 * 1024,
		Environment: []EnvironmentVariable{
			{Name: "ACCEPT_EULA", Value: "Y"},
			{Name: "MSSQL_SA_PASSWORD", Value: "dummy-test-password"},
		},
	}
	if err := ValidateSpec(spec); err != nil {
		t.Fatalf("ValidateSpec() error = %v", err)
	}
}

func TestValidateSpecRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	valid := Spec{
		RunID:       "run-123",
		Image:       "mcr.microsoft.com/mssql/server:2022-latest",
		CPUs:        2,
		MemoryBytes: 2 * 1024 * 1024 * 1024,
	}
	tests := []struct {
		name string
		edit func(*Spec)
	}{
		{
			name: "invalid run ID",
			edit: func(spec *Spec) { spec.RunID = "../production" },
		},
		{
			name: "image option injection",
			edit: func(spec *Spec) { spec.Image = "--privileged" },
		},
		{
			name: "zero CPU",
			edit: func(spec *Spec) { spec.CPUs = 0 },
		},
		{
			name: "infinite CPU",
			edit: func(spec *Spec) { spec.CPUs = math.Inf(1) },
		},
		{
			name: "insufficient memory",
			edit: func(spec *Spec) { spec.MemoryBytes = 1 },
		},
		{
			name: "invalid environment key",
			edit: func(spec *Spec) {
				spec.Environment = []EnvironmentVariable{{Name: "9BAD", Value: "x"}}
			},
		},
		{
			name: "duplicate environment key",
			edit: func(spec *Spec) {
				spec.Environment = []EnvironmentVariable{
					{Name: "KEY", Value: "one"},
					{Name: "KEY", Value: "two"},
				}
			},
		},
		{
			name: "newline environment value",
			edit: func(spec *Spec) {
				spec.Environment = []EnvironmentVariable{{Name: "KEY", Value: "one\ntwo"}}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			spec := valid
			test.edit(&spec)
			if err := ValidateSpec(spec); !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("ValidateSpec() error = %v, want ErrInvalidSpec", err)
			}
		})
	}
}

func TestContainerName(t *testing.T) {
	t.Parallel()

	if got, want := ContainerName("run-abc123"), "reksicherung-run-abc123"; got != want {
		t.Fatalf("ContainerName() = %q, want %q", got, want)
	}
}
