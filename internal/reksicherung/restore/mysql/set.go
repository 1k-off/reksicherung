package mysql

import (
	"regexp"
	"strings"
)

// SET statements are validated against an allowlist of the forms mysqldump
// emits. Anything else (other variables, GLOBAL scope, comments, expressions,
// qualified names, ...) fails closed. The checks exist to keep this lexer, the
// mysql client and the server in agreement about quoting: a SET that changes
// sql_mode (ANSI_QUOTES, NO_BACKSLASH_ESCAPES) or the connection character set
// to a multibyte charset whose trail bytes include 0x5c (GBK, SJIS, ...) would
// let a crafted dump hide statements from the lexer.

const setNoopAssignment = "@reksicherung_noop=0"

var (
	// A version-comment wrapped statement as mysqldump writes it. The body must
	// not contain another comment; the part patterns below reject '/', '*'.
	setWrapperRe = regexp.MustCompile(`(?s)^(/\*![0-9]*\s+)(.*?)(\s*\*/\s*;?)$`)
	setBodyRe    = regexp.MustCompile(`(?is)^SET\s+(.*?)\s*$`)

	setNamesRe   = regexp.MustCompile(`(?is)^names\s+(?:'([a-z0-9_]+)'|([a-z0-9_]+))(?:\s+collate\s+(?:'([a-z0-9_]+)'|([a-z0-9_]+)))?$`)
	setCharsetRe = regexp.MustCompile(`(?is)^(?:character\s+set|charset)\s+(?:'([a-z0-9_]+)'|([a-z0-9_]+))$`)

	setUserVarLHSRe = regexp.MustCompile(`(?is)^@([a-z0-9_$]+)\s*(?::=|=)\s*(.+)$`)
	setSysVarLHSRe  = regexp.MustCompile(`(?is)^(?:@@(?:session\.)?|(?:(?:session|local)\s+)?)([a-z_]+)\s*=\s*(.+)$`)

	setStringRe  = regexp.MustCompile(`(?s)^'([^'\\]*)'$`)
	setNumberRe  = regexp.MustCompile(`^-?[0-9]+$`)
	setUserRefRe = regexp.MustCompile(`(?i)^@([a-z0-9_$]+)$`)
	setSysRefRe  = regexp.MustCompile(`(?i)^@@(?:session\.)?([a-z_]+)$`)
	setWordRe    = regexp.MustCompile(`(?i)^[a-z0-9_]+$`)
	sqlModeValRe = regexp.MustCompile(`^[a-z_,]*$`)

	allowedCharsets = map[string]bool{
		"utf8": true, "utf8mb3": true, "utf8mb4": true, "latin1": true, "binary": true, "ascii": true,
	}
	// Session variables a dump may set or read. Matched case-insensitively.
	allowedSetVars = map[string]bool{
		"sql_mode": true, "time_zone": true, "unique_checks": true, "foreign_key_checks": true,
		"sql_notes": true, "sql_log_bin": true, "autocommit": true,
		"character_set_client": true, "character_set_connection": true, "character_set_results": true,
		"collation_connection": true,
	}
	charsetSetVars = map[string]bool{
		"character_set_client": true, "character_set_connection": true, "character_set_results": true,
	}
	// User variables mysqldump uses to save and restore sql_mode. They may only
	// ever be assigned from @@sql_mode, so restoring from them is safe.
	savedSQLModeVars = map[string]bool{"old_sql_mode": true, "saved_sql_mode": true}
)

type setValueKind uint8

const (
	valInvalid setValueKind = iota
	valString
	valNumber
	valUserVar
	valSysVar
	valWord
)

type setValue struct {
	kind setValueKind
	text string // string content, variable name, or word; lower-cased for names and words
}

func parseSetValue(raw string) setValue {
	raw = strings.TrimSpace(raw)
	switch {
	case setStringRe.MatchString(raw):
		return setValue{valString, setStringRe.FindStringSubmatch(raw)[1]}
	case setNumberRe.MatchString(raw):
		return setValue{valNumber, raw}
	case setUserRefRe.MatchString(raw):
		return setValue{valUserVar, strings.ToLower(setUserRefRe.FindStringSubmatch(raw)[1])}
	case setSysRefRe.MatchString(raw):
		name := strings.ToLower(setSysRefRe.FindStringSubmatch(raw)[1])
		if allowedSetVars[name] {
			return setValue{valSysVar, name}
		}
	case setWordRe.MatchString(raw):
		return setValue{valWord, strings.ToLower(raw)}
	}
	return setValue{}
}

// splitSetList splits on top-level commas (outside single-quoted strings).
func splitSetList(list string) []string {
	var parts []string
	start := 0
	inString := false
	for i := 0; i < len(list); i++ {
		switch c := list[i]; {
		case c == '\'':
			inString = !inString
		case c == ',' && !inString:
			parts = append(parts, strings.TrimSpace(list[start:i]))
			start = i + 1
		}
	}
	return append(parts, strings.TrimSpace(list[start:]))
}

func charsetAllowed(name string) bool {
	return allowedCharsets[strings.ToLower(name)]
}

