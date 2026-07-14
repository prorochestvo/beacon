package application_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/beacon/cmd/wasm/apiclient"
	"github.com/seilbekskindirov/beacon/cmd/wasm/application"
	"github.com/seilbekskindirov/beacon/internal/dto"
)

// currentPageWithFetcher constructs a MeWeatherCurrentPage backed by f.
func currentPageWithFetcher(f apiclient.Fetcher) *application.MeWeatherCurrentPage {
	return application.NewMeWeatherCurrentPage(apiclient.New(f), "init-token")
}

// currentResponse marshals a dto.WeatherCurrentResponse to JSON for fake fetcher setup.
func currentResponse(items []dto.WeatherCurrentItem) []byte {
	if items == nil {
		items = []dto.WeatherCurrentItem{}
	}
	return mustMarshal(dto.WeatherCurrentResponse{Items: items})
}

func TestMeWeatherCurrentPage_Load(t *testing.T) {
	t.Parallel()

	t.Run("happy path returns items with has_data true", func(t *testing.T) {
		t.Parallel()
		temp := 22.5
		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/weather/current": currentResponse([]dto.WeatherCurrentItem{
					{
						LocationID:     "1234",
						DisplayName:    "Almaty",
						Timezone:       "Asia/Almaty",
						HasData:        true,
						TempCurrent:    &temp,
						ConditionText:  "Clear sky",
						ConditionEmoji: "☀️",
					},
				}),
			},
		}
		page := currentPageWithFetcher(f)
		require.NoError(t, page.Load(t.Context()))

		st := page.State()
		require.Len(t, st.Items, 1)
		assert.Equal(t, "1234", st.Items[0].LocationID)
		assert.Equal(t, "Almaty", st.Items[0].DisplayName)
		assert.True(t, st.Items[0].HasData)
		assert.False(t, st.AuthFailure)
		assert.Nil(t, st.LoadError)
	})

	t.Run("empty server response returns non-nil empty slice", func(t *testing.T) {
		t.Parallel()
		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/weather/current": currentResponse(nil),
			},
		}
		page := currentPageWithFetcher(f)
		require.NoError(t, page.Load(t.Context()))

		st := page.State()
		assert.NotNil(t, st.Items)
		assert.Empty(t, st.Items)
	})

	t.Run("no-data city is included with has_data false", func(t *testing.T) {
		t.Parallel()
		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/weather/current": currentResponse([]dto.WeatherCurrentItem{
					{LocationID: "1234", DisplayName: "Almaty", Timezone: "Asia/Almaty", HasData: false},
				}),
			},
		}
		page := currentPageWithFetcher(f)
		require.NoError(t, page.Load(t.Context()))

		st := page.State()
		require.Len(t, st.Items, 1)
		assert.False(t, st.Items[0].HasData)
		assert.Equal(t, "1234", st.Items[0].LocationID)
	})

	t.Run("401 sets AuthFailure and stores LoadError", func(t *testing.T) {
		t.Parallel()
		f := &editFakeFetcher{
			urlErr: map[string]error{
				"/api/me/weather/current": errors.New("http 401"),
			},
		}
		page := currentPageWithFetcher(f)
		err := page.Load(t.Context())

		require.Error(t, err)
		st := page.State()
		assert.True(t, st.AuthFailure)
		assert.NotNil(t, st.LoadError)
	})

	t.Run("network error does not set AuthFailure", func(t *testing.T) {
		t.Parallel()
		f := &editFakeFetcher{
			urlErr: map[string]error{
				"/api/me/weather/current": errors.New("connection refused"),
			},
		}
		page := currentPageWithFetcher(f)
		err := page.Load(t.Context())

		require.Error(t, err)
		st := page.State()
		assert.False(t, st.AuthFailure)
		assert.NotNil(t, st.LoadError)
	})

	t.Run("loading flag is reset to false after successful load", func(t *testing.T) {
		t.Parallel()
		f := &editFakeFetcher{
			urlJSON: map[string][]byte{
				"/api/me/weather/current": currentResponse(nil),
			},
		}
		page := currentPageWithFetcher(f)
		require.NoError(t, page.Load(t.Context()))
		assert.False(t, page.State().Loading)
	})

	t.Run("loading flag is reset to false after error", func(t *testing.T) {
		t.Parallel()
		f := &editFakeFetcher{
			urlErr: map[string]error{
				"/api/me/weather/current": errors.New("network down"),
			},
		}
		page := currentPageWithFetcher(f)
		err := page.Load(t.Context())
		require.Error(t, err)
		assert.False(t, page.State().Loading)
	})
}
