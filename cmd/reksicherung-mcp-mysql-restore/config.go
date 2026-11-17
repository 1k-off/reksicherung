package main

import (
	"errors"
	"net/url"
	"strings"

	"github.com/1k-off/reksicherung/internal/reksicherung/restore/staging"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets/openbao"
)

const (
	stageDirEnv     = "REKSICHERUNG_ARTIFACT_STAGE_DIR"
	mysqlCmdEnv     = "REKSICHERUNG_MYSQL"
	mysqlCheckEnv   = "REKSICHERUNG_MYSQLCHECK"
	tempDirEnv      = "REKSICHERUNG_MYSQL_TMPDIR"
	openBaoAddrEnv  = "REKSICHERUNG_OPENBAO_ADDR"
	openBaoTokenEnv = "REKSICHERUNG_OPENBAO_TOKEN"
)

type restoreConfig struct {
	stageDirectory string
	tempDirectory  string
	mysql          string
	mysqlCheck     string
	openBaoAddress string
	openBaoToken   string
}

func loadRestoreConfig(getenv func(string) string) (restoreConfig, error) {
	config := restoreConfig{
		stageDirectory: getenv(stageDirEnv),
		tempDirectory:  getenv(tempDirEnv),
		mysql:          getenv(mysqlCmdEnv),
		mysqlCheck:     getenv(mysqlCheckEnv),
		openBaoAddress: getenv(openBaoAddrEnv),
		openBaoToken:   getenv(openBaoTokenEnv),
	}
	if strings.TrimSpace(config.stageDirectory) == "" {
		config.stageDirectory = staging.DefaultDirectory
	}
	if strings.TrimSpace(config.openBaoAddress) == "" || strings.TrimSpace(config.openBaoToken) == "" {
		return restoreConfig{}, errors.New("required restore configuration is missing")
	}
	if !validOpenBaoEndpoint(config.openBaoAddress) {
		return restoreConfig{}, errors.New("invalid OpenBao endpoint")
	}
	if config.mysql == "" {
		config.mysql = "mysql"
	}
	if config.mysqlCheck == "" {
		config.mysqlCheck = "mysqlcheck"
	}
	if !validExecutable(config.mysql) {
		return restoreConfig{}, errors.New("invalid mysql executable")
	}
	if !validExecutable(config.mysqlCheck) {
		return restoreConfig{}, errors.New("invalid mysqlcheck executable")
	}
	if strings.ContainsAny(config.tempDirectory, "\x00\r\n") {
		return restoreConfig{}, errors.New("invalid temporary directory")
	}
	return config, nil
}

func validExecutable(value string) bool {
	return strings.TrimSpace(value) == value && !strings.HasPrefix(value, "-") &&
		!strings.ContainsAny(value, "\x00\r\n")
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
