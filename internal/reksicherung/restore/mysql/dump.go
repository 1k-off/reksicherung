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
)

var (
	// Detects any line that begins a schema-level statement we must not pass
	// through unexamined.
	databaseStatementStartRe = regexp.MustCompile("(?i)^(?:/\\*![0-9]*\\s+)?(?:CREATE|DROP|ALTER)\\s+(?:DATABASE|SCHEMA)\\b")
	// Accepted shapes: mysqldump's CREATE DATABASE (optionally with conditional
	// comments) and DROP DATABASE wrapped in a version comment.
	databaseStatementRe = regexp.MustCompile("(?i)^(/\\*![0-9]*\\s+)?(CREATE|DROP)\\s+(?:DATABASE|SCHEMA)\\s+" +
		"(?:/\\*![0-9]*\\s+)?(?:IF\\s+(?:NOT\\s+)?EXISTS\\s*)?(?:\\*/\\s*)?`([^`]+)`(.*)$")
	useStatementStartRe = regexp.MustCompile("(?i)^USE\\b")
	useStatementRe      = regexp.MustCompile("(?i)^USE\\s+`([^`]+)`\\s*;\\s*$")
	// mysql client commands that can execute local programs or change the
	// connection. The client is additionally run with --binary-mode.
	clientCommandRe = regexp.MustCompile("(?i)^\\s*(?:\\\\|(?:system|source|connect|tee|pager|edit)(?:\\s|;|$))")
)

// DumpRewriter streams a mysqldump-style logical dump and rewrites the single
// source database name to the run-scoped target. It fails closed with
// ErrInvalidDump for any other schema-level statement.
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
}

// NewDumpRewriter wraps source with a streaming rewriter. At most maxBytes of
// input are consumed; beyond that reads fail with ErrDumpLimit.
func NewDumpRewriter(source io.Reader, sourceDatabase, targetDatabase string, maxBytes int64) (*DumpRewriter, error) {
	if source == nil || !validDatabaseName(sourceDatabase) || !validDatabaseName(targetDatabase) ||
		maxBytes < 1 || maxBytes > maxDumpBytes {
		return nil, ErrInvalidConfig
	}
	return &DumpRewriter{
		reader: bufio.NewReaderSize(source, dumpReadBufferBytes),
		source: sourceDatabase,
		target: targetDatabase,
		max:    maxBytes,
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
	switch {
	case errors.Is(err, bufio.ErrBufferFull):
		// Longer than the read buffer: only bulk data statements may be this
		// long, and they are forwarded without inspection beyond their prefix.
		if !rewriter.inLong {
			if !isDataStatement(chunk) {
				rewriter.err = ErrInvalidDump
				return
			}
			rewriter.inLong = true
		}
		rewriter.pending = bytes.Clone(chunk)
	case err == nil || errors.Is(err, io.EOF):
		if errors.Is(err, io.EOF) {
			rewriter.err = io.EOF
		}
		if len(chunk) == 0 {
			return
		}
		if rewriter.inLong {
			rewriter.inLong = false
			rewriter.pending = bytes.Clone(chunk)
			return
		}
		out, rewriteErr := rewriter.rewriteLine(chunk)
		if rewriteErr != nil {
			rewriter.err = rewriteErr
			return
		}
		rewriter.pending = out
	default:
		rewriter.err = err
	}
}

func isDataStatement(line []byte) bool {
	return hasFoldPrefix(line, "INSERT ") || hasFoldPrefix(line, "REPLACE ")
}

func hasFoldPrefix(line []byte, prefix string) bool {
	return len(line) >= len(prefix) && strings.EqualFold(string(line[:len(prefix)]), prefix)
}

func (rewriter *DumpRewriter) rewriteLine(line []byte) ([]byte, error) {
	if isDataStatement(line) {
		return bytes.Clone(line), nil
	}
	body := string(line)
	eol := ""
	if strings.HasSuffix(body, "\n") {
		body = body[:len(body)-1]
		eol = "\n"
		if strings.HasSuffix(body, "\r") {
			body = body[:len(body)-1]
			eol = "\r\n"
		}
	}

	if databaseStatementStartRe.MatchString(body) {
		rewritten, err := rewriter.rewriteDatabaseStatement(body)
		if err != nil {
			return nil, err
		}
		return []byte(rewritten + eol), nil
	}
	if useStatementStartRe.MatchString(body) {
		match := useStatementRe.FindStringSubmatch(body)
		if match == nil || match[1] != rewriter.source {
			return nil, ErrInvalidDump
		}
		return []byte("USE " + quoteIdentifier(rewriter.target) + ";" + eol), nil
	}
	if clientCommandRe.MatchString(body) {
		return nil, ErrInvalidDump
	}
	qualifier := "`" + rewriter.source + "`."
	if strings.Contains(body, qualifier) {
		body = strings.ReplaceAll(body, qualifier, quoteIdentifier(rewriter.target)+".")
	}
	return []byte(body + eol), nil
}

func (rewriter *DumpRewriter) rewriteDatabaseStatement(body string) (string, error) {
	match := databaseStatementRe.FindStringSubmatch(body)
	if match == nil || len(body) > 4096 {
		return "", ErrInvalidDump
	}
	wrapper, verb, name, tail := match[1], strings.ToUpper(match[2]), match[3], match[4]
	if name != rewriter.source || strings.Contains(tail, "`") ||
		strings.Count(tail, ";") != 1 || !strings.HasSuffix(strings.TrimRight(tail, " \t"), ";") {
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
