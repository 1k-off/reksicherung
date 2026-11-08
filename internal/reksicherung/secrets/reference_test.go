package secrets

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseReference(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Reference
		wantErr bool
	}{
		{
			name:  "standard reference",
			input: "openbao://secret/reksicherung/demo#password",
			want: Reference{
				Provider: "openbao",
				Mount:    "secret",
				Path:     "reksicherung/demo",
				Key:      "password",
			},
		},
		{
			name:  "escaped path and key",
			input: "openbao://secret/team%20name/database%3Fprod#db%2Bpassword",
			want: Reference{
				Provider: "openbao",
				Mount:    "secret",
				Path:     "team name/database?prod",
				Key:      "db+password",
			},
		},
		{
			name:    "empty path",
			input:   "openbao://secret/#password",
			wantErr: true,
		},
		{
			name:    "trailing empty path segment",
			input:   "openbao://secret/a/#password",
			wantErr: true,
		},
		{
			name:    "encoded slash in a path segment",
			input:   "openbao://secret/a%2Fb#password",
			wantErr: true,
		},
		{
			name:    "dot path segment",
			input:   "openbao://secret/a/../b#password",
			wantErr: true,
		},
		{
			name:    "encoded key separator",
			input:   "openbao://secret/a#db%2Fpassword",
			wantErr: true,
		},
		{
			name:    "missing key",
			input:   "openbao://secret/a",
			wantErr: true,
		},
		{
			name:    "empty key",
			input:   "openbao://secret/a#",
			wantErr: true,
		},
		{
			name:    "query is not accepted",
			input:   "openbao://secret/a?version=1#password",
			wantErr: true,
		},
		{
			name:    "userinfo is not accepted",
			input:   "openbao://token@secret/a#password",
			wantErr: true,
		},
		{
			name:    "unsupported provider",
			input:   "vault://secret/a#password",
			wantErr: true,
		},
		{
			name:    "invalid escape",
			input:   "openbao://secret/a%2#password",
			wantErr: true,
		},
		{
			name:    "empty mount",
			input:   "openbao:///a#password",
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseReference(test.input)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidReference) {
					t.Fatalf("ParseReference() error = %v, want %v", err, ErrInvalidReference)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseReference() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("ParseReference() = %#v, want %#v", got, test.want)
			}
			if !got.Valid() {
				t.Errorf("ParseReference() returned an invalid reference: %#v", got)
			}
		})
	}
}

func TestReferenceValidRejectsUnsafeComponents(t *testing.T) {
	tests := []struct {
		name string
		ref  Reference
	}{
		{name: "wrong provider", ref: Reference{Provider: "vault", Mount: "secret", Path: "a", Key: "key"}},
		{name: "empty mount", ref: Reference{Provider: "openbao", Path: "a", Key: "key"}},
		{name: "dot path", ref: Reference{Provider: "openbao", Mount: "secret", Path: "../a", Key: "key"}},
		{name: "empty path segment", ref: Reference{Provider: "openbao", Mount: "secret", Path: "a//b", Key: "key"}},
		{name: "empty key", ref: Reference{Provider: "openbao", Mount: "secret", Path: "a"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.ref.Valid() {
				t.Errorf("Reference.Valid() = true for %#v", test.ref)
			}
		})
	}
}
