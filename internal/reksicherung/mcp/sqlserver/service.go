// Package sqlserver provides a deliberately narrow, read-only SQL Server
// inspection and validation API for restore-review workflows.
package sqlserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
)

// runScopedDatabasePattern matches restore MCP run-derived database names.
var runScopedDatabasePattern = regexp.MustCompile(`^reksicherung_[0-9a-f]{32}$`)

var (
	ErrInvalidInput           = errors.New("invalid SQL Server inspection input")
	ErrNotFound               = errors.New("SQL Server object not found")
	ErrQueryFailed            = errors.New("SQL Server inspection failed")
	ErrTimeout                = errors.New("SQL Server inspection timed out")
	ErrCanceled               = errors.New("SQL Server inspection canceled")
	ErrIntegrityCheckFailed   = errors.New("SQL Server integrity check failed")
	ErrIntegrityCheckTimeout  = errors.New("SQL Server integrity check timed out")
	ErrIntegrityCheckCanceled = errors.New("SQL Server integrity check canceled")
)

const (
	defaultQueryTimeout     = 10 * time.Second
	defaultIntegrityTimeout = 5 * time.Minute
	defaultSampleLimit      = 100
	defaultMetadataCap      = 500

	hardQueryTimeout     = 2 * time.Minute
	hardIntegrityTimeout = 30 * time.Minute
	hardSampleLimit      = 1000
	hardMetadataCap      = 5000
	integrityCheckMaxDOP = 2
)

// Options cap the work each service operation can request. Zero-valued fields
// use conservative defaults; values above the hard limits are rejected.
type Options struct {
	// TargetDatabase scopes every operation to one configured restore database.
	TargetDatabase   string
	QueryTimeout     time.Duration
	IntegrityTimeout time.Duration
	MaxSampleRows    int
	MaxTables        int
	MaxColumns       int
}

// TableRef identifies a table using SQL Server database, schema, and table
// identifiers. Names are always quoted as identifiers, never interpolated as
// SQL fragments.
type TableRef struct {
	Database string
	Schema   string
	Table    string
}

// Table is metadata only; it never contains table data.
type Table struct {
	Schema string
	Name   string
}

// TableList is bounded and reports whether more tables exist than were
// returned.
type TableList struct {
	Tables    []Table
	Truncated bool
}

// Column contains schema metadata only.
type Column struct {
	Name      string
	DataType  string
	MaxLength int
	Precision int
	Scale     int
	Nullable  bool
}

// TableDescription is bounded and reports whether more columns exist.
type TableDescription struct {
	Ref       TableRef
	Columns   []Column
	Truncated bool
}

// ValidationQuery counts rows in a table, up to Limit+1. It deliberately
// accepts no caller-supplied predicates and cannot return row-level values.
type ValidationQuery struct {
	Table TableRef
	Limit int
}

// ValidationResult contains only bounded aggregate information. If Truncated
// is true, ObservedRows equals Limit and the actual matching count is unknown.
type ValidationResult struct {
	ObservedRows int
	Limit        int
	Truncated    bool
}

// IntegrityResult reports only whether the bounded DBCC CHECKDB command
// completed. It does not reproduce SQL Server's diagnostic messages.
type IntegrityResult struct {
	Completed bool
}

type rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type sqlDB struct {
	db *sql.DB
}

func (db sqlDB) QueryContext(ctx context.Context, query string, args ...any) (rows, error) {
	return db.db.QueryContext(ctx, query, args...)
}

func (db sqlDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return db.db.ExecContext(ctx, query, args...)
}

// Service exposes metadata inspection, bounded aggregate validation, and a
// target-scoped DBCC CHECKDB operation.
type Service struct {
	db      queryer
	options Options
}

// New constructs a service around a configured database/sql connection pool.
// The SQL Server login is an essential part of the read-only boundary: grant
// it only CONNECT and SELECT on the restored database/schema, and do not grant
// write, DDL, impersonation, or procedure-execution permissions.
func New(db *sql.DB, options Options) (*Service, error) {
	if db == nil {
		return nil, ErrInvalidInput
	}
	if _, err := quoteIdentifier(options.TargetDatabase); err != nil {
		return nil, ErrInvalidInput
	}
	normalized, err := normalizeOptions(options)
	if err != nil {
		return nil, err
	}
	return &Service{db: sqlDB{db: db}, options: normalized}, nil
}

func newService(db queryer, options Options) (*Service, error) {
	if db == nil {
		return nil, ErrInvalidInput
	}
	if _, err := quoteIdentifier(options.TargetDatabase); err != nil {
		return nil, ErrInvalidInput
	}
	normalized, err := normalizeOptions(options)
	if err != nil {
		return nil, err
	}
	return &Service{db: db, options: normalized}, nil
}

