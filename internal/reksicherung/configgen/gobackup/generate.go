// Package gobackup generates secret-safe GoBackup configuration from the
// shared configgen input contract.
package gobackup

import (
	"fmt"
	"strings"

	"github.com/1k-off/reksicherung/internal/reksicherung/configgen/input"
	"gopkg.in/yaml.v3"
)

// Result contains the generated GoBackup YAML and its runtime secret bindings.
type Result struct {
	YAML     []byte
	Bindings []input.Binding
}

// Generate validates input and renders deterministic GoBackup YAML. Secret
// references become runtime $REKSICHERUNG_* placeholders; no secret values
// are resolved or returned.
func Generate(in input.Input) (Result, error) {
	if err := in.Validate(); err != nil {
		return Result{}, err
	}
	if err := ValidateForProvider(in); err != nil {
		return Result{}, err
	}
	bindings := input.NewBinder()
	doc := buildDocument(in, bindings)
	output, err := yaml.Marshal(doc)
	if err != nil {
		return Result{}, fmt.Errorf("marshal GoBackup config: %w", err)
	}
	return Result{YAML: output, Bindings: bindings.Bindings()}, nil
}

func buildDocument(in input.Input, bindings *input.Binder) map[string]any {
	mode := in.Mode
	if mode == "" {
		mode = input.ModeSimple
	}
	web := map[string]any{
		"host": defaultString(in.Web.Host, "127.0.0.1"),
		"port": defaultPort(in.Web.Port, 2703),
	}
	models := make(map[string]any)
	projectName := input.StableName(in.Project.Name)
	switch mode {
	case input.ModeSimple:
		models[projectName+"_default"] = buildModel(
			in,
			bindings,
			"default",
			in.Schedules.Cron,
			defaultKeep(in.Schedules.Keep, 14),
			in.Schedules.HealthcheckURLRef,
			"",
			"primary",
		)
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
			pathSuffix := defaultString(tier.schedule.PathSuffix, tier.name)
			storageName := defaultString(tier.schedule.StorageName, "s3_"+tier.name)
			models[projectName+"_"+tier.name] = buildModel(
				in,
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
	in input.Input,
	bindings *input.Binder,
	tier string,
	cron string,
	keep int,
	healthcheckURLRef string,
	pathSuffix string,
	storageName string,
) map[string]any {
	description := in.Project.Name + " " + tier + " backup"
	if tier == "default" {
		description = in.Project.Name + " backup"
	}
	if in.Project.Service != "" && in.Project.Service != in.Project.Name {
		description = in.Project.Name + " " + in.Project.Service + " " + tier + " backup"
		if tier == "default" {
			description = in.Project.Name + " " + in.Project.Service + " backup"
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
			storageName: buildStorage(in.Storage, bindings, pathSuffix, keep),
		},
		"notifiers": map[string]any{
			tier: buildNotifier(
				bindings.Placeholder(healthcheckBinding, healthcheckURLRef),
				in.Notifiers,
			),
		},
	}
	if in.Archive != nil {
		archive := map[string]any{"includes": in.Archive.Includes}
		if len(in.Archive.Excludes) > 0 {
			archive["excludes"] = in.Archive.Excludes
		}
		model["archive"] = archive
		model["compress_with"] = map[string]any{
			"type": defaultString(in.Archive.CompressType, "tgz"),
		}
	}
	if len(in.Databases) > 0 {
		model["databases"] = buildDatabases(in.Databases, bindings)
	}
	if in.Encryption.Enabled {
		model["encrypt_with"] = map[string]any{
			"type":     "openssl",
			"password": bindings.Placeholder("encryption_password", in.Encryption.PasswordRef),
			"salt":     defaultBool(in.Encryption.Salt, true),
			"base64":   defaultBool(in.Encryption.Base64, false),
		}
	}
	return model
}

func buildStorage(storage input.Storage, bindings *input.Binder, pathSuffix string, keep int) map[string]any {
	result := map[string]any{
		"type": strings.ToLower(storage.Type),
		"keep": keep,
	}
	if fullPath := input.JoinStoragePath(storage.PathPrefix, pathSuffix); fullPath != "" {
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
			result["access_key_id"] = bindings.Placeholder("storage_access_key_id", storage.AccessKeyIDRef)
		}
		if storage.SecretAccessKeyRef != "" {
			result["secret_access_key"] = bindings.Placeholder("storage_secret_access_key", storage.SecretAccessKeyRef)
		}
	case "azure":
		result["account"] = storage.Account
		result["container"] = storage.Container
		result["tenant_id"] = bindings.Placeholder("storage_tenant_id", storage.TenantIDRef)
		result["client_id"] = bindings.Placeholder("storage_client_id", storage.ClientIDRef)
		result["client_secret"] = bindings.Placeholder("storage_client_secret", storage.ClientSecretRef)
	case "sftp":
		result["host"] = storage.Host
		if storage.Port > 0 {
			result["port"] = storage.Port
		}
		result["username"] = bindings.Placeholder("storage_username", storage.UsernameRef)
		if storage.PasswordRef != "" {
			result["password"] = bindings.Placeholder("storage_password", storage.PasswordRef)
		}
	}
	return result
}

func buildDatabases(databases []input.Database, bindings *input.Binder) map[string]any {
	result := make(map[string]any, len(databases))
	for _, database := range databases {
		dbType := strings.ToLower(database.Type)
		entry := map[string]any{
			"type":     dbType,
			"host":     database.Host,
			"database": database.Database,
			"username": bindings.Placeholder("databases_"+database.Name+"_username", database.UsernameRef),
			"password": bindings.Placeholder("databases_"+database.Name+"_password", database.PasswordRef),
		}
		if database.Port > 0 {
			entry["port"] = database.Port
		}
		if database.TrustServerCertificate != nil {
			entry["trustServerCertificate"] = *database.TrustServerCertificate
		} else if dbType == "mssql" {
			entry["trustServerCertificate"] = false
		}
		if database.IgnoreSchemaCheck {
			entry["args"] = "/p:VerifyExtraction=false"
		}
		result[database.Name] = entry
	}
	return result
}

func buildNotifier(url string, config input.Notifiers) map[string]any {
	return map[string]any{
		"type":       "healthchecks",
		"url":        url,
		"on_success": defaultBool(config.OnSuccess, true),
		"on_failure": defaultBool(config.OnFailure, true),
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
