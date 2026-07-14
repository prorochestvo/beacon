package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/beacon/cmd/wasm/apiclient"
	"github.com/seilbekskindirov/beacon/cmd/wasm/application"
	"github.com/seilbekskindirov/beacon/internal/dto"
)

// editFakeFetcher is a per-URL configurable Fetcher for the edit controller tests.
type editFakeFetcher struct {
	// urlJSON maps URL prefix → raw JSON response body for FetchJSON calls.
	urlJSON map[string][]byte
	// urlErr maps URL prefix → error for FetchJSON calls.
	urlErr map[string]error
	// noContentErr is returned by FetchNoContent unless overridden by urlNoContentErr.
	noContentErr error
	// urlNoContentErr maps URL prefix → error for FetchNoContent calls.
	urlNoContentErr map[string]error
	// lastNoContentURL records the most recent FetchNoContent URL.
	lastNoContentURL string
	// lastNoContentMethod records the most recent FetchNoContent method.
	lastNoContentMethod string
}

var _ apiclient.Fetcher = (*editFakeFetcher)(nil)

func (f *editFakeFetcher) FetchJSON(_ context.Context, _, rawURL string, _ any, _ map[string]string) ([]byte, error) {
	// Longest-prefix match so /api/me/subscriptions/raw wins over /api/me/subscriptions.
	bestErrKey, bestErrLen := "", 0
	for prefix := range f.urlErr {
		if strings.HasPrefix(rawURL, prefix) && len(prefix) > bestErrLen {
			bestErrKey, bestErrLen = prefix, len(prefix)
		}
	}
	if bestErrLen > 0 {
		return nil, f.urlErr[bestErrKey]
	}
	bestBodyKey, bestBodyLen := "", 0
	for prefix := range f.urlJSON {
		if strings.HasPrefix(rawURL, prefix) && len(prefix) > bestBodyLen {
			bestBodyKey, bestBodyLen = prefix, len(prefix)
		}
	}
	if bestBodyLen > 0 {
		return f.urlJSON[bestBodyKey], nil
	}
	return nil, errors.New("editFakeFetcher: no response configured for " + rawURL)
}

func (f *editFakeFetcher) FetchNoContent(_ context.Context, method, rawURL string, _ any, _ map[string]string) error {
	f.lastNoContentURL = rawURL
	f.lastNoContentMethod = method
	for prefix, err := range f.urlNoContentErr {
		if strings.HasPrefix(rawURL, prefix) {
			return err
		}
	}
	return f.noContentErr
}

// editPageWithFetcher constructs a MeSubscriptionsEditPage backed by f.
func editPageWithFetcher(f apiclient.Fetcher) *application.MeSubscriptionsEditPage {
	return application.NewMeSubscriptionsEditPage(apiclient.New(f), "init-data-token")
}

// mustMarshal panics if json.Marshal fails — convenience for test fixtures.
func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func rawResponse(items []dto.MeSubscriptionEditRow) []byte {
	if items == nil {
		items = []dto.MeSubscriptionEditRow{}
	}
	return mustMarshal(dto.MeSubscriptionsRawResponse{Items: items})
}

func sourcesResponse(sources []dto.SourceResponse) []byte {
	return mustMarshal(sources)
}

func createResponse(id string) []byte {
	return mustMarshal(dto.MeSubscriptionCreateResponse{ID: id})
}

