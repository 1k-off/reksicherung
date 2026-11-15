package mysql

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func mustRead(t *testing.T, reader io.Reader) string {
	t.Helper()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRewriteSingleDatabaseDump(t *testing.T) {
	in := "CREATE DATABASE `app`;\nUSE `app`;\nCREATE TABLE t(i INT);\n"
	out, err := RewriteDump(strings.NewReader(in), "app", "reksicherung_abc")
	if err != nil {
		t.Fatal(err)
	}
	got := mustRead(t, out)
	if strings.Contains(got, "`app`") || !strings.Contains(got, "`reksicherung_abc`") {
		t.Fatalf("rewrite failed: %s", got)
	}
	if !strings.Contains(got, "CREATE DATABASE IF NOT EXISTS `reksicherung_abc`;") ||
		!strings.Contains(got, "USE `reksicherung_abc`;") || !strings.Contains(got, "CREATE TABLE t(i INT);") {
		t.Fatalf("unexpected output: %s", got)
	}
}

func TestRewriteMysqldumpStyleStatements(t *testing.T) {
	in := "-- dump\n" +
		"/*!40000 DROP DATABASE IF EXISTS `app`*/;\n" +
		"CREATE DATABASE /*!32312 IF NOT EXISTS*/ `app` /*!40100 DEFAULT CHARACTER SET utf8mb4 */ /*!80016 DEFAULT ENCRYPTION='N' */;\r\n" +
		"USE `app`;\n" +
		"CREATE TABLE `app` (\n  `app` int\n);\n" +
		"INSERT INTO `t` VALUES ('USE `other`;','`app`.x');\n" +
		"CREATE VIEW `v` AS select `app`.`t`.`i` AS `i` from `app`.`t`;\n"
	out, err := RewriteDump(strings.NewReader(in), "app", "reksicherung_x")
	if err != nil {
		t.Fatal(err)
	}
	got := mustRead(t, out)
	for _, want := range []string{
		"/*!40000 DROP DATABASE IF EXISTS `reksicherung_x`*/;\n",
		"CREATE DATABASE IF NOT EXISTS `reksicherung_x` /*!40100 DEFAULT CHARACTER SET utf8mb4 */ /*!80016 DEFAULT ENCRYPTION='N' */;\r\n",
		"USE `reksicherung_x`;\n",
		"CREATE TABLE `app` (\n  `app` int\n);\n", // table/column named like the source are untouched
		"INSERT INTO `t` VALUES ('USE `other`;','`app`.x');\n",
		"select `reksicherung_x`.`t`.`i` AS `i` from `reksicherung_x`.`t`;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRewriteRejectsSecondCreateDatabase(t *testing.T) {
	in := "CREATE DATABASE `app`;\nCREATE DATABASE `other`;\n"
	_, err := RewriteDump(strings.NewReader(in), "app", "reksicherung_abc")
	if !errors.Is(err, ErrInvalidDump) {
		t.Fatalf("err=%v", err)
	}
}

func TestRewriteFailsClosed(t *testing.T) {
	tests := map[string]string{
		"duplicate create":      "CREATE DATABASE `app`;\nCREATE DATABASE IF NOT EXISTS `app`;\n",
		"other use":             "USE `other`;\n",
		"unquoted use":          "USE app;\n",
		"other drop":            "/*!40000 DROP DATABASE IF EXISTS `other`*/;\n",
		"alter database":        "ALTER DATABASE `app` CHARACTER SET utf8mb4;\n",
		"unquoted create":       "CREATE DATABASE app;\n",
		"chained statement":     "CREATE DATABASE `app`; DROP DATABASE `x`;\n",
		"client system command": "system rm -rf /\n",
		"client backslash":      "\\! id\n",
		"client source":         "source /etc/passwd\n",
		"case mismatch":         "USE `App`;\n",
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := RewriteDump(strings.NewReader(in), "app", "reksicherung_abc"); !errors.Is(err, ErrInvalidDump) {
				t.Fatalf("err=%v, want ErrInvalidDump", err)
			}
		})
	}
}

func TestRewriteLongDataLineStreamsAndLongOtherLineRejected(t *testing.T) {
	long := "INSERT INTO `t` VALUES (" + strings.Repeat("1,", 100000) + "1);\n"
	out, err := RewriteDump(strings.NewReader(long+"USE `app`;\n"), "app", "reksicherung_abc")
	if err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, out); got != long+"USE `reksicherung_abc`;\n" {
		t.Fatal("long data line was altered")
	}
	other := "CREATE TABLE t (" + strings.Repeat("a INT,", 20000) + "z INT);\n"
	if _, err := RewriteDump(strings.NewReader(other), "app", "reksicherung_abc"); !errors.Is(err, ErrInvalidDump) {
		t.Fatalf("err=%v, want ErrInvalidDump", err)
	}
}

func TestRewriterEnforcesSizeLimit(t *testing.T) {
	rewriter, err := NewDumpRewriter(strings.NewReader(strings.Repeat("SELECT 1;\n", 100)), "app", "reksicherung_abc", 50)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, rewriter); !errors.Is(err, ErrDumpLimit) {
		t.Fatalf("err=%v, want ErrDumpLimit", err)
	}
}

func TestRewriterRejectsInvalidNames(t *testing.T) {
	for _, source := range []string{"", "a`b", "a b", strings.Repeat("a", 65)} {
		if _, err := NewDumpRewriter(strings.NewReader(""), source, "reksicherung_abc", 10); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("source %q err=%v", source, err)
		}
	}
}
