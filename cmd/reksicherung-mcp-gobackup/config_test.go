package main

import (
	"strings"
	"testing"
)

func TestLoadConfigRequiresSafeStorageAndOpenBaoSettings(t *testing.T) {
	valid := map[string]string{
		s3EndpointEnv: "https://s3.example.invalid",
		s3RegionEnv:   "us-east-1",
		s3BucketEnv:   "backups",
		s3PrefixEnv:   "production/sqlserver/",
		accessRefEnv:  "openbao://secret/reksicherung/storage#access-key",
		secretRefEnv:  "openbao://secret/reksicherung/storage#secret-key",
		openBaoAddr:   "https://bao.example.invalid",
		openBaoToken:  "private-token",
	}
	getenv := func(name string) string { return valid[name] }
	if _, err := loadConfig(getenv); err != nil {
		t.Fatalf("loadConfig(valid) error = %v", err)
	}

	for _, test := range []struct {
		name   string
		change func(map[string]string)
	}{
		{name: "missing prefix", change: func(values map[string]string) { delete(values, s3PrefixEnv) }},
		{name: "root prefix", change: func(values map[string]string) { values[s3PrefixEnv] = "/" }},
		{name: "traversing prefix", change: func(values map[string]string) { values[s3PrefixEnv] = "production/../other/" }},
		{name: "invalid secret reference", change: func(values map[string]string) { values[accessRefEnv] = "literal-secret" }},
		{name: "missing OpenBao token", change: func(values map[string]string) { delete(values, openBaoToken) }},
		{name: "remote plaintext OpenBao", change: func(values map[string]string) { values[openBaoAddr] = "http://bao.example.invalid" }},
		{name: "remote plaintext S3", change: func(values map[string]string) { values[s3EndpointEnv] = "http://s3.example.invalid" }},
		{name: "endpoint credentials", change: func(values map[string]string) { values[s3EndpointEnv] = "https://user:password@s3.example.invalid" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := make(map[string]string, len(valid))
			for key, value := range valid {
				values[key] = value
			}
			test.change(values)
			_, err := loadConfig(func(name string) string { return values[name] })
			if err == nil {
				t.Fatal("loadConfig() succeeded with invalid configuration")
			}
			if strings.Contains(err.Error(), "private-token") || strings.Contains(err.Error(), "password") {
				t.Fatalf("loadConfig() leaked secret material: %v", err)
			}
		})
	}
}

func TestLoadConfigAllowsLoopbackPlaintextS3(t *testing.T) {
	values := map[string]string{
		s3EndpointEnv: "http://127.0.0.1:9000",
		s3RegionEnv:   "us-east-1",
		s3BucketEnv:   "backups",
		s3PrefixEnv:   "production/sqlserver/",
		accessRefEnv:  "openbao://secret/reksicherung/storage#access-key",
		secretRefEnv:  "openbao://secret/reksicherung/storage#secret-key",
		openBaoAddr:   "https://bao.example.invalid",
		openBaoToken:  "private-token",
	}
	if _, err := loadConfig(func(name string) string { return values[name] }); err != nil {
		t.Fatalf("loadConfig(loopback S3 endpoint) error = %v", err)
	}
}
