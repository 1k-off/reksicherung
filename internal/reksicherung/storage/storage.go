// Package storage defines provider-neutral access to backup objects.
package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	// ErrInvalidInput indicates that an operation received an unsafe or
	// unsupported key, prefix, token, or limit.
	ErrInvalidInput = errors.New("storage: invalid input")
	// ErrObjectNotFound indicates that the requested object does not exist.
	ErrObjectNotFound = errors.New("storage: object not found")
	// ErrObjectTooLarge indicates that an object exceeds a configured download
	// limit.
	ErrObjectTooLarge = errors.New("storage: object exceeds size limit")
)

// Object contains provider-neutral metadata for one stored object.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// ListOptions bounds a single listing page. ContinuationToken is opaque and
// should be passed back unchanged to retrieve the next page.
type ListOptions struct {
	Prefix            string
	ContinuationToken string
	Limit             int32
}

// ListPage is one bounded page of object metadata.
type ListPage struct {
	Objects           []Object
	ContinuationToken string
}

// Store provides bounded listing, streaming reads, and metadata lookup for
// backup objects.
type Store interface {
	List(ctx context.Context, options ListOptions) (ListPage, error)
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Stat(ctx context.Context, key string) (Object, error)
}
