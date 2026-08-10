package collection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/prorochestvo/loginjector"
	"github.com/seilbekskindirov/beacon/internal/repository"
)

const (
	// DefaultHotWindow is how much history the hot tables keep. Everything older is moved
	// to their archive twins, which keep it forever.
	//
	// 180 days is the product requirement, not a tuning knob discovered by measurement:
	// the dashboard's longest chart period is 360 days and is served from the union of
	// both tiers, so this number bounds the working set without bounding what a user can
	// ask for.
	DefaultHotWindow = 180 * 24 * time.Hour

	// DefaultArchiveRetention is how much history the archive keeps. Zero means forever,
	// which is what beacon runs: the archive exists precisely so nothing is ever lost.
	//
	// It is a compile-time constant rather than an environment variable on purpose. It is
	// the only setting in the project that can destroy history, and a value that can only
	// change through a reviewed commit is harder to get wrong than one that can change
	// through a typo in an env file.
	DefaultArchiveRetention time.Duration = 0

	// DefaultVacuumInterval is the minimum gap between VACUUM runs.
	//
	// VACUUM is what turns the roll-over's deletes into free space the operating system
	// can see — SQLite otherwise keeps the freed pages inside the file forever. It is also
	// the most expensive thing this agent does: it rebuilds the database into a temporary
	// copy, so it needs transient free space on the order of the file's own size. Weekly
	// keeps that cost rare while still reclaiming pages long before they accumulate.
	DefaultVacuumInterval = 7 * 24 * time.Hour
)

// NewMaintenanceAgent constructs a MaintenanceAgent. meta and db are required and at least
// one tiered repository must be given; zero values for the three durations fall back to
// the defaults above, except retention, where zero is a meaningful value (keep forever)
// and is passed through unchanged.
func NewMaintenanceAgent(
	meta metaRepository,
	db vacuumer,
	hotWindow, retention, vacuumInterval time.Duration,
	logger io.Writer,
	tables ...tieredRepository,
) (*MaintenanceAgent, error) {
	if meta == nil || db == nil {
		return nil, errors.New("maintenance agent: meta repository and database are both required")
	}
	if len(tables) == 0 {
		return nil, errors.New("maintenance agent: at least one tiered repository is required")
	}
	for i, t := range tables {
		if t == nil {
			return nil, fmt.Errorf("maintenance agent: tiered repository at index %d is nil", i)
		}
	}
	if hotWindow <= 0 {
		hotWindow = DefaultHotWindow
	}
	if retention < 0 {
		retention = DefaultArchiveRetention
	}
	if vacuumInterval <= 0 {
		vacuumInterval = DefaultVacuumInterval
	}
	if logger == nil {
		logger = io.Discard
	}
	return &MaintenanceAgent{
		meta:           meta,
		db:             db,
		tables:         tables,
		hotWindow:      hotWindow,
		retention:      retention,
		vacuumInterval: vacuumInterval,
		now:            time.Now,
		logger:         logger,
	}, nil
}

// MaintenanceAgent bounds the hot tables and reclaims the space that frees.
//
// It runs after collection on each tick, in three ordered steps: move aged rows into the
// archive twins, apply archive retention if any is configured, then vacuum on a cadence.
// The order is what makes the third step worth anything — VACUUM reclaims what the first
// two freed, so running it first would compact a file that is about to be perforated again.
type MaintenanceAgent struct {
	meta           metaRepository
	db             vacuumer
	tables         []tieredRepository
	hotWindow      time.Duration
	retention      time.Duration
	vacuumInterval time.Duration
	now            func() time.Time
	logger         io.Writer
}

