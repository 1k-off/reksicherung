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

func TestRewriteSetStatementsFailClosed(t *testing.T) {
	tests := map[string]string{
		// Other-database qualifiers / unknown targets.
		"set qualified sysvar":       "SET other.x = 1;\n",
		"set qualified rhs":          "SET @a = other.t;\n",
		"set select subquery":        "SET @a = (SELECT 1 FROM `other`.`t`);\n",
		"set backtick":               "SET @a = `other`.`t`;\n",
		"set unknown variable":       "SET foo = 1;\n",
		"set global":                 "SET GLOBAL sql_mode = '';\n",
		"set global sysvar":          "SET @@GLOBAL.sql_mode = '';\n",
		"set persist":                "SET PERSIST max_allowed_packet = 1;\n",
		"set password":               "SET PASSWORD = 'x';\n",
		"set gtid purged":            "SET @@GLOBAL.GTID_PURGED='abc:1';\n",
		"set comment inside":         "SET /* c */ @a = 1;\n",
		"set conditional inside":     "SET /*!40101 NAMES gbk */;\n",
		"set double wrapper":         "/*!40101 SET NAMES utf8 */ /*!40101 SET NAMES gbk */;\n",
		"set mariadb wrapper":        "/*M!100100 SET NAMES gbk */;\n",
		"set hash comment":           "SET @a = 1 # x\n;\n",
		"set double quoted":          "SET @a = \"x\";\n",
		"set expression":             "SET @a = 1 + 1;\n",
		"set subselect user var":     "SET @a = @b, @c = other.d;\n",
		"set trailing empty item":    "SET @a = 1,;\n",
		"set backslash string":       "SET @a = 'x\\';\n",
		"saved mode from literal":    "SET @saved_sql_mode = 'ANSI_QUOTES';\n",
		"sysvar rhs not allowlisted": "SET @a = @@GLOBAL.sql_mode;\n",
		// Charset desync.
		"names gbk":                "SET NAMES gbk;\n",
		"names sjis":               "SET NAMES 'sjis';\n",
		"names big5 collate":       "SET NAMES big5 COLLATE big5_chinese_ci;\n",
		"names allowed bad collat": "SET NAMES utf8mb4 COLLATE gbk_chinese_ci;\n",
		"names default":            "SET NAMES DEFAULT;\n",
		"names conditional gbk":    "/*!40101 SET NAMES gbk */;\n",
		"names versioned gbk":      "/*!50503 SET NAMES gbk */;\n",
		"names later in list":      "SET @a = 1, NAMES gbk;\n",
		"character set gbk":        "SET CHARACTER SET gbk;\n",
		"charset gbk":              "SET CHARSET gbk;\n",
		"character set cond":       "/*!40101 SET CHARACTER SET cp932 */;\n",
		"client gbk":               "SET character_set_client = gbk;\n",
		"client session gbk":       "SET SESSION character_set_client = 'gbk';\n",
		"client at at gbk":         "SET @@character_set_client = gbk;\n",
		"client cond gbk":          "/*!40101 SET character_set_client = gbk */;\n",
		"connection gbk":           "SET character_set_connection = sjis;\n",
		"connection session gbk":   "SET @@session.character_set_connection = gb18030;\n",
		"results gbk":              "SET character_set_results = gbk;\n",
		"collation connection gbk": "SET collation_connection = gbk_chinese_ci;\n",
		"collation cond gbk":       "/*!40101 SET collation_connection = 'sjis_bin' */;\n",
		"charset uppercase gbk":    "SET NAMES GBK;\n",
		"charset hex":              "SET character_set_client = 0x67626b;\n",
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := RewriteDump(strings.NewReader(in), "app", "reksicherung_abc"); !errors.Is(err, ErrInvalidDump) {
				t.Fatalf("err=%v, want ErrInvalidDump", err)
			}
		})
	}
}

