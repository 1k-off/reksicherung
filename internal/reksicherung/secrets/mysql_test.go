package secrets

import (
	"context"
	"errors"
	"testing"
)

func TestResolveMySQLDefaultsPort(t *testing.T) {
	provider := mapProvider{
		MySQLPath: {
			FieldSQLHost:     "mysql",
			FieldSQLUsername: "restore",
			FieldSQLPassword: "password1",
		},
	}
	m, err := ResolveMySQL(context.Background(), provider)
	if err != nil || m.Port != 3306 || m.Host != "mysql" {
		t.Fatalf("got %#v err=%v", m, err)
	}
	defer m.Clear()
	dsn, err := m.DSN("reksicherung_abc")
	if err != nil {
		t.Fatalf("DSN() error = %v", err)
	}
	if dsn != "restore:password1@tcp(mysql:3306)/reksicherung_abc" {
		t.Fatalf("dsn = %q", dsn)
	}
	m.Clear()
	if m.Password != nil {
		t.Fatal("Clear() left password set")
	}
}

func TestResolveMySQLCustomPortAndIPv6(t *testing.T) {
	provider := mapProvider{
		MySQLPath: {
			FieldSQLHost:     "::1",
			FieldSQLPort:     "3307",
			FieldSQLUsername: "restore",
			FieldSQLPassword: "p@ss:w/rd",
		},
	}
	m, err := ResolveMySQL(context.Background(), provider)
	if err != nil {
		t.Fatalf("ResolveMySQL() error = %v", err)
	}
	defer m.Clear()
	dsn, err := m.DSN("db1")
	if err != nil {
		t.Fatalf("DSN() error = %v", err)
	}
	if dsn != "restore:p@ss:w/rd@tcp([::1]:3307)/db1" {
		t.Fatalf("dsn = %q", dsn)
	}
}

func TestResolveMySQLRequiresFields(t *testing.T) {
	provider := mapProvider{MySQLPath: {FieldSQLHost: "mysql"}}
	if _, err := ResolveMySQL(context.Background(), provider); !errors.Is(err, ErrIncompleteMySQL) {
		t.Fatalf("error = %v, want ErrIncompleteMySQL", err)
	}
	if _, err := ResolveMySQL(context.Background(), nil); !errors.Is(err, ErrIncompleteMySQL) {
		t.Fatalf("nil provider error = %v, want ErrIncompleteMySQL", err)
	}
}

func TestResolveMySQLRejectsInvalidPort(t *testing.T) {
	provider := mapProvider{
		MySQLPath: {
			FieldSQLHost:     "mysql",
			FieldSQLPort:     "70000",
			FieldSQLUsername: "restore",
			FieldSQLPassword: "password1",
		},
	}
	if _, err := ResolveMySQL(context.Background(), provider); !errors.Is(err, ErrIncompleteMySQL) {
		t.Fatalf("error = %v, want ErrIncompleteMySQL", err)
	}
}

func TestMySQLDSNRejectsUnsafeInput(t *testing.T) {
	m := MySQLMaterial{Host: "mysql", Port: 3306, Username: "restore", Password: []byte("password1")}
	for _, name := range []string{"", " db", "db/x", "db?x=1", "a\nb"} {
		if _, err := m.DSN(name); !errors.Is(err, ErrIncompleteMySQL) {
			t.Errorf("DSN(%q) error = %v, want ErrIncompleteMySQL", name, err)
		}
	}
	bad := MySQLMaterial{Host: "mysql", Port: 3306, Username: "a:b", Password: []byte("password1")}
	if _, err := bad.DSN("db"); !errors.Is(err, ErrIncompleteMySQL) {
		t.Errorf("DSN with colon username error = %v", err)
	}
}
