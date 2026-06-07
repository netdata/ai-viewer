package presenter

import (
	"context"
	"database/sql"
	"errors"
)

// CheckSchema verifies the SQLite store's schema_meta.version row
// matches expectedVersion. Returns ErrSchemaMismatch wrapped with
// context when the row is absent or carries a different version. The
// caller (the serve binary's main) surfaces the error with an exit
// code so the operator sees a clear failure.
func CheckSchema(ctx context.Context, db *sql.DB, expectedVersion int) error {
	if db == nil {
		return errors.New("presenter.CheckSchema: nil db")
	}
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT value FROM schema_meta WHERE key='version'`).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.Join(ErrSchemaMismatch, errors.New("schema_meta.version row missing"))
		}
		return errors.Join(ErrSchemaMismatch, err)
	}
	var v int
	for _, c := range raw {
		if c < '0' || c > '9' {
			return errors.Join(ErrSchemaMismatch, errors.New("schema_meta.version is non-numeric"))
		}
		v = v*10 + int(c-'0')
	}
	if v != expectedVersion {
		return errors.Join(ErrSchemaMismatch, &schemaVersionError{got: v, want: expectedVersion})
	}
	return nil
}

// schemaVersionError carries the structured numbers behind
// ErrSchemaMismatch so the operator-facing log line shows both sides
// of the mismatch.
type schemaVersionError struct {
	got, want int
}

func (e *schemaVersionError) Error() string {
	return "schema_meta.version is " + itoa(e.got) + ", want " + itoa(e.want)
}

// itoa is a tiny stand-in for strconv.Itoa so the file does not pull in
// strconv solely for two error strings.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