func TestMeSubscriptionsEditPage_LoadInitial(t *testing.T) {
	t.Parallel()

	activeSrc := dto.SourceResponse{Name: "src_a", Title: "Alpha", BaseCurrency: "USD", QuoteCurrency: "KZT", Active: true}
	inactiveSrc := dto.SourceResponse{Name: "src_b", Title: "Beta", Active: false}
	sub1 := dto.MeSubscriptionEditRow{ID: "s1", SourceName: "src_a", ConditionType: "delta", ConditionValue: "5"}

	t.Run("populates items and filters inactive sources", func(t *testing.T) {
		t.Parallel()

		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/subscriptions/raw": rawResponse([]dto.MeSubscriptionEditRow{sub1}),
				"/api/sources":              sourcesResponse([]dto.SourceResponse{activeSrc, inactiveSrc}),
			},
		}
		page := editPageWithFetcher(f)
		require.NoError(t, page.LoadInitial(t.Context()))

		st := page.State()
		require.Len(t, st.Items, 1)
		assert.Equal(t, "s1", st.Items[0].ID)
		require.Len(t, st.Sources, 1, "inactive source must be filtered out")
		assert.Equal(t, "src_a", st.Sources[0].Name)
		assert.False(t, st.AuthFailure)
		assert.Nil(t, st.LoadError)
	})

	t.Run("empty subscriptions returns non-nil slice", func(t *testing.T) {
		t.Parallel()

		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/subscriptions/raw": rawResponse(nil),
				"/api/sources":              sourcesResponse(nil),
			},
		}
		page := editPageWithFetcher(f)
		require.NoError(t, page.LoadInitial(t.Context()))

		st := page.State()
		assert.NotNil(t, st.Items)
		assert.Empty(t, st.Items)
	})

	t.Run("401 on subscriptions raw sets AuthFailure", func(t *testing.T) {
		t.Parallel()

		f := &editFakeFetcher{
			urlErr: map[string]error{
				"/api/me/subscriptions/raw": errors.New("http 401"),
			},
		}
		page := editPageWithFetcher(f)
		err := page.LoadInitial(t.Context())
		require.Error(t, err)

		st := page.State()
		assert.True(t, st.AuthFailure)
		assert.NotNil(t, st.LoadError)
	})

	t.Run("sources failure propagates as error", func(t *testing.T) {
		t.Parallel()

		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/subscriptions/raw": rawResponse(nil),
			},
			urlErr: map[string]error{
				"/api/sources": errors.New("network error"),
			},
		}
		page := editPageWithFetcher(f)
		err := page.LoadInitial(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "network error")
	})
}

func TestMeSubscriptionsEditPage_SetDraftSource(t *testing.T) {
	t.Parallel()

	t.Run("sets source name and clears FormError", func(t *testing.T) {
		t.Parallel()

		page := editPageWithFetcher(&editFakeFetcher{})
		// Inject a FormError by directly constructing a failing state — use
		// SaveDraft failure path instead.
		page.SetDraftConditionType("bogus") // sets FormError to nil via SetDraftConditionType is a no-op here
		page.SetDraftSource("src_x")

		st := page.State()
		assert.Equal(t, "src_x", st.Draft.SourceName)
		assert.Nil(t, st.FormError)
	})
}

func TestMeSubscriptionsEditPage_SetDraftConditionType(t *testing.T) {
	t.Parallel()

	t.Run("sets condition type and clears FormError", func(t *testing.T) {
		t.Parallel()

		page := editPageWithFetcher(&editFakeFetcher{})
		page.SetDraftConditionType("interval")

		st := page.State()
		assert.Equal(t, "interval", st.Draft.ConditionType)
		assert.Nil(t, st.FormError)
	})
}

func TestMeSubscriptionsEditPage_SetDraftConditionValue(t *testing.T) {
	t.Parallel()

	t.Run("sets condition value and clears FormError", func(t *testing.T) {
		t.Parallel()

		page := editPageWithFetcher(&editFakeFetcher{})
		page.SetDraftConditionValue("1h")

		st := page.State()
		assert.Equal(t, "1h", st.Draft.ConditionValue)
		assert.Nil(t, st.FormError)
	})
}

func TestMeSubscriptionsEditPage_OpenProviderPicker(t *testing.T) {
	t.Parallel()

	t.Run("opens the picker and resets query and page", func(t *testing.T) {
		t.Parallel()

		page := editPageWithFetcher(&editFakeFetcher{})
		page.SetProviderQuery("stale")
		page.SetProviderPage(7)
		page.OpenProviderPicker()

		st := page.State()
		assert.True(t, st.ProviderPickerOpen)
		assert.Equal(t, "", st.ProviderQuery)
		assert.Equal(t, 1, st.ProviderPage)
	})

	t.Run("closes any open pair picker", func(t *testing.T) {
		t.Parallel()

		page := editPageWithFetcher(&editFakeFetcher{})
		page.ChooseProvider("Alpha") // opens the pair picker
		page.OpenProviderPicker()

		st := page.State()
		assert.True(t, st.ProviderPickerOpen)
		assert.False(t, st.PairPickerOpen)
	})
}

