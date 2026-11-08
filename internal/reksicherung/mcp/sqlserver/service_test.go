package sqlserver

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestQuoteIdentifier(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      string
		wantError bool
	}{
		{name: "plain", input: "sales", want: "[sales]"},
		{name: "closing bracket", input: "sales]2025", want: "[sales]]2025]"},
		{name: "empty", input: "", wantError: true},
		{name: "control", input: "sales\nitems", wantError: true},
		{name: "embedded nul", input: "sales\x00items", wantError: true},
		{name: "too many UTF-16 code units", input: strings.Repeat("😀", 65), wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := quoteIdentifier(test.input)
			if (err != nil) != test.wantError {
				t.Fatalf("quoteIdentifier() error = %v, wantError %v", err, test.wantError)
			}
			if err == nil && got != test.want {
				t.Fatalf("quoteIdentifier() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBuildValidationSQLQuotesAndParameterizes(t *testing.T) {
	request := ValidationQuery{
		Table: TableRef{
			Database: "restore]db",
			Schema:   "dbo",
			Table:    "orders",
		},
		Predicates: []Predicate{
			{Column: "status", Operator: Equal, Value: "x'; DROP TABLE dbo.orders;--"},
			{Column: "deleted_at", Operator: IsNull},
		},
		Limit: 12,
	}
	query, args, err := buildValidationSQL(request)
	if err != nil {
		t.Fatalf("buildValidationSQL() error = %v", err)
	}
	for _, fragment := range []string{
		"FROM [restore]]db].[dbo].[orders] AS t",
		"t.[status] = @p2",
		"t.[deleted_at] IS NULL",
		"SELECT TOP (@p1)",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("query %q does not contain %q", query, fragment)
		}
	}
	if strings.Contains(query, "DROP TABLE") || strings.Contains(query, "x';") {
		t.Fatalf("query contains parameter data or a second statement: %q", query)
	}
	if want := []any{13, "x'; DROP TABLE dbo.orders;--"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestBuildValidationSQLRejectsUnsupportedInput(t *testing.T) {
	base := ValidationQuery{
		Table: TableRef{Database: "restore", Schema: "dbo", Table: "orders"},
		Limit: 3,
	}
	tests := []struct {
		name  string
		query ValidationQuery
	}{
		{
			name: "SQL fragment operator",
			query: withPredicate(base, Predicate{
				Column: "id", Operator: Operator("= 1; DELETE FROM orders;--"), Value: 1,
			}),
		},
		{
			name: "null value for comparison",
			query: withPredicate(base, Predicate{
				Column: "id", Operator: Equal, Value: nil,
			}),
		},
		{
			name: "unsupported value type",
			query: withPredicate(base, Predicate{
				Column: "id", Operator: Equal, Value: []byte("1"),
			}),
		},
		{
			name: "overlong string",
			query: withPredicate(base, Predicate{
				Column: "id", Operator: Equal, Value: strings.Repeat("x", maxStringValue+1),
			}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := buildValidationSQL(test.query); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("buildValidationSQL() error = %v, want ErrInvalidInput", err)
			}
		})
	}
	if _, err := New(nil, Options{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("New(nil) error = %v, want ErrInvalidInput", err)
	}
	if _, err := newService(&fakeQueryer{}, Options{
		TargetDatabase: "restore",
		MaxSampleRows:  hardSampleLimit + 1,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("newService() with excessive cap error = %v, want ErrInvalidInput", err)
	}
	if _, err := newService(&fakeQueryer{}, Options{
		TargetDatabase:   "restore",
		IntegrityTimeout: hardIntegrityTimeout + time.Millisecond,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("newService() with excessive integrity timeout error = %v, want ErrInvalidInput", err)
	}
}

func TestServiceBoundsMetadataAndReturnsAggregateOnly(t *testing.T) {
	fake := &fakeQueryer{aggregate: int64(3)}
	service, err := newService(fake, Options{
		TargetDatabase: "restore",
		MaxTables:      1,
		MaxColumns:     2,
		MaxSampleRows:  2,
	})
	if err != nil {
		t.Fatalf("newService() error = %v", err)
	}

	tables, err := service.ListTables(context.Background(), "restore", "")
	if err != nil {
		t.Fatalf("ListTables() error = %v", err)
	}
	if len(tables.Tables) != 1 || !tables.Truncated || tables.Tables[0] != (Table{Schema: "dbo", Name: "orders"}) {
		t.Fatalf("ListTables() = %#v, want one bounded table and Truncated", tables)
	}

	ref := TableRef{Database: "restore", Schema: "dbo", Table: "orders"}
	description, err := service.DescribeTable(context.Background(), ref)
	if err != nil {
		t.Fatalf("DescribeTable() error = %v", err)
	}
	if len(description.Columns) != 2 || !description.Truncated {
		t.Fatalf("DescribeTable() = %#v, want two columns and Truncated", description)
	}

	result, err := service.Validate(context.Background(), ValidationQuery{
		Table:      ref,
		Predicates: []Predicate{{Column: "status", Operator: Equal, Value: "ready"}},
		Limit:      2,
	})
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if result != (ValidationResult{ObservedRows: 2, Limit: 2, Truncated: true}) {
		t.Fatalf("Validate() = %#v, want capped aggregate result", result)
	}
	for _, call := range fake.calls {
		if strings.Contains(call.query, "SELECT *") || strings.Contains(call.query, "SELECT t.") {
			t.Errorf("query projects row data: %q", call.query)
		}
	}
	if got := fake.calls[len(fake.calls)-1].args; !reflect.DeepEqual(got, []any{3, "ready"}) {
		t.Fatalf("validation args = %#v, want limit+1 and bound predicate", got)
	}
}

func TestValidateRejectsUnknownColumnAndPredicateOverflow(t *testing.T) {
	fake := &fakeQueryer{}
	service, err := newService(fake, Options{TargetDatabase: "restore", MaxPredicates: 1})
	if err != nil {
		t.Fatalf("newService() error = %v", err)
	}
	ref := TableRef{Database: "restore", Schema: "dbo", Table: "orders"}
	_, err = service.Validate(context.Background(), ValidationQuery{
		Table: ref, Limit: 1, Predicates: []Predicate{{Column: "private", Operator: IsNotNull}},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Validate() unknown column error = %v, want ErrInvalidInput", err)
	}
	queriesAfterUnknownColumn := len(fake.calls)
	_, err = service.Validate(context.Background(), ValidationQuery{
		Table: ref,
		Limit: 1,
		Predicates: []Predicate{
			{Column: "status", Operator: IsNotNull},
			{Column: "id", Operator: IsNotNull},
		},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Validate() excess predicates error = %v, want ErrInvalidInput", err)
	}
	if len(fake.calls) != queriesAfterUnknownColumn {
		t.Fatal("excess predicates reached the database")
	}
}

func TestServiceCannotEscapeConfiguredDatabase(t *testing.T) {
	fake := &fakeQueryer{}
	service, err := newService(fake, Options{TargetDatabase: "restore"})
	if err != nil {
		t.Fatalf("newService() error = %v", err)
	}
	if _, err := service.ListTables(context.Background(), "master", "dbo"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ListTables() cross-database error = %v, want ErrInvalidInput", err)
	}
	if _, err := service.DescribeTable(context.Background(), TableRef{
		Database: "master", Schema: "dbo", Table: "users",
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("DescribeTable() cross-database error = %v, want ErrInvalidInput", err)
	}
	if len(fake.calls) != 0 {
		t.Fatal("cross-database requests reached the database")
	}
}

func TestCheckIntegrityUsesFixedTargetScopedDBCC(t *testing.T) {
	fake := &fakeQueryer{}
	service, err := newService(fake, Options{TargetDatabase: "restore]db"})
	if err != nil {
		t.Fatalf("newService() error = %v", err)
	}

	result, err := service.CheckIntegrity(context.Background())
	if err != nil {
		t.Fatalf("CheckIntegrity() error = %v", err)
	}
	if result != (IntegrityResult{Completed: true}) {
		t.Fatalf("CheckIntegrity() = %#v, want completed result", result)
	}
	if len(fake.execCalls) != 1 {
		t.Fatalf("ExecContext() calls = %d, want 1", len(fake.execCalls))
	}
	want := "DBCC CHECKDB ([restore]]db]) WITH NO_INFOMSGS, MAXDOP = 2"
	if fake.execCalls[0] != want {
		t.Fatalf("DBCC statement = %q, want %q", fake.execCalls[0], want)
	}
	if strings.Contains(strings.ToUpper(fake.execCalls[0]), "REPAIR") {
		t.Fatalf("DBCC statement contains a repair option: %q", fake.execCalls[0])
	}
}

func TestCheckIntegrityRejectsNilContextBeforeExecution(t *testing.T) {
	fake := &fakeQueryer{}
	service, err := newService(fake, Options{TargetDatabase: "restore"})
	if err != nil {
		t.Fatalf("newService() error = %v", err)
	}
	if _, err := service.CheckIntegrity(nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CheckIntegrity(nil) error = %v, want ErrInvalidInput", err)
	}
	if len(fake.execCalls) != 0 {
		t.Fatal("invalid target reached the database")
	}
}

func TestCheckIntegrityClassifiesTimeoutAndSanitizesErrors(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		fake := &blockingExecQueryer{}
		service, err := newService(fake, Options{
			TargetDatabase:   "restore",
			IntegrityTimeout: time.Millisecond,
		})
		if err != nil {
			t.Fatalf("newService() error = %v", err)
		}
		_, err = service.CheckIntegrity(context.Background())
		if !errors.Is(err, ErrIntegrityCheckTimeout) {
			t.Fatalf("CheckIntegrity() error = %v, want ErrIntegrityCheckTimeout", err)
		}
	})

	t.Run("database error", func(t *testing.T) {
		fake := &fakeQueryer{execErr: errors.New("password=do-not-return")}
		service, err := newService(fake, Options{TargetDatabase: "restore"})
		if err != nil {
			t.Fatalf("newService() error = %v", err)
		}
		_, err = service.CheckIntegrity(context.Background())
		if !errors.Is(err, ErrIntegrityCheckFailed) || strings.Contains(err.Error(), "do-not-return") {
			t.Fatalf("CheckIntegrity() error = %v, want generic sanitized integrity error", err)
		}
	})
}

func TestDatabaseErrorsAreRedactedAndDeadlinesClassified(t *testing.T) {
	fake := &fakeQueryer{queryErr: errors.New("login failed: password=not-for-output")}
	service, err := newService(fake, Options{TargetDatabase: "restore"})
	if err != nil {
		t.Fatalf("newService() error = %v", err)
	}
	_, err = service.ListTables(context.Background(), "restore", "")
	if !errors.Is(err, ErrQueryFailed) || strings.Contains(err.Error(), "not-for-output") {
		t.Fatalf("ListTables() error = %v, want generic redacted ErrQueryFailed", err)
	}

	fake.queryErr = context.DeadlineExceeded
	_, err = service.ListTables(context.Background(), "restore", "")
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("ListTables() deadline error = %v, want ErrTimeout", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake.queryErr = nil
	_, err = service.ListTables(ctx, "restore", "")
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("ListTables() cancellation error = %v, want ErrCanceled", err)
	}
}

func withPredicate(query ValidationQuery, predicate Predicate) ValidationQuery {
	query.Predicates = []Predicate{predicate}
	return query
}

type queryCall struct {
	query string
	args  []any
}

type fakeQueryer struct {
	calls     []queryCall
	aggregate int64
	queryErr  error
	execCalls []string
	execErr   error
}

func (fake *fakeQueryer) QueryContext(ctx context.Context, query string, args ...any) (rows, error) {
	fake.calls = append(fake.calls, queryCall{query: query, args: append([]any(nil), args...)})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if fake.queryErr != nil {
		return nil, fake.queryErr
	}
	switch {
	case strings.Contains(query, "SELECT TOP (@p1) s.name"):
		return &fakeRows{values: [][]any{
			{"dbo", "orders"},
			{"archive", "old_orders"},
		}}, nil
	case strings.Contains(query, "sys.columns"):
		return &fakeRows{values: [][]any{
			{"id", "int", int64(4), int64(10), int64(0), false},
			{"status", "nvarchar", int64(80), int64(0), int64(0), true},
			{"created_at", "datetime2", int64(8), int64(27), int64(7), false},
		}}, nil
	case strings.HasPrefix(query, "SELECT COUNT_BIG(*)"):
		return &fakeRows{values: [][]any{{fake.aggregate}}}, nil
	default:
		return nil, errors.New("unexpected test query")
	}
}

func (fake *fakeQueryer) ExecContext(ctx context.Context, query string, _ ...any) (sql.Result, error) {
	fake.execCalls = append(fake.execCalls, query)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, fake.execErr
}

type blockingExecQueryer struct{}

func (*blockingExecQueryer) QueryContext(context.Context, string, ...any) (rows, error) {
	return nil, errors.New("unexpected query")
}

func (*blockingExecQueryer) ExecContext(ctx context.Context, _ string, _ ...any) (sql.Result, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type fakeRows struct {
	values [][]any
	index  int
	closed bool
}

func (rows *fakeRows) Next() bool {
	return rows.index < len(rows.values)
}

func (rows *fakeRows) Scan(destinations ...any) error {
	if rows.index >= len(rows.values) {
		return errors.New("no current row")
	}
	values := rows.values[rows.index]
	if len(values) != len(destinations) {
		return errors.New("wrong scan arity")
	}
	for index, value := range values {
		destination := reflect.ValueOf(destinations[index])
		if destination.Kind() != reflect.Pointer || destination.IsNil() {
			return errors.New("destination is not a pointer")
		}
		valueRef := reflect.ValueOf(value)
		if valueRef.Type().ConvertibleTo(destination.Elem().Type()) {
			destination.Elem().Set(valueRef.Convert(destination.Elem().Type()))
		} else {
			return errors.New("incompatible scan value")
		}
	}
	rows.index++
	return nil
}

func (rows *fakeRows) Err() error { return nil }

func (rows *fakeRows) Close() error {
	rows.closed = true
	return nil
}
