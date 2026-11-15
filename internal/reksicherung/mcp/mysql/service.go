// Package mysql provides a deliberately narrow, read-only MySQL inspection and
// validation API for restore-review workflows. MySQL "schema" and "database"
// are synonyms, so every operation is scoped to one database.
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// AnchorDatabase is the database a process may connect to when it is not pinned
// to one restore database. A service targeting it can only be re-scoped to a
// run-scoped database via ForDatabase; the anchor itself is never inspectable.
const AnchorDatabase = "information_schema"

// runScopedDatabasePattern matches restore MCP run-derived database names.
var runScopedDatabasePattern = regexp.MustCompile(`^reksicherung_[0-9a-f]{32}$`)

var (
	ErrInvalidInput           = errors.New("invalid MySQL inspection input")
	ErrNotFound               = errors.New("MySQL object not found")
	ErrQueryFailed            = errors.New("MySQL inspection failed")
	ErrTimeout                = errors.New("MySQL inspection timed out")
	ErrCanceled               = errors.New("MySQL inspection canceled")
	ErrIntegrityCheckFailed   = errors.New("MySQL integrity check failed")
	ErrIntegrityCheckTimeout  = errors.New("MySQL integrity check timed out")
	ErrIntegrityCheckCanceled = errors.New("MySQL integrity check canceled")
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

	// maxIdentifierLength is MySQL's identifier limit (characters).
	maxIdentifierLength = 64
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

// TableRef identifies a table. Database is required. Schema is optional and,
// because MySQL schemas are databases, must be empty or equal to Database.
// Names are always quoted as identifiers, never interpolated as SQL fragments.
type TableRef struct {
	Database string
	Schema   string
	Table    string
}

// Table is metadata only; it never contains table data. Schema is the MySQL
// database that contains the table.
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

// IntegrityResult reports only whether the fixed CHECK TABLE statements
// completed without a command error for every table. It does not interpret or
// reproduce CHECK TABLE's per-table diagnostic rows, so CommandCompleted does
// not mean the database is free of corruption. When the table inventory is
// larger than the configured cap, only TablesChecked tables are checked,
// Truncated is true, and CommandCompleted is false.
type IntegrityResult struct {
	CommandCompleted bool
	TablesChecked    int
	Truncated        bool
}

type rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (rows, error)
}

type sqlDB struct {
	db *sql.DB
}

func (db sqlDB) QueryContext(ctx context.Context, query string, args ...any) (rows, error) {
	return db.db.QueryContext(ctx, query, args...)
}

// Service exposes metadata inspection, bounded aggregate validation, and a
// target-scoped CHECK TABLE completion check.
type Service struct {
	db      queryer
	options Options
}

