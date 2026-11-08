// Package sqlserver provides a deliberately narrow, read-only SQL Server
// inspection and validation API for restore-review workflows.
package sqlserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
)

var (
	ErrInvalidInput = errors.New("invalid SQL Server inspection input")
	ErrNotFound     = errors.New("SQL Server object not found")
	ErrQueryFailed  = errors.New("SQL Server inspection failed")
	ErrTimeout      = errors.New("SQL Server inspection timed out")
	ErrCanceled     = errors.New("SQL Server inspection canceled")
)

const (
	defaultQueryTimeout = 10 * time.Second
	defaultSampleLimit  = 100
	defaultMetadataCap  = 500
	defaultPredicateCap = 8

	hardQueryTimeout = 2 * time.Minute
	hardSampleLimit  = 1000
	hardMetadataCap  = 5000
	hardPredicateCap = 16
	maxStringValue   = 4096
)

// Options cap the work each service operation can request. Zero-valued fields
// use conservative defaults; values above the hard limits are rejected.
type Options struct {
	// TargetDatabase scopes every operation to one configured restore database.
	TargetDatabase string
	QueryTimeout   time.Duration
	MaxSampleRows  int
	MaxTables      int
	MaxColumns     int
	MaxPredicates  int
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

// Operator is the small set of predicates the validation API can express.
// SQL text, expressions, joins, subqueries, and statements are not accepted.
type Operator string

const (
	Equal        Operator = "eq"
	NotEqual     Operator = "ne"
	LessThan     Operator = "lt"
	LessOrEqual  Operator = "le"
	GreaterThan  Operator = "gt"
	GreaterEqual Operator = "ge"
	Like         Operator = "like"
	NotLike      Operator = "not_like"
	IsNull       Operator = "is_null"
	IsNotNull    Operator = "is_not_null"
)

// Predicate compares a column with a parameter. Value is ignored for the
// null-check operators and required for all other operators.
type Predicate struct {
	Column   string
	Operator Operator
	Value    any
}

// ValidationQuery counts only matching rows, up to Limit+1. It deliberately
// cannot project or return row-level values.
type ValidationQuery struct {
	Table      TableRef
	Predicates []Predicate
	Limit      int
}

// ValidationResult contains only bounded aggregate information. If Truncated
// is true, ObservedRows equals Limit and the actual matching count is unknown.
type ValidationResult struct {
	ObservedRows int
	Limit        int
	Truncated    bool
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

// Service exposes only metadata inspection and bounded aggregate validation.
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

func normalizeOptions(options Options) (Options, error) {
	if options.QueryTimeout == 0 {
		options.QueryTimeout = defaultQueryTimeout
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
	if options.MaxPredicates == 0 {
		options.MaxPredicates = defaultPredicateCap
	}
	if options.QueryTimeout < time.Millisecond || options.QueryTimeout > hardQueryTimeout ||
		options.MaxSampleRows < 1 || options.MaxSampleRows > hardSampleLimit ||
		options.MaxTables < 1 || options.MaxTables > hardMetadataCap ||
		options.MaxColumns < 1 || options.MaxColumns > hardMetadataCap ||
		options.MaxPredicates < 1 || options.MaxPredicates > hardPredicateCap {
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
	if request.Limit < 1 || request.Limit > s.options.MaxSampleRows ||
		len(request.Predicates) > s.options.MaxPredicates {
		return ValidationResult{}, ErrInvalidInput
	}
	if request.Table.Database != s.options.TargetDatabase {
		return ValidationResult{}, ErrInvalidInput
	}
	if _, _, err := quoteTable(request.Table); err != nil {
		return ValidationResult{}, ErrInvalidInput
	}
	for _, predicate := range request.Predicates {
		if err := validatePredicate(predicate); err != nil {
			return ValidationResult{}, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, s.options.QueryTimeout)
	defer cancel()

	description, err := s.describeTable(ctx, request.Table)
	if err != nil {
		return ValidationResult{}, err
	}
	columns := make(map[string]struct{}, len(description.Columns))
	for _, column := range description.Columns {
		columns[column.Name] = struct{}{}
	}
	for _, predicate := range request.Predicates {
		if _, ok := columns[predicate.Column]; !ok {
			return ValidationResult{}, ErrInvalidInput
		}
	}

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
	args := make([]any, 0, 1+len(request.Predicates))
	args = append(args, request.Limit+1)
	where := make([]string, 0, len(request.Predicates))
	for _, predicate := range request.Predicates {
		column, err := quoteIdentifier(predicate.Column)
		if err != nil {
			return "", nil, ErrInvalidInput
		}
		operator, needsValue, err := sqlOperator(predicate.Operator)
		if err != nil {
			return "", nil, err
		}
		expression := "t." + column + " " + operator
		if needsValue {
			if err := validateValue(predicate.Value); err != nil {
				return "", nil, err
			}
			args = append(args, predicate.Value)
			expression += fmt.Sprintf(" @p%d", len(args))
		}
		where = append(where, expression)
	}
	query := "SELECT COUNT_BIG(*) FROM (SELECT TOP (@p1) 1 AS [sample] FROM " +
		qualifiedTable + " AS t"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += ") AS bounded_sample"
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

func sqlOperator(operator Operator) (string, bool, error) {
	switch operator {
	case Equal:
		return "=", true, nil
	case NotEqual:
		return "<>", true, nil
	case LessThan:
		return "<", true, nil
	case LessOrEqual:
		return "<=", true, nil
	case GreaterThan:
		return ">", true, nil
	case GreaterEqual:
		return ">=", true, nil
	case Like:
		return "LIKE", true, nil
	case NotLike:
		return "NOT LIKE", true, nil
	case IsNull:
		return "IS NULL", false, nil
	case IsNotNull:
		return "IS NOT NULL", false, nil
	default:
		return "", false, ErrInvalidInput
	}
}

func validatePredicate(predicate Predicate) error {
	if _, err := quoteIdentifier(predicate.Column); err != nil {
		return ErrInvalidInput
	}
	_, needsValue, err := sqlOperator(predicate.Operator)
	if err != nil {
		return err
	}
	if needsValue {
		return validateValue(predicate.Value)
	}
	return nil
}

func validateValue(value any) error {
	switch value := value.(type) {
	case string:
		if len(value) > maxStringValue {
			return ErrInvalidInput
		}
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, time.Time:
		return nil
	case float32:
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return ErrInvalidInput
		}
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return ErrInvalidInput
		}
	default:
		return ErrInvalidInput
	}
	return nil
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
