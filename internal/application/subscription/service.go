// Package subscription is the application service behind a user's own rate
// subscriptions: it loads the rows one user owns, enriches them with source
// metadata and the latest collected rate, and applies the search and pagination
// rules the subscription list depends on.
//
// It is free of HTTP and Telegram concerns. Parsing a query string and rendering
// JSON belong to the gateway; what the product does with a user's subscriptions
// lives here, where a second caller can reach the same rules instead of
// reimplementing them.
package subscription

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
)

// SourceRow is one row of the grouped subscription list: every condition a user
// holds against one rate source, that source's metadata, and its latest
// collected value.
type SourceRow struct {
	// SourceName identifies the rate source the conditions belong to.
	SourceName string
	// SourceTitle, BaseCurrency and QuoteCurrency describe the source. All three
	// are empty when the source metadata could not be resolved — a subscription
	// outlives a source that has been removed from the catalogue.
	SourceTitle   string
	BaseCurrency  string
	QuoteCurrency string
	// Conditions holds "<type>:<value>" for every condition on this source, in
	// the order the store returned them.
	Conditions []string
	// LatestPrice is the most recently collected price for the source, and
	// LatestAt the UTC instant it was collected. LatestAt is the zero time when
	// the source has never produced a value, and LatestPrice is meaningless then.
	LatestPrice float64
	LatestAt    time.Time
}

// NewSubscription is a request to create one subscription. There is no user
// field: ownership comes from the authenticated caller passed alongside it, and
// a request body has never been allowed to name its own owner.
type NewSubscription struct {
	SourceName     string
	ConditionType  domain.SubscriptionConditionType
	ConditionValue string
}

// ConditionUpdate is the mutable part of an existing subscription. Its source is
// deliberately absent: moving a subscription to another source is a delete and a
// create, not an update.
type ConditionUpdate struct {
	ConditionType  domain.SubscriptionConditionType
	ConditionValue string
}

// ConditionRow is one subscription exactly as stored — a single condition
// carrying the stable identifier that addresses it for update and delete — plus
// the source metadata a caller needs to label it.
type ConditionRow struct {
	ID             string
	SourceName     string
	SourceTitle    string
	BaseCurrency   string
	QuoteCurrency  string
	ConditionType  domain.SubscriptionConditionType
	ConditionValue string
	UpdatedAt      time.Time
}

// Service assembles a user's subscription views from the subscription, source
// and rate-value stores. Construct it with NewService; it holds no mutable state
// and is safe for concurrent use.
type Service struct {
	subs    SubscriptionsStore
	sources SourcesLoader
	values  ValuesLoader
}

// NewService constructs a Service over the three stores a subscription view
// reads from. In production all three are repositories.
func NewService(subs SubscriptionsStore, sources SourcesLoader, values ValuesLoader) *Service {
	return &Service{subs: subs, sources: sources, values: values}
}

