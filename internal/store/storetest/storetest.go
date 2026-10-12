//
// OpenMCX Licence Information : AGPL-3.0-or-later
//

// Package storetest gives tests a store on a throwaway MySQL database (tools/test starts the server and sets
// MCXADMIN_TEST_MYSQL); without it the test is skipped.
//
// OpenMCX administration (no 3GPP specification).
package storetest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql" // the MySQL driver of sql.Open

	"github.com/openMCX/openmcx-admin/internal/store"
)

// ------------------------------------------------------------------------------
// Open creates a database of a random name, opens the store on it (migrations applied) and drops the database when
// the test ends.
func Open(test *testing.T) (*store.Store, *sql.DB) {
	test.Helper()
	dsn := os.Getenv("MCXADMIN_TEST_MYSQL")
	if dsn == "" {
		test.Skip("MCXADMIN_TEST_MYSQL not set")
	}
	name := "t_" + hex.EncodeToString([]byte(rand.Text())[:4])
	root, err := sql.Open("mysql", dsn)
	if err != nil {
		test.Fatal(err)
	}
	if _, err := root.Exec("CREATE DATABASE " + name); err != nil {
		test.Fatal(err)
	}
	test.Cleanup(func() {
		if _, err := root.Exec("DROP DATABASE " + name); err != nil {
			test.Errorf("drop %s: %v", name, err)
		}
		if err := root.Close(); err != nil {
			test.Errorf("close: %v", err)
		}
	})
	db, err := sql.Open("mysql", strings.Replace(dsn, ")/", ")/"+name, 1))
	if err != nil {
		test.Fatal(err)
	}
	// Registered after the drop, so it runs first.
	test.Cleanup(func() {
		if err := db.Close(); err != nil {
			test.Errorf("close %s: %v", name, err)
		}
	})
	st, err := store.Open(context.Background(), db)
	if err != nil {
		test.Fatal(err)
	}
	return st, db
}
