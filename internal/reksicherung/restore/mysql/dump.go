package mysql

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"regexp"
	"strings"
)

const (
	dumpReadBufferBytes = 64 << 10
	// maxBufferedRewriteBytes bounds the RewriteDump convenience helper, which
	// buffers the rewritten dump in memory. Production restores stream.
	maxBufferedRewriteBytes = int64(64 << 20)
	// maxHeldBytes bounds an identifier plus the whitespace/comments that
	// follow it while we wait to see whether a '.' (qualifier) comes next.
	maxHeldBytes = 4096
	// maxCaptureBytes bounds statements that are buffered whole (USE, schema
	// statements, SET).
	maxCaptureBytes = 1 << 20
	maxWordBuffer   = 256
)

var (
	// Accepted shapes: mysqldump's CREATE DATABASE (optionally with conditional
	// comments) and DROP DATABASE wrapped in a version comment. The statement
	// text may span lines (\s matches newlines).
	databaseStatementRe = regexp.MustCompile("(?is)^(/\\*![0-9]*\\s+)?(CREATE|DROP)\\s+(?:DATABASE|SCHEMA)\\s+" +
		"(?:/\\*![0-9]*\\s+)?(?:IF\\s+(?:NOT\\s+)?EXISTS\\s*)?(?:\\*/\\s*)?`([^`]+)`(.*)$")
	useStatementRe = regexp.MustCompile("(?i)^USE\\s+`([^`]+)`\\s*;$")
	// mysql client commands that can execute local programs or change the
	// connection. The client is additionally run with --binary-mode and
	// --local-infile=0. Backslash commands are rejected anywhere in code by the
	// lexer; this catches the word commands at the start of a line.
	clientCommandRe     = regexp.MustCompile("(?i)^\\s*(?:\\\\|(?:system|source|connect|tee|pager|edit)(?:\\s|;|$))")
	delimiterLineRe     = regexp.MustCompile("(?i)^\\s*delimiter\\b")
	delimiterAllowedRe  = regexp.MustCompile("(?i)^[ \\t]*delimiter[ \\t]+(?:;;|;)[ \\t]*\\r?\\n?$")
	sqlModeStatementRe  = regexp.MustCompile("^(?:/\\*![0-9]*\\s+)?set\\s+(?:@old_sql_mode\\s*=\\s*@@sql_mode\\s*,\\s*)?(?:session\\s+|@@session\\.)?sql_mode\\s*=\\s*(?:@old_sql_mode|'([a-z_,]*)')\\s*(?:\\*/)?\\s*;$")
	forbiddenSQLModes   = []string{"ansi", "ansi_quotes", "no_backslash_escapes", "oracle", "db2", "mssql", "postgresql", "maxdb"}
	forbiddenFirstWords = map[string]bool{
		"SYSTEM": true, "SOURCE": true, "CONNECT": true, "TEE": true, "PAGER": true, "EDIT": true,
		"PREPARE": true, "EXECUTE": true,
	}
)

type lexState uint8

const (
	lexCode lexState = iota
	lexSingle
	lexDouble
	lexBacktick
	lexBlock // plain /* */ comment
	lexLine  // -- or # comment
)

type stmtKind uint8

const (
	kindIdle     stmtKind = iota // between statements
	kindClassify                 // reading the first keywords
	kindNormal                   // ordinary statement, streamed and qualifier-checked
	kindDatabase                 // CREATE/DROP/ALTER DATABASE|SCHEMA, buffered whole
	kindUse                      // USE, buffered whole
	kindSet                      // SET, buffered whole (sql_mode allowlist)
)

const (
	wordNone uint8 = iota
	wordBuffered
	wordPass
)

// heldToken is an identifier withheld from the output until the next
// significant byte shows whether it is a database qualifier ("name" + '.').
type heldToken struct {
	active   bool
	raw      []byte // identifier followed by any whitespace/comments seen since
	identLen int
	name     string
	quoted   bool
}

