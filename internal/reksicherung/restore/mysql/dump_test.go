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

func TestRewriteBypassShapesFailClosed(t *testing.T) {
	tests := map[string]string{
		"leading space use":            "  USE `other`;\n",
		"leading tab create":           "\tCREATE DATABASE `other`;\n",
		"leading block comment use":    "/* c */ USE `other`;\n",
		"leading line comment same":    "-- c\nUSE other;\n",
		"hash comment then use":        "# c\n USE `other`;\n",
		"conditional use":              "/*!40000 USE `other`*/;\n",
		"conditional create other":     "/*!50000 CREATE DATABASE `other` */;\n",
		"create or replace":            "CREATE OR REPLACE DATABASE `app`;\n",
		"create or replace spaced":     "  create   or\n replace schema `other`;\n",
		"split create database":        "CREATE\nDATABASE `other`;\n",
		"split database name":          "CREATE DATABASE\n`other`;\n",
		"split use":                    "USE\n`other`;\n",
		"split drop schema":            "DROP\nSCHEMA `other`;\n",
		"alter schema indented":        "   ALTER SCHEMA `app` CHARACTER SET utf8mb4;\n",
		"comment inside create":        "CREATE /* x */ DATABASE `app`;\n",
		"statement after statement":    "CREATE TABLE t (a INT); USE `other`;\n",
		"statement after insert":       "INSERT INTO t VALUES (1); USE `other`;\n",
		"create after insert":          "INSERT INTO t VALUES (1);CREATE DATABASE `other`;\n",
		"next line after insert":       "INSERT INTO t VALUES (1);\n  USE other;\n",
		"replace then use":             "REPLACE INTO t VALUES (1); USE `other`;\n",
		"insert other qualifier":       "INSERT INTO `other`.`t` VALUES (1);\n",
		"insert unquoted other":        "INSERT INTO other.t VALUES (1);\n",
		"select other qualifier":       "SELECT * FROM `other`.`t`;\n",
		"unquoted other":               "SELECT * FROM other.t;\n",
		"spaced qualifier":             "SELECT * FROM `other` . `t`;\n",
		"comment qualifier":            "SELECT * FROM `other`/**/.`t`;\n",
		"create table like other":      "CREATE TABLE a LIKE `other`.`b`;\n",
		"case mismatch qualifier":      "SELECT * FROM `App`.`t`;\n",
		"three part other":             "SELECT `other`.`t`.`i` FROM x;\n",
		"backslash command mid line":   "SELECT 1; \\! id\n",
		"backslash command after stmt": "SELECT 1;\\. /etc/passwd\n",
		"prepare":                      "PREPARE s FROM 'select 1';\n",
		"execute":                      "EXECUTE s;\n",
		"delimiter other":              "DELIMITER //\n",
		"delimiter not line start":     "SELECT 1; DELIMITER //\n",
		"ansi quotes":                  "SET sql_mode='ANSI_QUOTES';\n",
		"ansi mode":                    "/*!40101 SET SQL_MODE='ANSI' */;\n",
		"no backslash escapes":         "SET @@SESSION.sql_mode = 'NO_BACKSLASH_ESCAPES';\n",
		"sql_mode from variable":       "SET @a='ANSI_QUOTES'; SET sql_mode=@a;\n",
		"sql_mode expression":          "SET sql_mode=CONCAT('ANSI','_QUOTES');\n",
		"old sql mode poisoned":        "SET @OLD_SQL_MODE='ANSI_QUOTES';\n",
		"system after comment":         "/* c */ system id\n",
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := RewriteDump(strings.NewReader(in), "app", "reksicherung_abc"); !errors.Is(err, ErrInvalidDump) {
				t.Fatalf("err=%v, want ErrInvalidDump", err)
			}
		})
	}
}