// ObtainMeSubscriptions returns one page of userID's subscriptions grouped one
// row per source, and the number of rows matching query before pagination.
//
// query is an optional case-insensitive substring matched against the source
// title, the source name, and the "BASE/QUOTE" pair label; an empty query
// matches everything and is not trimmed first. A group whose source metadata
// could not be resolved is dropped by a non-empty query and kept by an empty
// one: filtering cannot judge what it cannot read, and hiding the row outright
// would lose a subscription the user still holds.
//
// page is 1-based. A page past the end yields no rows and the unfiltered total
// rather than an error. Both arguments are expected pre-clamped by the caller.
func (s *Service) ObtainMeSubscriptions(ctx context.Context, userID, query string, page, pageSize int64) ([]SourceRow, int64, error) {
	// TODO: DM-only assumption — this bot stores subscriptions keyed by Telegram chat_id,
	// which equals user_id for direct chats. If the bot is ever added to groups the
	// subscriptions keyed under group chat_ids will not appear here. See plan R5.
	subs, err := s.subs.ObtainRateUserSubscriptionsByUserID(ctx, domain.UserTypeTelegram, userID)
	if err != nil {
		return nil, 0, err
	}

	groups := groupBySource(subs)

	// Bulk-load every distinct source up front so the search and render loops are
	// O(1) per group instead of one look-up each (the previous 2*M N+1 pattern).
	sourceNames := make([]string, 0, len(groups))
	for _, g := range groups {
		sourceNames = append(sourceNames, g.sourceName)
	}
	sourceMap, err := s.sources.ObtainRateSourcesByNames(ctx, sourceNames)
	if err != nil {
		return nil, 0, err
	}

	filtered := filterGroups(groups, sourceMap, query)
	total := int64(len(filtered))
	pageItems := paginateGroups(filtered, page, pageSize, total)

	// Bulk-load the latest value per page item, not per group: only rendered rows
	// carry a price, and pageSize used to cost one round-trip each.
	rateNames := make([]string, 0, len(pageItems))
	for _, g := range pageItems {
		rateNames = append(rateNames, g.sourceName)
	}
	latestValues, err := s.values.ObtainLatestRateValuesBySourceNames(ctx, rateNames)
	if err != nil {
		return nil, 0, err
	}

	rows := make([]SourceRow, 0, len(pageItems))
	for _, g := range pageItems {
		row := SourceRow{
			SourceName: g.sourceName,
			Conditions: g.conditions,
		}
		if src, ok := sourceMap[g.sourceName]; ok {
			row.SourceTitle = src.Title
			row.BaseCurrency = src.BaseCurrency
			row.QuoteCurrency = src.QuoteCurrency
		}
		if rv, ok := latestValues[g.sourceName]; ok {
			row.LatestPrice = rv.Price
			row.LatestAt = rv.Timestamp.UTC()
		}
		rows = append(rows, row)
	}

	return rows, total, nil
}

// ObtainMeSubscriptionsRaw returns userID's subscriptions one row per condition,
// each carrying the stable identifier an editor needs to address it, ordered
// source name ascending then most recently updated first. The order is part of
// the contract: a caller groups rows by source without sorting them again.
//
// Unlike ObtainMeSubscriptions this neither groups nor paginates and loads no
// rate values — an editor needs identifiers, not prices.
func (s *Service) ObtainMeSubscriptionsRaw(ctx context.Context, userID string) ([]ConditionRow, error) {
	subs, err := s.subs.ObtainRateUserSubscriptionsByUserID(ctx, domain.UserTypeTelegram, userID)
	if err != nil {
		return nil, err
	}

	// Collect distinct source names for a bulk metadata load (avoids N+1).
	seen := make(map[string]struct{}, len(subs))
	sourceNames := make([]string, 0, len(subs))
	for _, sub := range subs {
		if _, ok := seen[sub.SourceName]; !ok {
			seen[sub.SourceName] = struct{}{}
			sourceNames = append(sourceNames, sub.SourceName)
		}
	}
	sourceMap, err := s.sources.ObtainRateSourcesByNames(ctx, sourceNames)
	if err != nil {
		return nil, err
	}

	sort.Slice(subs, func(i, j int) bool {
		if subs[i].SourceName != subs[j].SourceName {
			return subs[i].SourceName < subs[j].SourceName
		}
		return subs[i].UpdatedAt.After(subs[j].UpdatedAt)
	})

	rows := make([]ConditionRow, 0, len(subs))
	for _, sub := range subs {
		row := ConditionRow{
			ID:             sub.ID,
			SourceName:     sub.SourceName,
			ConditionType:  sub.ConditionType,
			ConditionValue: sub.ConditionValue,
			UpdatedAt:      sub.UpdatedAt.UTC(),
		}
		if src, ok := sourceMap[sub.SourceName]; ok {
			row.SourceTitle = src.Title
			row.BaseCurrency = src.BaseCurrency
			row.QuoteCurrency = src.QuoteCurrency
		}
		rows = append(rows, row)
	}

	return rows, nil
}

