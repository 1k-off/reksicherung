package main

import (
	"testing"
)

func TestLoadRestoreConfigRequiresOpenBaoOnly(t *testing.T) {
	values := map[string]string{
		openBaoAddrEnv:  "https://openbao.example.test",
		openBaoTokenEnv: "protected-token",
	}
	config, err := loadRestoreConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("loadRestoreConfig() error = %v", err)
	}
	if config.backupDirectory != "/var/opt/mssql/backup" || config.dataDirectory != "/var/opt/mssql/data" ||
		config.stageDirectory != "/var/lib/reksicherung/artifacts" {
		t.Fatalf("defaults = %#v", config)
	}
}

func TestLoadRestoreConfigRejectsInsecureOpenBao(t *testing.T) {
	values := map[string]string{
		openBaoAddrEnv:  "http://bao.example.test",
		openBaoTokenEnv: "protected-token",
	}
	if _, err := loadRestoreConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("loadRestoreConfig() accepted plaintext remote OpenBao")
	}
}
