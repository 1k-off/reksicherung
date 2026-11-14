package gobackup

import (
	"errors"
	"strings"

	"github.com/1k-off/reksicherung/internal/reksicherung/configgen/input"
)

// ValidateForProvider enforces GoBackup-specific storage mapping rules.
func ValidateForProvider(in input.Input) error {
	if strings.EqualFold(in.Storage.Type, "azure") {
		if in.Storage.AccountKeyRef != "" || in.Storage.AccountSASRef != "" {
			return errors.New("gobackup: azure account_key_ref/account_sas_ref are not supported; use tenant/client/secret refs")
		}
		if in.Storage.TenantIDRef == "" || in.Storage.ClientIDRef == "" || in.Storage.ClientSecretRef == "" {
			return errors.New("gobackup: azure requires tenant_id_ref, client_id_ref, and client_secret_ref")
		}
	}
	return nil
}