// DumpRewriter streams a mysqldump-style logical dump and rewrites the single
// source database name to the run-scoped target. It lexes the SQL (strings,
// backtick identifiers, comments, statement boundaries) and fails closed with
// ErrInvalidDump for any other schema-level statement or qualifier.
type DumpRewriter struct {
	reader  *bufio.Reader
	source  string
	target  string
	max     int64
	read    int64
	creates int
	pending []byte
	inLong  bool
	err     error

	lexErr error
	carry  []byte
	out    []byte

	state      lexState
	escaped    bool
	star       bool
	inCond     bool
	skipDigits bool
	lineBlank  bool
	prevSig    byte

	word          []byte
	wordMode      uint8
	wordCheckable bool
	wordLineStart bool

	ident     []byte
	identPrev byte
	held      heldToken

	kind    stmtKind
	words   []string
	capture []byte
}

// NewDumpRewriter wraps source with a streaming rewriter. At most maxBytes of
// input are consumed; beyond that reads fail with ErrDumpLimit.
func NewDumpRewriter(source io.Reader, sourceDatabase, targetDatabase string, maxBytes int64) (*DumpRewriter, error) {
	if source == nil || !validDatabaseName(sourceDatabase) || !validDatabaseName(targetDatabase) ||
		maxBytes < 1 || maxBytes > maxDumpBytes {
		return nil, ErrInvalidConfig
	}
	return &DumpRewriter{
		reader:    bufio.NewReaderSize(source, dumpReadBufferBytes),
		source:    sourceDatabase,
		target:    targetDatabase,
		max:       maxBytes,
		lineBlank: true,
	}, nil
}

// Err returns the first rewrite or read failure, if any (io.EOF excluded).
func (rewriter *DumpRewriter) Err() error {
	if rewriter.err == nil || errors.Is(rewriter.err, io.EOF) {
		return nil
	}
	return rewriter.err
}

// RewriteDump rewrites a small dump in memory. It validates the whole input
// before returning, so any rewrite error is reported here. Dumps larger than
// 64 MiB are rejected; use NewDumpRewriter to stream.
func RewriteDump(dump io.Reader, sourceDatabase, targetDatabase string) (io.Reader, error) {
	rewriter, err := NewDumpRewriter(dump, sourceDatabase, targetDatabase, maxBufferedRewriteBytes)
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	if _, err := io.Copy(&buffer, rewriter); err != nil {
		return nil, err
	}
	return &buffer, nil
}

// Read implements io.Reader.
func (rewriter *DumpRewriter) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	for len(rewriter.pending) == 0 {
		if rewriter.err != nil {
			return 0, rewriter.err
		}
		rewriter.fill()
	}
	n := copy(buffer, rewriter.pending)
	rewriter.pending = rewriter.pending[n:]
	return n, nil
}

func (rewriter *DumpRewriter) fill() {
	chunk, err := rewriter.reader.ReadSlice('\n')
	rewriter.read += int64(len(chunk))
	if rewriter.read > rewriter.max {
		rewriter.err = ErrDumpLimit
		return
	}
	full := errors.Is(err, bufio.ErrBufferFull)
	eof := errors.Is(err, io.EOF)
	if err != nil && !full && !eof {
		rewriter.err = err
		return
	}
	lineStart := !rewriter.inLong
	// Longer than the read buffer: only bulk data statements may be this long.
	if full && lineStart && !isDataStatement(chunk) {
		rewriter.err = ErrInvalidDump
		return
	}
	if lineStart && rewriter.state == lexCode && len(chunk) > 0 {
		if clientCommandRe.Match(chunk) ||
			(delimiterLineRe.Match(chunk) && !delimiterAllowedRe.Match(chunk)) {
			rewriter.err = ErrInvalidDump
			return
		}
	}
	data := chunk
	if len(rewriter.carry) > 0 {
		data = append(bytes.Clone(rewriter.carry), chunk...)
		rewriter.carry = nil
	}
	consumed := rewriter.scan(data, !full)
	if consumed < len(data) && rewriter.lexErr == nil {
		rewriter.carry = bytes.Clone(data[consumed:])
	}
	rewriter.inLong = full
	if eof && rewriter.lexErr == nil {
		rewriter.finish()
	}
	if rewriter.lexErr != nil {
		rewriter.err = rewriter.lexErr
		return
	}
	if eof {
		rewriter.err = io.EOF
	}
	rewriter.pending = rewriter.out
	rewriter.out = nil
}

