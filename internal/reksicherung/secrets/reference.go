// Package secrets defines provider-neutral secret references and resolution.
package secrets

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"unicode"
)

// ErrInvalidReference indicates that a secret reference is malformed or uses
// a provider scheme that is not supported by this package.
var ErrInvalidReference = errors.New("secrets: invalid reference")

// Reference identifies one value in a secret provider.
//
// Fields contain decoded values. Provider implementations must validate a
// Reference before using it because callers can construct one directly.
type Reference struct {
	Provider string
	Mount    string
	Path     string
	Key      string
}

// SecretProvider resolves secret references at runtime.
type SecretProvider interface {
	Resolve(ctx context.Context, reference Reference) ([]byte, error)
}

// ParseReference parses an OpenBao KV v2 reference in the form
// openbao://<mount>/<path>#<key>.
//
// Path segments and keys are percent-decoded. Empty segments, dot segments,
// separators embedded in a segment, control characters, queries, and
// references without a key are rejected.
func ParseReference(raw string) (Reference, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "openbao" || u.Opaque != "" || u.User != nil ||
		u.Host == "" || u.Hostname() != u.Host || u.Port() != "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment == "" {
		return Reference{}, ErrInvalidReference
	}

	mount, err := url.PathUnescape(u.Host)
	if err != nil || !validSegment(mount) {
		return Reference{}, ErrInvalidReference
	}

	escapedPath := u.EscapedPath()
	if !strings.HasPrefix(escapedPath, "/") {
		return Reference{}, ErrInvalidReference
	}
	escapedSegments := strings.Split(strings.TrimPrefix(escapedPath, "/"), "/")
	pathSegments := make([]string, len(escapedSegments))
	for i, segment := range escapedSegments {
		decoded, decodeErr := url.PathUnescape(segment)
		if decodeErr != nil || !validSegment(decoded) {
			return Reference{}, ErrInvalidReference
		}
		pathSegments[i] = decoded
	}

	key, err := url.PathUnescape(u.EscapedFragment())
	if err != nil || !validKey(key) {
		return Reference{}, ErrInvalidReference
	}

	return Reference{
		Provider: "openbao",
		Mount:    mount,
		Path:     strings.Join(pathSegments, "/"),
		Key:      key,
	}, nil
}

// Valid reports whether all components are safe, non-empty OpenBao
// reference components.
func (r Reference) Valid() bool {
	if r.Provider != "openbao" || !validSegment(r.Mount) || !validKey(r.Key) {
		return false
	}
	for _, segment := range strings.Split(r.Path, "/") {
		if !validSegment(segment) {
			return false
		}
	}
	return r.Path != ""
}

func validSegment(value string) bool {
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, `/\`) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validKey(value string) bool {
	if value == "" || strings.ContainsAny(value, `/\`) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