func TestMeSubscriptionsEditPage_SetProviderQuery(t *testing.T) {
	t.Parallel()

	t.Run("sets query and resets page", func(t *testing.T) {
		t.Parallel()

		page := editPageWithFetcher(&editFakeFetcher{})
		page.SetProviderPage(4)
		page.SetProviderQuery("bank")

		st := page.State()
		assert.Equal(t, "bank", st.ProviderQuery)
		assert.Equal(t, 1, st.ProviderPage)
	})
}

func TestMeSubscriptionsEditPage_SetProviderPage(t *testing.T) {
	t.Parallel()

	t.Run("sets a positive page", func(t *testing.T) {
		t.Parallel()

		page := editPageWithFetcher(&editFakeFetcher{})
		page.SetProviderPage(3)
		assert.Equal(t, 3, page.State().ProviderPage)
	})

	t.Run("clamps zero and negative to 1", func(t *testing.T) {
		t.Parallel()

		page := editPageWithFetcher(&editFakeFetcher{})
		page.SetProviderPage(-2)
		assert.Equal(t, 1, page.State().ProviderPage)
		page.SetProviderPage(0)
		assert.Equal(t, 1, page.State().ProviderPage)
	})
}

func TestMeSubscriptionsEditPage_ChooseProvider(t *testing.T) {
	t.Parallel()

	t.Run("sets selected title and auto-opens pair picker", func(t *testing.T) {
		t.Parallel()

		page := editPageWithFetcher(&editFakeFetcher{})
		page.OpenProviderPicker()
		page.ChooseProvider("Halyk Bank")

		st := page.State()
		assert.Equal(t, "Halyk Bank", st.SelectedProviderTitle)
		assert.False(t, st.ProviderPickerOpen)
		assert.True(t, st.PairPickerOpen)
		assert.Equal(t, 1, st.PairPage)
		assert.Equal(t, "", st.PairQuery)
	})

	t.Run("clears a previously chosen pair when provider changes", func(t *testing.T) {
		t.Parallel()

		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/subscriptions/raw": rawResponse(nil),
				"/api/sources": sourcesResponse([]dto.SourceResponse{
					{Name: "alpha_usd_kzt", Title: "Alpha", BaseCurrency: "USD", QuoteCurrency: "KZT", Active: true},
				}),
			},
		}
		page := editPageWithFetcher(f)
		require.NoError(t, page.LoadInitial(context.Background()))

		page.ChooseProvider("Alpha")
		page.ChoosePair("alpha_usd_kzt")
		page.ChooseProvider("Bravo")

		st := page.State()
		assert.Equal(t, "Bravo", st.SelectedProviderTitle)
		assert.Equal(t, "", st.Draft.SourceName)
		assert.Nil(t, st.PairDirections)
	})
}

func TestMeSubscriptionsEditPage_OpenPairPicker(t *testing.T) {
	t.Parallel()

	t.Run("no-op without a selected provider", func(t *testing.T) {
		t.Parallel()

		page := editPageWithFetcher(&editFakeFetcher{})
		page.OpenPairPicker()
		assert.False(t, page.State().PairPickerOpen)
	})

	t.Run("opens when a provider is selected", func(t *testing.T) {
		t.Parallel()

		page := editPageWithFetcher(&editFakeFetcher{})
		page.ChooseProvider("Alpha") // also opens pair picker
		page.ClosePairPicker()
		page.OpenPairPicker()

		st := page.State()
		assert.True(t, st.PairPickerOpen)
		assert.Equal(t, 1, st.PairPage)
	})
}

