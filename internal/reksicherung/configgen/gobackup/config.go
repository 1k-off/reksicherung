// Package gobackup decodes and generates secret-safe GoBackup configuration.
package gobackup

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"unicode"

	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
	"gopkg.in/yaml.v3"
)

type Mode string

const (
	ModeSimple   Mode = "simple"
	ModeEnhanced Mode = "enhanced"
)

// Input is the supported backup-as-code input schema. Credential values are
// represented only by secret references; plaintext credential fields are not
// part of this schema and Decode rejects them.
type Input struct {
	Project    Project    `yaml:"project"`
	Mode       Mode       `yaml:"mode,omitempty"`
	Storage    Storage    `yaml:"storage"`
	Schedules  Schedules  `yaml:"schedules"`
	Databases  []Database `yaml:"databases,omitempty"`
	Archive    *Archive   `yaml:"archive,omitempty"`
	Encryption Encryption `yaml:"encryption,omitempty"`
	Notifiers  Notifiers  `yaml:"notifiers,omitempty"`
	Scripts    Scripts    `yaml:"scripts,omitempty"`
	Web        Web        `yaml:"web,omitempty"`
}

type Project struct {
	Name    string `yaml:"name"`
	Service string `yaml:"service,omitempty"`
}

type Storage struct {
	Type               string `yaml:"type"`
	Bucket             string `yaml:"bucket,omitempty"`
	Region             string `yaml:"region,omitempty"`
	Endpoint           string `yaml:"endpoint,omitempty"`
	PathPrefix         string `yaml:"path_prefix,omitempty"`
	AccessKeyIDRef     string `yaml:"access_key_id_ref,omitempty"`
	SecretAccessKeyRef string `yaml:"secret_access_key_ref,omitempty"`
	Host               string `yaml:"host,omitempty"`
	Port               int    `yaml:"port,omitempty"`
	UsernameRef        string `yaml:"username_ref,omitempty"`
	PasswordRef        string `yaml:"password_ref,omitempty"`
}

type Schedule struct {
	Cron              string `yaml:"cron"`
	Keep              int    `yaml:"keep,omitempty"`
	HealthcheckURLRef string `yaml:"healthcheck_url_ref"`
	PathSuffix        string `yaml:"path_suffix,omitempty"`
	StorageName       string `yaml:"storage_name,omitempty"`
}

type Schedules struct {
	Cron              string    `yaml:"cron,omitempty"`
	Keep              int       `yaml:"keep,omitempty"`
	HealthcheckURLRef string    `yaml:"healthcheck_url_ref,omitempty"`
	Daily             *Schedule `yaml:"daily,omitempty"`
	Weekly            *Schedule `yaml:"weekly,omitempty"`
	Monthly           *Schedule `yaml:"monthly,omitempty"`
}

type Database struct {
	Name                   string `yaml:"name"`
	Type                   string `yaml:"type"`
	Host                   string `yaml:"host"`
	Port                   int    `yaml:"port,omitempty"`
	Database               string `yaml:"database"`
	UsernameRef            string `yaml:"username_ref"`
	PasswordRef            string `yaml:"password_ref"`
	TrustServerCertificate *bool  `yaml:"trust_server_certificate,omitempty"`
	Args                   string `yaml:"args,omitempty"`
	IgnoreSchemaCheck      bool   `yaml:"ignore_schema_check,omitempty"`
}

type Archive struct {
	Includes     []string `yaml:"includes"`
	Excludes     []string `yaml:"excludes,omitempty"`
	CompressType string   `yaml:"compress_type,omitempty"`
}

type Encryption struct {
	Enabled     bool   `yaml:"enabled,omitempty"`
	PasswordRef string `yaml:"password_ref,omitempty"`
	Salt        *bool  `yaml:"salt,omitempty"`
	Base64      *bool  `yaml:"base64,omitempty"`
}

type Notifiers struct {
	OnSuccess *bool `yaml:"on_success,omitempty"`
	OnFailure *bool `yaml:"on_failure,omitempty"`
}

type Scripts struct {
	Before string `yaml:"before,omitempty"`
	After  string `yaml:"after,omitempty"`
}

type Web struct {
	Host string `yaml:"host,omitempty"`
	Port int    `yaml:"port,omitempty"`
}

