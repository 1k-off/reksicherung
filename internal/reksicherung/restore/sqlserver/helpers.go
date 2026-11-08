// Package sqlserver contains bounded operations for an operator-managed SQL
// Server, plus validation helpers for native backup metadata.
package sqlserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	nativeRestoreTimeout = 6 * time.Hour
	fileListTimeout      = 30 * time.Second
	checkDBTimeout       = 30 * time.Minute
	dropDBTimeout        = 30 * time.Second
	maxNativeBackupBytes = int64(1 << 40)
	maxOutputBytes       = 64 << 10
	maxBackupFiles       = 16
	fileListColumns      = 22
	// SQL Server 2022 RESTORE HEADERONLY returns 59 columns. Unknown output
	// shapes fail closed rather than guessing which backup set was selected.
	headerOnlyColumns = 59
)

var (
	ErrInvalidConfig         = errors.New("invalid SQL Server restore configuration")
	ErrTimeout               = errors.New("SQL Server operation timed out")
	ErrCanceled              = errors.New("SQL Server operation canceled")
	ErrCommandFailed         = errors.New("SQL Server command failed")
	ErrOutputLimit           = errors.New("SQL Server command output exceeded limit")
	ErrInvalidBackupMetadata = errors.New("SQL Server backup metadata is invalid or unsupported")
	ErrBackupTransferFailed  = errors.New("SQL Server backup transfer failed")
	ErrBackupCleanupFailed   = errors.New("SQL Server backup cleanup failed")
)

// IntegrityResult reports command completion only. It does not state that the
// database is free of consistency errors.
type IntegrityResult struct {
	Database         string
	CommandCompleted bool
}

// DatabaseNameForRun returns the only database name that restore operations
// may modify for a run.
func DatabaseNameForRun(runID string) (string, error) {
	if !validRunID(runID) {
		return "", ErrInvalidConfig
	}
	digest := sha256.Sum256([]byte(runID))
	return "reksicherung_" + hex.EncodeToString(digest[:16]), nil
}

type backupFile struct {
	logicalName string
	fileType    string
}

func quoteStringLiteral(value string) string {
	return "N'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func parseBackupFileList(output []byte) ([]backupFile, error) {
	if len(output) == 0 || len(output) > maxOutputBytes || !utf8.Valid(output) {
		return nil, ErrInvalidBackupMetadata
	}
	text := strings.TrimSuffix(string(output), "\n")
	if text == "" {
		return nil, ErrInvalidBackupMetadata
	}
	rows := strings.Split(text, "\n")
	if len(rows) > maxBackupFiles {
		return nil, ErrInvalidBackupMetadata
	}

	files := make([]backupFile, 0, len(rows))
	seenNames := make(map[string]struct{}, len(rows))
	dataCount, logCount := 0, 0
	for _, row := range rows {
		row = strings.TrimSuffix(row, "\r")
		if row == "" || len(row) > 4096 {
			return nil, ErrInvalidBackupMetadata
		}
		for _, char := range row {
			if unicode.IsControl(char) {
				return nil, ErrInvalidBackupMetadata
			}
		}

		columns := strings.Split(row, "|")
		if len(columns) != fileListColumns {
			return nil, ErrInvalidBackupMetadata
		}
		logicalName, fileType := columns[0], columns[2]
		if !validLogicalFileName(logicalName) {
			return nil, ErrInvalidBackupMetadata
		}
		switch fileType {
		case "D":
			dataCount++
		case "L":
			logCount++
		default:
			return nil, ErrInvalidBackupMetadata
		}
		key := strings.ToLower(logicalName)
		if _, exists := seenNames[key]; exists {
			return nil, ErrInvalidBackupMetadata
		}
		seenNames[key] = struct{}{}
		files = append(files, backupFile{logicalName: logicalName, fileType: fileType})
	}
	if dataCount == 0 || logCount == 0 {
		return nil, ErrInvalidBackupMetadata
	}
	return files, nil
}

func validateSingleFullBackupSet(output []byte, expectedDatabase string) error {
	if len(output) == 0 || len(output) > maxOutputBytes || !utf8.Valid(output) {
		return ErrInvalidBackupMetadata
	}
	text := strings.TrimSuffix(string(output), "\n")
	if text == "" || strings.Contains(text, "\n") {
		return ErrInvalidBackupMetadata
	}
	row := strings.TrimSuffix(text, "\r")
	for _, char := range row {
		if unicode.IsControl(char) {
			return ErrInvalidBackupMetadata
		}
	}
	columns := strings.Split(row, "|")
	if len(columns) != headerOnlyColumns || columns[2] != "1" || columns[5] != "1" ||
		columns[9] != expectedDatabase {
		return ErrInvalidBackupMetadata
	}
	return nil
}

func validLogicalFileName(name string) bool {
	if name == "" || len(name) > 512 || strings.TrimSpace(name) != name || !utf8.ValidString(name) {
		return false
	}
	runeCount := 0
	for _, char := range name {
		runeCount++
		if runeCount > 128 || unicode.IsControl(char) || char == '|' {
			return false
		}
	}
	return true
}

func boundedChildContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, timeout)
}

func contextError(err error) error {
	if errors.Is(err, context.Canceled) {
		return ErrCanceled
	}
	return ErrTimeout
}

func validDatabaseName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '$' ||
			char == '#' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func validRunID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for index, char := range id {
		alphaNumeric := char >= 'a' && char <= 'z' ||
			char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9'
		if index == 0 && !alphaNumeric {
			return false
		}
		if !alphaNumeric && char != '_' && char != '-' && char != '.' {
			return false
		}
	}
	return true
}

func quoteIdentifier(name string) string {
	// validDatabaseName excludes SQL Server's closing bracket delimiter.
	return "[" + name + "]"
}
