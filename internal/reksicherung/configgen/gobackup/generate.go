package gobackup

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Binding maps a runtime environment variable to a provider-neutral secret
// reference. It intentionally contains no resolved secret value.
type Binding struct {
	EnvironmentVariable string `json:"environment_variable" yaml:"environment_variable"`
	SecretReference     string `json:"secret_reference" yaml:"secret_reference"`
}

// Result contains the generated GoBackup YAML and its runtime secret bindings.
type Result struct {
	YAML     []byte
	Bindings []Binding
}

type binder struct {
	bindings []Binding
	seen     map[string]struct{}
}

// Generate validates input and renders deterministic GoBackup YAML. Secret
// references become runtime $REKSICHERUNG_* placeholders; no secret values
// are resolved or returned.
func Generate(input Input) (Result, error) {
	if err := input.Validate(); err != nil {
		return Result{}, err
	}
	bindings := &binder{seen: make(map[string]struct{})}
	doc := buildDocument(input, bindings)
	output, err := yaml.Marshal(doc)
	if err != nil {
		return Result{}, fmt.Errorf("marshal GoBackup config: %w", err)
	}
	sort.Slice(bindings.bindings, func(i, j int) bool {
		return bindings.bindings[i].EnvironmentVariable < bindings.bindings[j].EnvironmentVariable
	})
	return Result{YAML: output, Bindings: bindings.bindings}, nil
}

func (b *binder) placeholder(path, reference string) string {
	if reference == "" {
		return ""
	}
	envName := "REKSICHERUNG_" + strings.ToUpper(stableName(path))
	if _, exists := b.seen[envName]; !exists {
		b.seen[envName] = struct{}{}
		b.bindings = append(b.bindings, Binding{
			EnvironmentVariable: envName,
			SecretReference:     reference,
		})
	}
	return "$" + envName
}

func buildDocument(input Input, bindings *binder) map[string]any {
	mode := input.Mode
	if mode == "" {
		mode = ModeSimple
	}
	web := map[string]any{
		"host": defaultString(input.Web.Host, "127.0.0.1"),
		"port": defaultPort(input.Web.Port, 2703),
	}
	models := make(map[string]any)
	projectName := stableName(input.Project.Name)
	switch mode {
	case ModeSimple:
		models[projectName+"_default"] = buildModel(
			input,
			bindings,
			"default",
			input.Schedules.Cron,
			defaultKeep(input.Schedules.Keep, 14),
			input.Schedules.HealthcheckURLRef,
			"",
			"primary",
		)
	case ModeEnhanced:
		for _, tier := range []struct {
			name     string
			schedule *Schedule
			keep     int
		}{
			{name: "daily", schedule: input.Schedules.Daily, keep: 7},
			{name: "weekly", schedule: input.Schedules.Weekly, keep: 4},
			{name: "monthly", schedule: input.Schedules.Monthly, keep: 12},
		} {
			pathSuffix := defaultString(tier.schedule.PathSuffix, tier.name)
			storageName := defaultString(tier.schedule.StorageName, "s3_"+tier.name)
			models[projectName+"_"+tier.name] = buildModel(
				input,
				bindings,
				tier.name,
				tier.schedule.Cron,
				defaultKeep(tier.schedule.Keep, tier.keep),
				tier.schedule.HealthcheckURLRef,
				pathSuffix,
				storageName,
			)
		}
	}
	return map[string]any{"web": web, "models": models}
}