// Decode reads exactly one YAML document and rejects fields outside Input.
func Decode(r io.Reader) (Input, error) {
	if r == nil {
		return Input{}, errors.New("decode config: reader is nil")
	}
	decoder := yaml.NewDecoder(r)
	decoder.KnownFields(true)

	var input Input
	if err := decoder.Decode(&input); err != nil {
		if errors.Is(err, io.EOF) {
			return Input{}, errors.New("decode config: document is empty")
		}
		return Input{}, fmt.Errorf("decode config: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return Input{}, errors.New("decode config: multiple YAML documents are not supported")
	} else if !errors.Is(err, io.EOF) {
		return Input{}, fmt.Errorf("decode config: %w", err)
	}
	return input, nil
}

// Validate checks required fields, safe names and paths, and every supplied
// secret reference. Defaults are applied only while generating output.
func (in Input) Validate() error {
	if !validProjectName(in.Project.Name) {
		return errors.New("project.name is required and must be a safe name")
	}
	if in.Project.Service != "" && !validProjectName(in.Project.Service) {
		return errors.New("project.service must be a safe name")
	}

	mode := in.Mode
	if mode == "" {
		mode = ModeSimple
	}
	if mode != ModeSimple && mode != ModeEnhanced {
		return errors.New("mode must be simple or enhanced")
	}
	if err := validateStorage(in.Storage); err != nil {
		return err
	}
	if len(in.Databases) == 0 && in.Archive == nil {
		return errors.New("at least one of databases or archive must be configured")
	}

	switch mode {
	case ModeSimple:
		if strings.TrimSpace(in.Schedules.Cron) == "" {
			return errors.New("schedules.cron is required in simple mode")
		}
		if in.Schedules.Keep < 0 {
			return errors.New("schedules.keep cannot be negative")
		}
		if err := validateReference(in.Schedules.HealthcheckURLRef, "schedules.healthcheck_url_ref", true); err != nil {
			return err
		}
		if in.Schedules.Daily != nil || in.Schedules.Weekly != nil || in.Schedules.Monthly != nil {
			return errors.New("enhanced schedule tiers are only supported in enhanced mode")
		}
	case ModeEnhanced:
		if in.Schedules.Cron != "" || in.Schedules.Keep != 0 || in.Schedules.HealthcheckURLRef != "" {
			return errors.New("simple schedule fields are only supported in simple mode")
		}
		for _, tier := range []struct {
			name     string
			schedule *Schedule
		}{
			{name: "daily", schedule: in.Schedules.Daily},
			{name: "weekly", schedule: in.Schedules.Weekly},
			{name: "monthly", schedule: in.Schedules.Monthly},
		} {
			if tier.schedule == nil {
				return fmt.Errorf("schedules.%s is required in enhanced mode", tier.name)
			}
			if strings.TrimSpace(tier.schedule.Cron) == "" {
				return fmt.Errorf("schedules.%s.cron is required", tier.name)
			}
			if tier.schedule.Keep < 0 {
				return fmt.Errorf("schedules.%s.keep cannot be negative", tier.name)
			}
			if err := validateReference(tier.schedule.HealthcheckURLRef, "schedules."+tier.name+".healthcheck_url_ref", true); err != nil {
				return err
			}
			if tier.schedule.StorageName != "" && !validKeyName(tier.schedule.StorageName) {
				return fmt.Errorf("schedules.%s.storage_name must be a safe name", tier.name)
			}
			if tier.schedule.PathSuffix != "" && !validRelativePath(tier.schedule.PathSuffix) {
				return fmt.Errorf("schedules.%s.path_suffix must be a safe relative path", tier.name)
			}
		}
	}

	if err := validateDatabases(in.Databases); err != nil {
		return err
	}
	if in.Archive != nil {
		if len(in.Archive.Includes) == 0 {
			return errors.New("archive.includes must contain at least one absolute path")
		}
		paths := append(append([]string(nil), in.Archive.Includes...), in.Archive.Excludes...)
		for _, p := range paths {
			if !validArchivePath(p) {
				return errors.New("archive paths must be absolute and cannot contain traversal segments")
			}
		}
		if in.Archive.CompressType != "" && !validKeyName(in.Archive.CompressType) {
			return errors.New("archive.compress_type must be a safe type name")
		}
	}
	if in.Encryption.Enabled {
		if err := validateReference(in.Encryption.PasswordRef, "encryption.password_ref", true); err != nil {
			return err
		}
	} else if in.Encryption.PasswordRef != "" {
		return errors.New("encryption.password_ref requires encryption.enabled")
	}
	if in.Web.Host != "" && !validNetworkHost(in.Web.Host) {
		return errors.New("web.host must be a safe host or bind address")
	}
	if in.Web.Port < 0 || in.Web.Port > 65535 {
		return errors.New("web.port must be between 1 and 65535 when set")
	}
	return nil
}

func validateStorage(s Storage) error {
	switch strings.ToLower(s.Type) {
	case "s3", "minio":
		if !validBucket(s.Bucket) {
			return errors.New("storage.bucket is required and must be safe for s3/minio")
		}
		if s.Host != "" || s.Port != 0 || s.UsernameRef != "" || s.PasswordRef != "" {
			return errors.New("sftp fields are not valid for s3/minio storage")
		}
		if s.Endpoint != "" {
			u, err := url.Parse(s.Endpoint)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") ||
				u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
				return errors.New("storage.endpoint must be an http(s) URL without userinfo, query, or fragment")
			}
		}
	case "sftp":
		if !validNetworkHost(s.Host) {
			return errors.New("storage.host is required for sftp")
		}
		if s.UsernameRef == "" {
			return errors.New("storage.username_ref is required for sftp")
		}
		if s.Bucket != "" || s.Region != "" || s.Endpoint != "" || s.AccessKeyIDRef != "" || s.SecretAccessKeyRef != "" {
			return errors.New("s3 fields are not valid for sftp storage")
		}
	default:
		return errors.New("storage.type must be s3, minio, or sftp")
	}
	if s.Port < 0 || s.Port > 65535 {
		return errors.New("storage.port must be between 1 and 65535 when set")
	}
	if s.PathPrefix != "" && !validRelativePath(s.PathPrefix) {
		return errors.New("storage.path_prefix must be a safe relative path")
	}
	for _, ref := range []struct {
		value string
		field string
	}{
		{s.AccessKeyIDRef, "storage.access_key_id_ref"},
		{s.SecretAccessKeyRef, "storage.secret_access_key_ref"},
		{s.UsernameRef, "storage.username_ref"},
		{s.PasswordRef, "storage.password_ref"},
	} {
		if err := validateReference(ref.value, ref.field, false); err != nil {
			return err
		}
	}
	return nil
}

