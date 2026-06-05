// Package application contains the WASM page controllers for the Mini App.
package application

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seilbekskindirov/monitor/cmd/wasm/apiclient"
	"github.com/seilbekskindirov/monitor/internal/dto"
)

// PickerPageSize is the fixed page size for the source-picker overlays.
// Both the provider picker and the pair picker paginate at this size.
const PickerPageSize = 5

// SubscriptionListPageSize is the fixed page size for the editor's
// subscription list view (Your subscriptions).
const SubscriptionListPageSize = 5

// EditView identifies which logical sub-screen of the editor is active.
// The screen is conceptually two pages: the list of existing subscriptions
// and the form to add a new one. They share state so navigation between
// them does not refetch.
type EditView string

const (
	// EditViewList shows the paginated, searchable list of subscriptions plus
	// a "+ Add" affordance that switches to EditViewForm.
	EditViewList EditView = "list"
	// EditViewForm shows the create form (provider/pair/direction/condition).
	// "Back" from here returns to EditViewList.
	EditViewForm EditView = "form"
)

// MeSubscriptionsEditState is the read-only snapshot consumed by the editor UI.
//
// Concurrency note: WASM runs on a single OS thread so state mutations are
// safe without a mutex. If the project ever moves to multi-threaded WASM,
// add a sync.Mutex around state reads/writes.
type MeSubscriptionsEditState struct {
	// Items is the current persisted set of subscriptions, one row per condition,
	// sorted source_name ASC / updated_at DESC by the server.
	Items []dto.MeSubscriptionEditRow
	// Sources lists all active sources for the create-form picker. Populated
	// by LoadInitial; filtered to Active=true before storage.
	Sources []dto.SourceResponse
	// Loading is true while LoadInitial is in flight.
	Loading bool
	// LoadError is the most recent non-nil load error. Nil on success.
	LoadError error
	// Draft is the in-progress create form state.
	Draft MeSubscriptionDraft
	// FormError holds the most recent inline error from a save attempt.
	// Cleared whenever any SetDraft* setter is called.
	FormError error
	// AuthFailure is true when any call received a 401 response.
	// The UI renders the "open from bot" message when this is set.
	AuthFailure bool
	// SelectedProviderTitle is the provider chosen in the provider picker.
	// Independent of Draft.SourceName so the intermediate state "provider
	// chosen, pair not yet" is representable.
	SelectedProviderTitle string
	// ProviderPickerOpen indicates the provider overlay is visible.
	ProviderPickerOpen bool
	// ProviderQuery is the live search filter for the provider picker. Empty
	// when no filter is active. Page resets to 1 on every query change.
	ProviderQuery string
	// ProviderPage is the 1-based current page in the provider picker.
	ProviderPage int
	// PairPickerOpen indicates the pair overlay is visible. The pair picker
	// is only meaningful when SelectedProviderTitle is non-empty.
	PairPickerOpen bool
	// PairQuery is the live search filter for the pair picker.
	PairQuery string
	// PairPage is the 1-based current page in the pair picker.
	PairPage int
	// PairDirections holds the BID/ASK (or single-direction) options for the
	// currently chosen currency pair. Populated by ChoosePair, cleared by
	// ChooseProvider / ClearDraft. When len >= 2, the UI renders a direction
	// radio group; when len == 1, the direction is implicit and Draft.SourceName
	// is auto-set to the only option. Empty when no pair is selected.
	PairDirections []PairDirection
	// ActiveView is the currently rendered sub-screen — list or form.
	// Defaults to EditViewList on first mount; SaveDraft success returns
	// the user to the list view so the new row is visible immediately.
	ActiveView EditView
	// ListQuery is the live search filter for the subscriptions list view.
	// Empty when no filter is active. Page resets to 1 on every change.
	ListQuery string
	// ListPage is the 1-based current page in the subscriptions list view.
	ListPage int
}

// PairDirection is one BID/ASK option for the currently chosen currency pair.
// SourceName is the unique source identifier the API expects; Label is the
// human-readable direction extracted from SourceName (e.g. "BID" / "ASK").
type PairDirection struct {
	Label      string
	SourceName string
}

// MeSubscriptionDraft holds the fields of the in-progress create form.
type MeSubscriptionDraft struct {
	SourceName     string
	ConditionType  string // delta | interval | daily | cron
	ConditionValue string
}