func isDataStatement(line []byte) bool {
	return hasFoldPrefix(line, "INSERT ") || hasFoldPrefix(line, "REPLACE ")
}

func hasFoldPrefix(line []byte, prefix string) bool {
	return len(line) >= len(prefix) && strings.EqualFold(string(line[:len(prefix)]), prefix)
}

func (rewriter *DumpRewriter) fail(err error) {
	if rewriter.lexErr == nil {
		rewriter.lexErr = err
	}
}

func (rewriter *DumpRewriter) capturing() bool {
	return rewriter.kind == kindClassify || rewriter.kind >= kindDatabase
}

func (rewriter *DumpRewriter) emit(p []byte) {
	switch {
	case rewriter.held.active:
		rewriter.held.raw = append(rewriter.held.raw, p...)
		if len(rewriter.held.raw) > maxHeldBytes {
			rewriter.fail(ErrInvalidDump)
		}
	case rewriter.capturing():
		rewriter.capture = append(rewriter.capture, p...)
		if len(rewriter.capture) > maxCaptureBytes {
			rewriter.fail(ErrInvalidDump)
		}
	default:
		rewriter.out = append(rewriter.out, p...)
	}
}

func (rewriter *DumpRewriter) emitByte(c byte) {
	switch {
	case rewriter.held.active:
		rewriter.held.raw = append(rewriter.held.raw, c)
		if len(rewriter.held.raw) > maxHeldBytes {
			rewriter.fail(ErrInvalidDump)
		}
	case rewriter.capturing():
		rewriter.capture = append(rewriter.capture, c)
		if len(rewriter.capture) > maxCaptureBytes {
			rewriter.fail(ErrInvalidDump)
		}
	default:
		rewriter.out = append(rewriter.out, c)
	}
}

func isWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '_' || c == '$' || c >= 0x80
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' || c == '\v'
}

// scan lexes data and returns how many bytes were consumed. Unconsumed bytes
// (a possible comment opener at the end of a partial chunk) must be passed
// again with the next chunk. final reports that no more input follows data
// on the current line.
func (rewriter *DumpRewriter) scan(data []byte, final bool) int {
	i := 0
	for i < len(data) && rewriter.lexErr == nil {
		c := data[i]
		switch rewriter.state {
		case lexSingle, lexDouble:
			quote := byte('\'')
			if rewriter.state == lexDouble {
				quote = '"'
			}
			j := i
			closed := false
			for j < len(data) {
				b := data[j]
				j++
				if rewriter.escaped {
					rewriter.escaped = false
					continue
				}
				if b == '\\' {
					rewriter.escaped = true
				} else if b == quote {
					closed = true
					break
				}
			}
			rewriter.emit(data[i:j])
			i = j
			if closed {
				rewriter.state = lexCode
				rewriter.prevSig = quote
			}
		case lexBacktick:
			i++
			if c != '`' {
				rewriter.ident = append(rewriter.ident, c)
				if len(rewriter.ident) > maxHeldBytes {
					rewriter.fail(ErrInvalidDump)
				}
				continue
			}
			rewriter.closeIdent()
		case lexBlock:
			i++
			rewriter.emitByte(c)
			if rewriter.star && c == '/' {
				rewriter.state = lexCode
			}
			rewriter.star = c == '*'
		case lexLine:
			i++
			rewriter.emitByte(c)
			if c == '\n' {
				rewriter.state = lexCode
				rewriter.lineBlank = true
			}
		default:
			next, ok := rewriter.scanCode(data, i, final)
			if !ok {
				return i
			}
			i = next
		}
	}
	return i
}