func validateDatabases(databases []Database) error {
	seen := make(map[string]struct{}, len(databases))
	outputNames := make(map[string]struct{}, len(databases))
	for i, db := range databases {
		if !validKeyName(db.Name) {
			return fmt.Errorf("databases[%d].name must be a safe name", i)
		}
		if _, ok := seen[db.Name]; ok {
			return fmt.Errorf("databases[%d].name is duplicated", i)
		}
		seen[db.Name] = struct{}{}
		generatedName := stableName(db.Name)
		if _, ok := outputNames[generatedName]; ok {
			return fmt.Errorf("databases[%d].name collides with another generated name", i)
		}
		outputNames[generatedName] = struct{}{}
		switch strings.ToLower(db.Type) {
		case "mssql", "postgresql", "mysql":
		default:
			return fmt.Errorf("databases[%d].type must be mssql, postgresql, or mysql", i)
		}
		if !validNetworkHost(db.Host) {
			return fmt.Errorf("databases[%d].host is required", i)
		}
		if strings.TrimSpace(db.Database) == "" || !safeText(db.Database) {
			return fmt.Errorf("databases[%d].database is required", i)
		}
		if db.Port < 0 || db.Port > 65535 {
			return fmt.Errorf("databases[%d].port must be between 1 and 65535 when set", i)
		}
		for _, ref := range []struct {
			value string
			field string
		}{
			{db.UsernameRef, fmt.Sprintf("databases[%d].username_ref", i)},
			{db.PasswordRef, fmt.Sprintf("databases[%d].password_ref", i)},
		} {
			if err := validateReference(ref.value, ref.field, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateReference(value, field string, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s is required", field)
		}
		return nil
	}
	if _, err := secrets.ParseReference(value); err != nil {
		return fmt.Errorf("%s must be a valid secret reference", field)
	}
	return nil
}

func validProjectName(value string) bool {
	if len(value) == 0 || len(value) > 64 || !safeText(value) {
		return false
	}
	hasLetterOrDigit := false
	for _, r := range value {
		if isASCIIAlphaNumeric(r) {
			hasLetterOrDigit = true
			continue
		}
		if !strings.ContainsRune(" -_.", r) {
			return false
		}
	}
	return hasLetterOrDigit
}

func validKeyName(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	hasLetterOrDigit := false
	for _, r := range value {
		if isASCIIAlphaNumeric(r) {
			hasLetterOrDigit = true
			continue
		}
		if !strings.ContainsRune("-_.", r) {
			return false
		}
	}
	return hasLetterOrDigit
}

func isASCIIAlphaNumeric(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

func validBucket(value string) bool {
	if len(value) < 3 || len(value) > 63 || strings.Contains(value, "..") {
		return false
	}
	if !isASCIILowerAlphaNumeric(rune(value[0])) || !isASCIILowerAlphaNumeric(rune(value[len(value)-1])) {
		return false
	}
	for i, r := range value {
		if isASCIILowerAlphaNumeric(r) || r == '.' || r == '-' {
			if i > 0 && ((r == '.' && value[i-1] == '-') || (r == '-' && value[i-1] == '.')) {
				return false
			}
			continue
		}
		return false
	}
	return true
}

func validRelativePath(value string) bool {
	if !safeText(value) || strings.Contains(value, `\`) {
		return false
	}
	trimmed := strings.Trim(value, "/")
	if trimmed == "" {
		return true
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func validNetworkHost(value string) bool {
	if value == "" || len(value) > 253 || strings.Contains(value, "..") {
		return false
	}
	for _, r := range value {
		if isASCIIAlphaNumeric(r) || strings.ContainsRune(".-_:%[]", r) {
			continue
		}
		return false
	}
	return true
}

func isASCIILowerAlphaNumeric(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
}

func validArchivePath(value string) bool {
	if !path.IsAbs(value) || !safeText(value) || strings.Contains(value, `\`) {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return false
		}
	}
	return true
}

func safeText(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func stableName(value string) string {
	var b strings.Builder
	underscore := false
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			underscore = false
			continue
		}
		if !underscore && b.Len() > 0 {
			b.WriteByte('_')
			underscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}
