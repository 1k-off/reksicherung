package secrets

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestResolveSQLServerDefaultsPortAndBuildsAddresses(t *testing.T) {
	provider := mapProvider{
		SQLServerPath: {
			FieldSQLHost:     "sqlserver",
			FieldSQLUsername: "reksicherung_restore",
			FieldSQLPassword: "secret-pass",
		},
	}
	material, err := ResolveSQLServer(context.Background(), provider)
	if err != nil {
		t.Fatalf("ResolveSQLServer() error = %v", err)
	}
	defer material.Clear()
	if material.Port != 1433 || material.SQLCmdAddress() != "tcp:sqlserver,1433" {
		t.Fatalf("material = %#v address=%q", material, material.SQLCmdAddress())
	}
	dsn, err := material.MSSQLDSN("reksicherung_abc")
	if err != nil {
		t.Fatalf("MSSQLDSN() error = %v", err)
	}
	if !strings.Contains(dsn, "database=reksicherung_abc") || !strings.Contains(dsn, "sqlserver://") {
		t.Fatalf("dsn = %q", dsn)
	}
	if !strings.Contains(dsn, "TrustServerCertificate=true") {
		t.Fatalf("dsn missing TrustServerCertificate for compose host: %q", dsn)
	}
}

func TestResolveSQLServerRequiresFields(t *testing.T) {
	provider := mapProvider{SQLServerPath: {FieldSQLHost: "sqlserver"}}
	if _, err := ResolveSQLServer(context.Background(), provider); !errors.Is(err, ErrIncompleteSQLServer) {
		t.Fatalf("error = %v, want ErrIncompleteSQLServer", err)
	}
}
