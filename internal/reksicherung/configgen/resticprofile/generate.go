// Package resticprofile generates secret-safe resticprofile v1 configuration
// from the shared configgen input contract.
package resticprofile

import (
	"fmt"
	"strings"

	"github.com/1k-off/reksicherung/internal/reksicherung/configgen/input"
	"gopkg.in/yaml.v3"
)

// Result contains the generated resticprofile YAML, runtime secret bindings,
// and optional hook sidecar scripts (written beside the YAML by the CLI).
type Result struct {
	YAML     []byte
	Bindings []input.Binding
	Sidecars []Sidecar
}

// Generate validates the shared input and the resticprofile subset, then
// renders deterministic resticprofile v1 YAML. Secret references become
// $REKSICHERUNG_* placeholders; no secret values are resolved or returned.
//
// Windows hooks match legacy util/configgen restic: run_before / run_after
// become pure PowerShell <project>-backup.ps1 / <project>-cleanup.ps1 sidecars
// referenced via powershell -File C:\restic\config\….
func Generate(in input.Input) (Result, error) {
	if err := in.Validate(); err != nil {
		return Result{}, err
	}
	if err := ValidateForProvider(in); err != nil {
		return Result{}, err
	}
	bindings := input.NewBinder()
	sidecars, beforeInv, afterInv := wrapHooks(in, bindings)
	for _, sc := range sidecars {
		if err := validSidecarName(sc.Name); err != nil {
			return Result{}, err
		}
	}
	doc, err := buildDocument(in, bindings, beforeInv, afterInv)
	if err != nil {
		return Result{}, err
	}
	output, err := yaml.Marshal(doc)
	if err != nil {
		return Result{}, fmt.Errorf("marshal resticprofile config: %w", err)
	}
	return Result{YAML: output, Bindings: bindings.Bindings(), Sidecars: sidecars}, nil
}

func buildDocument(in input.Input, bindings *input.Binder, beforeInv, afterInv []string) (map[string]any, error) {
	env := map[string]any{}
	switch strings.ToLower(in.Storage.Type) {
	case "azure":
		env["AZURE_ACCOUNT_NAME"] = in.Storage.Account
		if in.Storage.AccountKeyRef != "" {
			env["AZURE_ACCOUNT_KEY"] = bindings.Placeholder("storage_account_key", in.Storage.AccountKeyRef)
		}
		if in.Storage.AccountSASRef != "" {
			env["AZURE_ACCOUNT_SAS"] = bindings.Placeholder("storage_account_sas", in.Storage.AccountSASRef)
		}
		if in.Storage.EndpointSuffix != "" {
			env["AZURE_ENDPOINT_SUFFIX"] = in.Storage.EndpointSuffix
		}
	default:
		if in.Storage.AccessKeyIDRef != "" {
			env["AWS_ACCESS_KEY_ID"] = bindings.Placeholder("storage_access_key_id", in.Storage.AccessKeyIDRef)
		}
		if in.Storage.SecretAccessKeyRef != "" {
			env["AWS_SECRET_ACCESS_KEY"] = bindings.Placeholder("storage_secret_access_key", in.Storage.SecretAccessKeyRef)
		}
	}
	env["RESTIC_PASSWORD"] = bindings.Placeholder("encryption_password", in.Encryption.PasswordRef)

	doc := map[string]any{
		"version": "1",
		"default": map[string]any{"env": env},
	}

	mode := in.Mode
	if mode == "" {
		mode = input.ModeSimple
	}
	projectName := input.StableName(in.Project.Name)

	switch mode {
	case input.ModeSimple:
		profile, err := buildProfile(in, bindings, projectName, "", in.Schedules.Cron, defaultKeep(in.Schedules.Keep, 14), in.Schedules.HealthcheckURLRef, "schedules_healthcheck_url", beforeInv, afterInv)
		if err != nil {
			return nil, err
		}
		doc[projectName] = profile
	case input.ModeEnhanced:
		for _, tier := range []struct {
			name     string
			schedule *input.Schedule
			keep     int
		}{
			{name: "daily", schedule: in.Schedules.Daily, keep: 7},
			{name: "weekly", schedule: in.Schedules.Weekly, keep: 4},
			{name: "monthly", schedule: in.Schedules.Monthly, keep: 12},
		} {
			pathSuffix := tier.schedule.PathSuffix
			if pathSuffix == "" {
				pathSuffix = tier.name
			}
			profileName := projectName + "-" + tier.name
			profile, err := buildProfile(
				in,
				bindings,
				profileName,
				pathSuffix,
				tier.schedule.Cron,
				defaultKeep(tier.schedule.Keep, tier.keep),
				tier.schedule.HealthcheckURLRef,
				"schedules_"+tier.name+"_healthcheck_url",
				beforeInv,
				afterInv,
			)
			if err != nil {
				return nil, err
			}
			doc[profileName] = profile
		}
	}
	return doc, nil
}

func buildProfile(
	in input.Input,
	bindings *input.Binder,
	profileName string,
	pathSuffix string,
	cron string,
	keep int,
	healthcheckURLRef string,
	healthcheckBindingPath string,
	beforeInv []string,
	afterInv []string,
) (map[string]any, error) {
	repo, err := repositoryURL(in.Storage, pathSuffix)
	if err != nil {
		return nil, err
	}
	healthURL := bindings.Placeholder(healthcheckBindingPath, healthcheckURLRef)
	excludes := in.Archive.Excludes
	if excludes == nil {
		excludes = []string{}
	} else {
		excludes = append([]string(nil), excludes...)
	}
	backup := map[string]any{
		"source":               append([]string(nil), in.Archive.Includes...),
		"exclude":              excludes,
		"schedule-permission":  "system",
		"schedule-log":         fmt.Sprintf("logs/%s-restic-backup.log", profileName),
		"schedule":             cron,
		"check-after":          true,
		"check-before":         false,
	}
	if len(beforeInv) > 0 {
		backup["run-before"] = append([]string(nil), beforeInv...)
	}
	if len(afterInv) > 0 {
		backup["run-after"] = append([]string(nil), afterInv...)
	}
	onSuccess := defaultBool(in.Notifiers.OnSuccess, true)
	onFailure := defaultBool(in.Notifiers.OnFailure, true)
	if onSuccess {
		backup["send-before"] = []map[string]any{{
			"method": "HEAD",
			"url":    healthURL + "/start",
		}}
		backup["send-after"] = []map[string]any{{
			"method": "HEAD",
			"url":    healthURL,
		}}
	}
	if onFailure {
		backup["send-after-fail"] = map[string]any{
			"method": "POST",
			"url":    healthURL + "/fail",
			"body":   "${ERROR}\\n\\n${ERROR_STDERR}",
		}
	}

	return map[string]any{
		"inherit":    "default",
		"initialize": true,
		"repository": repo,
		"backup":     backup,
		"forget": map[string]any{
			"keep-last":            keep,
			"prune":                true,
			"schedule-permission":  "system",
			"schedule-log":         fmt.Sprintf("logs/%s-restic-forget.log", profileName),
			"schedule":             cron,
		},
	}, nil
}

func defaultKeep(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

func defaultBool(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}