// CreateMeSubscription stores a new subscription owned by userID and returns the
// identifier the store generated for it.
//
// The source is resolved before the insert. The foreign key would refuse an
// unknown one anyway, but as a constraint violation the caller can do nothing
// with; a *internal.PublicError names the problem instead. An unparseable
// condition is reported the same way. Every other failure is the store's, and is
// returned as it came.
func (s *Service) CreateMeSubscription(ctx context.Context, userID string, req NewSubscription) (string, error) {
	src, err := s.sources.ObtainRateSourceByName(ctx, req.SourceName)
	if err != nil {
		return "", fmt.Errorf("source lookup: %w", err)
	}
	if src == nil {
		return "", internal.NewPublicError("unknown source")
	}

	record := domain.RateUserSubscription{
		UserType:       domain.UserTypeTelegram,
		UserID:         userID,
		SourceName:     req.SourceName,
		ConditionType:  req.ConditionType,
		ConditionValue: req.ConditionValue,
	}
	if validationErr := record.Validate(); validationErr != nil {
		return "", invalidConditionError(record.ConditionType)
	}

	if err := s.subs.RetainRateUserSubscription(ctx, &record); err != nil {
		return "", fmt.Errorf("retain subscription: %w", err)
	}
	return record.ID, nil
}

// UpdateMeSubscription rewrites the condition of subscription id, which userID
// must own. Only the condition changes: the identifier, the owner, the source
// and the notification cursors are carried over from the stored row.
//
// A subscription that does not exist and one owned by somebody else both report
// internal.ErrNotFound. See obtainOwned.
func (s *Service) UpdateMeSubscription(ctx context.Context, userID, id string, upd ConditionUpdate) error {
	sub, err := s.obtainOwned(ctx, userID, id)
	if err != nil {
		return err
	}

	sub.ConditionType = upd.ConditionType
	sub.ConditionValue = upd.ConditionValue
	if validationErr := sub.Validate(); validationErr != nil {
		return invalidConditionError(sub.ConditionType)
	}

	if err := s.subs.RetainRateUserSubscription(ctx, sub); err != nil {
		return fmt.Errorf("retain subscription: %w", err)
	}
	return nil
}

// DeleteMeSubscription removes subscription id, which userID must own, reporting
// internal.ErrNotFound when it does not exist or belongs to somebody else.
//
// The rate_user_events rows recording what this subscription already delivered
// are left in place. They are keyed to the source rather than to the
// subscription and are treated as historical truth: unsubscribing is not a
// request to rewrite what was sent.
func (s *Service) DeleteMeSubscription(ctx context.Context, userID, id string) error {
	sub, err := s.obtainOwned(ctx, userID, id)
	if err != nil {
		return err
	}

	if err := s.subs.RemoveRateUserSubscription(ctx, sub); err != nil {
		return fmt.Errorf("remove subscription: %w", err)
	}
	return nil
}

// obtainOwned loads subscription id and reports internal.ErrNotFound unless
// userID owns it.
//
// Ownership is a (channel, user) pair, not a user: rows are keyed by UserType
// as well as UserID, and every other read in this service scopes by both. This
// one compared UserID alone, which is correct only while UserTypeTelegram is
// the sole channel — the moment a second one can mint the same UserID string,
// its owner would pass this check for somebody else's subscription.
//
// "No such row" and "somebody else's row" are one answer on purpose, and the
// caller is given no way to tell them apart. Ownership is decided here so that
// the sentinel crossing the boundary says only "not found for you" — a service
// that answered "forbidden" would be one refactor away from a 403 that confirms
// another user's subscription exists.
func (s *Service) obtainOwned(ctx context.Context, userID, id string) (*domain.RateUserSubscription, error) {
	sub, err := s.subs.ObtainRateUserSubscriptionByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("subscription lookup: %w", err)
	}
	if sub == nil || sub.UserType != domain.UserTypeTelegram || sub.UserID != userID {
		return nil, internal.ErrNotFound
	}
	return sub, nil
}