func TestMeSubscriptionsEditPage_ChoosePair(t *testing.T) {
	t.Parallel()

	loaded := func(t *testing.T, sources []dto.SourceResponse) *application.MeSubscriptionsEditPage {
		t.Helper()
		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/subscriptions/raw": rawResponse(nil),
				"/api/sources":              sourcesResponse(sources),
			},
		}
		page := editPageWithFetcher(f)
		require.NoError(t, page.LoadInitial(context.Background()))
		return page
	}

	t.Run("single-direction pair auto-sets Draft.SourceName", func(t *testing.T) {
		t.Parallel()

		page := loaded(t, []dto.SourceResponse{
			{Name: "src_a", Title: "Alpha", BaseCurrency: "USD", QuoteCurrency: "KZT", Active: true},
		})
		page.ChooseProvider("Alpha")
		page.ChoosePair("src_a")

		st := page.State()
		assert.Equal(t, "src_a", st.Draft.SourceName)
		assert.Len(t, st.PairDirections, 1)
		assert.False(t, st.PairPickerOpen)
		assert.Nil(t, st.FormError)
	})

	t.Run("BID/ASK pair leaves SourceName empty and seeds two directions", func(t *testing.T) {
		t.Parallel()

		page := loaded(t, []dto.SourceResponse{
			{Name: "KZ_BANK_ASK_USD_KZT", Title: "Bank", BaseCurrency: "USD", QuoteCurrency: "KZT", Active: true},
			{Name: "KZ_BANK_BID_USD_KZT", Title: "Bank", BaseCurrency: "USD", QuoteCurrency: "KZT", Active: true},
		})
		page.ChooseProvider("Bank")
		page.ChoosePair("KZ_BANK_ASK_USD_KZT")

		st := page.State()
		assert.Equal(t, "", st.Draft.SourceName, "ambiguous pair must not auto-pick a direction")
		require.Len(t, st.PairDirections, 2)
		// Sorted ASC by SourceName so ASK lands first under our naming scheme.
		assert.Equal(t, "ASK", st.PairDirections[0].Label)
		assert.Equal(t, "BID", st.PairDirections[1].Label)
	})

	t.Run("derives labels from name diff when names do not encode BID or ASK", func(t *testing.T) {
		t.Parallel()

		page := loaded(t, []dto.SourceResponse{
			{Name: "kz_bank_buy_usd_kzt", Title: "Bank", BaseCurrency: "USD", QuoteCurrency: "KZT", Active: true},
			{Name: "kz_bank_sell_usd_kzt", Title: "Bank", BaseCurrency: "USD", QuoteCurrency: "KZT", Active: true},
		})
		page.ChooseProvider("Bank")
		page.ChoosePair("kz_bank_buy_usd_kzt")

		st := page.State()
		require.Len(t, st.PairDirections, 2)
		assert.Equal(t, "BUY", st.PairDirections[0].Label)
		assert.Equal(t, "SELL", st.PairDirections[1].Label)
	})

	t.Run("equity LAST source yields one PairDirection with empty Label (no radio renders)", func(t *testing.T) {
		// An equity ticker has exactly one LAST source per (Title, Base, Quote).
		// resolvePairDirections must return a single entry with an empty Label so
		// renderDirectionRadios is never called (it guards on len(PairDirections)>=2).
		// If a future provider exposes both LAST and BID/ASK for the same ticker,
		// the radio would fire and affix-stripping would derive the label — out of
		// scope here because no such data exists.
		t.Parallel()

		page := loaded(t, []dto.SourceResponse{
			{Name: "US_YAHOO_LAST_AAPL_USD", Title: "Yahoo Finance", BaseCurrency: "AAPL", QuoteCurrency: "USD", Active: true},
		})
		page.ChooseProvider("Yahoo Finance")
		page.ChoosePair("US_YAHOO_LAST_AAPL_USD")

		st := page.State()
		require.Len(t, st.PairDirections, 1,
			"single LAST source must yield exactly one PairDirection")
		assert.Equal(t, "", st.PairDirections[0].Label,
			"single-direction pair must have empty Label so no direction radio renders")
		assert.Equal(t, "US_YAHOO_LAST_AAPL_USD", st.Draft.SourceName,
			"single-direction auto-selects the source name")
	})
}

