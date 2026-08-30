// Package repository implements SQLite-backed persistence for all domain types.
// Each repository serialises access through the *sqlitedb.DB transaction helper.
package repository

import (
	"database/sql"
	"errors"
	"log"

	"github.com/prorochestvo/loginjector"
)

// printRollbackError rolls back tx and logs any failure that is not
// sql.ErrTxDone (which simply means the transaction was already committed
// or rolled back — the expected outcome on the success path).
func printRollbackError(r interface{ Rollback() error }) {
	if err := r.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		err = errors.Join(err, loginjector.NewTraceError())
		log.Print(err)
	}
}
