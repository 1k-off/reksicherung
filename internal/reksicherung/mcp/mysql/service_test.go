package mysql

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

const runDB = "reksicherung_0123456789abcdef0123456789abcdef"

func TestQuoteIdentifier(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      string
		wantError bool
	}{
		{name: "plain", input: "sales", want: "`sales`"},
		{name: "backtick doubled", input: "sales`2025", want: "`sales``2025`"},
		{name: "injection stays quoted", input: "x`; DROP TABLE y; --", want: "`x``; DROP TABLE y; --`"},
		{name: "empty", input: "", wantError: true},
		{name: "control", input: "sales\nitems", wantError: true},
		{name: "embedded nul", input: "sales\x00items", wantError: true},
		{name: "too long", input: strings.Repeat("a", 65), wantError: true},
		{name: "max length", input: strings.Repeat("a", 64), want: "`" + strings.Repeat("a", 64) + "`"},
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

func TestBuildValidationSQLCountsRowsWithoutCallerPredicates(t *testing.T) {
	request := ValidationQuery{
		Table: TableRef{Database: "restore`db", Schema: "restore`db", Table: "orders"},
		Limit: 12,
	}
	query, args, err := buildValidationSQL(request)
	if err != nil {
		t.Fatalf("buildValidationSQL() error = %v", err)
	}
	for _, fragment := range []string{
		"SELECT COUNT(*) FROM (SELECT 1 FROM `restore``db`.`orders` LIMIT ?) AS bounded_sample",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("query %q does not contain %q", query, fragment)
		}
	}
	if strings.Contains(query, "WHERE") || strings.Contains(query, ";") {
		t.Fatalf("query contains a predicate or a second statement: %q", query)
	}
	if want := []any{13}; !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want limit+1", args)
	}
}

func TestBuildValidationSQLRejectsInvalidTable(t *testing.T) {
	base := ValidationQuery{Table: TableRef{Database: "restore", Schema: "restore", Table: ""}, Limit: 3}
	if _, _, err := buildValidationSQL(base); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("buildValidationSQL() error = %v, want ErrInvalidInput", err)
	}
}

