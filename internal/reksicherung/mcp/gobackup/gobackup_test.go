package gobackup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/storage"
)

const testPrefix = "prod/sqlserver/"

type fakeStore struct {
	pages     map[string]storage.ListPage
	listCalls []storage.ListOptions
	listFunc  func(context.Context, storage.ListOptions) (storage.ListPage, error)
	statFunc  func(context.Context, string) (storage.Object, error)
	getFunc   func(context.Context, string) (io.ReadCloser, error)
}

func (store *fakeStore) List(ctx context.Context, options storage.ListOptions) (storage.ListPage, error) {
	store.listCalls = append(store.listCalls, options)
	if store.listFunc != nil {
		return store.listFunc(ctx, options)
	}
	return store.pages[options.ContinuationToken], nil
}

func (store *fakeStore) Stat(ctx context.Context, key string) (storage.Object, error) {
	if store.statFunc == nil {
		return storage.Object{Key: key, Size: 5}, nil
	}
	return store.statFunc(ctx, key)
}

func (store *fakeStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if store.getFunc == nil {
		return io.NopCloser(bytes.NewBufferString("data")), nil
	}
	return store.getFunc(ctx, key)
}

func newTestService(t *testing.T, store storage.Store, config Config) *Service {
	t.Helper()
	if config.Prefix == "" {
		config.Prefix = testPrefix
	}
	service, err := New(store, config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return service
}

func TestListAndLatestOrderDeterministicallyAndSkipDirectoryMarkers(t *testing.T) {
	timestamp := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	store := &fakeStore{pages: map[string]storage.ListPage{
		"": {Objects: []storage.Object{
			{Key: testPrefix + "z.bacpac", Size: 9, LastModified: timestamp},
			{Key: testPrefix + "directory/", Size: 0, LastModified: timestamp.Add(time.Hour)},
			{Key: testPrefix + "a.bacpac", Size: 7, LastModified: timestamp},
			{Key: testPrefix + "older.bacpac", Size: 3, LastModified: timestamp.Add(-time.Hour)},
		}},
	}}
	service := newTestService(t, store, Config{})

	backups, err := service.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	gotIDs := []string{backups[0].ID, backups[1].ID, backups[2].ID}
	wantIDs := []string{"a.bacpac", "z.bacpac", "older.bacpac"}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("List() IDs = %v, want %v", gotIDs, wantIDs)
	}
	latest, err := service.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if latest.ID != "a.bacpac" {
		t.Fatalf("Latest().ID = %q, want a.bacpac", latest.ID)
	}
	if len(store.listCalls) != 2 || store.listCalls[0].Prefix != testPrefix || store.listCalls[0].Limit != defaultPageSize {
		t.Fatalf("List options = %#v", store.listCalls)
	}
}

func TestListPaginationAndLimits(t *testing.T) {
	store := &fakeStore{pages: map[string]storage.ListPage{
		"":     {Objects: []storage.Object{{Key: testPrefix + "one", Size: 1}}, ContinuationToken: "next"},
		"next": {Objects: []storage.Object{{Key: testPrefix + "two", Size: 2}}},
	}}
	service := newTestService(t, store, Config{PageSize: 1})
	backups, err := service.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(backups) != 2 || store.listCalls[1].ContinuationToken != "next" {
		t.Fatalf("List() = %#v, calls = %#v", backups, store.listCalls)
	}

	looping := &fakeStore{listFunc: func(_ context.Context, _ storage.ListOptions) (storage.ListPage, error) {
		return storage.ListPage{ContinuationToken: "repeat"}, nil
	}}
	loopService := newTestService(t, looping, Config{})
	if _, err := loopService.List(context.Background()); !errors.Is(err, ErrPaginationLoop) {
		t.Fatalf("List() loop error = %v, want ErrPaginationLoop", err)
	}

	capped := &fakeStore{pages: map[string]storage.ListPage{
		"":     {Objects: []storage.Object{{Key: testPrefix + "one", Size: 1}}, ContinuationToken: "next"},
		"next": {Objects: []storage.Object{{Key: testPrefix + "two", Size: 2}}},
	}}
	cappedService := newTestService(t, capped, Config{PageSize: 1, MaxBackups: 1})
	if _, err := cappedService.List(context.Background()); !errors.Is(err, ErrBackupLimit) {
		t.Fatalf("List() backup cap error = %v, want ErrBackupLimit", err)
	}

	pageCapped := newTestService(t, &fakeStore{listFunc: func(_ context.Context, _ storage.ListOptions) (storage.ListPage, error) {
		return storage.ListPage{ContinuationToken: "more"}, nil
	}}, Config{MaxPages: 1})
	if _, err := pageCapped.List(context.Background()); !errors.Is(err, ErrBackupLimit) {
		t.Fatalf("List() page cap error = %v, want ErrBackupLimit", err)
	}
}

func TestListRejectsDuplicateAndUnsafeKeys(t *testing.T) {
	tests := []struct {
		name    string
		objects []storage.Object
		pages   map[string]storage.ListPage
	}{
		{
			name: "duplicate",
			pages: map[string]storage.ListPage{
				"":     {Objects: []storage.Object{{Key: testPrefix + "same", Size: 1}}, ContinuationToken: "next"},
				"next": {Objects: []storage.Object{{Key: testPrefix + "same", Size: 1}}},
			},
		},
		{name: "traversal", objects: []storage.Object{{Key: testPrefix + "../outside", Size: 1}}},
		{name: "outside prefix", objects: []storage.Object{{Key: "other/backup", Size: 1}}},
		{name: "control input", objects: []storage.Object{{Key: testPrefix + "bad\nkey", Size: 1}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{pages: test.pages}
			if test.pages == nil {
				store.pages = map[string]storage.ListPage{"": {Objects: test.objects}}
			}
			if _, err := newTestService(t, store, Config{}).List(context.Background()); err == nil {
				t.Fatal("List() error = nil, want invalid listing error")
			}
		})
	}
}

