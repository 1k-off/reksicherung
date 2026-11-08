package main

import (
	"testing"
)

func TestLoadRestoreConfigAndAddressValidation(t *testing.T) {
	values := validRestoreEnvironment()
	config, err := loadRestoreConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("loadRestoreConfig() error = %v", err)
	}
	if config.address != values[sqlAddressEnv] || config.passwordRef != values[sqlPasswordRef] ||
		config.sqlCmd != "sqlcmd" {
		t.Fatalf("loaded config = %#v", config)
	}
	for _, address := range []string{
		"sqlserver:1433", "tcp:sqlserver:1433", "tcp:sqlserver,0", "tcp:sqlserver,65536",
		"tcp:-bad,1433", "tcp:server,1433 -Q",
	} {
		if validSQLCmdAddress(address) {
			t.Errorf("validSQLCmdAddress(%q) = true", address)
		}
	}
	for _, address := range []string{"tcp:sqlserver,1433", "tcp:localhost,1433", "tcp:[::1],1433"} {
		if !validSQLCmdAddress(address) {
			t.Errorf("validSQLCmdAddress(%q) = false", address)
		}
	}
}

func TestLoadRestoreConfigRejectsInsecureAndMalformedSettings(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]string)
	}{
		{name: "missing backup mount", change: func(env map[string]string) { delete(env, backupDirEnv) }},
		{name: "plaintext remote OpenBao", change: func(env map[string]string) { env[openBaoAddrEnv] = "http://bao.example.test" }},
		{name: "OpenBao user info", change: func(env map[string]string) { env[openBaoAddrEnv] = "https://user:secret@bao.example.test" }},
		{name: "literal password is not a reference", change: func(env map[string]string) { env[sqlPasswordRef] = "plain-text" }},
		{name: "unsafe server address", change: func(env map[string]string) { env[sqlAddressEnv] = "tcp:sqlserver,1433 -Q DROP DATABASE" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := validRestoreEnvironment()
			test.change(values)
			if _, err := loadRestoreConfig(func(key string) string { return values[key] }); err == nil {
				t.Fatal("loadRestoreConfig() accepted invalid settings")
			}
		})
	}
}

func validRestoreEnvironment() map[string]string {
	return map[string]string{
		sqlAddressEnv:   "tcp:sqlserver,1433",
		sqlUsernameEnv:  "reksicherung_restore",
		sqlPasswordRef:  "openbao://secret/reksicherung/sql#password",
		stageDirEnv:     "/srv/reksicherung/staged",
		backupDirEnv:    "/srv/sqlserver/backup",
		dataDirEnv:      "/var/opt/mssql/data",
		openBaoAddrEnv:  "https://openbao.example.test",
		openBaoTokenEnv: "protected-token",
	}
}
