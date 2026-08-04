package collection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/prorochestvo/loginjector"
	"github.com/seilbekskindirov/beacon/internal/domain"
)

// DefaultArchiveBatchSize is how many rows one reconciliation pass copies per round trip.
// Small enough that a first run over a full hot tier never holds more than a few hundred
// kilobytes of rows in memory, large enough that steady state — roughly 200 new rate
// values and 200 new execution records a day against a tick every few minutes — finishes
// in a single batch per table.
const DefaultArchiveBatchSize = 500

// archiveSourceRepository is the hot tier's read surface for reconciliation.
type archiveSourceRepository interface {
	ObtainRateValuesAfter(ctx context.Context, after time.Time, afterID string, limit int64) ([]domain.RateValue, error)
}

// archiveTargetRepository is the archive tier's write surface for reconciliation.
type archiveTargetRepository interface {
	RetainRateValues(ctx context.Context, records []domain.RateValue) (int, error)
	ObtainArchiveWatermark(ctx context.Context) (time.Time, string, bool, error)
}

// archiveMetaSourceRepository reads the hot tier's source definitions.
type archiveMetaSourceRepository interface {
	ObtainAllRateSources(ctx context.Context) ([]domain.RateSource, error)
}

// archiveMetaTargetRepository writes the archive tier's source mirror.
type archiveMetaTargetRepository interface {
	RetainRateSources(ctx context.Context, records []domain.RateSource) (int, error)
}

// archiveHistorySourceRepository is the hot tier's execution-history read surface.
type archiveHistorySourceRepository interface {
	ObtainExecutionHistoryAfter(ctx context.Context, after time.Time, afterID string, limit int64) ([]domain.ExecutionHistory, error)
}

// archiveHistoryTargetRepository is the archive tier's execution-history write surface.
type archiveHistoryTargetRepository interface {
	RetainExecutionHistories(ctx context.Context, records []domain.ExecutionHistory) (int, error)
	ObtainArchiveWatermark(ctx context.Context) (time.Time, string, bool, error)
}

// NewArchiveAgent constructs an ArchiveAgent. Every repository is required; the hot set
// and the archive set must be opened on different databases. batchSize falls back to
// DefaultArchiveBatchSize when not positive.
func NewArchiveAgent(
	hot archiveSourceRepository,
	archive archiveTargetRepository,
	hotMeta archiveMetaSourceRepository,
	archiveMeta archiveMetaTargetRepository,
	hotHistory archiveHistorySourceRepository,
	archiveHistory archiveHistoryTargetRepository,
	batchSize int,
	logger io.Writer,
) (*ArchiveAgent, error) {
	if hot == nil || archive == nil || hotMeta == nil || archiveMeta == nil ||
		hotHistory == nil || archiveHistory == nil {
		return nil, errors.New("archive agent: every hot and archive repository is required")
	}
	if batchSize <= 0 {
		batchSize = DefaultArchiveBatchSize
	}
	if logger == nil {
		logger = io.Discard
	}
	return &ArchiveAgent{
		hot:            hot,
		archive:        archive,
		hotMeta:        hotMeta,
		archiveMeta:    archiveMeta,
		hotHistory:     hotHistory,
		archiveHistory: archiveHistory,
		batchSize:      batchSize,
		logger:         logger,
	}, nil
}

// ArchiveAgent copies the hot tier's append-only tables into the archive so the archive
// is a superset of the hot database at all times.
//
// Reconciliation rather than dual-writing at the point of collection is deliberate,
// and the reasons compound:
//
//   - The collection write path is untouched, so a failing archive can never stop the
//     collector from recording a rate.
//   - SQLite documents that a transaction spanning attached databases is not atomic
//     under journal_mode=WAL, which beacon uses. Any scheme that writes both tiers in
//     one logical operation therefore has a crash window; here there is none, because
//     the archive write is an independent, idempotent copy of a row the hot tier
//     already committed.
//   - It is self-healing. A pass that fails, or a row that a crash left uncopied, is
//     picked up by the next pass with no bookkeeping beyond the watermark the archive
//     itself reports.
//
// The superset property is what makes pruning the hot tier safe later: a row may only
// be dropped from the hot database once it is provably in the archive.
type ArchiveAgent struct {
	hot            archiveSourceRepository
	archive        archiveTargetRepository
	hotMeta        archiveMetaSourceRepository
	archiveMeta    archiveMetaTargetRepository
	hotHistory     archiveHistorySourceRepository
	archiveHistory archiveHistoryTargetRepository
	batchSize      int
	logger         io.Writer
}

// ArchiveReport is what one reconciliation pass copied, per table.
type ArchiveReport struct {
	// RateValues is the number of rate values newly written to the archive.
	RateValues int
	// ExecutionHistory is the number of collector outcomes newly written to the archive.
	ExecutionHistory int
}

// Total is the number of rows the pass added across every table.
func (r ArchiveReport) Total() int { return r.RateValues + r.ExecutionHistory }