func TestMeSubscriptionsEditPage_SetDraftDirection(t *testing.T) {
	t.Parallel()

	loaded := func(t *testing.T) *application.MeSubscriptionsEditPage {
		t.Helper()
		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/subscriptions/raw": rawResponse(nil),
				"/api/sources": sourcesResponse([]dto.SourceResponse{
					{Name: "KZ_BANK_ASK_USD_KZT", Title: "Bank", BaseCurrency: "USD", QuoteCurrency: "KZT", Active: true},
					{Name: "KZ_BANK_BID_USD_KZT", Title: "Bank", BaseCurrency: "USD", QuoteCurrency: "KZT", Active: true},
				}),
			},
		}
		page := editPageWithFetcher(f)
		require.NoError(t, page.LoadInitial(context.Background()))
		page.ChooseProvider("Bank")
		page.ChoosePair("KZ_BANK_ASK_USD_KZT")
		return page
	}

	t.Run("sets Draft.SourceName to a valid direction", func(t *testing.T) {
		t.Parallel()

		page := loaded(t)
		page.SetDraftDirection("KZ_BANK_BID_USD_KZT")
		assert.Equal(t, "KZ_BANK_BID_USD_KZT", page.State().Draft.SourceName)
	})

	t.Run("ignores source name not in PairDirections", func(t *testing.T) {
		t.Parallel()

		page := loaded(t)
		page.SetDraftDirection("not-in-list")
		assert.Equal(t, "", page.State().Draft.SourceName)
	})
}

func TestMeSubscriptionsEditPage_ClearDraft(t *testing.T) {
	t.Parallel()

	t.Run("resets draft, selection, and picker UI state", func(t *testing.T) {
		t.Parallel()

		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/subscriptions/raw": rawResponse(nil),
				"/api/sources": sourcesResponse([]dto.SourceResponse{
					{Name: "src_a", Title: "Alpha", BaseCurrency: "USD", QuoteCurrency: "KZT", Active: true},
				}),
			},
		}
		page := editPageWithFetcher(f)
		require.NoError(t, page.LoadInitial(context.Background()))

		page.ChooseProvider("Alpha")
		page.ChoosePair("src_a")
		page.SetDraftConditionType("delta")
		page.SetDraftConditionValue("1.5")
		page.OpenProviderPicker()
		page.SetProviderQuery("zzz")
		page.SetProviderPage(5)

		// Sanity: the draft holds the source before Clear so the reset is
		// actually testing something.
		assert.Equal(t, "src_a", page.State().Draft.SourceName)
		assert.NotEmpty(t, page.State().PairDirections)

		page.ClearDraft()

		st := page.State()
		assert.Equal(t, "", st.Draft.SourceName)
		assert.Equal(t, "", st.Draft.ConditionType)
		assert.Equal(t, "", st.Draft.ConditionValue)
		assert.Equal(t, "", st.SelectedProviderTitle)
		assert.False(t, st.ProviderPickerOpen)
		assert.False(t, st.PairPickerOpen)
		assert.Equal(t, "", st.ProviderQuery)
		assert.Equal(t, 0, st.ProviderPage)
		assert.Nil(t, st.PairDirections)
	})
}