// MeSubscriptionsEditPage is the page controller for the subscription editor
// screen. It is pure Go with no syscall/js dependencies and is therefore
// testable under the host toolchain via make test.
//
// Client-side validation in SaveDraft mirrors domain.RateUserSubscription.Validate()
// for delta, interval, and daily to give immediate feedback without a round-trip.
// Cron expressions pass a non-empty check only; full structural validation is
// delegated to the server to keep the WASM bundle size under control.
type MeSubscriptionsEditPage struct {
	client   *apiclient.Client
	initData string
	state    MeSubscriptionsEditState
}

// NewMeSubscriptionsEditPage constructs a controller. initData is the Telegram
// WebApp initData string forwarded unchanged on every authenticated call.
// The default view is the list (EditViewList).
func NewMeSubscriptionsEditPage(client *apiclient.Client, initData string) *MeSubscriptionsEditPage {
	return &MeSubscriptionsEditPage{
		client:   client,
		initData: initData,
		state: MeSubscriptionsEditState{
			ActiveView: EditViewList,
			ListPage:   1,
		},
	}
}

// State returns a snapshot of the current controller state.
// The caller must not mutate the returned slices.
func (p *MeSubscriptionsEditPage) State() MeSubscriptionsEditState { return p.state }

// LoadInitial fan-outs a MeSubscriptionsRaw call and a ListSources call.
// Sources are filtered to Active=true before being stored. A 401 on either
// call sets AuthFailure=true; the first non-nil error aborts both.
func (p *MeSubscriptionsEditPage) LoadInitial(ctx context.Context) error {
	p.state.Loading = true
	defer func() { p.state.Loading = false }()
	p.state.LoadError = nil

	raw, err := p.client.MeSubscriptionsRaw(ctx, p.initData)
	if err != nil {
		if strings.Contains(err.Error(), AuthFailureSentinel) {
			p.state.AuthFailure = true
		}
		p.state.LoadError = err
		return err
	}

	sources, err := p.client.ListSources(ctx, 200)
	if err != nil {
		p.state.LoadError = err
		return err
	}

	p.state.Items = raw.Items
	if p.state.Items == nil {
		p.state.Items = []dto.MeSubscriptionEditRow{}
	}

	active := make([]dto.SourceResponse, 0, len(sources))
	for _, s := range sources {
		if s.Active {
			active = append(active, s)
		}
	}
	p.state.Sources = active
	return nil
}

// SetDraftSource sets the source name in the draft form and clears any
// pending FormError so the user starts fresh after each field edit.
//
// This setter is retained for tests that drive the draft directly. UI code
// should go through OpenProviderPicker / ChooseProvider / ChoosePair instead;
// they keep SelectedProviderTitle in sync with Draft.SourceName.
func (p *MeSubscriptionsEditPage) SetDraftSource(name string) {
	p.state.Draft.SourceName = name
	p.state.FormError = nil
}

// SetDraftConditionType sets the condition type in the draft form and clears
// any pending FormError.
func (p *MeSubscriptionsEditPage) SetDraftConditionType(t string) {
	p.state.Draft.ConditionType = t
	p.state.FormError = nil
}

// SetDraftConditionValue sets the condition value in the draft form and clears
// any pending FormError.
func (p *MeSubscriptionsEditPage) SetDraftConditionValue(v string) {
	p.state.Draft.ConditionValue = v
	p.state.FormError = nil
}

// OpenProviderPicker shows the provider overlay and resets its search and
// pagination so the user always lands on page 1 of an unfiltered list.
// The pair picker is closed if currently open.
func (p *MeSubscriptionsEditPage) OpenProviderPicker() {
	p.state.ProviderPickerOpen = true
	p.state.ProviderQuery = ""
	p.state.ProviderPage = 1
	p.state.PairPickerOpen = false
}

// CloseProviderPicker hides the provider overlay without mutating selection.
func (p *MeSubscriptionsEditPage) CloseProviderPicker() {
	p.state.ProviderPickerOpen = false
}

// SetProviderQuery updates the provider-picker search filter and resets
// pagination to page 1.
func (p *MeSubscriptionsEditPage) SetProviderQuery(q string) {
	p.state.ProviderQuery = q
	p.state.ProviderPage = 1
}

// SetProviderPage sets the 1-based page index for the provider picker.
// Negative or zero values are clamped to 1.
func (p *MeSubscriptionsEditPage) SetProviderPage(page int) {
	if page < 1 {
		page = 1
	}
	p.state.ProviderPage = page
}

