package resticprofile

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/1k-off/reksicherung/internal/reksicherung/configgen/input"
)

const windowsResticConfigDir = `C:\restic\config`

// Sidecar is a companion file emitted next to the resticprofile YAML.
// Name is a basename only (no directories) so the CLI can write it beside -out.
type Sidecar struct {
	Name    string
	Content []byte
}

// openbaoRefInText matches openbao://…#key refs embedded in hook command strings.
var openbaoRefInText = regexp.MustCompile(`openbao://[^\s'",]+`)

// wrapHooks maps I-CONFIGGEN hooks onto resticprofile run-before/run-after.
//
// Windows (legacy parity with util/configgen restic):
//   - run_before → pure PowerShell <project>-backup.ps1;
//     profile run-before calls `powershell -File C:\restic\config\<project>-backup.ps1`
//   - run_after  → pure PowerShell <project>-cleanup.ps1;
//     profile run-after calls `powershell -File C:\restic\config\<project>-cleanup.ps1`
//
// POSIX: both lists stay inline (no sidecars).
// Embedded openbao://…#key refs become $REKSICHERUNG_* (YAML) or $env:REKSICHERUNG_* (PS1).
func wrapHooks(in input.Input, bindings *input.Binder) ([]Sidecar, []string, []string) {
	if len(in.Hooks.RunBefore) == 0 && len(in.Hooks.RunAfter) == 0 {
		return nil, nil, nil
	}
	project := input.StableName(in.Project.Name)
	windows := usesWindowsHost(in)

	beforeCmds := bindHookCommands(in.Hooks.RunBefore, bindings, "hooks_run_before")
	afterCmds := bindHookCommands(in.Hooks.RunAfter, bindings, "hooks_run_after")

	if !windows {
		return nil, beforeCmds, afterCmds
	}

	var sidecars []Sidecar
	var beforeInv, afterInv []string
	if len(beforeCmds) > 0 {
		sc, inv := windowsHookSidecar(project, "backup", beforeCmds)
		sidecars = append(sidecars, sc)
		beforeInv = []string{inv}
	}
	if len(afterCmds) > 0 {
		sc, inv := windowsHookSidecar(project, "cleanup", afterCmds)
		sidecars = append(sidecars, sc)
		afterInv = []string{inv}
	}
	return sidecars, beforeInv, afterInv
}

func windowsHookSidecar(project, phase string, commands []string) (Sidecar, string) {
	scriptName := project + "-" + phase + ".ps1"
	body := renderPowerShellScript(powershellEnvPlaceholders(commands))
	inv := "powershell -File " + windowsResticConfigDir + `\` + scriptName
	return Sidecar{Name: scriptName, Content: []byte(body)}, inv
}

// powershellEnvPlaceholders turns $REKSICHERUNG_* into $env:REKSICHERUNG_* so
// safe scripts read process environment; UnsafeText still substitutes both forms.
func powershellEnvPlaceholders(commands []string) []string {
	out := make([]string, len(commands))
	for i, command := range commands {
		out[i] = strings.ReplaceAll(command, "$REKSICHERUNG_", "$env:REKSICHERUNG_")
	}
	return out
}

func bindHookCommands(commands input.StringList, bindings *input.Binder, pathPrefix string) []string {
	if len(commands) == 0 {
		return nil
	}
	out := make([]string, 0, len(commands))
	for i, command := range commands {
		out = append(out, bindOpenBaoRefsInText(command, bindings, fmt.Sprintf("%s_%d", pathPrefix, i)))
	}
	return out
}

func bindOpenBaoRefsInText(text string, bindings *input.Binder, pathPrefix string) string {
	if bindings == nil || !strings.Contains(text, "openbao://") {
		return text
	}
	index := 0
	return openbaoRefInText.ReplaceAllStringFunc(text, func(ref string) string {
		path := fmt.Sprintf("%s_ref_%d", pathPrefix, index)
		index++
		return bindings.Placeholder(path, ref)
	})
}

func renderPowerShellScript(commands []string) string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\r\n")
	b.WriteString("try {\r\n")
	for _, command := range commands {
		b.WriteString(command)
		b.WriteString("\r\n")
	}
	b.WriteString("} catch {\r\n")
	b.WriteString("    [Console]::Error.WriteLine($_.ToString())\r\n")
	b.WriteString("    exit 1\r\n")
	b.WriteString("}\r\n")
	b.WriteString("exit 0\r\n")
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