// Run performs one maintenance pass.
//
// It stops at the first error. Each step is independently idempotent and resumable — the
// roll-over's predicate is a timestamp, not a cursor, so an aborted pass simply moves the
// same rows next tick — which means giving up early costs nothing but a tick's delay, and
// pressing on past a failure risks vacuuming a file whose roll-over half-completed.
func (a *MaintenanceAgent) Run(ctx context.Context) (MaintenanceReport, error) {
	report := MaintenanceReport{Moved: map[string]int64{}, Pruned: map[string]int64{}}

	now := a.now().UTC()
	hotCutoff := now.Add(-a.hotWindow)

	// A zero cutoff means "no window" to the repository layer, which is how retention off
	// is expressed. Compute it only when retention is actually configured so the zero
	// value cannot be produced by arithmetic and mistaken for a real cutoff.
	var archiveCutoff time.Time
	if a.retention > 0 {
		archiveCutoff = now.Add(-a.retention)
	}

	for _, table := range a.tables {
		moved, err := table.RolloverToArchive(ctx, hotCutoff)
		if err != nil {
			return report, errors.Join(
				fmt.Errorf("maintenance: rollover %s: %w", table.Name(), err),
				loginjector.NewTraceError(),
			)
		}
		if moved > 0 {
			report.Moved[table.Name()] = moved
		}

		pruned, err := table.PruneArchive(ctx, archiveCutoff)
		if err != nil {
			return report, errors.Join(
				fmt.Errorf("maintenance: prune %s: %w", table.Name(), err),
				loginjector.NewTraceError(),
			)
		}
		if pruned > 0 {
			report.Pruned[table.Name()] = pruned
		}
	}

	vacuumed, err := a.maybeVacuum(ctx, now)
	if err != nil {
		return report, err
	}
	report.Vacuumed = vacuumed

	if total := report.Total(); total > 0 {
		fmt.Fprintf(a.logger, "maintenance: archived %d row(s) past the %s hot window\n", total, a.hotWindow)
	}
	for name, n := range report.Pruned {
		fmt.Fprintf(a.logger, "maintenance: pruned %d row(s) from the %s archive\n", n, name)
	}
	if vacuumed {
		fmt.Fprintln(a.logger, "maintenance: vacuumed the database")
	}

	return report, nil
}

// maybeVacuum runs VACUUM when the recorded interval has elapsed, and reports whether it
// did.
//
// Three details are load-bearing:
//
//   - The stamp is written only after VACUUM succeeds. A run that loses the write lock to
//     the web or notifier process must retry on the next tick; stamping first would turn a
//     transient SQLITE_BUSY into a skipped week.
//   - A missing stamp means "never vacuumed" and runs immediately, so a fresh deployment
//     does not wait an interval before its first reclaim.
//   - An unparseable stamp is an error rather than a silent re-vacuum. VACUUM is expensive
//     enough that quietly running it every tick because a value got corrupted is worse
//     than surfacing the corruption.
func (a *MaintenanceAgent) maybeVacuum(ctx context.Context, now time.Time) (bool, error) {
	raw, ok, err := a.meta.ObtainServiceMeta(ctx, repository.ServiceMetaKeyLastVacuum)
	if err != nil {
		return false, errors.Join(fmt.Errorf("maintenance: read vacuum stamp: %w", err), loginjector.NewTraceError())
	}
	if ok {
		last, parseErr := time.Parse(time.RFC3339, raw)
		if parseErr != nil {
			return false, errors.Join(
				fmt.Errorf("maintenance: vacuum stamp %q is not RFC3339: %w", raw, parseErr),
				loginjector.NewTraceError(),
			)
		}
		if now.Sub(last) < a.vacuumInterval {
			return false, nil
		}
	}

	if err = a.db.Vacuum(ctx); err != nil {
		return false, errors.Join(fmt.Errorf("maintenance: vacuum: %w", err), loginjector.NewTraceError())
	}

	if err = a.meta.RetainServiceMeta(ctx, repository.ServiceMetaKeyLastVacuum, now.Format(time.RFC3339)); err != nil {
		return false, errors.Join(fmt.Errorf("maintenance: record vacuum stamp: %w", err), loginjector.NewTraceError())
	}
	return true, nil
}

// MaintenanceReport is what one pass did.
type MaintenanceReport struct {
	// Moved counts rows relocated from a hot table into its archive twin, by table name.
	Moved map[string]int64
	// Pruned counts rows deleted from an archive twin, by table name. Empty when retention
	// is off, which is the configured state.
	Pruned map[string]int64
	// Vacuumed reports whether this pass ran VACUUM and recorded its stamp.
	Vacuumed bool
}

// Total is the number of rows the pass moved.
func (r MaintenanceReport) Total() int64 {
	var total int64
	for _, n := range r.Moved {
		total += n
	}
	return total
}

// tieredRepository is one table pair the maintenance pass keeps bounded.
type tieredRepository interface {
	RolloverToArchive(ctx context.Context, cutoff time.Time) (int64, error)
	PruneArchive(ctx context.Context, cutoff time.Time) (int64, error)
	Name() string
}

// metaRepository stores the VACUUM cadence stamp.
type metaRepository interface {
	ObtainServiceMeta(ctx context.Context, key string) (string, bool, error)
	RetainServiceMeta(ctx context.Context, key, value string) error
}

// vacuumer runs VACUUM. It is the raw SQLite client rather than a repository because
// VACUUM is a whole-file operation that SQLite rejects inside a transaction, which is the
// only thing repositories offer.
type vacuumer interface {
	Vacuum(ctx context.Context) error
}