// SubscriptionsStore reads and writes subscription rows. Satisfied by
// *repository.RateUserSubscriptionRepository.
//
// ObtainRateUserSubscriptionByID reports (nil, nil) for an id that does not
// exist; a missing row is not an error at this level, it is one of the two
// inputs to the ownership answer.
type SubscriptionsStore interface {
	ObtainRateUserSubscriptionsByUserID(
		ctx context.Context, userType domain.UserType, userID string,
	) ([]domain.RateUserSubscription, error)
	ObtainRateUserSubscriptionByID(ctx context.Context, id string) (*domain.RateUserSubscription, error)
	RetainRateUserSubscription(ctx context.Context, record *domain.RateUserSubscription) error
	RemoveRateUserSubscription(ctx context.Context, record *domain.RateUserSubscription) error
}

// SourcesLoader resolves source metadata (title, base, quote). A name with no
// row is absent from the map ObtainRateSourcesByNames returns, and is a nil
// record from ObtainRateSourceByName — neither is an error.
type SourcesLoader interface {
	ObtainRateSourcesByNames(ctx context.Context, names []string) (map[string]domain.RateSource, error)
	ObtainRateSourceByName(ctx context.Context, name string) (*domain.RateSource, error)
}

// ValuesLoader resolves the latest collected value per source name. A source
// that has never been collected is absent from the returned map.
type ValuesLoader interface {
	ObtainLatestRateValuesBySourceNames(ctx context.Context, names []string) (map[string]domain.RateValue, error)
}

// invalidConditionError is the answer to a condition value the domain refuses.
// It names the condition type and not the value: the value came from the caller
// and telling them what they just sent adds nothing they do not know.
func invalidConditionError(kind domain.SubscriptionConditionType) error {
	return internal.NewPublicError(
		fmt.Sprintf("invalid condition value for %s: check the format and try again", kind),
	)
}

// sourceGroup accumulates every condition one user holds against a single
// source while the subscription list is being assembled.
type sourceGroup struct {
	sourceName string
	conditions []string
}

// groupBySource collapses subscriptions into one group per source, preserving
// both the order sources were first seen in and the order of conditions within
// a source.
func groupBySource(subs []domain.RateUserSubscription) []sourceGroup {
	index := make(map[string]int, len(subs))
	groups := make([]sourceGroup, 0, len(subs))
	for _, s := range subs {
		condition := string(s.ConditionType) + ":" + s.ConditionValue
		if at, ok := index[s.SourceName]; ok {
			groups[at].conditions = append(groups[at].conditions, condition)
			continue
		}
		index[s.SourceName] = len(groups)
		groups = append(groups, sourceGroup{sourceName: s.SourceName, conditions: []string{condition}})
	}
	return groups
}

// filterGroups keeps the groups matching query. An empty query is not a search
// and returns groups untouched, including any whose source is unresolved.
func filterGroups(groups []sourceGroup, sources map[string]domain.RateSource, query string) []sourceGroup {
	if query == "" {
		return groups
	}
	needle := strings.ToLower(query)
	var matched []sourceGroup
	for _, g := range groups {
		src, ok := sources[g.sourceName]
		if !ok {
			continue
		}
		pair := strings.ToLower(src.BaseCurrency + "/" + src.QuoteCurrency)
		if strings.Contains(strings.ToLower(src.Title), needle) ||
			strings.Contains(strings.ToLower(src.Name), needle) ||
			strings.Contains(pair, needle) {
			matched = append(matched, g)
		}
	}
	return matched
}

// paginateGroups returns the page-th window of pageSize groups, page being
// 1-based. A window starting before the first row or past the last is empty
// rather than a panic: this is a package boundary now, so a client asking for
// page 99 of three rows must not be able to take the process down.
func paginateGroups(groups []sourceGroup, page, pageSize, total int64) []sourceGroup {
	offset := (page - 1) * pageSize
	if offset < 0 || offset >= total {
		return nil
	}
	return groups[offset:min(offset+pageSize, total)]
}
