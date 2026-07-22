package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/beacon/cmd/wasm/apiclient"
	"github.com/seilbekskindirov/beacon/cmd/wasm/application"
	"github.com/seilbekskindirov/beacon/internal/dto"
)

// weatherFakeFetcher is a method-aware Fetcher used by weather city tests.
// It distinguishes GET from POST so that error keys for the create POST can be
// set without accidentally matching the GET search URL (which shares a prefix).
type weatherFakeFetcher struct {
	// getJSON maps URL prefix → JSON body for GET requests via FetchJSON.
	getJSON map[string][]byte
	// getErr maps URL prefix → error for GET requests via FetchJSON.
	getErr map[string]error
	// postErr maps URL prefix → error for POST requests via FetchJSON.
	postErr map[string]error
	// delErr maps URL prefix → error for DELETE requests via FetchNoContent.
	delErr map[string]error
	// lastPostBody records the body of the most recent POST FetchJSON call so tests
	// can assert how the page marshals the create request.
	lastPostBody any
}

var _ apiclient.Fetcher = (*weatherFakeFetcher)(nil)

func (f *weatherFakeFetcher) FetchJSON(_ context.Context, method, rawURL string, body any, _ map[string]string) ([]byte, error) {
	if method == "POST" {
		f.lastPostBody = body
		for prefix, err := range f.postErr {
			if strings.HasPrefix(rawURL, prefix) {
				return nil, err
			}
		}
		// No POST error configured → success with empty JSON object.
		return []byte(`{}`), nil
	}
	// GET path: error map takes precedence (longest prefix).
	bestErrKey, bestErrLen := "", 0
	for prefix := range f.getErr {
		if strings.HasPrefix(rawURL, prefix) && len(prefix) > bestErrLen {
			bestErrKey, bestErrLen = prefix, len(prefix)
		}
	}
	if bestErrLen > 0 {
		return nil, f.getErr[bestErrKey]
	}
	bestBodyKey, bestBodyLen := "", 0
	for prefix := range f.getJSON {
		if strings.HasPrefix(rawURL, prefix) && len(prefix) > bestBodyLen {
			bestBodyKey, bestBodyLen = prefix, len(prefix)
		}
	}
	if bestBodyLen > 0 {
		return f.getJSON[bestBodyKey], nil
	}
	return nil, errors.New("weatherFakeFetcher: no GET response configured for " + rawURL)
}

func (f *weatherFakeFetcher) FetchNoContent(_ context.Context, _, rawURL string, _ any, _ map[string]string) error {
	for prefix, err := range f.delErr {
		if strings.HasPrefix(rawURL, prefix) {
			return err
		}
	}
	return nil
}

// weatherPageWithFetcher constructs a MeWeatherCitiesPage backed by f.
func weatherPageWithFetcher(f apiclient.Fetcher) *application.MeWeatherCitiesPage {
	return application.NewMeWeatherCitiesPage(apiclient.New(f), "init-token")
}

// citiesResponse marshals a dto.WeatherCitiesResponse to JSON for fake fetcher setup.
func citiesResponse(rows []dto.WeatherCityRow) []byte {
	if rows == nil {
		rows = []dto.WeatherCityRow{}
	}
	return mustMarshal(dto.WeatherCitiesResponse{Items: rows})
}

// searchResponse marshals a dto.WeatherCitySearchResponse to JSON for fake fetcher setup.
func searchResponse(items []dto.WeatherCitySearchItem) []byte {
	if items == nil {
		items = []dto.WeatherCitySearchItem{}
	}
	return mustMarshal(dto.WeatherCitySearchResponse{Items: items})
}

// createCityResponse marshals a dto.WeatherCityCreateResponse to JSON for fake fetcher setup.
func createCityResponse(id string) []byte {
	return mustMarshal(dto.WeatherCityCreateResponse{ID: id})
}