// ChooseProvider records the chosen provider title, clears any previously
// chosen pair (including its resolved BID/ASK directions), and auto-opens
// the pair picker so the user can continue without a second tap on the
// pair trigger.
func (p *MeSubscriptionsEditPage) ChooseProvider(title string) {
	p.state.SelectedProviderTitle = title
	p.state.Draft.SourceName = ""
	p.state.PairDirections = nil
	p.state.ProviderPickerOpen = false
	p.state.PairQuery = ""
	p.state.PairPage = 1
	p.state.PairPickerOpen = true
	p.state.FormError = nil
}

// OpenPairPicker shows the pair overlay only when a provider is already
// selected; otherwise it is a no-op so a misplaced trigger click cannot
// surface an empty overlay.
func (p *MeSubscriptionsEditPage) OpenPairPicker() {
	if p.state.SelectedProviderTitle == "" {
		return
	}
	p.state.PairPickerOpen = true
	p.state.PairQuery = ""
	p.state.PairPage = 1
	p.state.ProviderPickerOpen = false
}

// ClosePairPicker hides the pair overlay without mutating selection.
func (p *MeSubscriptionsEditPage) ClosePairPicker() {
	p.state.PairPickerOpen = false
}

// SetPairQuery updates the pair-picker search filter and resets pagination
// to page 1.
func (p *MeSubscriptionsEditPage) SetPairQuery(q string) {
	p.state.PairQuery = q
	p.state.PairPage = 1
}

// SetPairPage sets the 1-based page index for the pair picker. Negative or
// zero values are clamped to 1.
func (p *MeSubscriptionsEditPage) SetPairPage(page int) {
	if page < 1 {
		page = 1
	}
	p.state.PairPage = page
}

// ChoosePair commits the currency pair represented by sourceName as the
// active pair on the draft. sourceName is the anchor source picked from the
// pair list (in practice the alphabetically-first source within the
// (Title, Base, Quote) bucket — see ui.pairsForProvider). ChoosePair then
// looks up every source sharing the same (Title, Base, Quote) and records
// them in PairDirections so the UI can render a BID/ASK selector. When the
// pair has only one underlying source, Draft.SourceName is set immediately
// and no further selection is required.
func (p *MeSubscriptionsEditPage) ChoosePair(sourceName string) {
	var anchor dto.SourceResponse
	for _, s := range p.state.Sources {
		if s.Name == sourceName {
			anchor = s
			break
		}
	}
	dirs := resolvePairDirections(p.state.Sources, anchor)
	p.state.PairDirections = dirs
	switch len(dirs) {
	case 0:
		// Anchor not found in Sources — should not happen, but keep the state
		// honest by clearing Draft.SourceName so Save fails validation
		// instead of silently storing a phantom source.
		p.state.Draft.SourceName = ""
	case 1:
		// Single direction available — no choice to surface in the UI.
		p.state.Draft.SourceName = dirs[0].SourceName
	default:
		// Multiple directions — wait for an explicit SetDraftDirection call.
		p.state.Draft.SourceName = ""
	}
	p.state.PairPickerOpen = false
	p.state.FormError = nil
}

// SetDraftDirection sets Draft.SourceName from one of the PairDirections
// options surfaced by the most recent ChoosePair. It is a no-op when the
// supplied sourceName does not appear in PairDirections so a stale UI event
// cannot install an unrelated source on the draft.
func (p *MeSubscriptionsEditPage) SetDraftDirection(sourceName string) {
	for _, d := range p.state.PairDirections {
		if d.SourceName == sourceName {
			p.state.Draft.SourceName = sourceName
			p.state.FormError = nil
			return
		}
	}
}

// resolvePairDirections returns every source sharing (Title, Base, Quote)
// with anchor, sorted by SourceName ASC so the rendered radio order is
// deterministic. When more than one direction exists, each direction's
// Label is derived from the segment of its Name that DIFFERS from the
// other names in the bucket (longest common prefix and suffix stripped).
// For the project's KZ_<bank>_<dir>_<base>_<quote> data this yields the
// expected "BID" / "ASK" pair; for any other naming scheme it falls back
// to whatever non-shared substring distinguishes the rows. Single-direction
// pairs return one entry with an empty Label — the UI does not render a
// radio in that case.
func resolvePairDirections(sources []dto.SourceResponse, anchor dto.SourceResponse) []PairDirection {
	if anchor.Name == "" {
		return nil
	}
	matches := make([]dto.SourceResponse, 0, 2)
	for _, s := range sources {
		if s.Title != anchor.Title {
			continue
		}
		if s.BaseCurrency != anchor.BaseCurrency || s.QuoteCurrency != anchor.QuoteCurrency {
			continue
		}
		matches = append(matches, s)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Name < matches[j].Name })
	if len(matches) == 0 {
		return nil
	}
	if len(matches) == 1 {
		return []PairDirection{{Label: "", SourceName: matches[0].Name}}
	}
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = m.Name
	}
	prefix, suffix := longestCommonPrefix(names), longestCommonSuffix(names)
	out := make([]PairDirection, 0, len(matches))
	for i, m := range matches {
		mid := strings.TrimPrefix(m.Name, prefix)
		mid = strings.TrimSuffix(mid, suffix)
		mid = strings.Trim(mid, "_-")
		if mid == "" {
			// Names collapse to empty after stripping shared affixes — fall
			// back to a positional label so the radio remains clickable.
			mid = fmt.Sprintf("Option %d", i+1)
		}
		out = append(out, PairDirection{
			Label:      strings.ToUpper(mid),
			SourceName: m.Name,
		})
	}
	return out
}