func TestRewriteBypassShapesAcceptedForms(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"split create", "CREATE\nDATABASE `app`;\n", "CREATE DATABASE IF NOT EXISTS `reksicherung_abc`;\n"},
		{"indented use", "  USE `app`;\n", "  USE `reksicherung_abc`;\n"},
		{"comment before use", "/* c */ USE `app`;\n", "/* c */ USE `reksicherung_abc`;\n"},
		{"insert qualifier", "INSERT INTO `app`.`t` VALUES (1);\n", "INSERT INTO `reksicherung_abc`.`t` VALUES (1);\n"},
		{"replace qualifier", "REPLACE INTO `app` . `t` VALUES (1);\n", "REPLACE INTO `reksicherung_abc` . `t` VALUES (1);\n"},
		{"unquoted source", "SELECT * FROM app.t;\n", "SELECT * FROM `reksicherung_abc`.t;\n"},
		{"string content untouched", "INSERT INTO t VALUES ('a;USE `other`;', 'other.t');\n", "INSERT INTO t VALUES ('a;USE `other`;', 'other.t');\n"},
		{"escaped quote in string", "INSERT INTO t VALUES ('a\\';USE other;');\n", "INSERT INTO t VALUES ('a\\';USE other;');\n"},
		{"multi-line string", "INSERT INTO t VALUES ('x\nUSE `other`;\n');\n", "INSERT INTO t VALUES ('x\nUSE `other`;\n');\n"},
		{"comment content", "-- USE `other`;\n/* USE `other`; */\nSELECT 1;\n", "-- USE `other`;\n/* USE `other`; */\nSELECT 1;\n"},
		{"numbers and variables", "SET @@SESSION.SQL_LOG_BIN= 0;\nINSERT INTO t VALUES (1.5,-2.5e-3,0x1F);\n",
			"SET @@SESSION.SQL_LOG_BIN= 0;\nINSERT INTO t VALUES (1.5,-2.5e-3,0x1F);\n"},
		{"trigger pseudo rows", "CREATE TRIGGER x BEFORE INSERT ON t FOR EACH ROW SET NEW.a = OLD.b;\n",
			"CREATE TRIGGER x BEFORE INSERT ON t FOR EACH ROW SET NEW.a = OLD.b;\n"},
		{"sql mode dump", "/*!40101 SET @OLD_SQL_MODE=@@SQL_MODE, SQL_MODE='NO_AUTO_VALUE_ON_ZERO' */;\nSET SQL_MODE=@OLD_SQL_MODE;\n",
			"/*!40101 SET @OLD_SQL_MODE=@@SQL_MODE, SQL_MODE='NO_AUTO_VALUE_ON_ZERO' */;\nSET SQL_MODE=@OLD_SQL_MODE;\n"},
		{"delimiter triggers", "DELIMITER ;;\nSELECT 1;;\nDELIMITER ;\n", "DELIMITER ;;\nSELECT 1;;\nDELIMITER ;\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out, err := RewriteDump(strings.NewReader(test.in), "app", "reksicherung_abc")
			if err != nil {
				t.Fatal(err)
			}
			if got := mustRead(t, out); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestRewriteLongInsertLineMultiStatementFailsClosed(t *testing.T) {
	long := "INSERT INTO `t` VALUES (" + strings.Repeat("'a',", 40000) + "1); USE `other`;\n"
	if _, err := RewriteDump(strings.NewReader(long), "app", "reksicherung_abc"); !errors.Is(err, ErrInvalidDump) {
		t.Fatalf("err=%v, want ErrInvalidDump", err)
	}
	// A comment opener split across the read-buffer boundary must still be seen.
	prefix := "INSERT INTO t VALUES (1); " + strings.Repeat(" ", dumpReadBufferBytes-len("INSERT INTO t VALUES (1); ")-1)
	boundary := prefix + "/* x */ USE `other`;\n"
	if _, err := RewriteDump(strings.NewReader(boundary), "app", "reksicherung_abc"); !errors.Is(err, ErrInvalidDump) {
		t.Fatalf("boundary err=%v, want ErrInvalidDump", err)
	}
}
