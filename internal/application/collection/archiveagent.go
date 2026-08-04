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

// DefaultArchiveBatchSize is how many rate values one reconciliation pass copies per
// round trip. Small enough that a first run over a full hot tier never holds more than
// a few hundred kilobytes of rows in memory, large enough that steady state — roughly
// 200 new rows a day against a tick every few minutes — finishes in a single batch.
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

// NewArchiveAgent constructs an ArchiveAgent. All four repositories are required; the
// hot pair and the archive pair must be opened on different databases. batchSize falls
// back to DefaultArchiveBatchSize when not positive.
func NewArchiveAgent(
	hot archiveSourceRepository,
	archive archiveTargetRepository,
	hotMeta archiveMetaSourceRepository,
	archiveMeta archiveMetaTargetRepository,
	batchSize int,
	logger io.Writer,
) (*ArchiveAgent, error) {
	if hot == nil || archive == nil || hotMeta == nil || archiveMeta == nil {
		return nil, errors.New("archive agent: hot and archive repositories are both required")
	}
	if batchSize <= 0 {
		batchSize = DefaultArchiveBatchSize
	}
	if logger == nil {
		logger = io.Discard
	}
	return &ArchiveAgent{
		hot:         hot,
		archive:     archive,
		hotMeta:     hotMeta,
		archiveMeta: archiveMeta,
		batchSize:   batchSize,
		logger:      logger,
	}, nil
}

// ArchiveAgent copies rate values from the hot tier into the archive so the archive is
// a superset of the hot database at all times.
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
	hot         archiveSourceRepository
	archive     archiveTargetRepository
	hotMeta     archiveMetaSourceRepository
	archiveMeta archiveMetaTargetRepository
	batchSize   int
	logger      io.Writer
}

// Run copies every hot rate value the archive does not yet hold and reports how many
// rows it inserted. It resumes from the archive's own watermark, so the first run over
// a populated database backfills all of history and every later run copies only what
// arrived since.
//
// The pass stops at the first error rather than skipping ahead: a gap in the archive
// must never be stepped over, because the pruning that follows trusts the archive to
// be complete. The already-copied prefix stays committed and the next run resumes from
// it.
func (a *ArchiveAgent) Run(ctx context.Context) (int, error) {
	// Sources first, values second. The archive's history view groups rows by the
	// provider title it reads from this mirror, so a value copied ahead of the source
	// that produced it is a row the grouped count cannot see. Doing it in this order
	// means the gap never exists rather than closing a tick later.
	if err := a.syncSources(ctx); err != nil {
		return 0, err
	}

	cursorTS, cursorID, _, err := a.archive.ObtainArchiveWatermark(ctx)
	if err != nil {
		return 0, errors.Join(fmt.Errorf("archive: read watermark: %w", err), loginjector.NewTraceError())
	}

	copied := 0
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// A cancelled tick stops cleanly: what was copied stays copied, and the
			// next run resumes from the new watermark.
			return copied, nil
		}

		batch, batchErr := a.hot.ObtainRateValuesAfter(ctx, cursorTS, cursorID, int64(a.batchSize))
		if batchErr != nil {
			return copied, errors.Join(fmt.Errorf("archive: read hot batch: %w", batchErr), loginjector.NewTraceError())
		}
		if len(batch) == 0 {
			break
		}

		inserted, retainErr := a.archive.RetainRateValues(ctx, batch)
		if retainErr != nil {
			return copied, errors.Join(fmt.Errorf("archive: write batch: %w", retainErr), loginjector.NewTraceError())
		}
		copied += inserted

		// Advance past the last row of the batch, whether or not it was new: rows the
		// archive already held are still progress through the hot tier.
		last := batch[len(batch)-1]
		cursorTS, cursorID = last.Timestamp, last.ID

		if len(batch) < a.batchSize {
			break
		}
	}

	if copied > 0 {
		fmt.Fprintf(a.logger, "archive: copied %d rate value(s) into the history tier\n", copied)
	}
	return copied, nil
}

// syncSources mirrors the hot tier's source definitions into the archive so archived
// values stay interpretable — both for the history view's grouping and for reading the
// archive on its own years from now.
//
// The whole set goes over on every pass. It is a table of a few dozen rows against a
// value table that grows without bound, so there is nothing to gain from tracking what
// changed, and an unconditional upsert also repairs a mirror that drifted for any
// reason. Sources the hot tier has dropped stay in the mirror: their values are still
// archived and still need a title.
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
