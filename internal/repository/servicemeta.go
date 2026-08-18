package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/prorochestvo/loginjector"
)

// ServiceMetaRepository stores small pieces of service state that belong to no domain
// entity — currently only when the database was last vacuumed.
type ServiceMetaRepository struct {
	db db
}

// NewServiceMetaRepository returns a repository for the service_meta key/value table.
func NewServiceMetaRepository(db db) (*ServiceMetaRepository, error) {
	return &ServiceMetaRepository{db: db}, nil
}

// Name returns the name of the underlying database table.
func (r *ServiceMetaRepository) Name() string { return serviceMetaTableName }

// CheckUP verifies that the repository can read from the service_meta table.
func (r *ServiceMetaRepository) CheckUP(ctx context.Context) error {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	var count int64
	cmd := "SELECT COUNT(*) FROM " + serviceMetaTableName + ";"
	if err = tx.QueryRowContext(ctx, cmd).Scan(&count); err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", cmd), loginjector.NewTraceError())
	}
	return nil
}

// ObtainServiceMeta reads one key. ok is false with a nil error when the key has never
// been written — "never vacuumed" is an ordinary state, not a failure.
func (r *ServiceMetaRepository) ObtainServiceMeta(ctx context.Context, key string) (value string, ok bool, err error) {
	tx, err := r.db.ReadOnlyTransaction(ctx)
	if err != nil {
		return "", false, errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	cmd := "SELECT " + serviceMetaValueFieldName +
		" FROM " + serviceMetaTableName +
		" WHERE " + serviceMetaKeyFieldName + " = ?;"

	switch scanErr := tx.QueryRowContext(ctx, cmd, key).Scan(&value); {
	case errors.Is(scanErr, sql.ErrNoRows):
		return "", false, nil
	case scanErr != nil:
		return "", false, errors.Join(scanErr, fmt.Errorf("SQL: %s", cmd), loginjector.NewTraceError())
	}
	return value, true, nil
}

// RetainServiceMeta writes one key, replacing any previous value.
func (r *ServiceMetaRepository) RetainServiceMeta(ctx context.Context, key, value string) error {
	if key == "" {
		return errors.Join(errors.New("service meta: key is empty"), loginjector.NewTraceError())
	}

	tx, err := r.db.Transaction(ctx)
	if err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	defer printRollbackError(tx)

	cmd := "INSERT INTO " + serviceMetaTableName +
		" (" + serviceMetaKeyFieldName + ", " + serviceMetaValueFieldName + ") VALUES (?, ?)" +
		" ON CONFLICT(" + serviceMetaKeyFieldName + ") DO UPDATE SET " +
		serviceMetaValueFieldName + " = excluded." + serviceMetaValueFieldName + ";"

	if _, err = tx.ExecContext(ctx, cmd, key, value); err != nil {
		return errors.Join(err, fmt.Errorf("SQL: %s", cmd), loginjector.NewTraceError())
	}

	if err = tx.Commit(); err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	return nil
}

const (
	serviceMetaTableName      = "service_meta"
	serviceMetaKeyFieldName   = "key"
	serviceMetaValueFieldName = "value"

	// ServiceMetaKeyLastVacuum records when VACUUM last completed. It gates the cadence,
	// so it is written only after a successful run.
	ServiceMetaKeyLastVacuum = "last_vacuum_at"
)
