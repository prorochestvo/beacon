package sqlitedb

import "strings"

// connectionOptions appends busy_timeout and foreign_keys as
// modernc.org/sqlite ?_pragma= query parameters, plus _txlock=immediate. The
// driver applies all three in its Open hook on every new connection the
// database/sql pool creates — the only way to make these per-connection settings
// hold across SetMaxOpenConns(N>1). busy_timeout is listed first so the 5-second
// retry window is in place before the foreign_keys=ON check (itself a candidate
// for busy-wait under write contention).
//
// _txlock=immediate is what makes that retry window reachable for writes. Without
// it a transaction opens DEFERRED: it begins as a reader and promotes to a writer
// at its first write statement, and SQLite refuses to invoke the busy handler on a
// promotion — two connections both waiting to promote would deadlock, so it
// returns SQLITE_BUSY at once instead. busy_timeout therefore never applies to
// that path, which is why the failures it caused landed in milliseconds rather
// than after five seconds of waiting. Taking the write lock at BEGIN is not a
// promotion, so the busy handler runs and the wait becomes real.
//
// The driver applies the begin mode only when sql.TxOptions.ReadOnly is false, so
// ReadOnlyTransaction still opens a plain deferred BEGIN and readers continue to
// run concurrently with a writer under WAL.
//
// journal_mode is not appended here: it is persisted in the database file header
// and set once via db.Exec in the NewSQLiteClientEx path.
func connectionOptions(path string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate"
}
