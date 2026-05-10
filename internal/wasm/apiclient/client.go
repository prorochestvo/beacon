package apiclient

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/seilbekskindirov/monitor/pkg/api"
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
func (c *Client) ListSources(ctx context.Context, limit int) ([]api.SourceResponse, error) {
	raw, err := c.fetcher.FetchJSON(ctx, "GET", sourcesURL(limit), nil, nil)
	if err != nil {
		return nil, err
	}
	var out []api.SourceResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode sources: %w", err)
	}
	return out, nil
}

// ListRates fetches the most recent rate values for the named source.
func (c *Client) ListRates(ctx context.Context, name string, limit int) ([]api.RateResponse, error) {
	raw, err := c.fetcher.FetchJSON(ctx, "GET", ratesURL(name, limit), nil, nil)
	if err != nil {
		return nil, err
	}
	var out []api.RateResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode rates: %w", err)
	}
	return out, nil
}

// ListSubscriptions fetches a page of subscription details for the named source.
func (c *Client) ListSubscriptions(ctx context.Context, name string, page int) ([]api.SubscriptionDetailResponse, error) {
	raw, err := c.fetcher.FetchJSON(ctx, "GET", subscriptionsURL(name, page), nil, nil)
	if err != nil {
		return nil, err
	}
	var out []api.SubscriptionDetailResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode subscriptions: %w", err)
	}
	return out, nil
}

// ListDailyEvents fetches a page of daily event summaries for the named source.
func (c *Client) ListDailyEvents(ctx context.Context, name string, page int) ([]api.DailyEventResponse, error) {
	raw, err := c.fetcher.FetchJSON(ctx, "GET", dailyEventsURL(name, page), nil, nil)
	if err != nil {
		return nil, err
	}
	var out []api.DailyEventResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode daily events: %w", err)
	}
	return out, nil
}

// ListExecutionErrors fetches a page of failed execution history records across all sources.
func (c *Client) ListExecutionErrors(ctx context.Context, page int) ([]api.ExecutionErrorResponse, error) {
	raw, err := c.fetcher.FetchJSON(ctx, "GET", executionErrorsURL(page), nil, nil)
	if err != nil {
		return nil, err
	}
	var out []api.ExecutionErrorResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode execution errors: %w", err)
	}
	return out, nil
}

// ListFailedNotifications fetches a window of failed notification pool records.
// The server uses ?offset=&limit= (not ?page=), mirroring index.html:333.
func (c *Client) ListFailedNotifications(ctx context.Context, offset, limit int) ([]api.NotificationResponse, error) {
	raw, err := c.fetcher.FetchJSON(ctx, "GET", failedNotificationsURL(offset, limit), nil, nil)
	if err != nil {
		return nil, err
	}
	var out []api.NotificationResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode failed notifications: %w", err)
	}
	return out, nil
}

// Stats fetches global statistics: source counts and total error count.
func (c *Client) Stats(ctx context.Context) (api.StatsResponse, error) {
	raw, err := c.fetcher.FetchJSON(ctx, "GET", statsURL(), nil, nil)
	if err != nil {
		return api.StatsResponse{}, err
	}
	var out api.StatsResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return api.StatsResponse{}, fmt.Errorf("decode stats: %w", err)
	}
	return out, nil
}

// SetSourceActive enables or disables the named source.
// The server returns 204 No Content on success; no response body is decoded.
func (c *Client) SetSourceActive(ctx context.Context, name string, active bool) error {
	return c.fetcher.FetchNoContent(ctx, "PATCH", sourceActiveURL(name), api.SourceActiveRequest{Active: active}, nil)
}

// MeSubscriptions fetches the caller's own subscriptions enriched with the latest rate
// values. initData is the Telegram WebApp initData string read by the caller from
// window.Telegram.WebApp.initData; this method sets the X-Telegram-Init-Data header
// from that parameter — it does not read js.Global() itself.
func (c *Client) MeSubscriptions(ctx context.Context, initData string, page, pageSize int, q string) (api.MeSubscriptionsResponse, error) {
	raw, err := c.fetcher.FetchJSON(ctx, "GET", meSubscriptionsURL(page, pageSize, q), nil, meSubscriptionsHeaders(initData))
	if err != nil {
		return api.MeSubscriptionsResponse{}, err
	}
	var out api.MeSubscriptionsResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return api.MeSubscriptionsResponse{}, fmt.Errorf("decode me subscriptions: %w", err)
	}
	return out, nil
}
