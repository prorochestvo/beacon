// Package apiclient provides a typed Go client for the /api/... HTTP routes.
// It is used by the WASM frontend; transport is abstracted through the Fetcher
// interface so tests can inject an in-process fake without real HTTP calls.
package apiclient

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/seilbekskindirov/monitor/internal/dto"
)

// Client is a typed HTTP client for the /api/... routes consumed by the WASM frontend.
// Construct one per WASM lifetime via New and inject it into the application layer.
// The client is free of DOM concerns; transport is delegated to the Fetcher.
type Client struct {
	fetcher Fetcher
}

// New constructs a Client backed by the given Fetcher.
func New(f Fetcher) *Client { return &Client{fetcher: f} }

// ListSources fetches all configured rate sources with their latest execution status.
func (c *Client) ListSources(ctx context.Context, limit int) ([]dto.SourceResponse, error) {
	raw, err := c.fetcher.FetchJSON(ctx, "GET", sourcesURL(limit), nil, nil)
	if err != nil {
		return nil, err
	}
	var out []dto.SourceResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode sources: %w", err)
	}
	return out, nil
}

// ListRates fetches the most recent rate values for the named source.
func (c *Client) ListRates(ctx context.Context, name string, limit int) ([]dto.RateResponse, error) {
	raw, err := c.fetcher.FetchJSON(ctx, "GET", ratesURL(name, limit), nil, nil)
	if err != nil {
		return nil, err
	}
	var out []dto.RateResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode rates: %w", err)
	}
	return out, nil
}

// ListSubscriptions fetches a page of subscription details for the named source.
func (c *Client) ListSubscriptions(ctx context.Context, name string, page int) ([]dto.SubscriptionDetailResponse, error) {
	raw, err := c.fetcher.FetchJSON(ctx, "GET", subscriptionsURL(name, page), nil, nil)
	if err != nil {
		return nil, err
	}
	var out []dto.SubscriptionDetailResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode subscriptions: %w", err)
	}
	return out, nil
}

// ListDailyEvents fetches a page of daily event summaries for the named source.
func (c *Client) ListDailyEvents(ctx context.Context, name string, page int) ([]dto.DailyEventResponse, error) {
	raw, err := c.fetcher.FetchJSON(ctx, "GET", dailyEventsURL(name, page), nil, nil)
	if err != nil {
		return nil, err
	}
	var out []dto.DailyEventResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode daily events: %w", err)
	}
	return out, nil
}

// ListExecutionErrors fetches a page of failed execution history records across all sources.
func (c *Client) ListExecutionErrors(ctx context.Context, page int) ([]dto.ExecutionErrorResponse, error) {
	raw, err := c.fetcher.FetchJSON(ctx, "GET", executionErrorsURL(page), nil, nil)
	if err != nil {
		return nil, err
	}
	var out []dto.ExecutionErrorResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode execution errors: %w", err)
	}
	return out, nil
}

// ListFailedNotifications fetches a window of failed notification pool records.
// The server uses ?offset=&limit= (not ?page=), mirroring index.html:333.
func (c *Client) ListFailedNotifications(ctx context.Context, offset, limit int) ([]dto.NotificationResponse, error) {
	raw, err := c.fetcher.FetchJSON(ctx, "GET", failedNotificationsURL(offset, limit), nil, nil)
	if err != nil {
		return nil, err
	}
	var out []dto.NotificationResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode failed notifications: %w", err)
	}
	return out, nil
}

// Stats fetches global statistics: source counts and total error count.
func (c *Client) Stats(ctx context.Context) (dto.StatsResponse, error) {
	raw, err := c.fetcher.FetchJSON(ctx, "GET", statsURL(), nil, nil)
	if err != nil {
		return dto.StatsResponse{}, err
	}
	var out dto.StatsResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return dto.StatsResponse{}, fmt.Errorf("decode stats: %w", err)
	}
	return out, nil
}

// SetSourceActive enables or disables the named source.
// The server returns 204 No Content on success; no response body is decoded.
func (c *Client) SetSourceActive(ctx context.Context, name string, active bool) error {
	return c.fetcher.FetchNoContent(ctx, "PATCH", sourceActiveURL(name), dto.SourceActiveRequest{Active: active}, nil)
}

// RatesChart fetches aggregated chart data for a source over the given period.
// period must be one of "week", "month", "year", or "" to let the server default.
// The endpoint is public — no headers required.
func (c *Client) RatesChart(ctx context.Context, name, period string) ([]dto.ChartPointResponse, error) {
	raw, err := c.fetcher.FetchJSON(ctx, "GET", chartURL(name, period), nil, nil)
	if err != nil {
		return nil, err
	}
	var out []dto.ChartPointResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode chart points: %w", err)
	}
	return out, nil
}

// MeSubscriptions fetches the caller's own subscriptions enriched with the latest rate
// values. initData is the Telegram WebApp initData string read by the caller from
// window.Telegram.WebApp.initData; this method sets the X-Telegram-Init-Data header
// from that parameter — it does not read js.Global() itself.
func (c *Client) MeSubscriptions(ctx context.Context, initData string, page, pageSize int, q string) (dto.MeSubscriptionsResponse, error) {
	raw, err := c.fetcher.FetchJSON(ctx, "GET", meSubscriptionsURL(page, pageSize, q), nil, meSubscriptionsHeaders(initData))
	if err != nil {
		return dto.MeSubscriptionsResponse{}, err
	}
	var out dto.MeSubscriptionsResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return dto.MeSubscriptionsResponse{}, fmt.Errorf("decode me subscriptions: %w", err)
	}
	return out, nil
}
