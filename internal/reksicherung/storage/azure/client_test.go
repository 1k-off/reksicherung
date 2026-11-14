package azure

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/storage"
)

type fakeBackend struct {
	items   []blobItem
	next    string
	listErr error
	bodies  map[string][]byte
	getErr  error
	stats   map[string]blobItem
	statErr error
}

func (f *fakeBackend) List(context.Context, string, string, int32) ([]blobItem, string, error) {
	if f.listErr != nil {
		return nil, "", f.listErr
	}
	return append([]blobItem(nil), f.items...), f.next, nil
}

func (f *fakeBackend) Download(_ context.Context, key string) (io.ReadCloser, int64, error) {
	if f.getErr != nil {
		return nil, 0, f.getErr
	}
	body, ok := f.bodies[key]
	if !ok {
		return nil, 0, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(body)), int64(len(body)), nil
}

func (f *fakeBackend) Stat(_ context.Context, key string) (int64, time.Time, error) {
	if f.statErr != nil {
		return 0, time.Time{}, f.statErr
	}
	item, ok := f.stats[key]
	if !ok {
		return 0, time.Time{}, errors.New("not found")
	}
	return item.Size, item.LastModified, nil
}

func TestClientListGetStat(t *testing.T) {
	mod := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	backend := &fakeBackend{
		items: []blobItem{{Key: "shop/a.bak", Size: 12, LastModified: mod}},
		bodies: map[string][]byte{
			"shop/a.bak": []byte("hello-backup"),
		},
		stats: map[string]blobItem{
			"shop/a.bak": {Key: "shop/a.bak", Size: 12, LastModified: mod},
		},
	}
	client, err := newClient(backend, 0, 0)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}

	page, err := client.List(context.Background(), storage.ListOptions{Prefix: "shop/", Limit: 10})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page.Objects) != 1 || page.Objects[0].Key != "shop/a.bak" {
		t.Fatalf("List() = %#v", page)
	}

	reader, err := client.Get(context.Background(), "shop/a.bak")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil || string(got) != "hello-backup" {
		t.Fatalf("Get body = %q err=%v", got, err)
	}

	stat, err := client.Stat(context.Background(), "shop/a.bak")
	if err != nil || stat.Size != 12 {
		t.Fatalf("Stat() = %#v err=%v", stat, err)
	}
}

func TestClientRejectsUnsafeInput(t *testing.T) {
	client, err := newClient(&fakeBackend{}, 0, 0)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}
	if _, err := client.List(context.Background(), storage.ListOptions{Prefix: "../x", Limit: 1}); !errors.Is(err, storage.ErrInvalidInput) {
		t.Fatalf("List traversal error = %v", err)
	}
	if _, err := client.Get(context.Background(), "/abs"); !errors.Is(err, storage.ErrInvalidInput) {
		t.Fatalf("Get abs error = %v", err)
	}
}

func TestNewRejectsMixedCredentials(t *testing.T) {
	_, err := New(Config{
		AccountName:  "mystorageacct",
		Container:    "backups",
		AccountKey:   "key",
		TenantID:     "tenant",
		ClientID:     "client",
		ClientSecret: "secret",
	})
	if err == nil {
		t.Fatal("New() accepted mixed credentials")
	}
	if !strings.Contains(err.Error(), "invalid input") && !errors.Is(err, storage.ErrInvalidInput) {
		t.Fatalf("error = %v", err)
	}
}