// Run copies everything the archive does not yet hold and reports how many rows it
// inserted per table. It resumes from the archive's own watermarks, so the first run over
// a populated database backfills all of history and every later run copies only what
// arrived since.
//
// The pass stops at the first error rather than skipping ahead: a gap in the archive must
// never be stepped over, because the pruning that follows trusts the archive to be
// complete. The already-copied prefix stays committed and the next run resumes from it.
// That also means a rate-value failure leaves execution history uncopied this tick — the
// next tick copies both, and neither table's completeness depends on the other's.
func (a *ArchiveAgent) Run(ctx context.Context) (ArchiveReport, error) {
	var report ArchiveReport

	// Sources first, values second. The archive's history view groups rows by the
	// provider title it reads from this mirror, so a value copied ahead of the source
	// that produced it is a row the grouped count cannot see. Doing it in this order
	// means the gap never exists rather than closing a tick later.
	if err := a.syncSources(ctx); err != nil {
		return report, err
	}

	values, err := reconcile(
		ctx, a.batchSize,
		a.archive.ObtainArchiveWatermark,
		a.hot.ObtainRateValuesAfter,
		a.archive.RetainRateValues,
		func(v domain.RateValue) (time.Time, string) { return v.Timestamp, v.ID },
	)
	report.RateValues = values
	if err != nil {
		return report, errors.Join(fmt.Errorf("archive: rate values: %w", err), loginjector.NewTraceError())
	}

	history, err := reconcile(
		ctx, a.batchSize,
		a.archiveHistory.ObtainArchiveWatermark,
		a.hotHistory.ObtainExecutionHistoryAfter,
		a.archiveHistory.RetainExecutionHistories,
		func(h domain.ExecutionHistory) (time.Time, string) { return h.Timestamp, h.ID },
	)
	report.ExecutionHistory = history
	if err != nil {
		return report, errors.Join(fmt.Errorf("archive: execution history: %w", err), loginjector.NewTraceError())
	}

	if report.RateValues > 0 {
		fmt.Fprintf(a.logger, "archive: copied %d rate value(s) into the history tier\n", report.RateValues)
	}
	if report.ExecutionHistory > 0 {
		fmt.Fprintf(a.logger, "archive: copied %d execution record(s) into the history tier\n", report.ExecutionHistory)
	}
	return report, nil
}

// reconcile copies rows the archive does not yet hold, in batches, resuming from the
// watermark the archive reports. It returns how many rows the writes reported as new.
//
// Every table archived this way follows the identical protocol — read the watermark,
// walk the hot tier forward with a (timestamp, id) keyset cursor, write idempotently,
// advance past the batch — and that protocol is what pruning trusts for safety. It lives
// here once, generic over the row type, so a second table cannot drift from the first in
// a way that is only discovered after rows have already been deleted.
func reconcile[T any](
	ctx context.Context,
	batchSize int,
	watermark func(context.Context) (time.Time, string, bool, error),
	after func(context.Context, time.Time, string, int64) ([]T, error),
	retain func(context.Context, []T) (int, error),
	cursorOf func(T) (time.Time, string),
) (int, error) {
	cursorTS, cursorID, _, err := watermark(ctx)
	if err != nil {
		return 0, fmt.Errorf("read watermark: %w", err)
	}

	copied := 0
	for {
		if ctx.Err() != nil {
			// A cancelled tick stops cleanly: what was copied stays copied, and the
			// next run resumes from the new watermark.
			return copied, nil
		}

		batch, batchErr := after(ctx, cursorTS, cursorID, int64(batchSize))
		if batchErr != nil {
			return copied, fmt.Errorf("read hot batch: %w", batchErr)
		}
		if len(batch) == 0 {
			return copied, nil
		}

		inserted, retainErr := retain(ctx, batch)
		if retainErr != nil {
			return copied, fmt.Errorf("write batch: %w", retainErr)
		}
		copied += inserted

		// Advance past the last row of the batch, whether or not it was new: rows the
		// archive already held are still progress through the hot tier.
		cursorTS, cursorID = cursorOf(batch[len(batch)-1])

		if len(batch) < batchSize {
			return copied, nil
		}
	}
}

// syncSources mirrors the hot tier's source definitions into the archive so archived
// values stay interpretable — both for the history view's grouping and for reading the
// archive on its own years from now.
//
// The whole set goes over on every pass. It is a table of a few dozen rows against value
// tables that grow without bound, so there is nothing to gain from tracking what changed,
// and an unconditional upsert also repairs a mirror that drifted for any reason. Sources
// the hot tier has dropped stay in the mirror: their values are still archived and still
// need a title.
func (a *ArchiveAgent) syncSources(ctx context.Context) error {
	sources, err := a.hotMeta.ObtainAllRateSources(ctx)
	if err != nil {
		return errors.Join(fmt.Errorf("archive: read hot sources: %w", err), loginjector.NewTraceError())
	}
	if len(sources) == 0 {
		return nil
	}

	if _, err = a.archiveMeta.RetainRateSources(ctx, sources); err != nil {
		return errors.Join(fmt.Errorf("archive: mirror sources: %w", err), loginjector.NewTraceError())
	}
	return nil
}