// ForDatabase returns a service scoped to database. The configured target is
// always allowed. When the process target is master (Cursor/lab hosts that pin
// DATABASE=master), run-scoped restore databases (reksicherung_<hex>) are also
// allowed so semantic review can pass run_id without restarting MCP.
func (s *Service) ForDatabase(database string) (*Service, error) {
	if s == nil {
		return nil, ErrInvalidInput
	}
	if database == s.options.TargetDatabase {
		return s, nil
	}
	if s.options.TargetDatabase == "master" && runScopedDatabasePattern.MatchString(database) {
		clone := *s
		opts := s.options
		opts.TargetDatabase = database
		clone.options = opts
		return &clone, nil
	}
	return nil, ErrInvalidInput
}

func normalizeOptions(options Options) (Options, error) {
	if options.QueryTimeout == 0 {
		options.QueryTimeout = defaultQueryTimeout
	}
	if options.IntegrityTimeout == 0 {
		options.IntegrityTimeout = defaultIntegrityTimeout
	}
	if options.MaxSampleRows == 0 {
		options.MaxSampleRows = defaultSampleLimit
	}
	if options.MaxTables == 0 {
		options.MaxTables = defaultMetadataCap
	}
	if options.MaxColumns == 0 {
		options.MaxColumns = defaultMetadataCap
	}
	if options.QueryTimeout < time.Millisecond || options.QueryTimeout > hardQueryTimeout ||
		options.IntegrityTimeout < time.Millisecond || options.IntegrityTimeout > hardIntegrityTimeout ||
		options.MaxSampleRows < 1 || options.MaxSampleRows > hardSampleLimit ||
		options.MaxTables < 1 || options.MaxTables > hardMetadataCap ||
		options.MaxColumns < 1 || options.MaxColumns > hardMetadataCap {
		return Options{}, ErrInvalidInput
	}
	return options, nil
}

// ListTables returns at most the configured maximum number of user tables in
// database. schema may be empty to include every schema.
func (s *Service) ListTables(ctx context.Context, database, schema string) (TableList, error) {
	if ctx == nil {
		return TableList{}, ErrInvalidInput
	}
	if database != s.options.TargetDatabase {
		return TableList{}, ErrInvalidInput
	}
	dbIdentifier, err := quoteIdentifier(database)
	if err != nil {
		return TableList{}, ErrInvalidInput
	}
	if schema != "" {
		if _, err := quoteIdentifier(schema); err != nil {
			return TableList{}, ErrInvalidInput
		}
	}

	ctx, cancel := context.WithTimeout(ctx, s.options.QueryTimeout)
	defer cancel()
	query := "SELECT TOP (@p1) s.name, t.name FROM " + dbIdentifier +
		".sys.tables AS t JOIN " + dbIdentifier +
		".sys.schemas AS s ON s.schema_id = t.schema_id " +
		"WHERE t.is_ms_shipped = 0 AND (@p2 = N'' OR s.name = @p2) " +
		"ORDER BY s.name, t.name"
	result, err := s.db.QueryContext(ctx, query, s.options.MaxTables+1, schema)
	if err != nil {
		return TableList{}, classifyError(ctx, err)
	}
	defer result.Close()

	tables := make([]Table, 0, s.options.MaxTables)
	truncated := false
	for result.Next() {
		var table Table
		if err := result.Scan(&table.Schema, &table.Name); err != nil {
			return TableList{}, classifyError(ctx, err)
		}
		if len(tables) == s.options.MaxTables {
			truncated = true
			break
		}
		tables = append(tables, table)
	}
	if err := result.Err(); err != nil {
		return TableList{}, classifyError(ctx, err)
	}
	if err := result.Close(); err != nil {
		return TableList{}, classifyError(ctx, err)
	}
	return TableList{Tables: tables, Truncated: truncated}, nil
}

// DescribeTable returns bounded column metadata for a user table.
func (s *Service) DescribeTable(ctx context.Context, ref TableRef) (TableDescription, error) {
	if ctx == nil {
		return TableDescription{}, ErrInvalidInput
	}
	if ref.Database != s.options.TargetDatabase {
		return TableDescription{}, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, s.options.QueryTimeout)
	defer cancel()
	return s.describeTable(ctx, ref)
}

// Validate executes a generated, parameterized SELECT and returns only an
// aggregate count capped at the requested Limit. It never accepts SQL text.
func (s *Service) Validate(ctx context.Context, request ValidationQuery) (ValidationResult, error) {
	if ctx == nil {
		return ValidationResult{}, ErrInvalidInput
	}
	if request.Limit < 1 || request.Limit > s.options.MaxSampleRows {
		return ValidationResult{}, ErrInvalidInput
	}
	if request.Table.Database != s.options.TargetDatabase {
		return ValidationResult{}, ErrInvalidInput
	}
	if _, _, err := quoteTable(request.Table); err != nil {
		return ValidationResult{}, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, s.options.QueryTimeout)
	defer cancel()

	query, args, err := buildValidationSQL(request)
	if err != nil {
		return ValidationResult{}, err
	}
	result, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return ValidationResult{}, classifyError(ctx, err)
	}
	defer result.Close()
	if !result.Next() {
		if err := result.Err(); err != nil {
			return ValidationResult{}, classifyError(ctx, err)
		}
		return ValidationResult{}, ErrQueryFailed
	}
	var observed int64
	if err := result.Scan(&observed); err != nil {
		return ValidationResult{}, classifyError(ctx, err)
	}
	if err := result.Err(); err != nil {
		return ValidationResult{}, classifyError(ctx, err)
	}
	if err := result.Close(); err != nil {
		return ValidationResult{}, classifyError(ctx, err)
	}
	if observed > int64(request.Limit) {
		return ValidationResult{ObservedRows: request.Limit, Limit: request.Limit, Truncated: true}, nil
	}
	return ValidationResult{ObservedRows: int(observed), Limit: request.Limit}, nil
}