func TestMeWeatherCitiesPage_LoadCities(t *testing.T) {
	t.Parallel()

	city1 := dto.WeatherCityRow{
		ID: "c1", LocationID: "1234", DisplayName: "Almaty",
		Latitude: 43.25, Longitude: 76.94, Timezone: "Asia/Almaty",
		Country: "Kazakhstan", Admin1: "Almaty", NotifyHour: 7,
	}

	t.Run("happy path loads cities from server", func(t *testing.T) {
		t.Parallel()

		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/weather/cities": citiesResponse([]dto.WeatherCityRow{city1}),
			},
		}
		page := weatherPageWithFetcher(f)
		require.NoError(t, page.LoadCities(t.Context()))

		st := page.State()
		require.Len(t, st.Cities, 1)
		assert.Equal(t, "c1", st.Cities[0].ID)
		assert.Equal(t, "Almaty", st.Cities[0].DisplayName)
		assert.False(t, st.AuthFailure)
		assert.Nil(t, st.LoadError)
	})

	t.Run("empty list returns non-nil slice", func(t *testing.T) {
		t.Parallel()

		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/weather/cities": citiesResponse(nil),
			},
		}
		page := weatherPageWithFetcher(f)
		require.NoError(t, page.LoadCities(t.Context()))

		st := page.State()
		assert.NotNil(t, st.Cities)
		assert.Empty(t, st.Cities)
	})

	t.Run("401 sets AuthFailure", func(t *testing.T) {
		t.Parallel()

		f := &editFakeFetcher{
			urlErr: map[string]error{
				"/api/me/weather/cities": errors.New("http 401"),
			},
		}
		page := weatherPageWithFetcher(f)
		err := page.LoadCities(t.Context())

		require.Error(t, err)
		st := page.State()
		assert.True(t, st.AuthFailure)
		assert.NotNil(t, st.LoadError)
	})

	t.Run("network error does not set AuthFailure", func(t *testing.T) {
		t.Parallel()

		f := &editFakeFetcher{
			urlErr: map[string]error{
				"/api/me/weather/cities": errors.New("connection refused"),
			},
		}
		page := weatherPageWithFetcher(f)
		err := page.LoadCities(t.Context())

		require.Error(t, err)
		st := page.State()
		assert.False(t, st.AuthFailure)
	})
}

func TestMeWeatherCitiesPage_SearchCities(t *testing.T) {
	t.Parallel()

	item1 := dto.WeatherCitySearchItem{
		LocationID: "1234", DisplayName: "Almaty",
		Latitude: 43.25, Longitude: 76.94, Timezone: "Asia/Almaty",
		Country: "Kazakhstan", Admin1: "Almaty",
	}

	t.Run("empty query clears results without an API call", func(t *testing.T) {
		t.Parallel()

		f := &editFakeFetcher{} // no URLs configured — a call would return an error
		page := weatherPageWithFetcher(f)
		require.NoError(t, page.SearchCities(t.Context(), ""))

		st := page.State()
		assert.Nil(t, st.SearchResults)
		assert.Nil(t, st.SearchError)
	})

	t.Run("whitespace-only query clears results without an API call", func(t *testing.T) {
		t.Parallel()

		f := &editFakeFetcher{}
		page := weatherPageWithFetcher(f)
		require.NoError(t, page.SearchCities(t.Context(), "   "))

		st := page.State()
		assert.Nil(t, st.SearchResults)
	})

	t.Run("happy path populates SearchResults", func(t *testing.T) {
		t.Parallel()

		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/weather/cities/search": searchResponse([]dto.WeatherCitySearchItem{item1}),
			},
		}
		page := weatherPageWithFetcher(f)
		require.NoError(t, page.SearchCities(t.Context(), "Almaty"))

		st := page.State()
		require.Len(t, st.SearchResults, 1)
		assert.Equal(t, "1234", st.SearchResults[0].LocationID)
		assert.Equal(t, "Almaty", st.SearchResults[0].DisplayName)
	})

	t.Run("empty server results returns non-nil empty slice", func(t *testing.T) {
		t.Parallel()

		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/weather/cities/search": searchResponse(nil),
			},
		}
		page := weatherPageWithFetcher(f)
		require.NoError(t, page.SearchCities(t.Context(), "xyzzy"))

		st := page.State()
		assert.NotNil(t, st.SearchResults)
		assert.Empty(t, st.SearchResults)
	})

	t.Run("401 sets AuthFailure", func(t *testing.T) {
		t.Parallel()

		f := &editFakeFetcher{
			urlErr: map[string]error{
				"/api/me/weather/cities/search": errors.New("http 401"),
			},
		}
		page := weatherPageWithFetcher(f)
		err := page.SearchCities(t.Context(), "Almaty")

		require.Error(t, err)
		assert.True(t, page.State().AuthFailure)
	})

	t.Run("search error is stored in SearchError", func(t *testing.T) {
		t.Parallel()

		f := &editFakeFetcher{
			urlErr: map[string]error{
				"/api/me/weather/cities/search": errors.New("upstream timeout"),
			},
		}
		page := weatherPageWithFetcher(f)
		err := page.SearchCities(t.Context(), "Almaty")

		require.Error(t, err)
		st := page.State()
		assert.NotNil(t, st.SearchError)
		assert.False(t, st.AuthFailure)
	})
}

