package main

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
)

const (
	passwordRefEnv  = "REKSICHERUNG_RESTIC_PASSWORD_REF"
	openBaoAddrEnv  = "REKSICHERUNG_OPENBAO_ADDR"
	openBaoTokenEnv = "REKSICHERUNG_OPENBAO_TOKEN"
)

type backendSecret struct {
	name      string
	reference string
}

type serverConfig struct {
	repository     string
	passwordRef    string
	backendSecrets []backendSecret
	environment    []string
	openBaoAddress string
	openBaoToken   string
}

var repositoryURLPattern = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)
var sftpPasswordPattern = regexp.MustCompile(`(?i)sftp:[^/@:]+:[^@/]+@`)
var secretQueryKeyPattern = regexp.MustCompile(`(?i)(password|passwd|token|secret|access[_-]?key|credential|signature|(^|[-_])sig($|[-_])|awsaccesskeyid)`)

func loadConfig(getenv func(string) string) (serverConfig, error) {
	config := serverConfig{
		repository:     getenv(repositoryEnv),
		passwordRef:    getenv(passwordRefEnv),
		openBaoAddress: getenv(openBaoAddrEnv),
		openBaoToken:   getenv(openBaoTokenEnv),
	}
	required := []struct {
		name  string
		value string
	}{
		{repositoryEnv, config.repository},
		{passwordRefEnv, config.passwordRef},
		{openBaoAddrEnv, config.openBaoAddress},
		{openBaoTokenEnv, config.openBaoToken},
	}
	missing := make([]string, 0, len(required))
	for _, item := range required {
		if strings.TrimSpace(item.value) == "" {
			missing = append(missing, item.name)
		}
	}
	if len(missing) != 0 {
		return serverConfig{}, errors.New("required environment variables are missing: " + strings.Join(missing, ", "))
	}
	if _, err := secrets.ParseReference(config.passwordRef); err != nil {
		return serverConfig{}, errors.New("invalid Restic repository-password secret reference")
	}
	if !validOpenBaoEndpoint(config.openBaoAddress) {
		return serverConfig{}, errors.New("invalid OpenBao endpoint")
	}
	if !validRepository(config.repository) {
		return serverConfig{}, errors.New("invalid Restic repository configuration")
	}

	for _, mapping := range []struct {
		name string
		env  string
	}{
		{name: "AWS_ACCESS_KEY_ID", env: "REKSICHERUNG_RESTIC_AWS_ACCESS_KEY_ID_REF"},
		{name: "AWS_SECRET_ACCESS_KEY", env: "REKSICHERUNG_RESTIC_AWS_SECRET_ACCESS_KEY_REF"},
		{name: "AWS_SESSION_TOKEN", env: "REKSICHERUNG_RESTIC_AWS_SESSION_TOKEN_REF"},
		{name: "B2_ACCOUNT_ID", env: "REKSICHERUNG_RESTIC_B2_ACCOUNT_ID_REF"},
		{name: "B2_ACCOUNT_KEY", env: "REKSICHERUNG_RESTIC_B2_ACCOUNT_KEY_REF"},
		{name: "AZURE_ACCOUNT_NAME", env: "REKSICHERUNG_RESTIC_AZURE_ACCOUNT_NAME_REF"},
		{name: "AZURE_ACCOUNT_KEY", env: "REKSICHERUNG_RESTIC_AZURE_ACCOUNT_KEY_REF"},
	} {
		rawReference := getenv(mapping.env)
		if rawReference == "" {
			continue
		}
		if _, err := secrets.ParseReference(rawReference); err != nil {
			return serverConfig{}, errors.New("invalid configured Restic backend secret reference")
		}
		config.backendSecrets = append(config.backendSecrets, backendSecret{
			name: mapping.name, reference: rawReference,
		})
	}
	for _, name := range []string{"AWS_DEFAULT_REGION", "AWS_REGION"} {
		if value := getenv(name); value != "" {
			config.environment = append(config.environment, name+"="+value)
		}
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
	return parsed.Scheme == "https" || isLoopbackHost(parsed.Hostname())
}

func validRepository(repository string) bool {
	if repository == "" || strings.TrimSpace(repository) != repository {
		return false
	}
	if sftpPasswordPattern.MatchString(repository) {
		return false
	}
	for _, match := range repositoryURLPattern.FindAllString(repository, -1) {
		embeddedURL, err := url.Parse(strings.TrimRight(match, "),;"))
		if err != nil {
			return false
		}
		if embeddedURL.User != nil {
			if _, hasPassword := embeddedURL.User.Password(); hasPassword {
				return false
			}
		}
		for key := range embeddedURL.Query() {
			if secretQueryKeyPattern.MatchString(key) {
				return false
			}
		}
	}
	return true
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
