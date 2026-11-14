package resticprofile

import (
	"errors"
	"fmt"
	"strings"

	"github.com/1k-off/reksicherung/internal/reksicherung/configgen/input"
)

// ValidateForProvider checks the shared input against the resticprofile v1
// supported subset. Call after input.Validate.
func ValidateForProvider(in input.Input) error {
	if len(in.Databases) > 0 {
		return errors.New("resticprofile: databases are not supported")
	}
	if in.Web.Host != "" || in.Web.Port != 0 {
		return errors.New("resticprofile: web is not supported")
	}
	if in.Archive == nil {
		return errors.New("resticprofile: archive is required")
	}
	if in.Archive.CompressType != "" {
		return errors.New("resticprofile: archive.compress_type is not supported")
	}
	if in.Encryption.PasswordRef == "" {
		return errors.New("resticprofile: encryption.password_ref is required")
	}
	if !in.Encryption.Enabled {
		// Shared Validate already requires enabled when password_ref is set;
		// reject an explicit false for clarity if callers construct Input in Go.
		return errors.New("resticprofile: encryption.enabled cannot be false")
	}
	if in.Encryption.Salt != nil {
		return errors.New("resticprofile: encryption.salt is not supported")
	}
	if in.Encryption.Base64 != nil {
		return errors.New("resticprofile: encryption.base64 is not supported")
	}
	switch strings.ToLower(in.Storage.Type) {
	case "s3", "minio":
	case "azure":
		if in.Storage.AccountKeyRef == "" {
			return errors.New("resticprofile: azure requires account_key_ref")
		}
		if in.Storage.TenantIDRef != "" || in.Storage.ClientIDRef != "" || in.Storage.ClientSecretRef != "" {
			return errors.New("resticprofile: azure service principal refs are not supported")
		}
	case "sftp":
		return errors.New("resticprofile: storage.type sftp is not supported")
	default:
		return fmt.Errorf("resticprofile: storage.type %q is not supported", in.Storage.Type)
	}
	return nil
}
