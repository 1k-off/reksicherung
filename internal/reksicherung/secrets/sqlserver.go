package secrets

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const (
	// SQLServerPath is the project OpenBao KV path for the restore SQL instance.
	SQLServerPath = "reksicherung/sqlserver"

	FieldSQLHost     = "HOST"
	FieldSQLPort     = "PORT"
	FieldSQLUsername = "USERNAME"
	FieldSQLPassword = "PASSWORD"
)

// ErrIncompleteSQLServer indicates required SQL OpenBao fields are missing.
var ErrIncompleteSQLServer = errors.New("secrets: incomplete SQL Server material")

// SQLServerMaterial is the restore-target connection resolved from OpenBao.
type SQLServerMaterial struct {
	Host     string
	Port     int
	Username string
	Password []byte
}

// Clear overwrites the password bytes.
func (m *SQLServerMaterial) Clear() {
	if m == nil {
		return
	}
	clearBytes(m.Password)
	m.Password = nil
}

// SQLCmdAddress returns the sqlcmd-style address tcp:host,port.
func (m SQLServerMaterial) SQLCmdAddress() string {
	host := m.Host
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("tcp:%s,%d", host, m.Port)
}

// MSSQLDSN builds a go-mssqldb URL DSN for the given database name.
func (m SQLServerMaterial) MSSQLDSN(database string) (string, error) {
	if strings.TrimSpace(database) == "" || strings.TrimSpace(database) != database {
		return "", ErrIncompleteSQLServer
	}
	user := url.UserPassword(m.Username, string(m.Password))
	host := m.Host
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		host = "[" + host + "]"
	}
	u := &url.URL{
		Scheme: "sqlserver",
		User:   user,
		Host:   net.JoinHostPort(host, strconv.Itoa(m.Port)),
	}
	query := url.Values{}
	query.Set("database", database)
	// Restore targets are isolated (often compose/self-signed). Loopback lab
	// skips TLS; other hosts encrypt but trust the server certificate.
	if isLoopbackHost(m.Host) {
		query.Set("encrypt", "disable")
	} else {
		query.Set("encrypt", "true")
		query.Set("TrustServerCertificate", "true")
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

// ResolveSQLServer loads HOST/PORT/USERNAME/PASSWORD from
// secret/reksicherung/sqlserver. PORT defaults to 1433 when omitted.
func ResolveSQLServer(ctx context.Context, provider SecretProvider) (SQLServerMaterial, error) {
	if provider == nil {
		return SQLServerMaterial{}, ErrIncompleteSQLServer
	}
	host, err := resolveRequired(ctx, provider, SQLServerPath, FieldSQLHost)
	if err != nil {
		return SQLServerMaterial{}, err
	}
	username, err := resolveRequired(ctx, provider, SQLServerPath, FieldSQLUsername)
	if err != nil {
		return SQLServerMaterial{}, err
	}
	password, err := resolveRequiredBytes(ctx, provider, SQLServerPath, FieldSQLPassword)
	if err != nil {
		return SQLServerMaterial{}, err
	}
	port := 1433
	if raw, err := resolveOptional(ctx, provider, SQLServerPath, FieldSQLPort); err == nil && raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 65535 {
			clearBytes(password)
			return SQLServerMaterial{}, fmt.Errorf("%w: invalid PORT", ErrIncompleteSQLServer)
		}
		port = parsed
	}
	host = strings.TrimSpace(host)
	username = strings.TrimSpace(username)
	if host == "" || username == "" || len(password) == 0 ||
		strings.ContainsAny(host, "/\\ \t\r\n") || strings.HasPrefix(host, "-") ||
		strings.ContainsAny(username, "\x00\r\n") || len(username) > 128 {
		clearBytes(password)
		return SQLServerMaterial{}, ErrIncompleteSQLServer
	}
	return SQLServerMaterial{Host: host, Port: port, Username: username, Password: password}, nil
}

func resolveRequired(ctx context.Context, provider SecretProvider, path, key string) (string, error) {
	value, err := resolveRequiredBytes(ctx, provider, path, key)
	if err != nil {
		return "", err
	}
	out := string(value)
	clearBytes(value)
	if strings.TrimSpace(out) == "" {
		return "", ErrIncompleteSQLServer
	}
	return out, nil
}

func resolveRequiredBytes(ctx context.Context, provider SecretProvider, path, key string) ([]byte, error) {
	ref := Reference{Provider: "openbao", Mount: DefaultMount, Path: path, Key: key}
	value, err := provider.Resolve(ctx, ref)
	if err != nil || len(value) == 0 {
		clearBytes(value)
		return nil, ErrIncompleteSQLServer
	}
	return value, nil
}

func resolveOptional(ctx context.Context, provider SecretProvider, path, key string) (string, error) {
	ref := Reference{Provider: "openbao", Mount: DefaultMount, Path: path, Key: key}
	value, err := provider.Resolve(ctx, ref)
	if err != nil {
		return "", err
	}
	out := string(value)
	clearBytes(value)
	return out, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