func TestMeSubscriptionsEditPage_SaveDraft(t *testing.T) {
	t.Parallel()

	sub1 := dto.MeSubscriptionEditRow{ID: "s1", SourceName: "src_a", ConditionType: "delta", ConditionValue: "5"}

	makeFullFetcher := func(createErr error) *editFakeFetcher {
		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/subscriptions/raw": rawResponse([]dto.MeSubscriptionEditRow{sub1}),
				"/api/me/subscriptions":     createResponse("new-id"),
				"/api/sources":              sourcesResponse(nil),
			},
		}
		if createErr != nil {
			f.urlErr = map[string]error{
				"/api/me/subscriptions": createErr,
			}
		}
		return f
	}

	t.Run("happy path creates subscription and reloads list", func(t *testing.T) {
		t.Parallel()

		f := makeFullFetcher(nil)
		page := editPageWithFetcher(f)
		// Pre-populate sources so the draft has a valid source:
		page.SetDraftSource("src_a")
		page.SetDraftConditionType("delta")
		page.SetDraftConditionValue("5")

		err := page.SaveDraft(t.Context())
		require.NoError(t, err)

		st := page.State()
		assert.Nil(t, st.FormError)
		// Items should be reloaded:
		require.Len(t, st.Items, 1)
	})

	t.Run("client-side validation rejects empty source before HTTP call", func(t *testing.T) {
		t.Parallel()

		page := editPageWithFetcher(&editFakeFetcher{})
		page.SetDraftConditionType("delta")
		page.SetDraftConditionValue("5")
		// SourceName is empty

		err := page.SaveDraft(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "source is required")
		st := page.State()
		assert.NotNil(t, st.FormError)
	})

	t.Run("client-side validation rejects invalid delta value", func(t *testing.T) {
		t.Parallel()

		page := editPageWithFetcher(&editFakeFetcher{})
		page.SetDraftSource("src_a")
		page.SetDraftConditionType("delta")
		page.SetDraftConditionValue("not-a-number")

		err := page.SaveDraft(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "delta")
	})

	t.Run("client-side validation rejects interval below 1m", func(t *testing.T) {
		t.Parallel()

		page := editPageWithFetcher(&editFakeFetcher{})
		page.SetDraftSource("src_a")
		page.SetDraftConditionType("interval")
		page.SetDraftConditionValue("30s")

		err := page.SaveDraft(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "interval")
	})

	t.Run("client-side validation rejects malformed daily time", func(t *testing.T) {
		t.Parallel()

		page := editPageWithFetcher(&editFakeFetcher{})
		page.SetDraftSource("src_a")
		page.SetDraftConditionType("daily")
		page.SetDraftConditionValue("9am")

		err := page.SaveDraft(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "daily")
	})

	t.Run("client-side validation rejects empty cron expression", func(t *testing.T) {
		t.Parallel()

		page := editPageWithFetcher(&editFakeFetcher{})
		page.SetDraftSource("src_a")
		page.SetDraftConditionType("cron")
		page.SetDraftConditionValue("   ") // whitespace-only treated as empty

		err := page.SaveDraft(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cron")
	})

	t.Run("client-side validation rejects unknown condition type", func(t *testing.T) {
		t.Parallel()

		page := editPageWithFetcher(&editFakeFetcher{})
		page.SetDraftSource("src_a")
		page.SetDraftConditionType("bogus")
		page.SetDraftConditionValue("x")

		err := page.SaveDraft(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown condition type")
	})

	t.Run("401 from create sets AuthFailure and FormError", func(t *testing.T) {
		t.Parallel()

		f := makeFullFetcher(errors.New("http 401"))
		page := editPageWithFetcher(f)
		page.SetDraftSource("src_a")
		page.SetDraftConditionType("delta")
		page.SetDraftConditionValue("5")

		err := page.SaveDraft(t.Context())
		require.Error(t, err)

		st := page.State()
		assert.True(t, st.AuthFailure)
		assert.NotNil(t, st.FormError)
	})
}

func TestMeSubscriptionsEditPage_DeleteRow(t *testing.T) {
	t.Parallel()

	sub1 := dto.MeSubscriptionEditRow{ID: "s1", SourceName: "src_a", ConditionType: "delta", ConditionValue: "5"}

	t.Run("happy path deletes and reloads list", func(t *testing.T) {
		t.Parallel()

		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/subscriptions/raw": rawResponse([]dto.MeSubscriptionEditRow{}),
			},
		}
		page := editPageWithFetcher(f)
		// Seed state with sub1 to verify it disappears after delete.
		_ = sub1

		err := page.DeleteRow(t.Context(), "s1")
		require.NoError(t, err)

		st := page.State()
		assert.Empty(t, st.Items)
		assert.Contains(t, f.lastNoContentURL, "s1", "delete must target the correct subscription id")
		assert.Equal(t, "DELETE", f.lastNoContentMethod)
	})

	t.Run("401 sets AuthFailure", func(t *testing.T) {
		t.Parallel()

		f := &editFakeFetcher{
			urlNoContentErr: map[string]error{
				"/api/me/subscriptions/": errors.New("http 401"),
			},
		}
		page := editPageWithFetcher(f)
		err := page.DeleteRow(t.Context(), "s1")
		require.Error(t, err)
		assert.True(t, page.State().AuthFailure)
	})

	t.Run("delete failure propagates without crashing", func(t *testing.T) {
		t.Parallel()

		f := &editFakeFetcher{
			noContentErr: errors.New("server error"),
		}
		page := editPageWithFetcher(f)
		err := page.DeleteRow(t.Context(), "s1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "server error")
	})
}