func TestMeWeatherCitiesPage_SelectSearchResult(t *testing.T) {
	t.Parallel()

	item0 := dto.WeatherCitySearchItem{LocationID: "1", DisplayName: "Alpha"}
	item1 := dto.WeatherCitySearchItem{LocationID: "2", DisplayName: "Beta"}

	makePageWithResults := func(t *testing.T, items []dto.WeatherCitySearchItem) *application.MeWeatherCitiesPage {
		t.Helper()
		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/weather/cities/search": searchResponse(items),
			},
		}
		page := weatherPageWithFetcher(f)
		require.NoError(t, page.SearchCities(t.Context(), "query"))
		return page
	}

	t.Run("selects item at valid index", func(t *testing.T) {
		t.Parallel()
		page := makePageWithResults(t, []dto.WeatherCitySearchItem{item0, item1})
		page.SelectSearchResult(1)
		assert.Equal(t, "2", page.State().Selected.LocationID)
	})

	t.Run("negative index is no-op", func(t *testing.T) {
		t.Parallel()
		page := makePageWithResults(t, []dto.WeatherCitySearchItem{item0})
		page.SelectSearchResult(-1)
		assert.Nil(t, page.State().Selected)
	})

	t.Run("out-of-bounds index is no-op", func(t *testing.T) {
		t.Parallel()
		page := makePageWithResults(t, []dto.WeatherCitySearchItem{item0})
		page.SelectSearchResult(99)
		assert.Nil(t, page.State().Selected)
	})
}

func TestMeWeatherCitiesPage_SaveSelected(t *testing.T) {
	t.Parallel()

	item := dto.WeatherCitySearchItem{
		LocationID: "1234", DisplayName: "Almaty",
		Latitude: 43.25, Longitude: 76.94, Timezone: "Asia/Almaty",
		Country: "Kazakhstan", Admin1: "Almaty",
	}

	// makePageWithSelection builds a page with SearchResults loaded and item[0]
	// selected. Uses weatherFakeFetcher so the GET search call and the POST create
	// call can carry different error maps without prefix overlap.
	makePageWithSelection := func(t *testing.T, f *weatherFakeFetcher) *application.MeWeatherCitiesPage {
		t.Helper()
		f.getJSON["/api/me/weather/cities/search"] = searchResponse([]dto.WeatherCitySearchItem{item})
		page := application.NewMeWeatherCitiesPage(apiclient.New(f), "init-token")
		require.NoError(t, page.SearchCities(t.Context(), "Almaty"))
		page.SelectSearchResult(0)
		return page
	}

	t.Run("nil selected is no-op", func(t *testing.T) {
		t.Parallel()
		page := weatherPageWithFetcher(&editFakeFetcher{})
		// No selection — SaveSelected must be a safe no-op.
		require.NoError(t, page.SaveSelected(t.Context()))
	})

	t.Run("successful save clears search form and reloads list", func(t *testing.T) {
		t.Parallel()
		f := &weatherFakeFetcher{
			getJSON: map[string][]byte{
				"/api/me/weather/cities": citiesResponse([]dto.WeatherCityRow{{
					ID: "c-new", LocationID: "1234", DisplayName: "Almaty", NotifyHour: 7,
				}}),
			},
		}
		page := makePageWithSelection(t, f)

		require.NoError(t, page.SaveSelected(context.Background()))

		st := page.State()
		assert.Empty(t, st.SearchQuery)
		assert.Nil(t, st.SearchResults)
		assert.Nil(t, st.Selected)
		assert.Len(t, st.Cities, 1)
		assert.Equal(t, "c-new", st.Cities[0].ID)
	})

	t.Run("create error stores SaveError", func(t *testing.T) {
		t.Parallel()
		// postErr only affects the POST create call; GET search uses getJSON.
		f := &weatherFakeFetcher{
			getJSON: map[string][]byte{},
			postErr: map[string]error{"/api/me/weather/cities": errors.New("server error")},
		}
		page := makePageWithSelection(t, f)

		err := page.SaveSelected(context.Background())
		require.Error(t, err)
		assert.NotNil(t, page.State().SaveError)
	})

	t.Run("create 401 sets AuthFailure", func(t *testing.T) {
		t.Parallel()
		f := &weatherFakeFetcher{
			getJSON: map[string][]byte{},
			postErr: map[string]error{"/api/me/weather/cities": errors.New("http 401")},
		}
		page := makePageWithSelection(t, f)

		err := page.SaveSelected(context.Background())
		require.Error(t, err)
		assert.True(t, page.State().AuthFailure)
	})
}

