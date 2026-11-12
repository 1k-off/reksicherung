package main

import (
	"errors"
	"net/url"
	"strings"

	sqlserver "github.com/1k-off/reksicherung/internal/reksicherung/restore/sqlserver"
	"github.com/1k-off/reksicherung/internal/reksicherung/restore/staging"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets/openbao"
)

const (
	stageDirEnv     = "REKSICHERUNG_ARTIFACT_STAGE_DIR"
	backupDirEnv    = "REKSICHERUNG_SQLSERVER_BACKUP_DIR"
	dataDirEnv      = "REKSICHERUNG_SQLSERVER_DATA_DIR"
	sqlCmdEnv       = "REKSICHERUNG_SQLCMD"
	sqlPackageEnv   = "REKSICHERUNG_SQLPACKAGE"
	openBaoAddrEnv  = "REKSICHERUNG_OPENBAO_ADDR"
	openBaoTokenEnv = "REKSICHERUNG_OPENBAO_TOKEN"
)

type restoreConfig struct {
	stageDirectory  string
	backupDirectory string
	dataDirectory   string
	sqlCmd          string
	sqlPackage      string
	openBaoAddress  string
	openBaoToken    string
}

func loadRestoreConfig(getenv func(string) string) (restoreConfig, error) {
	config := restoreConfig{
		stageDirectory:  getenv(stageDirEnv),
		backupDirectory: getenv(backupDirEnv),
		dataDirectory:   getenv(dataDirEnv),
		sqlCmd:          getenv(sqlCmdEnv),
		sqlPackage:      getenv(sqlPackageEnv),
		openBaoAddress:  getenv(openBaoAddrEnv),
		openBaoToken:    getenv(openBaoTokenEnv),
	}
	if strings.TrimSpace(config.stageDirectory) == "" {
		config.stageDirectory = staging.DefaultDirectory
	}
	if strings.TrimSpace(config.backupDirectory) == "" {
		config.backupDirectory = sqlserver.DefaultBackupDirectory
	}
	if strings.TrimSpace(config.dataDirectory) == "" {
		config.dataDirectory = sqlserver.DefaultDataDirectory
	}
	if strings.TrimSpace(config.openBaoAddress) == "" || strings.TrimSpace(config.openBaoToken) == "" {
		return restoreConfig{}, errors.New("required restore configuration is missing")
	}
	if !validOpenBaoEndpoint(config.openBaoAddress) {
		return restoreConfig{}, errors.New("invalid OpenBao endpoint")
	}
	if config.sqlCmd == "" {
		config.sqlCmd = "sqlcmd"
	}
	if config.sqlPackage == "" {
		config.sqlPackage = "sqlpackage"
	}
	if strings.TrimSpace(config.sqlCmd) != config.sqlCmd || strings.HasPrefix(config.sqlCmd, "-") ||
		strings.ContainsAny(config.sqlCmd, "\x00\r\n") {
		return restoreConfig{}, errors.New("invalid sqlcmd executable")
	}
	if strings.TrimSpace(config.sqlPackage) != config.sqlPackage || strings.HasPrefix(config.sqlPackage, "-") ||
		strings.ContainsAny(config.sqlPackage, "\x00\r\n") {
		return restoreConfig{}, errors.New("invalid sqlpackage executable")
	}
	return config, nil
}

func validOpenBaoEndpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	return openbao.HTTPEndpointAllowed(parsed.Hostname())
}
