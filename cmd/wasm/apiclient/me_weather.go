package apiclient

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/seilbekskindirov/beacon/internal/dto"
)

// MeWeatherCitiesSearch calls the geocoding search endpoint with the given
// query. initData is forwarded via the X-Telegram-Init-Data header (never as
// a query parameter). Returns an empty Items slice (not an error) when the
// API finds no matches.
func (c *Client) MeWeatherCitiesSearch(ctx context.Context, initData, q string) (dto.WeatherCitySearchResponse, error) {
	raw, err := c.fetcher.FetchJSON(ctx, "GET", meWeatherCitiesSearchURL(q), nil, meSubscriptionsHeaders(initData))
	if err != nil {
		return dto.WeatherCitySearchResponse{}, err
	}
	var out dto.WeatherCitySearchResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return dto.WeatherCitySearchResponse{}, fmt.Errorf("decode weather city search: %w", err)
	}
	return out, nil
}

// MeWeatherCities fetches the authenticated caller's saved city weather
// subscriptions. initData is forwarded via the X-Telegram-Init-Data header.
func (c *Client) MeWeatherCities(ctx context.Context, initData string) (dto.WeatherCitiesResponse, error) {
	raw, err := c.fetcher.FetchJSON(ctx, "GET", meWeatherCitiesURL(), nil, meSubscriptionsHeaders(initData))
	if err != nil {
		return dto.WeatherCitiesResponse{}, err
	}
	var out dto.WeatherCitiesResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return dto.WeatherCitiesResponse{}, fmt.Errorf("decode weather cities: %w", err)
	}
	return out, nil
}

// MeWeatherCityCreate creates a new city weather subscription for the
// authenticated caller. Returns the generated city row ID on success.
// initData is forwarded via the X-Telegram-Init-Data header.
func (c *Client) MeWeatherCityCreate(ctx context.Context, initData string, body dto.WeatherCityCreateRequest) (dto.WeatherCityCreateResponse, error) {
	raw, err := c.fetcher.FetchJSON(ctx, "POST", meWeatherCitiesURL(), body, meSubscriptionsHeaders(initData))
	if err != nil {
		return dto.WeatherCityCreateResponse{}, err
	}
	var out dto.WeatherCityCreateResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return dto.WeatherCityCreateResponse{}, fmt.Errorf("decode create weather city response: %w", err)
	}
	return out, nil
}

// MeWeatherCityDelete removes a saved city subscription owned by the
// authenticated caller. The server returns 204 No Content on success.
// initData is forwarded via the X-Telegram-Init-Data header.
func (c *Client) MeWeatherCityDelete(ctx context.Context, initData, id string) error {
	return c.fetcher.FetchNoContent(ctx, "DELETE", meWeatherCityByIDURL(id), nil, meSubscriptionsHeaders(initData))
}