// CheckIntegrity runs a full DBCC CHECKDB against only the configured target
// database. The SQL statement and options are fixed; the database name is
// quoted as an identifier. The operation never uses a repair option.
func (s *Service) CheckIntegrity(ctx context.Context) (IntegrityResult, error) {
	if ctx == nil {
		return IntegrityResult{}, ErrInvalidInput
	}
	database, err := quoteIdentifier(s.options.TargetDatabase)
	if err != nil {
		return IntegrityResult{}, ErrInvalidInput
	}

	ctx, cancel := context.WithTimeout(ctx, s.options.IntegrityTimeout)
	defer cancel()
	statement := fmt.Sprintf(
		"DBCC CHECKDB (%s) WITH NO_INFOMSGS, MAXDOP = %d",
		database,
		integrityCheckMaxDOP,
	)
	if _, err := s.db.ExecContext(ctx, statement); err != nil {
		return IntegrityResult{}, classifyIntegrityError(ctx, err)
	}
	return IntegrityResult{Completed: true}, nil
}

func (s *Service) describeTable(ctx context.Context, ref TableRef) (TableDescription, error) {
	if ref.Database != s.options.TargetDatabase {
		return TableDescription{}, ErrInvalidInput
	}
	dbIdentifier, _, err := quoteTable(ref)
	if err != nil {
		return TableDescription{}, ErrInvalidInput
	}
	query := "SELECT TOP (@p1) c.name, ty.name, c.max_length, c.precision, c.scale, c.is_nullable " +
		"FROM " + dbIdentifier + ".sys.tables AS t " +
		"JOIN " + dbIdentifier + ".sys.schemas AS s ON s.schema_id = t.schema_id " +
		"JOIN " + dbIdentifier + ".sys.columns AS c ON c.object_id = t.object_id " +
		"JOIN " + dbIdentifier + ".sys.types AS ty ON ty.user_type_id = c.user_type_id " +
		"WHERE s.name = @p2 AND t.name = @p3 ORDER BY c.column_id"
	result, err := s.db.QueryContext(ctx, query, s.options.MaxColumns+1, ref.Schema, ref.Table)
	if err != nil {
		return TableDescription{}, classifyError(ctx, err)
	}
	defer result.Close()
	columns := make([]Column, 0, s.options.MaxColumns)
	truncated := false
	for result.Next() {
		var column Column
		if err := result.Scan(
			&column.Name,
			&column.DataType,
			&column.MaxLength,
			&column.Precision,
			&column.Scale,
			&column.Nullable,
		); err != nil {
			return TableDescription{}, classifyError(ctx, err)
		}
		if len(columns) == s.options.MaxColumns {
			truncated = true
			break
		}
		columns = append(columns, column)
	}
	if err := result.Err(); err != nil {
		return TableDescription{}, classifyError(ctx, err)
	}
	if err := result.Close(); err != nil {
		return TableDescription{}, classifyError(ctx, err)
	}
	if len(columns) == 0 {
		return TableDescription{}, ErrNotFound
	}
	return TableDescription{Ref: ref, Columns: columns, Truncated: truncated}, nil
}

func buildValidationSQL(request ValidationQuery) (string, []any, error) {
	_, qualifiedTable, err := quoteTable(request.Table)
	if err != nil {
		return "", nil, ErrInvalidInput
	}
	args := []any{request.Limit + 1}
	query := "SELECT COUNT_BIG(*) FROM (SELECT TOP (@p1) 1 AS [sample] FROM " +
		qualifiedTable + ") AS bounded_sample"
	return query, args, nil
}

func quoteTable(ref TableRef) (string, string, error) {
	database, err := quoteIdentifier(ref.Database)
	if err != nil {
		return "", "", err
	}
	schema, err := quoteIdentifier(ref.Schema)
	if err != nil {
		return "", "", err
	}
	table, err := quoteIdentifier(ref.Table)
	if err != nil {
		return "", "", err
	}
	return database, database + "." + schema + "." + table, nil
}

func quoteIdentifier(identifier string) (string, error) {
	if identifier == "" || len(utf16.Encode([]rune(identifier))) > 128 {
		return "", ErrInvalidInput
	}
	for _, char := range identifier {
		if char == 0 || unicode.IsControl(char) {
			return "", ErrInvalidInput
		}
	}
	return "[" + strings.ReplaceAll(identifier, "]", "]]") + "]", nil
}

func classifyError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return ErrCanceled
	}
	return ErrQueryFailed
}

func classifyIntegrityError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return ErrIntegrityCheckTimeout
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return ErrIntegrityCheckCanceled
	}
	return ErrIntegrityCheckFailed
}
