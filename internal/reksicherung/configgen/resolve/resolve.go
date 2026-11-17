// Package resolve materializes configgen YAML by substituting OpenBao-backed
// binding placeholders with resolved secret values (unsafe render path).
package resolve

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/1k-off/reksicherung/internal/reksicherung/configgen/input"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
	"gopkg.in/yaml.v3"
)

// UnsafeYAML replaces $REKSICHERUNG_* placeholders from bindings with values
// resolved through provider. Fail closed on missing refs or empty values.
// The returned document may contain secrets — callers must treat it as sensitive.
func UnsafeYAML(ctx context.Context, safeYAML []byte, bindings []input.Binding, provider secrets.SecretProvider) ([]byte, error) {
	if provider == nil {
		return nil, fmt.Errorf("resolve: secret provider is required")
	}
	if len(bindings) == 0 {
		return append([]byte(nil), safeYAML...), nil
	}

	replacements := make([]replacement, 0, len(bindings))
	for _, binding := range bindings {
		if binding.EnvironmentVariable == "" || binding.SecretReference == "" {
			return nil, fmt.Errorf("resolve: binding missing environment variable or secret reference")
		}
		ref, err := secrets.ParseReference(binding.SecretReference)
		if err != nil {
			return nil, fmt.Errorf("resolve: invalid secret reference for %s: %w", binding.EnvironmentVariable, err)
		}
		raw, err := provider.Resolve(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("resolve: %s: %w", binding.EnvironmentVariable, err)
		}
		value := string(raw)
		if value == "" {
			return nil, fmt.Errorf("resolve: %s resolved to an empty value", binding.EnvironmentVariable)
		}
		replacements = append(replacements, replacement{
			placeholder: "$" + binding.EnvironmentVariable,
			value:       value,
		})
	}
	// Longer placeholders first so $FOO does not clobber $FOO_BAR.
	sort.Slice(replacements, func(i, j int) bool {
		return len(replacements[i].placeholder) > len(replacements[j].placeholder)
	})

	var doc any
	if err := yaml.Unmarshal(safeYAML, &doc); err != nil {
		return nil, fmt.Errorf("resolve: parse safe yaml: %w", err)
	}
	if err := substitute(doc, replacements); err != nil {
		return nil, err
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("resolve: marshal unsafe yaml: %w", err)
	}
	return out, nil
}

type replacement struct {
	placeholder string
	value       string
}

func substitute(node any, replacements []replacement) error {
	switch value := node.(type) {
	case map[string]any:
		for key, child := range value {
			switch typed := child.(type) {
			case string:
				replaced, err := replaceAll(typed, replacements)
				if err != nil {
					return err
				}
				value[key] = replaced
			default:
				if err := substitute(child, replacements); err != nil {
					return err
				}
			}
		}
	case []any:
		for i, child := range value {
			switch typed := child.(type) {
			case string:
				replaced, err := replaceAll(typed, replacements)
				if err != nil {
					return err
				}
				value[i] = replaced
			default:
				if err := substitute(child, replacements); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func replaceAll(text string, replacements []replacement) (string, error) {
	out := text
	for _, item := range replacements {
		if !strings.Contains(out, item.placeholder) {
			continue
		}
		if strings.Contains(item.value, "\x00") {
			return "", fmt.Errorf("resolve: secret value contains NUL")
		}
		out = strings.ReplaceAll(out, item.placeholder, item.value)
	}
	if strings.Contains(out, "$REKSICHERUNG_") {
		return "", fmt.Errorf("resolve: unresolved REKSICHERUNG placeholder remains")
	}
	return out, nil
}