func TestMeWeatherCitiesPage_DeleteCity(t *testing.T) {
	t.Parallel()

	t.Run("successful delete reloads list", func(t *testing.T) {
		t.Parallel()
		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/weather/cities": citiesResponse(nil),
			},
		}
		page := weatherPageWithFetcher(f)

		require.NoError(t, page.DeleteCity(t.Context(), "city-1"))

		st := page.State()
		assert.Empty(t, st.Cities)
	})

	t.Run("delete error is returned and AuthFailure is not set for non-401", func(t *testing.T) {
		t.Parallel()
		f := &editFakeFetcher{
			urlNoContentErr: map[string]error{
				"/api/me/weather/cities/": errors.New("server error"),
			},
		}
		page := weatherPageWithFetcher(f)

		err := page.DeleteCity(t.Context(), "city-1")
		require.Error(t, err)
		assert.False(t, page.State().AuthFailure)
	})

	t.Run("delete 401 sets AuthFailure", func(t *testing.T) {
		t.Parallel()
		f := &editFakeFetcher{
			urlNoContentErr: map[string]error{
				"/api/me/weather/cities/": errors.New("http 401"),
			},
		}
		page := weatherPageWithFetcher(f)

		err := page.DeleteCity(t.Context(), "city-1")
		require.Error(t, err)
		assert.True(t, page.State().AuthFailure)
	})
}

func TestMeWeatherCitiesPage_RemoveCity(t *testing.T) {
	t.Parallel()

	t.Run("successful removal reloads list", func(t *testing.T) {
		t.Parallel()
		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/weather/cities": citiesResponse(nil),
			},
		}
		page := weatherPageWithFetcher(f)

		require.NoError(t, page.RemoveCity(t.Context(), "loc-1"))

		st := page.State()
		assert.Empty(t, st.Cities)
	})

	t.Run("removal error is returned and AuthFailure is not set for non-401", func(t *testing.T) {
		t.Parallel()
		f := &editFakeFetcher{
			urlNoContentErr: map[string]error{
				"/api/me/weather/locations/": errors.New("server error"),
			},
		}
		page := weatherPageWithFetcher(f)

		err := page.RemoveCity(t.Context(), "loc-1")
		require.Error(t, err)
		assert.False(t, page.State().AuthFailure)
	})

	t.Run("removal 401 sets AuthFailure", func(t *testing.T) {
		t.Parallel()
		f := &editFakeFetcher{
			urlNoContentErr: map[string]error{
				"/api/me/weather/locations/": errors.New("http 401"),
			},
		}
		page := weatherPageWithFetcher(f)

		err := page.RemoveCity(t.Context(), "loc-1")
		require.Error(t, err)
		assert.True(t, page.State().AuthFailure)
	})
}