// scanCode handles one lexical element in code state starting at data[i]. It
// returns the new index; ok is false when more lookahead input is needed.
func (rewriter *DumpRewriter) scanCode(data []byte, i int, final bool) (int, bool) {
	c := data[i]
	if rewriter.wordMode != wordNone {
		if isWordByte(c) {
			rewriter.addWordByte(c)
			return i + 1, true
		}
		rewriter.endWord()
		if rewriter.lexErr != nil {
			return i, true
		}
	}
	if rewriter.skipDigits {
		if c >= '0' && c <= '9' {
			rewriter.emitByte(c)
			return i + 1, true
		}
		rewriter.skipDigits = false
	}
	wasBlank := rewriter.lineBlank
	if !isSpaceByte(c) {
		rewriter.lineBlank = false
	}
	remaining := len(data) - i
	switch {
	case isSpaceByte(c):
		rewriter.emitByte(c)
		if c == '\n' {
			rewriter.lineBlank = true
		}
	case isWordByte(c):
		rewriter.beginWord(c, wasBlank)
	case c == '\'' || c == '"':
		rewriter.significant(false, false)
		rewriter.emitByte(c)
		rewriter.escaped = false
		rewriter.state = lexSingle
		if c == '"' {
			rewriter.state = lexDouble
		}
	case c == '`':
		rewriter.significant(false, false)
		rewriter.state = lexBacktick
		rewriter.ident = rewriter.ident[:0]
		rewriter.identPrev = rewriter.prevSig
	case c == '#':
		rewriter.emitByte(c)
		rewriter.state = lexLine
	case c == '-':
		if !final && (remaining < 2 || (data[i+1] == '-' && remaining < 3)) {
			return i, false
		}
		if remaining >= 2 && data[i+1] == '-' && (remaining < 3 || data[i+2] <= ' ') {
			rewriter.emit(data[i : i+2])
			rewriter.state = lexLine
			return i + 2, true
		}
		rewriter.symbol(c)
	case c == '/':
		if !final && (remaining < 2 || (data[i+1] == '*' && (remaining < 3 || (data[i+2] == 'M' && remaining < 4)))) {
			return i, false
		}
		if remaining >= 2 && data[i+1] == '*' {
			marker := 0
			switch {
			case remaining >= 3 && data[i+2] == '!':
				marker = 3
			case remaining >= 4 && data[i+2] == 'M' && data[i+3] == '!':
				marker = 4
			}
			if marker > 0 {
				rewriter.beginMarker()
				rewriter.emit(data[i : i+marker])
				rewriter.inCond = true
				rewriter.skipDigits = true
				return i + marker, true
			}
			rewriter.emit(data[i : i+2])
			rewriter.state = lexBlock
			rewriter.star = false
			return i + 2, true
		}
		rewriter.symbol(c)
	case c == '*':
		if rewriter.inCond {
			if !final && remaining < 2 {
				return i, false
			}
			if remaining >= 2 && data[i+1] == '/' {
				rewriter.emit(data[i : i+2])
				rewriter.inCond = false
				return i + 2, true
			}
		}
		rewriter.symbol(c)
	case c == '\\':
		// mysql client backslash commands (\!, \., \u, ...) are never valid
		// outside strings in a dump.
		rewriter.fail(ErrInvalidDump)
	case c == ';':
		rewriter.terminate()
	case c == '.':
		rewriter.significant(true, false)
		rewriter.emitByte(c)
		rewriter.prevSig = c
	default:
		rewriter.symbol(c)
	}
	return i + 1, true
}

func (rewriter *DumpRewriter) symbol(c byte) {
	rewriter.significant(false, false)
	rewriter.emitByte(c)
	rewriter.prevSig = c
}

// significant is called before any non-whitespace, non-comment token.
func (rewriter *DumpRewriter) significant(dot, word bool) {
	if rewriter.held.active {
		rewriter.resolveHeld(dot)
	}
	rewriter.beginToken(word)
}

func (rewriter *DumpRewriter) beginMarker() {
	if rewriter.kind == kindIdle {
		rewriter.startStatement()
	}
}

func (rewriter *DumpRewriter) startStatement() {
	rewriter.kind = kindClassify
	rewriter.words = rewriter.words[:0]
	rewriter.capture = rewriter.capture[:0]
}

func (rewriter *DumpRewriter) beginToken(word bool) {
	if rewriter.kind == kindIdle {
		rewriter.startStatement()
	}
	if rewriter.kind == kindClassify && !word {
		rewriter.decideNormal()
	}
}

func (rewriter *DumpRewriter) decideNormal() {
	rewriter.kind = kindNormal
	if len(rewriter.capture) > 0 {
		rewriter.out = append(rewriter.out, rewriter.capture...)
		rewriter.capture = rewriter.capture[:0]
	}
}

