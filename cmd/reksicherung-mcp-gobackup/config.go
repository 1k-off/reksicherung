package main

import (
	"errors"
	"net/url"
	"strings"

	"github.com/1k-off/reksicherung/internal/reksicherung/secrets/openbao"
)

const (
	openBaoAddr  = "REKSICHERUNG_OPENBAO_ADDR"
	openBaoToken = "REKSICHERUNG_OPENBAO_TOKEN"
)

type serverConfig struct {
	openBaoAddress string
	openBaoToken   string
}

func loadConfig(getenv func(string) string) (serverConfig, error) {
	config := serverConfig{
		openBaoAddress: getenv(openBaoAddr),
		openBaoToken:   getenv(openBaoToken),
	}
	if strings.TrimSpace(config.openBaoAddress) == "" || strings.TrimSpace(config.openBaoToken) == "" {
		return serverConfig{}, errors.New("required environment variables are missing: REKSICHERUNG_OPENBAO_ADDR, REKSICHERUNG_OPENBAO_TOKEN")
	}
	if !validOpenBaoEndpoint(config.openBaoAddress) {
		return serverConfig{}, errors.New("invalid OpenBao endpoint")
	}
	return config, nil
}

func validOpenBaoEndpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" ||
		parsed.Fragment != "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	return openbao.HTTPEndpointAllowed(parsed.Hostname())
}