func TestMeWeatherCitiesPage_OpenAlertForm(t *testing.T) {
	t.Parallel()

	city1 := dto.WeatherCityRow{
		ID: "c1", LocationID: "loc1", DisplayName: "Almaty",
		Latitude: 43.25, Longitude: 76.94, Timezone: "Asia/Almaty",
	}

	makeLoadedPage := func(t *testing.T) *application.MeWeatherCitiesPage {
		t.Helper()
		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/weather/cities": citiesResponse([]dto.WeatherCityRow{city1}),
			},
		}
		page := weatherPageWithFetcher(f)
		require.NoError(t, page.LoadCities(t.Context()))
		return page
	}

	t.Run("opens form with default kind alert_heat and empty value", func(t *testing.T) {
		t.Parallel()
		page := makeLoadedPage(t)
		page.OpenAlertForm("loc1")

		st := page.State()
		assert.Equal(t, "loc1", st.AlertFormLocationID)
		assert.Equal(t, "alert_heat", st.AlertFormKind)
		assert.Empty(t, st.AlertFormValue)
		assert.Nil(t, st.AlertSaveError)
	})

	t.Run("SetAlertFormKind updates kind", func(t *testing.T) {
		t.Parallel()
		page := makeLoadedPage(t)
		page.OpenAlertForm("loc1")
		page.SetAlertFormKind("alert_frost")
		assert.Equal(t, "alert_frost", page.State().AlertFormKind)
	})

	t.Run("SetAlertFormValue updates value", func(t *testing.T) {
		t.Parallel()
		page := makeLoadedPage(t)
		page.OpenAlertForm("loc1")
		page.SetAlertFormValue("35.5")
		assert.Equal(t, "35.5", page.State().AlertFormValue)
	})

	t.Run("CloseAlertForm clears all alert form state", func(t *testing.T) {
		t.Parallel()
		page := makeLoadedPage(t)
		page.OpenAlertForm("loc1")
		page.SetAlertFormKind("alert_thunderstorm")
		page.CloseAlertForm()

		st := page.State()
		assert.Empty(t, st.AlertFormLocationID)
		assert.Empty(t, st.AlertFormKind)
		assert.Empty(t, st.AlertFormValue)
		assert.Nil(t, st.AlertSaveError)
	})
}