// longestCommonPrefix returns the longest string that is a prefix of every
// input. Returns "" on empty input or no shared prefix.
func longestCommonPrefix(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	p := ss[0]
	for _, s := range ss[1:] {
		max := len(p)
		if len(s) < max {
			max = len(s)
		}
		i := 0
		for i < max && p[i] == s[i] {
			i++
		}
		p = p[:i]
		if p == "" {
			return ""
		}
	}
	return p
}

// longestCommonSuffix returns the longest string that is a suffix of every
// input. Returns "" on empty input or no shared suffix.
func longestCommonSuffix(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	p := ss[0]
	for _, s := range ss[1:] {
		max := len(p)
		if len(s) < max {
			max = len(s)
		}
		i := 0
		for i < max && p[len(p)-1-i] == s[len(s)-1-i] {
			i++
		}
		p = p[len(p)-i:]
		if p == "" {
			return ""
		}
	}
	return p
}

// ClosePickers hides both overlays. Used as the outside-click handler.
func (p *MeSubscriptionsEditPage) ClosePickers() {
	p.state.ProviderPickerOpen = false
	p.state.PairPickerOpen = false
}

// ShowListView switches the editor to the list view. Any open picker
// overlay is closed so the user lands on a clean state.
func (p *MeSubscriptionsEditPage) ShowListView() {
	p.state.ActiveView = EditViewList
	p.state.ProviderPickerOpen = false
	p.state.PairPickerOpen = false
}

// ShowFormView switches the editor to the create-subscription form. The
// form's draft is left untouched so users can resume an in-progress
// subscription if they navigated away by accident.
func (p *MeSubscriptionsEditPage) ShowFormView() {
	p.state.ActiveView = EditViewForm
}

// SetListQuery updates the list search filter and resets pagination to 1.
func (p *MeSubscriptionsEditPage) SetListQuery(q string) {
	p.state.ListQuery = q
	p.state.ListPage = 1
}

// SetListPage sets the 1-based page index for the subscription list.
// Negative or zero values are clamped to 1.
func (p *MeSubscriptionsEditPage) SetListPage(page int) {
	if page < 1 {
		page = 1
	}
	p.state.ListPage = page
}

// ClearDraft resets the draft to its initial empty state including the
// selected provider title, resolved pair directions, and any picker UI
// state. Used as the Clear button action.
func (p *MeSubscriptionsEditPage) ClearDraft() {
	p.state.Draft = MeSubscriptionDraft{}
	p.state.SelectedProviderTitle = ""
	p.state.PairDirections = nil
	p.state.ProviderPickerOpen = false
	p.state.PairPickerOpen = false
	p.state.ProviderQuery = ""
	p.state.PairQuery = ""
	p.state.ProviderPage = 0
	p.state.PairPage = 0
	p.state.FormError = nil
}

// SaveDraft validates the draft client-side, calls MeSubscriptionCreate on
// success, then refreshes the subscription list. On server error, FormError is
// populated with the server's message; on validation error, FormError is
// populated with a human-readable description and no HTTP call is made.
//
// A nil return means the subscription was created; the caller should redraw.
func (p *MeSubscriptionsEditPage) SaveDraft(ctx context.Context) error {
	if err := ValidateSubscriptionDraft(p.state.Draft); err != nil {
		p.state.FormError = err
		return err
	}

	if _, err := p.client.MeSubscriptionCreate(ctx, p.initData, dto.MeSubscriptionCreateRequest{
		SourceName:     p.state.Draft.SourceName,
		ConditionType:  p.state.Draft.ConditionType,
		ConditionValue: p.state.Draft.ConditionValue,
	}); err != nil {
		if strings.Contains(err.Error(), AuthFailureSentinel) {
			p.state.AuthFailure = true
		}
		p.state.FormError = err
		return err
	}

	// Reload the list and return the user to the list view so the new row
	// is visible without an extra tap. The draft is cleared so they can add
	// another without seeing stale form values.
	if err := p.reloadList(ctx); err != nil {
		return err
	}
	p.state.Draft = MeSubscriptionDraft{}
	p.state.SelectedProviderTitle = ""
	p.state.PairDirections = nil
	p.state.ActiveView = EditViewList
	return nil
}

