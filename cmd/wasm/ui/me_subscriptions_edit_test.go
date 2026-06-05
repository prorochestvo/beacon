package ui_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seilbekskindirov/monitor/cmd/wasm/application"
	"github.com/seilbekskindirov/monitor/cmd/wasm/ui"
	"github.com/seilbekskindirov/monitor/internal/dto"
)

func TestRenderMeSubscriptionsEdit(t *testing.T) {
	t.Parallel()

	activeSrc := dto.SourceResponse{Name: "src_a", Title: "Alpha", BaseCurrency: "USD", QuoteCurrency: "KZT", Active: true}
	sub1 := dto.MeSubscriptionEditRow{ID: "id-1", SourceName: "src_a", SourceTitle: "Alpha", ConditionType: "delta", ConditionValue: "5"}

	t.Run("auth failure renders auth message only", func(t *testing.T) {
		t.Parallel()

		html := ui.RenderMeSubscriptionsEdit(application.MeSubscriptionsEditState{AuthFailure: true})
		assert.Contains(t, html, "must be opened from the bot")
		assert.NotContains(t, html, "me-edit-form")
	})

	t.Run("loading state shows loading indicator", func(t *testing.T) {
		t.Parallel()

		html := ui.RenderMeSubscriptionsEdit(application.MeSubscriptionsEditState{Loading: true})
		assert.Contains(t, html, "Loading")
		assert.NotContains(t, html, "me-edit-form")
	})

	t.Run("load error shows error message", func(t *testing.T) {
		t.Parallel()

		html := ui.RenderMeSubscriptionsEdit(application.MeSubscriptionsEditState{LoadError: errors.New("network error")})
		assert.Contains(t, html, "network error")
		assert.NotContains(t, html, "me-edit-form")
	})

	t.Run("renders back button and title", func(t *testing.T) {
		t.Parallel()

		html := ui.RenderMeSubscriptionsEdit(application.MeSubscriptionsEditState{})
		assert.Contains(t, html, `id="me-edit-back"`)
		assert.Contains(t, html, "Manage subscriptions")
	})

	t.Run("renders closed provider and pair triggers when nothing is selected", func(t *testing.T) {
		t.Parallel()

		state := application.MeSubscriptionsEditState{
			Sources: []dto.SourceResponse{activeSrc},
		}
		html := ui.RenderMeSubscriptionsEdit(state)
		assert.Contains(t, html, `id="me-edit-provider-trigger"`)
		assert.Contains(t, html, `id="me-edit-pair-trigger"`)
		// Triggers show placeholder labels and the pair trigger is disabled
		// until a provider is chosen.
		assert.Contains(t, html, "select provider")
		assert.Contains(t, html, "select pair")
		assert.Contains(t, html, `id="me-edit-pair-trigger" type="button" aria-haspopup="listbox" aria-expanded="false" disabled`)
		// The picker overlays are NOT visible until opened — there should be no
		// <ul> of items in the initial closed state.
		assert.NotContains(t, html, "me-edit-picker-list")
	})

	t.Run("open provider picker shows search and items", func(t *testing.T) {
		t.Parallel()

		state := application.MeSubscriptionsEditState{
			Sources:            []dto.SourceResponse{activeSrc},
			ProviderPickerOpen: true,
			ProviderPage:       1,
		}
		html := ui.RenderMeSubscriptionsEdit(state)
		assert.Contains(t, html, `id="me-edit-provider-search"`)
		assert.Contains(t, html, `id="me-edit-provider-results-slot"`)
		assert.Contains(t, html, `data-provider="Alpha"`)
		assert.Contains(t, html, ">Alpha</li>")
	})

	t.Run("provider search filters items", func(t *testing.T) {
		t.Parallel()

		state := application.MeSubscriptionsEditState{
			Sources: []dto.SourceResponse{
				{Name: "src_a", Title: "Alpha", BaseCurrency: "USD", QuoteCurrency: "KZT", Active: true},
				{Name: "src_b", Title: "Bravo", BaseCurrency: "EUR", QuoteCurrency: "KZT", Active: true},
			},
			ProviderPickerOpen: true,
			ProviderQuery:      "bra",
			ProviderPage:       1,
		}
		html := ui.RenderMeSubscriptionsEdit(state)
		assert.Contains(t, html, ">Bravo</li>")
		assert.NotContains(t, html, ">Alpha</li>")
	})

	t.Run("provider picker paginates at PickerPageSize", func(t *testing.T) {
		t.Parallel()

		sources := make([]dto.SourceResponse, 0, application.PickerPageSize+3)
		for i := 0; i < application.PickerPageSize+3; i++ {
			sources = append(sources, dto.SourceResponse{
				Name:          "src_" + string(rune('a'+i)),
				Title:         "P" + string(rune('0'+i)),
				BaseCurrency:  "USD",
				QuoteCurrency: "KZT",
				Active:        true,
			})
		}
		state := application.MeSubscriptionsEditState{
			Sources:            sources,
			ProviderPickerOpen: true,
			ProviderPage:       2,
		}
		html := ui.RenderMeSubscriptionsEdit(state)
		assert.Contains(t, html, "page 2 of 2")
		assert.Contains(t, html, `data-kind="provider"`)
	})

	t.Run("selected provider appears in trigger label and enables pair trigger", func(t *testing.T) {
		t.Parallel()

		state := application.MeSubscriptionsEditState{
			Sources:               []dto.SourceResponse{activeSrc},
			SelectedProviderTitle: "Alpha",
		}
		html := ui.RenderMeSubscriptionsEdit(state)
		assert.Contains(t, html, ">Alpha</span>")
		// Pair trigger no longer carries the disabled attribute.
		assert.NotContains(t, html, `id="me-edit-pair-trigger" type="button" aria-haspopup="listbox" aria-expanded="false" disabled`)
	})

	t.Run("open pair picker lists pairs for selected provider only", func(t *testing.T) {
		t.Parallel()

		state := application.MeSubscriptionsEditState{
			Sources: []dto.SourceResponse{
				{Name: "alpha_usd", Title: "Alpha", BaseCurrency: "USD", QuoteCurrency: "KZT", Active: true},
				{Name: "alpha_eur", Title: "Alpha", BaseCurrency: "EUR", QuoteCurrency: "KZT", Active: true},
				{Name: "bravo_usd", Title: "Bravo", BaseCurrency: "USD", QuoteCurrency: "KZT", Active: true},
			},
			SelectedProviderTitle: "Alpha",
			PairPickerOpen:        true,
			PairPage:              1,
		}
		html := ui.RenderMeSubscriptionsEdit(state)
		assert.Contains(t, html, `data-source-name="alpha_usd"`)
		assert.Contains(t, html, `data-source-name="alpha_eur"`)
		assert.NotContains(t, html, `data-source-name="bravo_usd"`)
	})

	t.Run("pair picker collapses BID/ASK duplicates per provider", func(t *testing.T) {
		t.Parallel()

		// Same provider + same currency pair shows up twice in the source list
		// because each pair has a BID row and an ASK row. The picker must show
		// only one entry per (Base, Quote) and pick the alphabetically-first
		// Name (ASK in the canonical KZ_<bank>_<dir>_<base>_<quote> scheme).
		state := application.MeSubscriptionsEditState{
			Sources: []dto.SourceResponse{
				{Name: "KZ_BANK_BID_USD_KZT", Title: "Bank", BaseCurrency: "USD", QuoteCurrency: "KZT", Active: true},
				{Name: "KZ_BANK_ASK_USD_KZT", Title: "Bank", BaseCurrency: "USD", QuoteCurrency: "KZT", Active: true},
				{Name: "KZ_BANK_BID_EUR_KZT", Title: "Bank", BaseCurrency: "EUR", QuoteCurrency: "KZT", Active: true},
				{Name: "KZ_BANK_ASK_EUR_KZT", Title: "Bank", BaseCurrency: "EUR", QuoteCurrency: "KZT", Active: true},
			},
			SelectedProviderTitle: "Bank",
			PairPickerOpen:        true,
			PairPage:              1,
		}
		html := ui.RenderMeSubscriptionsEdit(state)
		// Exactly one row per pair, and the chosen underlying source is the
		// ASK side (alphabetically first).
		assert.Contains(t, html, `data-source-name="KZ_BANK_ASK_USD_KZT"`)
		assert.NotContains(t, html, `data-source-name="KZ_BANK_BID_USD_KZT"`)
		assert.Contains(t, html, `data-source-name="KZ_BANK_ASK_EUR_KZT"`)
		assert.NotContains(t, html, `data-source-name="KZ_BANK_BID_EUR_KZT"`)
	})

	t.Run("direction radio group renders when PairDirections has BID and ASK", func(t *testing.T) {
		t.Parallel()

		state := application.MeSubscriptionsEditState{
			Sources:               []dto.SourceResponse{activeSrc},
			SelectedProviderTitle: "Alpha",
			PairDirections: []application.PairDirection{
				{Label: "ASK", SourceName: "KZ_BANK_ASK_USD_KZT"},
				{Label: "BID", SourceName: "KZ_BANK_BID_USD_KZT"},
			},
		}
		html := ui.RenderMeSubscriptionsEdit(state)
		assert.Contains(t, html, `name="me-edit-direction"`)
		assert.Contains(t, html, `value="KZ_BANK_ASK_USD_KZT"`)
		assert.Contains(t, html, `value="KZ_BANK_BID_USD_KZT"`)
		assert.Contains(t, html, "> ASK</label>")
		assert.Contains(t, html, "> BID</label>")
		// BID/ASK-aware help line is shown.
		assert.Contains(t, html, "bank")
	})

	t.Run("direction radio is hidden when PairDirections has one entry", func(t *testing.T) {
		t.Parallel()

		state := application.MeSubscriptionsEditState{
			Sources:               []dto.SourceResponse{activeSrc},
			SelectedProviderTitle: "Alpha",
			PairDirections: []application.PairDirection{
				{Label: "", SourceName: "src_a"},
			},
		}
		html := ui.RenderMeSubscriptionsEdit(state)
		assert.NotContains(t, html, `name="me-edit-direction"`)
	})

	t.Run("direction radio uses derived labels for non-BID/ASK schemes", func(t *testing.T) {
		t.Parallel()

		state := application.MeSubscriptionsEditState{
			Sources:               []dto.SourceResponse{activeSrc},
			SelectedProviderTitle: "Alpha",
			PairDirections: []application.PairDirection{
				{Label: "BUY", SourceName: "kz_bank_buy_usd_kzt"},
				{Label: "SELL", SourceName: "kz_bank_sell_usd_kzt"},
			},
		}
		html := ui.RenderMeSubscriptionsEdit(state)
		assert.Contains(t, html, "> BUY</label>")
		assert.Contains(t, html, "> SELL</label>")
		// Generic help line, no BID/ASK explanation.
		assert.Contains(t, html, "Pick which direction")
		assert.NotContains(t, html, "purchase price")
	})

	t.Run("chosen direction radio is pre-checked", func(t *testing.T) {
		t.Parallel()

		state := application.MeSubscriptionsEditState{
			Sources:               []dto.SourceResponse{activeSrc},
			SelectedProviderTitle: "Alpha",
			Draft:                 application.MeSubscriptionDraft{SourceName: "KZ_BANK_BID_USD_KZT"},
			PairDirections: []application.PairDirection{
				{Label: "ASK", SourceName: "KZ_BANK_ASK_USD_KZT"},
				{Label: "BID", SourceName: "KZ_BANK_BID_USD_KZT"},
			},
		}
		html := ui.RenderMeSubscriptionsEdit(state)
		assert.Contains(t, html, `value="KZ_BANK_BID_USD_KZT" checked`)
		assert.NotContains(t, html, `value="KZ_BANK_ASK_USD_KZT" checked`)
	})

	t.Run("selected pair label appears in pair trigger", func(t *testing.T) {
		t.Parallel()

		state := application.MeSubscriptionsEditState{
			Sources:               []dto.SourceResponse{activeSrc},
			SelectedProviderTitle: "Alpha",
			Draft:                 application.MeSubscriptionDraft{SourceName: "src_a"},
		}
		html := ui.RenderMeSubscriptionsEdit(state)
		assert.Contains(t, html, ">USD/KZT</span>")
	})

	t.Run("renders all four condition type radio buttons", func(t *testing.T) {
		t.Parallel()

		html := ui.RenderMeSubscriptionsEdit(application.MeSubscriptionsEditState{})
		assert.Contains(t, html, `id="me-edit-cond-delta"`)
		assert.Contains(t, html, `id="me-edit-cond-interval"`)
		assert.Contains(t, html, `id="me-edit-cond-daily"`)
		assert.Contains(t, html, `id="me-edit-cond-cron"`)
	})

	t.Run("selected condition type is pre-checked", func(t *testing.T) {
		t.Parallel()

		state := application.MeSubscriptionsEditState{
			Draft: application.MeSubscriptionDraft{ConditionType: "interval"},
		}
		html := ui.RenderMeSubscriptionsEdit(state)
		assert.Contains(t, html, `id="me-edit-cond-interval" value="interval" checked`)
	})

	t.Run("placeholder changes per condition type", func(t *testing.T) {
		t.Parallel()

		types := map[string]string{
			"daily":    "09:00:00",
			"delta":    "1.5",
			"interval": "1h30m",
			"cron":     "0 9 * * 1-5",
		}
		for ct, wantPlaceholder := range types {
			ct, wantPlaceholder := ct, wantPlaceholder
			t.Run(ct, func(t *testing.T) {
				t.Parallel()
				state := application.MeSubscriptionsEditState{
					Draft: application.MeSubscriptionDraft{ConditionType: ct},
				}
				html := ui.RenderMeSubscriptionsEdit(state)
				assert.Contains(t, html, `placeholder="`+wantPlaceholder+`"`,
					"placeholder for condition type %q must be %q", ct, wantPlaceholder)
			})
		}
	})

	t.Run("FormError is rendered inline", func(t *testing.T) {
		t.Parallel()

		state := application.MeSubscriptionsEditState{
			FormError: errors.New("source is required"),
		}
		html := ui.RenderMeSubscriptionsEdit(state)
		assert.Contains(t, html, `id="me-edit-form-error"`)
		assert.Contains(t, html, "source is required")
	})

	t.Run("nil FormError hides the error region", func(t *testing.T) {
		t.Parallel()

		html := ui.RenderMeSubscriptionsEdit(application.MeSubscriptionsEditState{})
		assert.Contains(t, html, `id="me-edit-form-error" hidden`)
	})

	t.Run("renders save and cancel buttons", func(t *testing.T) {
		t.Parallel()

		html := ui.RenderMeSubscriptionsEdit(application.MeSubscriptionsEditState{})
		assert.Contains(t, html, `id="me-edit-save"`)
		assert.Contains(t, html, `id="me-edit-cancel"`)
	})

	t.Run("renders subscription list items with delete buttons", func(t *testing.T) {
		t.Parallel()

		state := application.MeSubscriptionsEditState{
			ActiveView: application.EditViewList,
			Items:      []dto.MeSubscriptionEditRow{sub1},
		}
		html := ui.RenderMeSubscriptionsEdit(state)
		assert.Contains(t, html, "Alpha")
		assert.Contains(t, html, "delta: 5")
		assert.Contains(t, html, `data-id="id-1"`)
		assert.Contains(t, html, `class="me-edit-delete"`)
	})

	t.Run("empty items shows empty-state message", func(t *testing.T) {
		t.Parallel()

		html := ui.RenderMeSubscriptionsEdit(application.MeSubscriptionsEditState{
			ActiveView: application.EditViewList,
			Items:      []dto.MeSubscriptionEditRow{},
		})
		assert.Contains(t, html, "No subscriptions yet")
		assert.NotContains(t, html, `class="me-edit-item"`)
	})

	t.Run("XSS in source title is escaped", func(t *testing.T) {
		t.Parallel()

		xssSub := dto.MeSubscriptionEditRow{
			ID:             "x1",
			SourceTitle:    `<script>alert(1)</script>`,
			ConditionType:  "delta",
			ConditionValue: "1",
		}
		html := ui.RenderMeSubscriptionsEdit(application.MeSubscriptionsEditState{
			ActiveView: application.EditViewList,
			Items:      []dto.MeSubscriptionEditRow{xssSub},
		})
		assert.NotContains(t, html, "<script>", "script tag must not appear unescaped")
		assert.Contains(t, html, "&lt;script&gt;")
	})

	t.Run("list view paginates items at SubscriptionListPageSize", func(t *testing.T) {
		t.Parallel()

		items := make([]dto.MeSubscriptionEditRow, 0, application.SubscriptionListPageSize+3)
		for i := 0; i < application.SubscriptionListPageSize+3; i++ {
			items = append(items, dto.MeSubscriptionEditRow{
				ID:             "id-" + string(rune('a'+i)),
				SourceTitle:    "Provider " + string(rune('A'+i)),
				ConditionType:  "delta",
				ConditionValue: "1",
			})
		}
		state := application.MeSubscriptionsEditState{
			ActiveView: application.EditViewList,
			Items:      items,
			ListPage:   2,
		}
		html := ui.RenderMeSubscriptionsEdit(state)
		assert.Contains(t, html, "page 2 of 2")
		assert.Contains(t, html, `data-kind="list"`)
	})

	t.Run("list view search filter narrows items", func(t *testing.T) {
		t.Parallel()

		state := application.MeSubscriptionsEditState{
			ActiveView: application.EditViewList,
			Items: []dto.MeSubscriptionEditRow{
				{ID: "i1", SourceTitle: "Halyk Bank", ConditionType: "delta", ConditionValue: "5"},
				{ID: "i2", SourceTitle: "QazPost", ConditionType: "daily", ConditionValue: "09:00:00"},
			},
			ListQuery: "halyk",
			ListPage:  1,
		}
		html := ui.RenderMeSubscriptionsEdit(state)
		assert.Contains(t, html, "Halyk Bank")
		assert.NotContains(t, html, "QazPost")
	})

	t.Run("list view shows + Add new subscription button", func(t *testing.T) {
		t.Parallel()

		state := application.MeSubscriptionsEditState{
			ActiveView: application.EditViewList,
		}
		html := ui.RenderMeSubscriptionsEdit(state)
		assert.Contains(t, html, `id="me-edit-add"`)
		assert.Contains(t, html, "Add new subscription")
	})

	t.Run("form view title and back button", func(t *testing.T) {
		t.Parallel()

		state := application.MeSubscriptionsEditState{
			ActiveView: application.EditViewForm,
		}
		html := ui.RenderMeSubscriptionsEdit(state)
		assert.Contains(t, html, "New subscription")
		assert.Contains(t, html, `id="me-edit-back"`)
		assert.Contains(t, html, `id="me-edit-save"`)
		// Form view does not render the list section.
		assert.NotContains(t, html, "Your subscriptions")
	})
}
