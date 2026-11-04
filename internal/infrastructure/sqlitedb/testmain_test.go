package sqlitedb

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMain pre-initialises the modernc.org/sqlite driver before any parallel
// test goroutines open their own connections.
// See internal/repository/testmain_test.go for a detailed explanation.
func TestMain(m *testing.M) {
	primeDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		panic("sqlitedb TestMain: prime sqlite open: " + err.Error())
	}
	if err = primeDB.PingContext(context.Background()); err != nil {
		panic("sqlitedb TestMain: prime sqlite ping: " + err.Error())
	}
	_ = primeDB.Close()

	os.Exit(m.Run())
}
