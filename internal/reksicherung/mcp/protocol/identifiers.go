// Package protocol contains helpers shared by MCP tool servers for shaping
// model-facing payloads. Restored-database identifiers are untrusted input
// (an attacker can plant them via a crafted dump); they must never be emitted
// to an LLM as free-form natural language.
package protocol

import (
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// EncodedPrefix marks identifiers that were not safe to emit literally.
	// Tool consumers must treat EncodedPrefix values as opaque data and pass
	// them back verbatim on subsequent tool calls.
	EncodedPrefix = "b64:"

	maxEncodedIdentifierBytes = 512
)

// ErrInvalidIdentifier indicates a tool argument is not a safe literal and not
// a valid EncodedPrefix payload.
var ErrInvalidIdentifier = errors.New("invalid protocol identifier")

// safeLiteral matches identifiers that are unambiguous as opaque tokens rather
// than natural-language instructions (ASCII letters/digits/underscore only).
var safeLiteral = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// EncodeIdentifier returns name unchanged when it is a safe literal; otherwise
// it returns EncodedPrefix + raw-URL base64 of the UTF-8 bytes. Empty strings
// are returned unchanged (callers decide whether empty is allowed).
func EncodeIdentifier(name string) string {
	if name == "" || isSafeLiteral(name) {
		return name
	}
	return EncodedPrefix + base64.RawURLEncoding.EncodeToString([]byte(name))
}

// DecodeIdentifier accepts a safe literal or an EncodedPrefix token produced by
// EncodeIdentifier. Unencoded unsafe text is rejected so callers cannot inject
// free-form identifiers through tool arguments either.
func DecodeIdentifier(name string) (string, error) {
	if name == "" {
		return "", nil
	}
	if strings.HasPrefix(name, EncodedPrefix) {
		payload := strings.TrimPrefix(name, EncodedPrefix)
		if payload == "" || len(payload) > maxEncodedIdentifierBytes {
			return "", ErrInvalidIdentifier
		}
		raw, err := base64.RawURLEncoding.DecodeString(payload)
		if err != nil || len(raw) == 0 || !utf8.Valid(raw) {
			return "", ErrInvalidIdentifier
		}
		return string(raw), nil
	}
	if !isSafeLiteral(name) {
		return "", ErrInvalidIdentifier
	}
	return name, nil
}

func isSafeLiteral(name string) bool {
	if len(name) > 128 || !utf8.ValidString(name) {
		return false
	}
	return safeLiteral.MatchString(name)
}
