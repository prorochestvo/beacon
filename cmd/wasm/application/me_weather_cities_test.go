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
}

var _ apiclient.Fetcher = (*weatherFakeFetcher)(nil)

func (f *weatherFakeFetcher) FetchJSON(_ context.Context, method, rawURL string, _ any, _ map[string]string) ([]byte, error) {
	if method == "POST" {
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
