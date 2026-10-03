package store

import (
	"reflect"
	"testing"
)

// ------------------------------------------------------------------------------
// TestSplitStatements: a ";" inside a quoted string or a comment does not end a statement; comments are dropped.
func TestSplitStatements(test *testing.T) {
	script := "-- a comment; with a semicolon\n" +
		"CREATE TABLE a (x VARCHAR(8) DEFAULT 'a;b');\n" +
		"INSERT INTO a VALUES ('it''s; fine'), (\"q;\"), ('back\\\\'';x');\n" +
		"CREATE TABLE `odd;name` (y INT); -- trailing\n" +
		"  ;\n"
	want := []string{
		"CREATE TABLE a (x VARCHAR(8) DEFAULT 'a;b')",
		"INSERT INTO a VALUES ('it''s; fine'), (\"q;\"), ('back\\\\'';x')",
		"CREATE TABLE `odd;name` (y INT)",
	}
	if got := splitStatements(script); !reflect.DeepEqual(got, want) {
		test.Fatalf("got %q\nwant %q", got, want)
	}
}
