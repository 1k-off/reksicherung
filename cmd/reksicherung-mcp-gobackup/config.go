package main

import (
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
)

const (
	s3EndpointEnv = "REKSICHERUNG_GOBACKUP_S3_ENDPOINT"
	s3RegionEnv   = "REKSICHERUNG_GOBACKUP_S3_REGION"
	s3BucketEnv   = "REKSICHERUNG_GOBACKUP_S3_BUCKET"
	s3PrefixEnv   = "REKSICHERUNG_GOBACKUP_S3_PREFIX"
	accessRefEnv  = "REKSICHERUNG_GOBACKUP_S3_ACCESS_KEY_REF"
	secretRefEnv  = "REKSICHERUNG_GOBACKUP_S3_SECRET_KEY_REF"
	openBaoAddr   = "REKSICHERUNG_OPENBAO_ADDR"
	openBaoToken  = "REKSICHERUNG_OPENBAO_TOKEN"
)

type serverConfig struct {
	endpoint       string
	region         string
	bucket         string
	prefix         string
	accessKeyRef   string
	secretKeyRef   string
	openBaoAddress string
	openBaoToken   string
}

func loadConfig(getenv func(string) string) (serverConfig, error) {
	config := serverConfig{
		endpoint:       getenv(s3EndpointEnv),
		region:         getenv(s3RegionEnv),
		bucket:         getenv(s3BucketEnv),
		prefix:         getenv(s3PrefixEnv),
		accessKeyRef:   getenv(accessRefEnv),
		secretKeyRef:   getenv(secretRefEnv),
		openBaoAddress: getenv(openBaoAddr),
		openBaoToken:   getenv(openBaoToken),
	}
	required := []struct {
		name  string
		value string
	}{
		{s3EndpointEnv, config.endpoint},
		{s3RegionEnv, config.region},
		{s3BucketEnv, config.bucket},
		{s3PrefixEnv, config.prefix},
		{accessRefEnv, config.accessKeyRef},
		{secretRefEnv, config.secretKeyRef},
		{openBaoAddr, config.openBaoAddress},
		{openBaoToken, config.openBaoToken},
	}
	missing := make([]string, 0)
	for _, item := range required {
		if strings.TrimSpace(item.value) == "" {
			missing = append(missing, item.name)
		}
	}
	if len(missing) > 0 {
		return serverConfig{}, errors.New("required environment variables are missing: " + strings.Join(missing, ", "))
	}
	if strings.TrimSpace(config.region) != config.region ||
		!strings.HasSuffix(config.prefix, "/") || len(config.prefix) > 1024 ||
		!validPrefix(config.prefix) {
		return serverConfig{}, errors.New("invalid S3 region or GoBackup object prefix")
	}
	if _, err := secrets.ParseReference(config.accessKeyRef); err != nil {
		return serverConfig{}, errors.New("invalid S3 access-key secret reference")
	}
	if _, err := secrets.ParseReference(config.secretKeyRef); err != nil {
		return serverConfig{}, errors.New("invalid S3 secret-key secret reference")
	}
	if !validEndpoint(config.endpoint) {
		return serverConfig{}, errors.New("invalid S3 endpoint")
	}
	if !validOpenBaoEndpoint(config.openBaoAddress) {
		return serverConfig{}, errors.New("invalid OpenBao endpoint")
	}
	return config, nil
}

func validPrefix(prefix string) bool {
	if prefix == "" || strings.HasPrefix(prefix, "/") || strings.Contains(prefix, `\`) {
		return false
	}
	trimmed := strings.TrimSuffix(prefix, "/")
	if trimmed == "" {
		return false
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for _, character := range segment {
			if character < ' ' || character == 127 {
				return false
			}
		}
	}
	return true
}

func validEndpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return false
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return false
	}
	return parsed.Scheme != "http" || isLoopbackHost(parsed.Hostname())
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
	return isLoopbackHost(parsed.Hostname())
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