func TestNewValidatesInputs(t *testing.T) {
	if _, err := New(nil, Options{TargetDatabase: "restore"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("New(nil) error = %v, want ErrInvalidInput", err)
	}
	if _, err := newService(&fakeQueryer{}, Options{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("newService() without target error = %v, want ErrInvalidInput", err)
	}
	if _, err := newService(&fakeQueryer{}, Options{TargetDatabase: "restore", MaxSampleRows: hardSampleLimit + 1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("excessive cap error = %v, want ErrInvalidInput", err)
	}
	if _, err := newService(&fakeQueryer{}, Options{TargetDatabase: "restore", IntegrityTimeout: hardIntegrityTimeout + time.Millisecond}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("excessive integrity timeout error = %v, want ErrInvalidInput", err)
	}
}

func TestServiceBoundsMetadataAndReturnsAggregateOnly(t *testing.T) {
	fake := &fakeQueryer{aggregate: int64(3)}
	service, err := newService(fake, Options{TargetDatabase: "restore", MaxTables: 1, MaxColumns: 2, MaxSampleRows: 2})
	if err != nil {
		t.Fatalf("newService() error = %v", err)
	}

	tables, err := service.ListTables(context.Background(), "restore", "")
	if err != nil {
		t.Fatalf("ListTables() error = %v", err)
	}
	if len(tables.Tables) != 1 || !tables.Truncated || tables.Tables[0] != (Table{Schema: "restore", Name: "orders"}) {
		t.Fatalf("ListTables() = %#v, want one bounded table and Truncated", tables)
	}
	if got := fake.calls[0].args; !reflect.DeepEqual(got, []any{"restore", 2}) {
		t.Fatalf("list args = %#v, want database and cap+1", got)
	}
	if !strings.Contains(fake.calls[0].query, "information_schema.tables") {
		t.Fatalf("list query = %q, want information_schema.tables", fake.calls[0].query)
	}

	ref := TableRef{Database: "restore", Table: "orders"}
	description, err := service.DescribeTable(context.Background(), ref)
	if err != nil {
		t.Fatalf("DescribeTable() error = %v", err)
	}
	if len(description.Columns) != 2 || !description.Truncated {
		t.Fatalf("DescribeTable() = %#v, want two columns and Truncated", description)
	}
	if description.Columns[0] != (Column{Name: "id", DataType: "int", Precision: 10, Nullable: false}) ||
		!description.Columns[1].Nullable || description.Columns[1].MaxLength != 80 {
		t.Fatalf("columns = %#v", description.Columns)
	}
	if got := fake.calls[1].args; !reflect.DeepEqual(got, []any{"restore", "orders", 3}) {
		t.Fatalf("describe args = %#v", got)
	}
	if !strings.Contains(fake.calls[1].query, "information_schema.columns") {
		t.Fatalf("describe query = %q, want information_schema.columns", fake.calls[1].query)
	}

	result, err := service.Validate(context.Background(), ValidationQuery{Table: ref, Limit: 2})
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
	if got := fake.calls[len(fake.calls)-1].args; !reflect.DeepEqual(got, []any{3}) {
		t.Fatalf("validation args = %#v, want only limit+1", got)
	}
}

func TestValidateUntruncatedCount(t *testing.T) {
	fake := &fakeQueryer{aggregate: int64(1)}
	service, _ := newService(fake, Options{TargetDatabase: "restore"})
	result, err := service.Validate(context.Background(), ValidationQuery{
		Table: TableRef{Database: "restore", Table: "orders"}, Limit: 5,
	})
	if err != nil || result != (ValidationResult{ObservedRows: 1, Limit: 5}) {
		t.Fatalf("Validate() = %#v, %v", result, err)
	}
}

func TestSchemaDefaultsToDatabaseAndMustMatch(t *testing.T) {
	fake := &fakeQueryer{}
	service, _ := newService(fake, Options{TargetDatabase: "restore"})
	if _, err := service.ListTables(context.Background(), "restore", "restore"); err != nil {
		t.Fatalf("ListTables() with schema == database error = %v", err)
	}
	if _, err := service.ListTables(context.Background(), "restore", "other"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ListTables() mismatching schema error = %v, want ErrInvalidInput", err)
	}
	if _, err := service.DescribeTable(context.Background(), TableRef{Database: "restore", Schema: "other", Table: "t"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("DescribeTable() mismatching schema error = %v, want ErrInvalidInput", err)
	}
	if _, err := service.Validate(context.Background(), ValidationQuery{
		Table: TableRef{Database: "restore", Schema: "other", Table: "t"}, Limit: 1,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Validate() mismatching schema error = %v, want ErrInvalidInput", err)
	}
}

func TestDescribeTableNotFound(t *testing.T) {
	fake := &fakeQueryer{noColumns: true}
	service, _ := newService(fake, Options{TargetDatabase: "restore"})
	_, err := service.DescribeTable(context.Background(), TableRef{Database: "restore", Table: "missing"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("DescribeTable() error = %v, want ErrNotFound", err)
	}
}

func TestValidateRejectsCrossDatabaseAndInvalidLimitBeforeQuery(t *testing.T) {
	fake := &fakeQueryer{}
	service, err := newService(fake, Options{TargetDatabase: "restore"})
	if err != nil {
		t.Fatalf("newService() error = %v", err)
	}
	_, err = service.Validate(context.Background(), ValidationQuery{
		Table: TableRef{Database: "mysql", Table: "user"}, Limit: 1,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Validate() cross-database error = %v, want ErrInvalidInput", err)
	}
	for _, limit := range []int{0, -1, defaultSampleLimit + 1} {
		_, err = service.Validate(context.Background(), ValidationQuery{
			Table: TableRef{Database: "restore", Table: "orders"}, Limit: limit,
		})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Validate() limit %d error = %v, want ErrInvalidInput", limit, err)
		}
	}
	if len(fake.calls) != 0 {
		t.Fatal("invalid validation request reached the database")
	}
}

func TestServiceCannotEscapeConfiguredDatabase(t *testing.T) {
	fake := &fakeQueryer{}
	service, err := newService(fake, Options{TargetDatabase: "restore"})
	if err != nil {
		t.Fatalf("newService() error = %v", err)
	}
	if _, err := service.ListTables(context.Background(), "mysql", ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ListTables() cross-database error = %v, want ErrInvalidInput", err)
	}
	if _, err := service.DescribeTable(context.Background(), TableRef{Database: "mysql", Table: "user"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("DescribeTable() cross-database error = %v, want ErrInvalidInput", err)
	}
	if len(fake.calls) != 0 {
		t.Fatal("cross-database requests reached the database")
	}
}

func TestNilContextRejected(t *testing.T) {
	service, _ := newService(&fakeQueryer{}, Options{TargetDatabase: "restore"})
	//nolint:staticcheck // deliberately nil
	if _, err := service.ListTables(nil, "restore", ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ListTables(nil) error = %v", err)
	}
	//nolint:staticcheck // deliberately nil
	if _, err := service.CheckIntegrity(nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CheckIntegrity(nil) error = %v", err)
	}
}

func TestForDatabase(t *testing.T) {
	fake := &fakeQueryer{}
	anchored, err := newService(fake, Options{TargetDatabase: AnchorDatabase})
	if err != nil {
		t.Fatalf("newService() error = %v", err)
	}
	scoped, err := anchored.ForDatabase(runDB)
	if err != nil {
		t.Fatalf("ForDatabase(run db) error = %v", err)
	}
	if _, err := scoped.ListTables(context.Background(), runDB, ""); err != nil {
		t.Fatalf("scoped ListTables() error = %v", err)
	}
	if _, err := anchored.ForDatabase("mysql"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ForDatabase(mysql) error = %v, want ErrInvalidInput", err)
	}
	if _, err := anchored.ForDatabase("reksicherung_XYZ"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ForDatabase(non-hex) error = %v, want ErrInvalidInput", err)
	}
	// The anchor itself is not an inspectable target.
	before := len(fake.calls)
	if _, err := anchored.ListTables(context.Background(), AnchorDatabase, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ListTables(anchor) error = %v, want ErrInvalidInput", err)
	}
	if _, err := anchored.CheckIntegrity(context.Background()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CheckIntegrity(anchor) error = %v, want ErrInvalidInput", err)
	}
	if len(fake.calls) != before {
		t.Fatal("anchor requests reached the database")
	}

	fixed, _ := newService(fake, Options{TargetDatabase: "restore"})
	if same, err := fixed.ForDatabase("restore"); err != nil || same != fixed {
		t.Fatalf("ForDatabase(same) = %v, %v", same, err)
	}
	if _, err := fixed.ForDatabase(runDB); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("fixed ForDatabase(run db) error = %v, want ErrInvalidInput", err)
	}
	var nilService *Service
	if _, err := nilService.ForDatabase("x"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil ForDatabase error = %v", err)
	}
}

func TestCheckIntegrityUsesFixedCheckTableOnListedTables(t *testing.T) {
	fake := &fakeQueryer{}
	service, err := newService(fake, Options{TargetDatabase: "restore`db"})
	if err != nil {
		t.Fatalf("newService() error = %v", err)
	}
	result, err := service.CheckIntegrity(context.Background())
	if err != nil {
		t.Fatalf("CheckIntegrity() error = %v", err)
	}
	if result != (IntegrityResult{CommandCompleted: true}) {
		t.Fatalf("CheckIntegrity() = %#v, want completed over two tables", result)
	}
	var checks []string
	for _, call := range fake.calls {
		if strings.HasPrefix(call.query, "CHECK TABLE") {
			checks = append(checks, call.query)
			if len(call.args) != 0 {
				t.Errorf("CHECK TABLE received args: %#v", call.args)
			}
		}
	}
	want := []string{
		"CHECK TABLE `restore``db`.`orders`",
		"CHECK TABLE `restore``db`.`old_orders`",
	}
	if !reflect.DeepEqual(checks, want) {
		t.Fatalf("CHECK TABLE statements = %q, want %q", checks, want)
	}
	for _, statement := range checks {
		upper := strings.ToUpper(statement)
		for _, forbidden := range []string{"REPAIR", "OPTIMIZE", "ANALYZE", "FOR UPGRADE", "CHANGED"} {
			if strings.Contains(upper, forbidden) {
				t.Errorf("statement contains %s: %q", forbidden, statement)
			}
		}
	}
}

func TestCheckIntegrityReportsTruncationAsIncomplete(t *testing.T) {
	fake := &fakeQueryer{}
	service, _ := newService(fake, Options{TargetDatabase: "restore", MaxTables: 1})
	result, err := service.CheckIntegrity(context.Background())
	if err != nil {
		t.Fatalf("CheckIntegrity() error = %v", err)
	}
	if result != (IntegrityResult{CommandCompleted: false}) {
		t.Fatalf("CheckIntegrity() = %#v, want incomplete result", result)
	}
}

func TestCheckIntegrityDoesNotInterpretCheckTableMessages(t *testing.T) {
	// CHECK TABLE reports problems as result rows, not command errors. The
	// service must discard them and report completion only.
	fake := &fakeQueryer{checkRows: [][]any{{"restore.orders", "check", "error", "Corrupt"}}}
	service, _ := newService(fake, Options{TargetDatabase: "restore"})
	result, err := service.CheckIntegrity(context.Background())
	if err != nil || !result.CommandCompleted {
		t.Fatalf("CheckIntegrity() = %#v, %v", result, err)
	}
}

func TestCheckIntegrityClassifiesTimeoutAndSanitizesErrors(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		service, err := newService(&blockingQueryer{}, Options{TargetDatabase: "restore", IntegrityTimeout: time.Millisecond})
		if err != nil {
			t.Fatalf("newService() error = %v", err)
		}
		if _, err := service.CheckIntegrity(context.Background()); !errors.Is(err, ErrIntegrityCheckTimeout) {
			t.Fatalf("CheckIntegrity() error = %v, want ErrIntegrityCheckTimeout", err)
		}
	})
	t.Run("database error", func(t *testing.T) {
		fake := &fakeQueryer{queryErr: errors.New("password=do-not-return")}
		service, _ := newService(fake, Options{TargetDatabase: "restore"})
		_, err := service.CheckIntegrity(context.Background())
		if !errors.Is(err, ErrIntegrityCheckFailed) || strings.Contains(err.Error(), "do-not-return") {
			t.Fatalf("CheckIntegrity() error = %v, want generic sanitized integrity error", err)
		}
	})
	t.Run("check statement error", func(t *testing.T) {
		fake := &fakeQueryer{checkErr: errors.New("password=do-not-return")}
		service, _ := newService(fake, Options{TargetDatabase: "restore"})
		_, err := service.CheckIntegrity(context.Background())
		if !errors.Is(err, ErrIntegrityCheckFailed) || strings.Contains(err.Error(), "do-not-return") {
			t.Fatalf("CheckIntegrity() error = %v, want generic sanitized integrity error", err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		service, _ := newService(&fakeQueryer{}, Options{TargetDatabase: "restore"})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := service.CheckIntegrity(ctx); !errors.Is(err, ErrIntegrityCheckCanceled) {
			t.Fatalf("CheckIntegrity() error = %v, want ErrIntegrityCheckCanceled", err)
		}
	})
}

func TestDatabaseErrorsAreRedactedAndDeadlinesClassified(t *testing.T) {
	fake := &fakeQueryer{queryErr: errors.New("access denied: password=not-for-output")}
	service, _ := newService(fake, Options{TargetDatabase: "restore"})
	_, err := service.ListTables(context.Background(), "restore", "")
	if !errors.Is(err, ErrQueryFailed) || strings.Contains(err.Error(), "not-for-output") {
		t.Fatalf("ListTables() error = %v, want generic redacted ErrQueryFailed", err)
	}

	fake.queryErr = context.DeadlineExceeded
	if _, err := service.ListTables(context.Background(), "restore", ""); !errors.Is(err, ErrTimeout) {
		t.Fatalf("ListTables() deadline error = %v, want ErrTimeout", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake.queryErr = nil
	if _, err := service.ListTables(ctx, "restore", ""); !errors.Is(err, ErrCanceled) {
		t.Fatalf("ListTables() cancellation error = %v, want ErrCanceled", err)
	}
}

type queryCall struct {
	query string
	args  []any
}

type fakeQueryer struct {
	calls     []queryCall
	aggregate int64
	queryErr  error
	noColumns bool
	checkErr  error
	checkRows [][]any
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
	case strings.Contains(query, "information_schema.tables") && !strings.Contains(query, "information_schema.columns"):
		return &fakeRows{values: [][]any{
			{"restore", "orders"},
			{"restore", "old_orders"},
		}}, nil
	case strings.Contains(query, "information_schema.columns"):
		if fake.noColumns {
			return &fakeRows{}, nil
		}
		return &fakeRows{values: [][]any{
			{"id", "int", int64(0), int64(10), int64(0), "NO"},
			{"status", "varchar", int64(80), int64(0), int64(0), "YES"},
			{"created_at", "datetime", int64(0), int64(0), int64(0), "NO"},
		}}, nil
	case strings.HasPrefix(query, "SELECT COUNT(*)"):
		return &fakeRows{values: [][]any{{fake.aggregate}}}, nil
	case strings.HasPrefix(query, "CHECK TABLE"):
		if fake.checkErr != nil {
			return nil, fake.checkErr
		}
		return &fakeRows{values: fake.checkRows}, nil
	default:
		return nil, errors.New("unexpected test query")
	}
}

type blockingQueryer struct{}

func (*blockingQueryer) QueryContext(ctx context.Context, _ string, _ ...any) (rows, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type fakeRows struct {
	values [][]any
	index  int
	// current is the row selected by the last Next call.
	started bool
}

func (rows *fakeRows) Next() bool {
	if rows.started {
		rows.index++
	}
	rows.started = true
	return rows.index < len(rows.values)
}

func (rows *fakeRows) Scan(destinations ...any) error {
	if !rows.started || rows.index >= len(rows.values) {
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
	return nil
}

func (rows *fakeRows) Err() error   { return nil }
func (rows *fakeRows) Close() error { return nil }
