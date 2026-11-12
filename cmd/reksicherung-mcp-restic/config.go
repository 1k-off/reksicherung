package main

import (
	"errors"
	"net/url"
	"strings"

	"github.com/1k-off/reksicherung/internal/reksicherung/secrets/openbao"
)

const (
	openBaoAddrEnv  = "REKSICHERUNG_OPENBAO_ADDR"
	openBaoTokenEnv = "REKSICHERUNG_OPENBAO_TOKEN"
)

type serverConfig struct {
	openBaoAddress string
	openBaoToken   string
}

func loadConfig(getenv func(string) string) (serverConfig, error) {
	config := serverConfig{
		openBaoAddress: getenv(openBaoAddrEnv),
		openBaoToken:   getenv(openBaoTokenEnv),
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
		parsed.ForceQuery || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return false
	}
	return parsed.Scheme == "https" || openbao.HTTPEndpointAllowed(parsed.Hostname())
}