func TestRewriteSetStatementsAcceptedForms(t *testing.T) {
	passthrough := []string{
		"SET NAMES utf8mb4;\n",
		"SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci;\n",
		"SET names 'latin1';\n",
		"SET NAMES binary;\n",
		"SET NAMES ascii;\n",
		"SET NAMES utf8;\n",
		"SET NAMES utf8mb3;\n",
		"SET CHARACTER SET utf8mb4;\n",
		"/*!40101 SET NAMES utf8mb4 */;\n",
		"/*!50503 SET NAMES utf8mb4 */;\n",
		"/*!40101 SET @OLD_CHARACTER_SET_CLIENT=@@CHARACTER_SET_CLIENT */;\n",
		"/*!40101 SET @OLD_CHARACTER_SET_RESULTS=@@CHARACTER_SET_RESULTS */;\n",
		"/*!40101 SET @OLD_COLLATION_CONNECTION=@@COLLATION_CONNECTION */;\n",
		"/*!40101 SET @saved_cs_client     = @@character_set_client */;\n",
		"/*!50503 SET character_set_client = utf8mb4 */;\n",
		"/*!40103 SET @OLD_TIME_ZONE=@@TIME_ZONE */;\n",
		"/*!40103 SET TIME_ZONE='+00:00' */;\n",
		"/*!40103 SET TIME_ZONE=@OLD_TIME_ZONE */;\n",
		"/*!40014 SET @OLD_UNIQUE_CHECKS=@@UNIQUE_CHECKS, UNIQUE_CHECKS=0 */;\n",
		"/*!40014 SET @OLD_FOREIGN_KEY_CHECKS=@@FOREIGN_KEY_CHECKS, FOREIGN_KEY_CHECKS=0 */;\n",
		"/*!40101 SET @OLD_SQL_MODE=@@SQL_MODE, SQL_MODE='NO_AUTO_VALUE_ON_ZERO' */;\n",
		"/*!40111 SET @OLD_SQL_NOTES=@@SQL_NOTES, SQL_NOTES=0 */;\n",
		"/*!40014 SET UNIQUE_CHECKS=@OLD_UNIQUE_CHECKS */;\n",
		"/*!40111 SET SQL_NOTES=@OLD_SQL_NOTES */;\n",
		"SET @@SESSION.SQL_LOG_BIN= 0;\n",
		"SET autocommit=0;\n",
		"SET sql_mode = 'ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION';\n",
		"SET @@session.sql_mode='NO_ENGINE_SUBSTITUTION';\n",
		"SET SQL_MODE=@OLD_SQL_MODE;\n",
	}
	for _, in := range passthrough {
		t.Run(strings.TrimSpace(in), func(t *testing.T) {
			out, err := RewriteDump(strings.NewReader(in), "app", "reksicherung_abc")
			if err != nil {
				t.Fatal(err)
			}
			if got := mustRead(t, out); got != in {
				t.Fatalf("got %q, want %q", got, in)
			}
		})
	}
	rewritten := []struct{ name, in, want string }{
		{"restore client charset", "/*!40101 SET character_set_client = @saved_cs_client */;\n",
			"/*!40101 SET @reksicherung_noop=0 */;\n"},
		{"restore old client", "/*!40101 SET CHARACTER_SET_CLIENT=@OLD_CHARACTER_SET_CLIENT */;\n",
			"/*!40101 SET @reksicherung_noop=0 */;\n"},
		{"restore old results", "/*!40101 SET CHARACTER_SET_RESULTS=@OLD_CHARACTER_SET_RESULTS */;\n",
			"/*!40101 SET @reksicherung_noop=0 */;\n"},
		{"restore old collation", "/*!40101 SET COLLATION_CONNECTION=@OLD_COLLATION_CONNECTION */;\n",
			"/*!40101 SET @reksicherung_noop=0 */;\n"},
		{"restore plain", "SET character_set_connection = @x;\n", "SET @reksicherung_noop=0;\n"},
		{"restore in list", "SET @a = 1, character_set_client = @a;\n", "SET @a = 1, @reksicherung_noop=0;\n"},
	}
	for _, test := range rewritten {
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

func TestRewriteSetAtEOFWithoutSemicolon(t *testing.T) {
	if _, err := RewriteDump(strings.NewReader("SET NAMES gbk"), "app", "reksicherung_abc"); !errors.Is(err, ErrInvalidDump) {
		t.Fatalf("err=%v, want ErrInvalidDump", err)
	}
	out, err := RewriteDump(strings.NewReader("SET NAMES utf8mb4"), "app", "reksicherung_abc")
	if err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, out); got != "SET NAMES utf8mb4" {
		t.Fatalf("got %q", got)
	}
}

func TestRewriteControlByteCommentDesyncFailsClosed(t *testing.T) {
	tests := map[string]string{
		// Server treats "--" + control byte as a comment, the client does not.
		"dashdash soh then use":   "SELECT 1; --\x01\nUSE `other`;\n",
		"dashdash soh semicolon":  "SELECT 1 --\x01 ; USE `other`;\n",
		"dashdash nul":            "SELECT 1; --\x00 ; USE `other`;\n",
		"dashdash del":            "SELECT 1; --\x7f ; USE `other`;\n",
		"dashdash escape":         "SELECT 1; --\x1b; USE `other`;\n",
		"control in code":         "SELECT\x01 1;\n",
		"nul in code":             "SELECT 1\x00;\n",
		"del in code":             "SELECT 1\x7f;\n",
		"control after statement": "SELECT 1;\x02USE `other`;\n",
		"dashdash eof control":    "SELECT 1 --\x01",
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := RewriteDump(strings.NewReader(in), "app", "reksicherung_abc"); !errors.Is(err, ErrInvalidDump) {
				t.Fatalf("err=%v, want ErrInvalidDump", err)
			}
		})
	}
}

func TestRewriteControlBytesAllowedInsideStringsAndComments(t *testing.T) {
	tests := []string{
		"INSERT INTO t VALUES ('a\x00b\x01c\x7f');\n",
		"-- comment \x01 with control\nSELECT 1;\n",
		"-- \x01\nSELECT 1;\n",
		"# \x01 hash\nSELECT 1;\n",
		"/* \x01 */ SELECT 1;\n",
		"SELECT 1; --\tcomment;\nSELECT 2;\n",
		"SELECT 1; --\r\nSELECT 2;\n",
		"SELECT 1;\n--\n",
		"SELECT 1;\n--",
	}
	for _, in := range tests {
		t.Run(strings.TrimSpace(in), func(t *testing.T) {
			out, err := RewriteDump(strings.NewReader(in), "app", "reksicherung_abc")
			if err != nil {
				t.Fatal(err)
			}
			if got := mustRead(t, out); got != in {
				t.Fatalf("got %q, want %q", got, in)
			}
		})
	}
}

func TestRewriteDashDashControlAcrossChunkBoundary(t *testing.T) {
	// "--" followed by a control byte right at the read-buffer boundary.
	head := "INSERT INTO t VALUES (1); "
	prefix := head + strings.Repeat(" ", dumpReadBufferBytes-len(head)-2)
	in := prefix + "--\x01 ; USE `other`;\n"
	if _, err := RewriteDump(strings.NewReader(in), "app", "reksicherung_abc"); !errors.Is(err, ErrInvalidDump) {
		t.Fatalf("err=%v, want ErrInvalidDump", err)
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