func TestMeWeatherCitiesPage_SavePendingAlert(t *testing.T) {
	t.Parallel()

	cityRow := dto.WeatherCityRow{
		ID: "c1", LocationID: "loc1", DisplayName: "Almaty",
		Latitude: 43.25, Longitude: 76.94, Timezone: "Asia/Almaty",
		Country: "Kazakhstan", Admin1: "Almaty",
		NotifyKind: "morning_summary",
	}

	makePageWithCity := func(t *testing.T, citiesAfterSave []dto.WeatherCityRow, postErr error) *application.MeWeatherCitiesPage {
		t.Helper()
		f := &weatherFakeFetcher{
			getJSON: map[string][]byte{
				"/api/me/weather/cities": citiesResponse([]dto.WeatherCityRow{cityRow}),
			},
		}
		if citiesAfterSave != nil {
			f.getJSON["/api/me/weather/cities"] = citiesResponse(citiesAfterSave)
		}
		if postErr != nil {
			f.postErr = map[string]error{"/api/me/weather/cities": postErr}
		}
		page := application.NewMeWeatherCitiesPage(apiclient.New(f), "init-token")
		require.NoError(t, page.LoadCities(t.Context()))
		return page
	}

	t.Run("no form open is no-op", func(t *testing.T) {
		t.Parallel()
		page := makePageWithCity(t, nil, nil)
		require.NoError(t, page.SavePendingAlert(t.Context()))
		assert.Empty(t, page.State().AlertFormLocationID)
	})

	t.Run("base city not found in Cities list returns error", func(t *testing.T) {
		t.Parallel()
		f := &weatherFakeFetcher{
			getJSON: map[string][]byte{
				"/api/me/weather/cities": citiesResponse([]dto.WeatherCityRow{}),
			},
		}
		page := application.NewMeWeatherCitiesPage(apiclient.New(f), "init-token")
		require.NoError(t, page.LoadCities(t.Context()))
		page.OpenAlertForm("loc-ghost")

		err := page.SavePendingAlert(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "loc-ghost")
	})

	t.Run("successful save closes form and reloads list", func(t *testing.T) {
		t.Parallel()
		savedRow := dto.WeatherCityRow{
			ID: "c-alert", LocationID: "loc1", DisplayName: "Almaty",
			Timezone: "Asia/Almaty", NotifyKind: "alert_heat", ConditionValue: "35",
		}
		f := &weatherFakeFetcher{
			getJSON: map[string][]byte{
				"/api/me/weather/cities": citiesResponse([]dto.WeatherCityRow{cityRow, savedRow}),
			},
		}
		// First GET loads initial list; POST succeeds (no postErr); second GET after save.
		initialFetcher := &weatherFakeFetcher{
			getJSON: map[string][]byte{
				"/api/me/weather/cities": citiesResponse([]dto.WeatherCityRow{cityRow}),
			},
		}
		_ = f // reassigned below for the reload
		page := application.NewMeWeatherCitiesPage(apiclient.New(initialFetcher), "init-token")
		require.NoError(t, page.LoadCities(t.Context()))
		// Swap fetcher so that post-save reload returns two rows.
		initialFetcher.getJSON["/api/me/weather/cities"] = citiesResponse([]dto.WeatherCityRow{cityRow, savedRow})

		page.OpenAlertForm("loc1")
		page.SetAlertFormKind("alert_heat")
		page.SetAlertFormValue("35")

		require.NoError(t, page.SavePendingAlert(t.Context()))

		st := page.State()
		assert.Empty(t, st.AlertFormLocationID, "form must be closed on success")
		assert.Nil(t, st.AlertSaveError)
		assert.Len(t, st.Cities, 2, "list must be reloaded after save")
	})

	t.Run("POST error stores AlertSaveError and keeps form open", func(t *testing.T) {
		t.Parallel()
		f := &weatherFakeFetcher{
			getJSON: map[string][]byte{
				"/api/me/weather/cities": citiesResponse([]dto.WeatherCityRow{cityRow}),
			},
			postErr: map[string]error{"/api/me/weather/cities": errors.New("validation: bad kind")},
		}
		page := application.NewMeWeatherCitiesPage(apiclient.New(f), "init-token")
		require.NoError(t, page.LoadCities(t.Context()))

		page.OpenAlertForm("loc1")
		page.SetAlertFormKind("alert_heat")
		page.SetAlertFormValue("35")

		err := page.SavePendingAlert(t.Context())
		require.Error(t, err)

		st := page.State()
		assert.NotEmpty(t, st.AlertFormLocationID, "form must stay open on error")
		assert.NotNil(t, st.AlertSaveError)
	})

	t.Run("POST 401 sets AuthFailure", func(t *testing.T) {
		t.Parallel()
		f := &weatherFakeFetcher{
			getJSON: map[string][]byte{
				"/api/me/weather/cities": citiesResponse([]dto.WeatherCityRow{cityRow}),
			},
			postErr: map[string]error{"/api/me/weather/cities": errors.New("http 401")},
		}
		page := application.NewMeWeatherCitiesPage(apiclient.New(f), "init-token")
		require.NoError(t, page.LoadCities(t.Context()))

		page.OpenAlertForm("loc1")
		err := page.SavePendingAlert(t.Context())
		require.Error(t, err)
		assert.True(t, page.State().AuthFailure)
	})

	t.Run("thunderstorm empty-value save succeeds without error", func(t *testing.T) {
		t.Parallel()
		f := &weatherFakeFetcher{
			getJSON: map[string][]byte{
				"/api/me/weather/cities": citiesResponse([]dto.WeatherCityRow{cityRow}),
			},
		}
		page := application.NewMeWeatherCitiesPage(apiclient.New(f), "init-token")
		require.NoError(t, page.LoadCities(t.Context()))

		page.OpenAlertForm("loc1")
		page.SetAlertFormKind("alert_thunderstorm")
		// ConditionValue is intentionally not set — empty is canonical for thunderstorm.

		require.NoError(t, page.SavePendingAlert(t.Context()))

		st := page.State()
		assert.Empty(t, st.AlertFormLocationID, "form must be closed on success")
		assert.Nil(t, st.AlertSaveError)
	})

	t.Run("morning_summary routes the value to NotifyHour, not ConditionValue", func(t *testing.T) {
		t.Parallel()
		f := &weatherFakeFetcher{
			getJSON: map[string][]byte{
				"/api/me/weather/cities": citiesResponse([]dto.WeatherCityRow{cityRow}),
			},
		}
		page := application.NewMeWeatherCitiesPage(apiclient.New(f), "init-token")
		require.NoError(t, page.LoadCities(t.Context()))

		page.OpenAlertForm("loc1")
		page.SetAlertFormKind("morning_summary")
		page.SetAlertFormValue("9")
		require.NoError(t, page.SavePendingAlert(t.Context()))

		body, ok := f.lastPostBody.(dto.WeatherCityCreateRequest)
		require.True(t, ok, "POST body must be a WeatherCityCreateRequest")
		require.NotNil(t, body.NotifyHour, "hour must be sent for morning_summary")
		assert.Equal(t, 9, *body.NotifyHour)
		assert.Empty(t, body.ConditionValue, "morning_summary must not send a condition_value")
	})

	t.Run("morning_summary with blank hour omits NotifyHour so the server default applies", func(t *testing.T) {
		t.Parallel()
		f := &weatherFakeFetcher{
			getJSON: map[string][]byte{
				"/api/me/weather/cities": citiesResponse([]dto.WeatherCityRow{cityRow}),
			},
		}
		page := application.NewMeWeatherCitiesPage(apiclient.New(f), "init-token")
		require.NoError(t, page.LoadCities(t.Context()))

		page.OpenAlertForm("loc1")
		page.SetAlertFormKind("morning_summary")
		// Value left blank on purpose.
		require.NoError(t, page.SavePendingAlert(t.Context()))

		body, ok := f.lastPostBody.(dto.WeatherCityCreateRequest)
		require.True(t, ok)
		assert.Nil(t, body.NotifyHour, "blank hour must omit NotifyHour")
	})

	t.Run("morning_summary non-numeric hour is rejected client-side without a POST", func(t *testing.T) {
		t.Parallel()
		f := &weatherFakeFetcher{
			getJSON: map[string][]byte{
				"/api/me/weather/cities": citiesResponse([]dto.WeatherCityRow{cityRow}),
			},
		}
		page := application.NewMeWeatherCitiesPage(apiclient.New(f), "init-token")
		require.NoError(t, page.LoadCities(t.Context()))

		page.OpenAlertForm("loc1")
		page.SetAlertFormKind("morning_summary")
		page.SetAlertFormValue("noon")
		err := page.SavePendingAlert(t.Context())
		require.Error(t, err)

		st := page.State()
		assert.NotEmpty(t, st.AlertFormLocationID, "form must stay open on a client-side validation error")
		assert.NotNil(t, st.AlertSaveError)
		assert.Nil(t, f.lastPostBody, "no POST must be made when the hour is invalid")
	})
}

func TestMeWeatherCitiesPage_ClearSearch(t *testing.T) {
	t.Parallel()

	t.Run("clears all search state", func(t *testing.T) {
		t.Parallel()
		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/weather/cities/search": searchResponse([]dto.WeatherCitySearchItem{
					{LocationID: "1", DisplayName: "Alpha"},
				}),
			},
		}
		page := weatherPageWithFetcher(f)
		require.NoError(t, page.SearchCities(t.Context(), "test"))
		page.SelectSearchResult(0)

		page.ClearSearch()

		st := page.State()
		assert.Empty(t, st.SearchQuery)
		assert.Nil(t, st.SearchResults)
		assert.Nil(t, st.Selected)
		assert.Nil(t, st.SearchError)
		assert.Nil(t, st.SaveError)
	})
}