// UpdateRow updates the condition fields of an existing subscription and
// reloads the list on success. FormError is populated on failure.
func (p *MeSubscriptionsEditPage) UpdateRow(ctx context.Context, id, conditionType, conditionValue string) error {
	draft := MeSubscriptionDraft{
		SourceName:     "",
		ConditionType:  conditionType,
		ConditionValue: conditionValue,
	}
	if err := validateCondition(draft.ConditionType, draft.ConditionValue); err != nil {
		p.state.FormError = err
		return err
	}

	err := p.client.MeSubscriptionUpdate(ctx, p.initData, id, dto.MeSubscriptionUpdateRequest{
		ConditionType:  conditionType,
		ConditionValue: conditionValue,
	})
	if err != nil {
		if strings.Contains(err.Error(), AuthFailureSentinel) {
			p.state.AuthFailure = true
		}
		p.state.FormError = err
		return err
	}
	return p.reloadList(ctx)
}

// DeleteRow removes the subscription with the given id and reloads the list
// on success.
func (p *MeSubscriptionsEditPage) DeleteRow(ctx context.Context, id string) error {
	err := p.client.MeSubscriptionDelete(ctx, p.initData, id)
	if err != nil {
		if strings.Contains(err.Error(), AuthFailureSentinel) {
			p.state.AuthFailure = true
		}
		return err
	}
	return p.reloadList(ctx)
}

// reloadList refreshes Items from the server without changing Sources or
// Draft state.
func (p *MeSubscriptionsEditPage) reloadList(ctx context.Context) error {
	raw, err := p.client.MeSubscriptionsRaw(ctx, p.initData)
	if err != nil {
		if strings.Contains(err.Error(), AuthFailureSentinel) {
			p.state.AuthFailure = true
		}
		return err
	}
	p.state.Items = raw.Items
	if p.state.Items == nil {
		p.state.Items = []dto.MeSubscriptionEditRow{}
	}
	return nil
}

// ValidateSubscriptionDraft mirrors domain.RateUserSubscription.Validate()
// for use in WASM. Cron expressions are accepted if non-empty and delegated
// to the server for structural validation (robfig/cron/v3 is not shipped to
// WASM because it bloats the bundle by ~130 KiB).
//
// Returns a non-nil error with a human-readable message on any validation
// failure. Returns nil when the draft is valid.
func ValidateSubscriptionDraft(d MeSubscriptionDraft) error {
	if d.SourceName == "" {
		return fmt.Errorf("source is required")
	}
	return validateCondition(d.ConditionType, d.ConditionValue)
}

// validateCondition validates a (conditionType, conditionValue) pair without
// requiring a source name. Matches the logic in domain.RateUserSubscription.Validate().
func validateCondition(conditionType, conditionValue string) error {
	switch conditionType {
	case "daily":
		if _, err := time.Parse(time.TimeOnly, conditionValue); err != nil {
			return fmt.Errorf("daily: expected HH:MM:SS (e.g. 09:00:00)")
		}
	case "delta":
		d, err := strconv.ParseFloat(conditionValue, 64)
		if err != nil {
			return fmt.Errorf("delta: expected a non-negative number (e.g. 1.5)")
		}
		if d < 0 {
			return fmt.Errorf("delta: threshold must be non-negative")
		}
	case "interval":
		dur, err := time.ParseDuration(conditionValue)
		if err != nil {
			return fmt.Errorf("interval: expected a Go duration (e.g. 1h30m)")
		}
		if dur < time.Minute {
			return fmt.Errorf("interval: minimum interval is 1m")
		}
	case "cron":
		if strings.TrimSpace(conditionValue) == "" {
			return fmt.Errorf("cron: expression must not be empty")
		}
	default:
		return fmt.Errorf("unknown condition type %q; must be one of delta, interval, daily, cron", conditionType)
	}
	return nil
}
