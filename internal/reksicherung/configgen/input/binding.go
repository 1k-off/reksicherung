package input

import (
	"sort"
	"strings"
)

// Binding maps a runtime environment variable to a provider-neutral secret
// reference. It intentionally contains no resolved secret value.
type Binding struct {
	EnvironmentVariable string `json:"environment_variable" yaml:"environment_variable"`
	SecretReference     string `json:"secret_reference" yaml:"secret_reference"`
}

// Binder collects unique $REKSICHERUNG_* placeholders during Generate.
type Binder struct {
	bindings []Binding
	seen     map[string]struct{}
}

// NewBinder returns an empty binder.
func NewBinder() *Binder {
	return &Binder{seen: make(map[string]struct{})}
}

// Placeholder registers a secret reference under a stable REKSICHERUNG_* env
// name derived from path and returns "$ENV_NAME". Empty references yield "".
func (b *Binder) Placeholder(path, reference string) string {
	if reference == "" {
		return ""
	}
	envName := "REKSICHERUNG_" + strings.ToUpper(StableName(path))
	if _, exists := b.seen[envName]; !exists {
		b.seen[envName] = struct{}{}
		b.bindings = append(b.bindings, Binding{
			EnvironmentVariable: envName,
			SecretReference:     reference,
		})
	}
	return "$" + envName
}

// Bindings returns a sorted copy of collected bindings.
func (b *Binder) Bindings() []Binding {
	out := append([]Binding(nil), b.bindings...)
	sort.Slice(out, func(i, j int) bool {
		return out[i].EnvironmentVariable < out[j].EnvironmentVariable
	})
	return out
}
