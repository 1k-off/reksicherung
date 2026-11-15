package secrets

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

const (
	// MySQLPath is the project OpenBao KV path for the restore MySQL instance.
	MySQLPath = "reksicherung/mysql"

	defaultMySQLPort = 3306
)

// ErrIncompleteMySQL indicates required MySQL OpenBao fields are missing.
var ErrIncompleteMySQL = errors.New("secrets: incomplete MySQL material")

// MySQLMaterial is the restore-target connection resolved from OpenBao.
type MySQLMaterial struct {
	Host     string
	Port     int
	Username string
	Password []byte
}

// Clear overwrites the password bytes.
func (m *MySQLMaterial) Clear() {
	if m == nil {
		return
	}
	clearBytes(m.Password)
	m.Password = nil
}

// DSN builds a go-sql-driver/mysql DSN (user:pass@tcp(host:port)/dbname) for
// the given database name.
func (m MySQLMaterial) DSN(database string) (string, error) {
	if strings.TrimSpace(database) == "" || strings.TrimSpace(database) != database ||
		strings.ContainsAny(database, "/?\x00\r\n") {
		return "", ErrIncompleteMySQL
	}
	if m.Host == "" || m.Port < 1 || m.Port > 65535 || m.Username == "" ||
		strings.ContainsAny(m.Username, ":@/\x00\r\n") {
		return "", ErrIncompleteMySQL
	}
	host := m.Host
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s", m.Username, m.Password, host, m.Port, database), nil
}

// ResolveMySQL loads HOST/PORT/USERNAME/PASSWORD from
// secret/reksicherung/mysql. PORT defaults to 3306 when omitted.
func ResolveMySQL(ctx context.Context, provider SecretProvider) (MySQLMaterial, error) {
	if provider == nil {
		return MySQLMaterial{}, ErrIncompleteMySQL
	}
	host, err := resolveRequired(ctx, provider, MySQLPath, FieldSQLHost)
	if err != nil {
		return MySQLMaterial{}, mysqlErr(err)
	}
	username, err := resolveRequired(ctx, provider, MySQLPath, FieldSQLUsername)
	if err != nil {
		return MySQLMaterial{}, mysqlErr(err)
	}
	password, err := resolveRequiredBytes(ctx, provider, MySQLPath, FieldSQLPassword)
	if err != nil {
		return MySQLMaterial{}, mysqlErr(err)
	}
	port := defaultMySQLPort
	if raw, err := resolveOptional(ctx, provider, MySQLPath, FieldSQLPort); err == nil && raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 65535 {
			clearBytes(password)
			return MySQLMaterial{}, fmt.Errorf("%w: invalid PORT", ErrIncompleteMySQL)
		}
		port = parsed
	}
	host = strings.TrimSpace(host)
	username = strings.TrimSpace(username)
	if host == "" || username == "" || len(password) == 0 ||
		strings.ContainsAny(host, "/\\ \t\r\n") || strings.HasPrefix(host, "-") ||
		strings.ContainsAny(username, ":@/\x00\r\n") || len(username) > 128 {
		clearBytes(password)
		return MySQLMaterial{}, ErrIncompleteMySQL
	}
	return MySQLMaterial{Host: host, Port: port, Username: username, Password: password}, nil
}

// mysqlErr maps the shared resolver sentinel to the MySQL one.
func mysqlErr(err error) error {
	if errors.Is(err, ErrIncompleteSQLServer) {
		return ErrIncompleteMySQL
	}
	return err
}