func (rewriter *DumpRewriter) beginWord(c byte, atLineStart bool) {
	letter := !(c >= '0' && c <= '9')
	if !letter {
		// Numbers (including long hex literals) are never identifiers.
		rewriter.significant(false, false)
		rewriter.emitByte(c)
		rewriter.wordMode = wordPass
		return
	}
	checkable := rewriter.prevSig != '@' && rewriter.prevSig != '.'
	rewriter.significant(false, true)
	rewriter.word = append(rewriter.word[:0], c)
	rewriter.wordMode = wordBuffered
	rewriter.wordCheckable = checkable
	rewriter.wordLineStart = atLineStart
}

func (rewriter *DumpRewriter) addWordByte(c byte) {
	if rewriter.wordMode == wordBuffered {
		if len(rewriter.word) < maxWordBuffer {
			rewriter.word = append(rewriter.word, c)
			return
		}
		rewriter.emit(rewriter.word)
		rewriter.wordMode = wordPass
	}
	rewriter.emitByte(c)
}

func (rewriter *DumpRewriter) endWord() {
	mode := rewriter.wordMode
	rewriter.wordMode = wordNone
	rewriter.prevSig = 'a'
	if mode == wordPass {
		if rewriter.kind == kindClassify {
			rewriter.classify("")
		}
		return
	}
	word := rewriter.word
	switch rewriter.kind {
	case kindClassify:
		rewriter.emit(word)
		rewriter.classify(strings.ToUpper(string(word)))
	case kindNormal:
		if rewriter.wordCheckable {
			rewriter.held = heldToken{
				active:   true,
				raw:      bytes.Clone(word),
				identLen: len(word),
				name:     string(word),
			}
			return
		}
		rewriter.emit(word)
	default:
		rewriter.emit(word)
	}
}

// classify records a leading keyword and decides how the statement is
// handled once enough keywords are known.
func (rewriter *DumpRewriter) classify(word string) {
	rewriter.words = append(rewriter.words, word)
	words := rewriter.words
	first := words[0]
	switch {
	case forbiddenFirstWords[first]:
		rewriter.fail(ErrInvalidDump)
	case first == "DELIMITER":
		if !rewriter.wordLineStart {
			rewriter.fail(ErrInvalidDump)
			return
		}
		rewriter.decideNormal()
	case first == "USE":
		rewriter.kind = kindUse
	case first == "SET":
		rewriter.kind = kindSet
	case first == "CREATE":
		if len(words) < 2 {
			return
		}
		object := words[1]
		if object == "OR" {
			if len(words) < 3 {
				return
			}
			if words[2] != "REPLACE" {
				rewriter.decideNormal()
				return
			}
			if len(words) < 4 {
				return
			}
			object = words[3]
		}
		rewriter.finishObjectKind(object)
	case first == "DROP" || first == "ALTER":
		if len(words) < 2 {
			return
		}
		rewriter.finishObjectKind(words[1])
	default:
		rewriter.decideNormal()
	}
}

func (rewriter *DumpRewriter) finishObjectKind(object string) {
	if object == "DATABASE" || object == "SCHEMA" {
		rewriter.kind = kindDatabase
		return
	}
	rewriter.decideNormal()
}

func (rewriter *DumpRewriter) closeIdent() {
	rewriter.state = lexCode
	rewriter.prevSig = '`'
	raw := make([]byte, 0, len(rewriter.ident)+2)
	raw = append(raw, '`')
	raw = append(raw, rewriter.ident...)
	raw = append(raw, '`')
	if rewriter.kind == kindNormal && rewriter.identPrev != '.' {
		rewriter.held = heldToken{
			active:   true,
			raw:      raw,
			identLen: len(raw),
			name:     string(rewriter.ident),
			quoted:   true,
		}
		return
	}
	rewriter.emit(raw)
}

