package main

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
)

const (
	sqlAddressEnv   = "REKSICHERUNG_SQLSERVER_ADDRESS"
	sqlUsernameEnv  = "REKSICHERUNG_SQLSERVER_RESTORE_USERNAME"
	sqlPasswordRef  = "REKSICHERUNG_SQLSERVER_RESTORE_PASSWORD_REF"
	stageDirEnv     = "REKSICHERUNG_ARTIFACT_STAGE_DIR"
	backupDirEnv    = "REKSICHERUNG_SQLSERVER_BACKUP_DIR"
	dataDirEnv      = "REKSICHERUNG_SQLSERVER_DATA_DIR"
	sqlCmdEnv       = "REKSICHERUNG_SQLCMD"
	sqlPackageEnv   = "REKSICHERUNG_SQLPACKAGE"
	openBaoAddrEnv  = "REKSICHERUNG_OPENBAO_ADDR"
	openBaoTokenEnv = "REKSICHERUNG_OPENBAO_TOKEN"
)

type restoreConfig struct {
	address         string
	username        string
	passwordRef     string
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
		address:         getenv(sqlAddressEnv),
		username:        getenv(sqlUsernameEnv),
		passwordRef:     getenv(sqlPasswordRef),
		stageDirectory:  getenv(stageDirEnv),
		backupDirectory: getenv(backupDirEnv),
		dataDirectory:   getenv(dataDirEnv),
		sqlCmd:          getenv(sqlCmdEnv),
		sqlPackage:      getenv(sqlPackageEnv),
		openBaoAddress:  getenv(openBaoAddrEnv),
		openBaoToken:    getenv(openBaoTokenEnv),
	}
	for _, item := range []struct{ name, value string }{
		{sqlAddressEnv, config.address},
		{sqlUsernameEnv, config.username},
		{sqlPasswordRef, config.passwordRef},
		{stageDirEnv, config.stageDirectory},
		{backupDirEnv, config.backupDirectory},
		{dataDirEnv, config.dataDirectory},
		{openBaoAddrEnv, config.openBaoAddress},
		{openBaoTokenEnv, config.openBaoToken},
	} {
		if strings.TrimSpace(item.value) == "" {
			return restoreConfig{}, errors.New("required restore configuration is missing")
		}
	}
	if !validSQLCmdAddress(config.address) || strings.TrimSpace(config.username) != config.username ||
		strings.ContainsAny(config.username, "\x00\r\n") || len(config.username) > 128 {
		return restoreConfig{}, errors.New("invalid SQL Server restore target")
	}
	if _, err := secrets.ParseReference(config.passwordRef); err != nil {
		return restoreConfig{}, errors.New("invalid SQL Server restore-password reference")
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

func validSQLCmdAddress(address string) bool {
	if !strings.HasPrefix(address, "tcp:") || strings.TrimSpace(address) != address {
		return false
	}
	host, portText, ok := strings.Cut(strings.TrimPrefix(address, "tcp:"), ",")
	if !ok || host == "" || strings.HasPrefix(host, "-") || strings.ContainsAny(host, "/\\ \t\r\n") {
		return false
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return false
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return net.ParseIP(strings.Trim(host, "[]")) != nil
	}
	return !strings.Contains(host, ":")
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
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