// collationAllowed accepts a collation only when it belongs to an allowed
// charset (utf8mb4_general_ci, latin1_bin, binary, ...).
func collationAllowed(name string) bool {
	name = strings.ToLower(name)
	if name == "binary" {
		return true
	}
	prefix, _, found := strings.Cut(name, "_")
	return found && allowedCharsets[prefix] && prefix != "binary"
}

func pick(groups ...string) string {
	for _, group := range groups {
		if group != "" {
			return group
		}
	}
	return ""
}

// rewriteSetStatement validates a buffered SET statement (including optional
// /*!NNNNN ... */ wrapper and trailing ';'). It returns the text to emit,
// which equals the input unless an assignment had to be neutralised.
func rewriteSetStatement(text string) (string, error) {
	prefix, body, suffix := "", text, ""
	if match := setWrapperRe.FindStringSubmatch(text); match != nil {
		prefix, body, suffix = match[1], match[2], match[3]
	} else {
		body = strings.TrimSuffix(strings.TrimRight(text, " \t\r\n"), ";")
		suffix = text[len(body):]
	}
	match := setBodyRe.FindStringSubmatch(body)
	if match == nil {
		return "", ErrInvalidDump
	}
	parts := splitSetList(match[1])
	changed := false
	for index, part := range parts {
		replacement, err := checkSetAssignment(part)
		if err != nil {
			return "", err
		}
		if replacement != "" {
			parts[index] = replacement
			changed = true
		}
	}
	if !changed {
		return text, nil
	}
	return prefix + "SET " + strings.Join(parts, ", ") + suffix, nil
}

// checkSetAssignment validates one comma-separated SET item. A non-empty
// replacement means the item must be replaced by that text.
func checkSetAssignment(part string) (string, error) {
	if part == "" {
		return "", ErrInvalidDump
	}
	if match := setNamesRe.FindStringSubmatch(part); match != nil {
		if !charsetAllowed(pick(match[1], match[2])) {
			return "", ErrInvalidDump
		}
		if collation := pick(match[3], match[4]); collation != "" && !collationAllowed(collation) {
			return "", ErrInvalidDump
		}
		return "", nil
	}
	if match := setCharsetRe.FindStringSubmatch(part); match != nil {
		if !charsetAllowed(pick(match[1], match[2])) {
			return "", ErrInvalidDump
		}
		return "", nil
	}
	if match := setUserVarLHSRe.FindStringSubmatch(part); match != nil {
		return "", checkUserVarAssignment(strings.ToLower(match[1]), parseSetValue(match[2]))
	}
	if match := setSysVarLHSRe.FindStringSubmatch(part); match != nil {
		return checkSysVarAssignment(strings.ToLower(match[1]), parseSetValue(match[2]))
	}
	return "", ErrInvalidDump
}

func checkUserVarAssignment(name string, value setValue) error {
	if savedSQLModeVars[name] {
		if value.kind == valSysVar && value.text == "sql_mode" {
			return nil
		}
		return ErrInvalidDump
	}
	switch value.kind {
	case valString, valNumber, valUserVar, valSysVar:
		return nil
	}
	return ErrInvalidDump
}

func checkSysVarAssignment(name string, value setValue) (string, error) {
	if !allowedSetVars[name] || value.kind == valInvalid {
		return "", ErrInvalidDump
	}
	switch {
	case name == "sql_mode":
		return "", checkSQLModeValue(value)
	case charsetSetVars[name]:
		switch value.kind {
		case valString, valWord:
			if !charsetAllowed(value.text) {
				return "", ErrInvalidDump
			}
			return "", nil
		case valUserVar, valSysVar:
			// mysqldump restores the saved charset from a user variable. The
			// variable's content cannot be trusted, so the assignment is
			// dropped; the session keeps the last allowlisted charset.
			return setNoopAssignment, nil
		}
		return "", ErrInvalidDump
	case name == "collation_connection":
		switch value.kind {
		case valString, valWord:
			if !collationAllowed(value.text) {
				return "", ErrInvalidDump
			}
			return "", nil
		case valUserVar, valSysVar:
			return setNoopAssignment, nil
		}
		return "", ErrInvalidDump
	case name == "time_zone":
		if value.kind == valString || value.kind == valUserVar || value.kind == valSysVar {
			return "", nil
		}
		return "", ErrInvalidDump
	default:
		switch value.kind {
		case valNumber, valUserVar, valSysVar:
			return "", nil
		case valWord:
			switch value.text {
			case "on", "off", "true", "false":
				return "", nil
			}
		}
		return "", ErrInvalidDump
	}
}

// checkSQLModeValue accepts a literal mode list without the modes that change
// quoting rules, or a restore from mysqldump's saved-mode variables.
func checkSQLModeValue(value setValue) error {
	switch value.kind {
	case valUserVar:
		if savedSQLModeVars[value.text] {
			return nil
		}
	case valString:
		lower := strings.ToLower(value.text)
		if !sqlModeValRe.MatchString(lower) {
			return ErrInvalidDump
		}
		for _, mode := range strings.Split(lower, ",") {
			for _, forbidden := range forbiddenSQLModes {
				if mode == forbidden {
					return ErrInvalidDump
				}
			}
		}
		return nil
	}
	return ErrInvalidDump
}