// resolveHeld releases the withheld identifier. When dot is true the
// identifier is a qualifier ("name.") and must be the source database, which
// is rewritten, or one of the trigger pseudo-tables.
func (rewriter *DumpRewriter) resolveHeld(dot bool) {
	held := rewriter.held
	rewriter.held = heldToken{}
	if !dot {
		rewriter.emit(held.raw)
		return
	}
	switch {
	case held.name == rewriter.source:
		rewriter.emit([]byte(quoteIdentifier(rewriter.target)))
	case !held.quoted && (strings.EqualFold(held.name, "NEW") || strings.EqualFold(held.name, "OLD")):
		rewriter.emit(held.raw[:held.identLen])
	default:
		rewriter.fail(ErrInvalidDump)
		return
	}
	rewriter.emit(held.raw[held.identLen:])
}

// terminate handles a ';' outside strings, comments and identifiers.
func (rewriter *DumpRewriter) terminate() {
	if rewriter.held.active {
		rewriter.resolveHeld(false)
	}
	if rewriter.kind == kindClassify {
		rewriter.decideNormal()
	}
	rewriter.emitByte(';')
	rewriter.prevSig = ';'
	rewriter.finishStatement()
}

// finishStatement completes the current statement; buffered kinds are
// validated and rewritten here.
func (rewriter *DumpRewriter) finishStatement() {
	kind := rewriter.kind
	rewriter.kind = kindIdle
	if kind < kindDatabase {
		return
	}
	text := string(rewriter.capture)
	rewriter.capture = rewriter.capture[:0]
	var (
		rewritten string
		err       error
	)
	switch kind {
	case kindDatabase:
		rewritten, err = rewriter.rewriteDatabaseStatement(text)
	case kindUse:
		match := useStatementRe.FindStringSubmatch(text)
		if match == nil || match[1] != rewriter.source {
			err = ErrInvalidDump
		} else {
			rewritten = "USE " + quoteIdentifier(rewriter.target) + ";"
		}
	default:
		err = checkSetStatement(text)
		rewritten = text
	}
	if err != nil {
		rewriter.fail(err)
		return
	}
	rewriter.out = append(rewriter.out, rewritten...)
}

// checkSetStatement rejects SET statements that could change sql_mode in a
// way that would desynchronise this lexer from the server and client
// (ANSI_QUOTES, NO_BACKSLASH_ESCAPES, and the combination modes).
func checkSetStatement(text string) error {
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "sql_mode") {
		return nil
	}
	match := sqlModeStatementRe.FindStringSubmatch(lower)
	if match == nil {
		return ErrInvalidDump
	}
	for _, mode := range strings.Split(match[1], ",") {
		for _, forbidden := range forbiddenSQLModes {
			if mode == forbidden {
				return ErrInvalidDump
			}
		}
	}
	return nil
}

// finish flushes lexer state at end of input.
func (rewriter *DumpRewriter) finish() {
	switch rewriter.state {
	case lexBacktick:
		rewriter.emit(append([]byte{'`'}, rewriter.ident...))
		rewriter.state = lexCode
	}
	if rewriter.wordMode != wordNone {
		rewriter.endWord()
	}
	if rewriter.held.active {
		rewriter.resolveHeld(false)
	}
	switch rewriter.kind {
	case kindClassify:
		rewriter.decideNormal()
		rewriter.kind = kindIdle
	case kindNormal:
		rewriter.kind = kindIdle
	case kindIdle:
	default:
		rewriter.finishStatement()
	}
}

func (rewriter *DumpRewriter) rewriteDatabaseStatement(body string) (string, error) {
	match := databaseStatementRe.FindStringSubmatch(body)
	if match == nil || len(body) > 4096 {
		return "", ErrInvalidDump
	}
	wrapper, verb, name, tail := match[1], strings.ToUpper(match[2]), match[3], match[4]
	if name != rewriter.source || strings.Contains(tail, "`") ||
		strings.Count(tail, ";") != 1 || !strings.HasSuffix(strings.TrimRight(tail, " \t\r\n"), ";") {
		return "", ErrInvalidDump
	}
	target := quoteIdentifier(rewriter.target)
	if verb == "CREATE" {
		rewriter.creates++
		if rewriter.creates > 1 {
			return "", ErrInvalidDump
		}
		return wrapper + "CREATE DATABASE IF NOT EXISTS " + target + tail, nil
	}
	return wrapper + "DROP DATABASE IF EXISTS " + target + tail, nil
}
