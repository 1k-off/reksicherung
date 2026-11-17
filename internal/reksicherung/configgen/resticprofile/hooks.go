package resticprofile

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/1k-off/reksicherung/internal/reksicherung/configgen/input"
)

// Sidecar is a companion file emitted next to the resticprofile YAML.
// Name is a basename only (no directories) so the CLI can write it beside -out.
type Sidecar struct {
	Name    string
	Content []byte
}

// wrapHooks writes hook commands into sidecar scripts and returns short
// invocations for run-before/run-after. This avoids nested-quote breakage when
// resticprofile runs hooks via cmd.exe on Windows
// (https://github.com/creativeprojects/resticprofile/issues/582).
func wrapHooks(in input.Input) ([]Sidecar, []string, []string) {
	if len(in.Hooks.RunBefore) == 0 && len(in.Hooks.RunAfter) == 0 {
		return nil, nil, nil
	}
	project := input.StableName(in.Project.Name)
	windows := usesWindowsHost(in)
	var sidecars []Sidecar
	var beforeInv, afterInv []string
	if len(in.Hooks.RunBefore) > 0 {
		sc, inv := hookScript(project, "run-before", in.Hooks.RunBefore, windows)
		sidecars = append(sidecars, sc)
		beforeInv = []string{inv}
	}
	if len(in.Hooks.RunAfter) > 0 {
		sc, inv := hookScript(project, "run-after", in.Hooks.RunAfter, windows)
		sidecars = append(sidecars, sc)
		afterInv = []string{inv}
	}
	return sidecars, beforeInv, afterInv
}

func hookScript(project, phase string, commands input.StringList, windows bool) (Sidecar, string) {
	base := project + "-" + phase
	if windows {
		name := base + ".ps1"
		// powershell -File avoids nested -Command quoting (resticprofile #582).
		inv := "powershell -NoProfile -ExecutionPolicy Bypass -File " + name
		return Sidecar{Name: name, Content: []byte(renderPowerShellScript(commands))}, inv
	}
	name := base + ".sh"
	return Sidecar{Name: name, Content: []byte(renderShScript(commands))}, "sh " + name
}

func renderPowerShellScript(commands input.StringList) string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\r\n")
	for _, command := range commands {
		// Run via cmd so operator lines can keep cmd/exe syntax (sqlpackage, del).
		// Single-quoted PowerShell strings keep nested double quotes intact.
		b.WriteString("cmd.exe /c '")
		b.WriteString(escapePowerShellSingleQuoted(command))
		b.WriteString("'\r\n")
		b.WriteString("if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }\r\n")
	}
	return b.String()
}

func escapePowerShellSingleQuoted(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

func renderShScript(commands input.StringList) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("set -eu\n")
	for _, command := range commands {
		b.WriteString(command)
		b.WriteByte('\n')
	}
	return b.String()
}

func usesWindowsHost(in input.Input) bool {
	if in.Archive == nil {
		return false
	}
	for _, value := range append(append([]string(nil), in.Archive.Includes...), in.Archive.Excludes...) {
		if looksLikeWindowsPath(value) {
			return true
		}
	}
	return false
}

func looksLikeWindowsPath(value string) bool {
	normalized := strings.ReplaceAll(value, `\`, `/`)
	if len(normalized) < 3 {
		return false
	}
	drive := normalized[0]
	if !((drive >= 'A' && drive <= 'Z') || (drive >= 'a' && drive <= 'z')) {
		return false
	}
	return normalized[1] == ':' && normalized[2] == '/'
}

func validSidecarName(name string) error {
	if name == "" || name != strings.TrimSpace(name) || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return fmt.Errorf("resticprofile: invalid sidecar name %q", name)
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("resticprofile: invalid sidecar name %q", name)
	}
	return nil
}
