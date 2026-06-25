package sqlitedb

import "strings"

// connectionOptions appends busy_timeout and foreign_keys as
// modernc.org/sqlite ?_pragma= query parameters. The driver applies them in its
// Open hook on every new connection the database/sql pool creates — the only way
// to make these per-connection settings hold across SetMaxOpenConns(N>1).
// busy_timeout is listed first so the 5-second retry window is in place before
// the foreign_keys=ON check (itself a candidate for busy-wait under write contention).
//
// journal_mode is not appended here: it is persisted in the database file header
// and set once via db.Exec in the NewSQLiteClientEx path.
func connectionOptions(path string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
}