// New constructs a service around a configured database/sql connection pool.
// The MySQL account is an essential part of the read-only boundary: grant it
// only SELECT (and, for CHECK TABLE, INSERT) on the restored database, and do
// not grant DDL, DROP, FILE, SUPER, or grant privileges.
func New(db *sql.DB, options Options) (*Service, error) {
	if db == nil {
		return nil, ErrInvalidInput
	}
	return newService(sqlDB{db: db}, options)
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
// always allowed. When the process target is AnchorDatabase, run-scoped restore
// databases (reksicherung_<32 hex>) are also allowed so semantic review can
// pass run_id without restarting the MCP process.
func (s *Service) ForDatabase(database string) (*Service, error) {
	if s == nil {
		return nil, ErrInvalidInput
	}
	if database == s.options.TargetDatabase {
		return s, nil
	}
	if s.options.TargetDatabase == AnchorDatabase && runScopedDatabasePattern.MatchString(database) {
		clone := *s
		clone.options.TargetDatabase = database
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

// inspectable reports whether the target may be inspected at all.
func (s *Service) inspectable() bool {
	return s.options.TargetDatabase != AnchorDatabase
}

// ListTables returns at most the configured maximum number of base tables in
// database. schema may be empty; otherwise it must equal database.
func (s *Service) ListTables(ctx context.Context, database, schema string) (TableList, error) {
	if ctx == nil || !s.inspectable() {
		return TableList{}, ErrInvalidInput
	}
	if database != s.options.TargetDatabase || (schema != "" && schema != database) {
		return TableList{}, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, s.options.QueryTimeout)
	defer cancel()
	list, err := s.listTables(ctx)
	if err != nil {
		return TableList{}, classifyError(ctx, err)
	}
	return list, nil
}

// listTables returns raw driver errors; callers classify them.
func (s *Service) listTables(ctx context.Context) (TableList, error) {
	const query = "SELECT table_schema, table_name FROM information_schema.tables " +
		"WHERE table_schema = ? AND table_type = 'BASE TABLE' ORDER BY table_name LIMIT ?"
	result, err := s.db.QueryContext(ctx, query, s.options.TargetDatabase, s.options.MaxTables+1)
	if err != nil {
		return TableList{}, err
	}
	defer result.Close()

	tables := make([]Table, 0, s.options.MaxTables)
	truncated := false
	for result.Next() {
		var table Table
		if err := result.Scan(&table.Schema, &table.Name); err != nil {
			return TableList{}, err
		}
		if len(tables) == s.options.MaxTables {
			truncated = true
			break
		}
		tables = append(tables, table)
	}
	if err := result.Err(); err != nil {
		return TableList{}, err
	}
	if err := result.Close(); err != nil {
		return TableList{}, err
	}
	return TableList{Tables: tables, Truncated: truncated}, nil
}

// DescribeTable returns bounded column metadata for a base table.
func (s *Service) DescribeTable(ctx context.Context, ref TableRef) (TableDescription, error) {
	if ctx == nil || !s.inspectable() {
		return TableDescription{}, ErrInvalidInput
	}
	if err := s.checkRef(ref); err != nil {
		return TableDescription{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.options.QueryTimeout)
	defer cancel()

	const query = "SELECT c.column_name, c.data_type, " +
		"COALESCE(c.character_maximum_length, 0), COALESCE(c.numeric_precision, 0), " +
		"COALESCE(c.numeric_scale, 0), c.is_nullable " +
		"FROM information_schema.columns AS c " +
		"JOIN information_schema.tables AS t ON t.table_schema = c.table_schema AND t.table_name = c.table_name " +
		"WHERE t.table_type = 'BASE TABLE' AND c.table_schema = ? AND c.table_name = ? " +
		"ORDER BY c.ordinal_position LIMIT ?"
	result, err := s.db.QueryContext(ctx, query, ref.Database, ref.Table, s.options.MaxColumns+1)
	if err != nil {
		return TableDescription{}, classifyError(ctx, err)
	}
	defer result.Close()
	columns := make([]Column, 0, s.options.MaxColumns)
	truncated := false
	for result.Next() {
		var (
			column                      Column
			maxLength, precision, scale int64
			nullable                    string
		)
		if err := result.Scan(&column.Name, &column.DataType, &maxLength, &precision, &scale, &nullable); err != nil {
			return TableDescription{}, classifyError(ctx, err)
		}
		if len(columns) == s.options.MaxColumns {
			truncated = true
			break
		}
		column.MaxLength, column.Precision, column.Scale = int(maxLength), int(precision), int(scale)
		column.Nullable = strings.EqualFold(nullable, "YES")
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
	ref.Schema = ref.Database
	return TableDescription{Ref: ref, Columns: columns, Truncated: truncated}, nil
}

// Validate executes a generated SELECT COUNT(*) over a LIMIT-bounded subquery
// and returns only an aggregate count capped at the requested Limit. It never
// accepts SQL text or predicates.
func (s *Service) Validate(ctx context.Context, request ValidationQuery) (ValidationResult, error) {
	if ctx == nil || !s.inspectable() {
		return ValidationResult{}, ErrInvalidInput
	}
	if request.Limit < 1 || request.Limit > s.options.MaxSampleRows {
		return ValidationResult{}, ErrInvalidInput
	}
	if err := s.checkRef(request.Table); err != nil {
		return ValidationResult{}, err
	}
	query, args, err := buildValidationSQL(request)
	if err != nil {
		return ValidationResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.options.QueryTimeout)
	defer cancel()

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

// CheckIntegrity runs a fixed, default-option CHECK TABLE against each base
// table (up to the configured table cap) in only the configured target
// database. No table option (such as EXTENDED or FOR UPGRADE) and no repair
// statement is ever issued. CHECK TABLE reports table problems as result rows
// rather than command errors; those rows are intentionally discarded, so the
// result states only that the commands completed.
func (s *Service) CheckIntegrity(ctx context.Context) (IntegrityResult, error) {
	if ctx == nil || !s.inspectable() {
		return IntegrityResult{}, ErrInvalidInput
	}
	database, err := quoteIdentifier(s.options.TargetDatabase)
	if err != nil {
		return IntegrityResult{}, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, s.options.IntegrityTimeout)
	defer cancel()

	list, err := s.listTables(ctx)
	if err != nil {
		return IntegrityResult{}, classifyIntegrityError(ctx, err)
	}
	checked := 0
	for _, table := range list.Tables {
		quotedTable, err := quoteIdentifier(table.Name)
		if err != nil {
			return IntegrityResult{}, ErrIntegrityCheckFailed
		}
		result, err := s.db.QueryContext(ctx, "CHECK TABLE "+database+"."+quotedTable)
		if err != nil {
			return IntegrityResult{}, classifyIntegrityError(ctx, err)
		}
		// Drain without reading diagnostic values.
		for result.Next() {
		}
		iterErr := result.Err()
		closeErr := result.Close()
		if iterErr != nil {
			return IntegrityResult{}, classifyIntegrityError(ctx, iterErr)
		}
		if closeErr != nil {
			return IntegrityResult{}, classifyIntegrityError(ctx, closeErr)
		}
		checked++
	}
	return IntegrityResult{
		CommandCompleted: !list.Truncated,
		TablesChecked:    checked,
		Truncated:        list.Truncated,
	}, nil
}

// checkRef validates a table reference against the service target.
func (s *Service) checkRef(ref TableRef) error {
	if ref.Database != s.options.TargetDatabase || (ref.Schema != "" && ref.Schema != ref.Database) {
		return ErrInvalidInput
	}
	if _, err := quoteIdentifier(ref.Table); err != nil {
		return ErrInvalidInput
	}
	return nil
}

func buildValidationSQL(request ValidationQuery) (string, []any, error) {
	database, err := quoteIdentifier(request.Table.Database)
	if err != nil {
		return "", nil, ErrInvalidInput
	}
	if request.Table.Schema != "" && request.Table.Schema != request.Table.Database {
		return "", nil, ErrInvalidInput
	}
	table, err := quoteIdentifier(request.Table.Table)
	if err != nil {
		return "", nil, ErrInvalidInput
	}
	query := "SELECT COUNT(*) FROM (SELECT 1 FROM " + database + "." + table +
		" LIMIT ?) AS bounded_sample"
	return query, []any{request.Limit + 1}, nil
}

// quoteIdentifier backtick-quotes a MySQL identifier, doubling embedded
// backticks. Empty, over-long, NUL, and control-character names are rejected.
func quoteIdentifier(identifier string) (string, error) {
	if identifier == "" || len([]rune(identifier)) > maxIdentifierLength {
		return "", ErrInvalidInput
	}
	for _, char := range identifier {
		if char == 0 || unicode.IsControl(char) {
			return "", ErrInvalidInput
		}
	}
	return "`" + strings.ReplaceAll(identifier, "`", "``") + "`", nil
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