func buildModel(
	input Input,
	bindings *binder,
	tier string,
	cron string,
	keep int,
	healthcheckURLRef string,
	pathSuffix string,
	storageName string,
) map[string]any {
	description := input.Project.Name + " " + tier + " backup"
	if tier == "default" {
		description = input.Project.Name + " backup"
	}
	if input.Project.Service != "" && input.Project.Service != input.Project.Name {
		description = input.Project.Name + " " + input.Project.Service + " " + tier + " backup"
		if tier == "default" {
			description = input.Project.Name + " " + input.Project.Service + " backup"
		}
	}
	healthcheckBinding := "schedules_" + tier + "_healthcheck_url"
	if tier == "default" {
		healthcheckBinding = "schedules_healthcheck_url"
	}

	model := map[string]any{
		"description":     description,
		"schedule":        map[string]any{"cron": cron},
		"default_storage": storageName,
		"storages": map[string]any{
			storageName: buildStorage(input.Storage, bindings, pathSuffix, keep),
		},
		"notifiers": map[string]any{
			tier: buildNotifier(
				bindings.placeholder(healthcheckBinding, healthcheckURLRef),
				input.Notifiers,
			),
		},
	}
	if strings.TrimSpace(input.Scripts.Before) != "" {
		model["before_script"] = strings.TrimSpace(input.Scripts.Before)
	}
	if strings.TrimSpace(input.Scripts.After) != "" {
		model["after_script"] = strings.TrimSpace(input.Scripts.After)
	}
	if input.Archive != nil {
		archive := map[string]any{"includes": input.Archive.Includes}
		if len(input.Archive.Excludes) > 0 {
			archive["excludes"] = input.Archive.Excludes
		}
		model["archive"] = archive
		model["compress_with"] = map[string]any{
			"type": defaultString(input.Archive.CompressType, "tgz"),
		}
	}
	if len(input.Databases) > 0 {
		model["databases"] = buildDatabases(input.Databases, bindings)
	}
	if input.Encryption.Enabled {
		model["encrypt_with"] = map[string]any{
			"type":     "openssl",
			"password": bindings.placeholder("encryption_password", input.Encryption.PasswordRef),
			"salt":     defaultBool(input.Encryption.Salt, true),
			"base64":   defaultBool(input.Encryption.Base64, false),
		}
	}
	return model
}

func buildStorage(storage Storage, bindings *binder, pathSuffix string, keep int) map[string]any {
	result := map[string]any{
		"type": strings.ToLower(storage.Type),
		"keep": keep,
	}
	if fullPath := storagePath(storage.PathPrefix, pathSuffix); fullPath != "" {
		result["path"] = fullPath
	}
	switch strings.ToLower(storage.Type) {
	case "s3", "minio":
		result["bucket"] = storage.Bucket
		if storage.Region != "" {
			result["region"] = storage.Region
		}
		if storage.Endpoint != "" {
			result["endpoint"] = storage.Endpoint
		}
		if storage.AccessKeyIDRef != "" {
			result["access_key_id"] = bindings.placeholder("storage_access_key_id", storage.AccessKeyIDRef)
		}
		if storage.SecretAccessKeyRef != "" {
			result["secret_access_key"] = bindings.placeholder("storage_secret_access_key", storage.SecretAccessKeyRef)
		}
	case "sftp":
		result["host"] = storage.Host
		if storage.Port > 0 {
			result["port"] = storage.Port
		}
		result["username"] = bindings.placeholder("storage_username", storage.UsernameRef)
		if storage.PasswordRef != "" {
			result["password"] = bindings.placeholder("storage_password", storage.PasswordRef)
		}
	}
	return result
}

func buildDatabases(databases []Database, bindings *binder) map[string]any {
	result := make(map[string]any, len(databases))
	for _, database := range databases {
		dbType := strings.ToLower(database.Type)
		entry := map[string]any{
			"type":     dbType,
			"host":     database.Host,
			"database": database.Database,
			"username": bindings.placeholder("databases_"+database.Name+"_username", database.UsernameRef),
			"password": bindings.placeholder("databases_"+database.Name+"_password", database.PasswordRef),
		}
		if database.Port > 0 {
			entry["port"] = database.Port
		}
		if database.TrustServerCertificate != nil {
			entry["trustServerCertificate"] = *database.TrustServerCertificate
		} else if dbType == "mssql" {
			entry["trustServerCertificate"] = true
		}
		args := database.Args
		if args == "" && database.IgnoreSchemaCheck {
			args = "/p:VerifyExtraction=false"
		}
		if args != "" {
			entry["args"] = args
		}
		result[database.Name] = entry
	}
	return result
}

func buildNotifier(url string, config Notifiers) map[string]any {
	return map[string]any{
		"type":       "healthchecks",
		"url":        url,
		"on_success": defaultBool(config.OnSuccess, true),
		"on_failure": defaultBool(config.OnFailure, true),
	}
}

func storagePath(prefix, suffix string) string {
	prefix = strings.Trim(prefix, "/")
	suffix = strings.Trim(suffix, "/")
	switch {
	case prefix == "":
		return suffix
	case suffix == "":
		return prefix
	default:
		return prefix + "/" + suffix
	}
}

func defaultKeep(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

func defaultString(value, fallback string) string {
	if value == "" {
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

func defaultPort(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}
