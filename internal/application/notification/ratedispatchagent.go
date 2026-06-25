// Package notification implements the two-agent notification pipeline:
// RateCheckAgent evaluates subscription conditions and queues events, and
// RateDispatchAgent delivers queued events via the configured transport.
package notification

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/domain"
	integration "github.com/seilbekskindirov/monitor/internal/infrastructure/telegrambot"
)

// NewRateDispatchAgent constructs a RateDispatchAgent wired to the given transport and event repository.
func NewRateDispatchAgent(
	cltTelegram telegramClient,
	rRateUserEvent rateUserEventRepository,
) (*RateDispatchAgent, error) {

	a := &RateDispatchAgent{
		rateUserEventRepository: rRateUserEvent,
		telegramClient:          cltTelegram,
		ttl:                     24 * time.Hour,
	}

	return a, nil
}

// RateDispatchAgent delivers pending and failed notification events and cancels
// events past the 24-hour TTL. One-shot: runs to completion per invocation.
type RateDispatchAgent struct {
	telegramClient          telegramClient
	rateUserEventRepository rateUserEventRepository
	ttl                     time.Duration
}

// rateUserEventRepository is the narrow storage interface required by this service.
type rateUserEventRepository interface {
	ObtainUnprocessedRateUserEvents(context.Context) ([]domain.RateUserEvent, error)
	RetainRateUserEvent(context.Context, *domain.RateUserEvent) error
	RemoveRateUserEventOlderThan(ctx context.Context, duration time.Duration) error
}

// telegramClient is the narrow Telegram transport interface required by this service.
type telegramClient interface {
	SendHTMLMessage(context.Context, integration.TelegramChatID, string) error
}

// Run fetches all unprocessed events and attempts delivery, updating each
// event's status. Returns a joined error containing all delivery failures.
func (a *RateDispatchAgent) Run(ctx context.Context) error {
	events, err := a.rateUserEventRepository.ObtainUnprocessedRateUserEvents(ctx)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	var errs []error

	now := time.Now().UTC()
	ttl := now.Add(-a.ttl)

	for _, event := range events {
		event.SentAt = now

		if event.CreatedAt.Before(ttl) {
			event.Status = domain.RateUserEventStatusCanceled
			event.LastError = strings.Join([]string{fmt.Sprintf("TTL (%s) exceeded", a.ttl.String()), event.LastError}, "\n")
		} else {
			switch event.UserType {
			case domain.UserTypeTelegram:
				err = a.runUserTypeTelegram(ctx, &event)
			default:
				err = fmt.Errorf("unsupported user type: %s", event.UserType)
			}
			if err != nil {
				event.LastError = err.Error()
				event.Status = domain.RateUserEventStatusFailed
				errs = append(errs, errors.Join(err, internal.NewTraceError()))
			} else {
				event.Status = domain.RateUserEventStatusSent
				event.LastError = ""
			}
		}

		err = a.rateUserEventRepository.RetainRateUserEvent(ctx, &event)
		if err != nil {
			errs = append(errs, errors.Join(err, internal.NewTraceError()))
		}

		// Delay to avoid Telegram rate limits. ctx-aware so SIGTERM mid-batch
		// aborts immediately instead of holding for up to (500ms × remaining events).
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return errors.Join(errs...)
		}
	}

	return errors.Join(errs...)
}

// Vacuum removes all non-pending records older than 180 days.
func (a *RateDispatchAgent) Vacuum(ctx context.Context) error {
	return a.rateUserEventRepository.RemoveRateUserEventOlderThan(ctx, 180*24*time.Hour)
}

func (a *RateDispatchAgent) runUserTypeTelegram(ctx context.Context, event *domain.RateUserEvent) error {
	if event == nil {
		err := errors.New("notification record is nil")
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	chatID, err := strconv.ParseInt(event.UserID, 10, 64)
	if err != nil || chatID == 0 {
		if err == nil {
			err = fmt.Errorf("invalid user id: %s", event.UserID)
		}
		err = errors.Join(fmt.Errorf("invalid Telegram chat ID: %s", event.UserID), err)
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	err = a.telegramClient.SendHTMLMessage(ctx, integration.TelegramChatID(chatID), event.Message)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	return nil
}