func TestSelectRejectsUnsafeIDsAndReturnsMetadata(t *testing.T) {
	store := &fakeStore{statFunc: func(_ context.Context, key string) (storage.Object, error) {
		if key != testPrefix+"nested/archive.bacpac" {
			t.Errorf("Stat key = %q", key)
		}
		return storage.Object{Key: key, Size: 42, LastModified: time.Unix(10, 0)}, nil
	}}
	service := newTestService(t, store, Config{})
	selected, err := service.Select(context.Background(), "nested/archive.bacpac")
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selected.ID != "nested/archive.bacpac" || selected.Size != 42 {
		t.Fatalf("Select() = %#v", selected)
	}
	for _, id := range []string{"", "../outside", "/absolute", "C:/absolute", "a/../b", "a//b", "a\\b", "a/\x00b", "directory/"} {
		if _, err := service.Select(context.Background(), id); !errors.Is(err, ErrInvalidBackupID) {
			t.Errorf("Select(%q) error = %v, want ErrInvalidBackupID", id, err)
		}
	}
}

func TestOpenStreamsExactObjectWithByteLimit(t *testing.T) {
	store := &fakeStore{getFunc: func(_ context.Context, key string) (io.ReadCloser, error) {
		if key != testPrefix+"archive.bacpac" {
			t.Errorf("Get key = %q", key)
		}
		return io.NopCloser(bytes.NewBufferString("12345")), nil
	}}
	service := newTestService(t, store, Config{MaxObjectBytes: 4})
	reader, err := service.Open(context.Background(), "archive.bacpac")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if !errors.Is(err, ErrObjectTooLarge) {
		t.Fatalf("ReadAll() error = %v, want ErrObjectTooLarge", err)
	}
	if string(body) != "1234" {
		t.Fatalf("ReadAll() = %q, want bounded prefix", body)
	}
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, ErrObjectTooLarge) {
		t.Fatalf("Read() after size error = %v, want ErrObjectTooLarge", err)
	}
}

func TestStoreErrorsAndCancellationPropagate(t *testing.T) {
	storeErr := errors.New("store failed")
	store := &fakeStore{listFunc: func(_ context.Context, _ storage.ListOptions) (storage.ListPage, error) {
		return storage.ListPage{}, storeErr
	}}
	service := newTestService(t, store, Config{})
	if _, err := service.List(context.Background()); !errors.Is(err, storeErr) {
		t.Fatalf("List() error = %v, want wrapped store error", err)
	}

	service = newTestService(t, &fakeStore{
		statFunc: func(context.Context, string) (storage.Object, error) {
			return storage.Object{}, storeErr
		},
		getFunc: func(context.Context, string) (io.ReadCloser, error) {
			return nil, storeErr
		},
	}, Config{})
	if _, err := service.Select(context.Background(), "archive"); !errors.Is(err, storeErr) {
		t.Fatalf("Select() error = %v, want wrapped store error", err)
	}
	if _, err := service.Open(context.Background(), "archive"); !errors.Is(err, storeErr) {
		t.Fatalf("Open() error = %v, want wrapped store error", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	beforeCalls := len(store.listCalls)
	if _, err := newTestService(t, store, Config{}).List(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("List(canceled) error = %v, want context.Canceled", err)
	}
	if len(store.listCalls) != beforeCalls {
		t.Fatal("List(canceled) called storage")
	}

	callContext, cancelCall := context.WithCancel(context.Background())
	canceledDuringCall := &fakeStore{listFunc: func(_ context.Context, _ storage.ListOptions) (storage.ListPage, error) {
		cancelCall()
		return storage.ListPage{}, nil
	}}
	if _, err := newTestService(t, canceledDuringCall, Config{}).List(callContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("List() cancellation error = %v, want context.Canceled", err)
	}

	ctx, cancelRead := context.WithCancel(context.Background())
	reader, err := newTestService(t, &fakeStore{getFunc: func(context.Context, string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewBufferString("data")), nil
	}}, Config{}).Open(ctx, "archive")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	cancelRead()
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Read(canceled) error = %v, want context.Canceled", err)
	}
	_ = reader.Close()
}

func TestNewRequiresSafeExplicitPrefixAndBoundedLimits(t *testing.T) {
	store := &fakeStore{}
	for _, config := range []Config{
		{},
		{Prefix: "/absolute/"},
		{Prefix: "C:/absolute/"},
		{Prefix: "unsafe/../prefix/"},
		{Prefix: "missing-slash"},
		{Prefix: "safe/", PageSize: maxPageSize + 1},
		{Prefix: "safe/", MaxBackups: maxBackups + 1},
		{Prefix: "safe/", MaxPages: maxPages + 1},
		{Prefix: "safe/", MaxObjectBytes: -1},
	} {
		if _, err := New(store, config); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("New(%#v) error = %v, want ErrInvalidConfig", config, err)
		}
	}
}