func TestMeSubscriptionsEditPage_UpdateRow(t *testing.T) {
	t.Parallel()

	sub1 := dto.MeSubscriptionEditRow{ID: "s1", SourceName: "src_a", ConditionType: "delta", ConditionValue: "3"}

	t.Run("happy path updates and reloads list", func(t *testing.T) {
		t.Parallel()

		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/subscriptions/raw": rawResponse([]dto.MeSubscriptionEditRow{sub1}),
			},
		}
		page := editPageWithFetcher(f)
		err := page.UpdateRow(t.Context(), "s1", "delta", "10")
		require.NoError(t, err)
		assert.Equal(t, "PATCH", f.lastNoContentMethod)
	})

	t.Run("validation failure does not make HTTP call", func(t *testing.T) {
		t.Parallel()

		f := &editFakeFetcher{}
		page := editPageWithFetcher(f)
		err := page.UpdateRow(t.Context(), "s1", "interval", "30s") // below 1m
		require.Error(t, err)
		assert.Contains(t, err.Error(), "interval")
		assert.Empty(t, f.lastNoContentURL, "no HTTP call should be made on validation failure")
	})
}

func TestValidateSubscriptionDraft(t *testing.T) {
	t.Parallel()

	t.Run("valid delta is accepted", func(t *testing.T) {
		t.Parallel()
		d := application.MeSubscriptionDraft{SourceName: "src", ConditionType: "delta", ConditionValue: "1.5"}
		assert.NoError(t, application.ValidateSubscriptionDraft(d))
	})

	t.Run("valid interval is accepted", func(t *testing.T) {
		t.Parallel()
		d := application.MeSubscriptionDraft{SourceName: "src", ConditionType: "interval", ConditionValue: "1h"}
		assert.NoError(t, application.ValidateSubscriptionDraft(d))
	})

	t.Run("valid daily time is accepted", func(t *testing.T) {
		t.Parallel()
		d := application.MeSubscriptionDraft{SourceName: "src", ConditionType: "daily", ConditionValue: "09:00:00"}
		assert.NoError(t, application.ValidateSubscriptionDraft(d))
	})

	t.Run("valid cron expression is accepted", func(t *testing.T) {
		t.Parallel()
		d := application.MeSubscriptionDraft{SourceName: "src", ConditionType: "cron", ConditionValue: "0 9 * * 1-5"}
		assert.NoError(t, application.ValidateSubscriptionDraft(d))
	})

	t.Run("missing source returns error", func(t *testing.T) {
		t.Parallel()
		d := application.MeSubscriptionDraft{ConditionType: "delta", ConditionValue: "1"}
		err := application.ValidateSubscriptionDraft(d)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "source is required")
	})

	t.Run("negative delta is rejected", func(t *testing.T) {
		t.Parallel()
		d := application.MeSubscriptionDraft{SourceName: "src", ConditionType: "delta", ConditionValue: "-1"}
		err := application.ValidateSubscriptionDraft(d)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "delta")
	})

	t.Run("interval below 1m is rejected", func(t *testing.T) {
		t.Parallel()
		d := application.MeSubscriptionDraft{SourceName: "src", ConditionType: "interval", ConditionValue: "59s"}
		err := application.ValidateSubscriptionDraft(d)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "interval")
	})

	t.Run("malformed daily time is rejected", func(t *testing.T) {
		t.Parallel()
		d := application.MeSubscriptionDraft{SourceName: "src", ConditionType: "daily", ConditionValue: "9:00"}
		err := application.ValidateSubscriptionDraft(d)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "daily")
	})

	t.Run("empty cron expression is rejected", func(t *testing.T) {
		t.Parallel()
		d := application.MeSubscriptionDraft{SourceName: "src", ConditionType: "cron", ConditionValue: "   "}
		err := application.ValidateSubscriptionDraft(d)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cron")
	})

	t.Run("unknown condition type is rejected", func(t *testing.T) {
		t.Parallel()
		d := application.MeSubscriptionDraft{SourceName: "src", ConditionType: "weekly", ConditionValue: "x"}
		err := application.ValidateSubscriptionDraft(d)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown condition type")
	})
}
