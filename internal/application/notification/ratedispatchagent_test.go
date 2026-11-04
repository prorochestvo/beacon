package notification

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
	integration "github.com/seilbekskindirov/beacon/internal/infrastructure/telegrambot"
	"github.com/seilbekskindirov/beacon/internal/repository"
	"github.com/stretchr/testify/require"
)

var _ rateUserEventRepository = &repository.RateUserEventRepository{}

// TestRateDispatchAgent_AbortIsMarked: a failed pool read means nothing was dispatched at
// all, as opposed to individual sends failing inside the loop.
func TestRateDispatchAgent_AbortIsMarked(t *testing.T) {
	t.Parallel()

	readErr := errors.New("pool read interrupted")
	agent, err := NewRateDispatchAgent(&mockTelegramClient{}, &mockRateUserEventRepository{obtainErr: readErr})
	require.NoError(t, err)

	runErr := agent.Run(t.Context())
	require.Error(t, runErr)
	require.ErrorIs(t, runErr, internal.ErrAgentAborted)
	require.ErrorIs(t, runErr, readErr, "the cause must survive the marking")
}

func TestNewRateDispatchAgent(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		agent, err := NewRateDispatchAgent(&mockTelegramClient{}, &mockRateUserEventRepository{})
		require.NoError(t, err)
		require.NotNil(t, agent)
	})
}

func TestRateDispatchAgent_Run(t *testing.T) {
	t.Parallel()

	t.Run("pending telegram event is sent and marked sent", func(t *testing.T) {
		t.Parallel()

		repo := &mockRateUserEventRepository{
			events: []domain.RateUserEvent{
				{
					UserType:  domain.UserTypeTelegram,
					UserID:    "123456",
					Message:   "hello",
					Status:    domain.RateUserEventStatusPending,
					CreatedAt: time.Now().UTC(),
				},
			},
		}
		tg := &mockTelegramClient{}

		agent, err := NewRateDispatchAgent(tg, repo)
		require.NoError(t, err)

		require.NoError(t, agent.Run(t.Context()))
		require.Len(t, repo.retained, 1)
		require.Equal(t, domain.RateUserEventStatusSent, repo.retained[0].Status)
		require.False(t, repo.retained[0].SentAt.IsZero())
	})

	t.Run("telegram failure marks event failed", func(t *testing.T) {
		t.Parallel()

		repo := &mockRateUserEventRepository{
			events: []domain.RateUserEvent{
				{
					UserType:  domain.UserTypeTelegram,
					UserID:    "123456",
					Message:   "hello",
					Status:    domain.RateUserEventStatusPending,
					CreatedAt: time.Now().UTC(),
				},
			},
		}
		tg := &mockTelegramClient{err: errors.New("send failed")}

		agent, err := NewRateDispatchAgent(tg, repo)
		require.NoError(t, err)

		err = agent.Run(t.Context())
		require.Error(t, err, "telegram send failure must surface to the caller")
		require.Contains(t, err.Error(), "send failed")
		require.Len(t, repo.retained, 1)
		require.Equal(t, domain.RateUserEventStatusFailed, repo.retained[0].Status)
		require.NotEmpty(t, repo.retained[0].LastError)
	})

	t.Run("event older than 24h TTL is canceled", func(t *testing.T) {
		t.Parallel()

		repo := &mockRateUserEventRepository{
			events: []domain.RateUserEvent{
				{
					UserType:  domain.UserTypeTelegram,
					UserID:    "123456",
					Message:   "stale",
					Status:    domain.RateUserEventStatusPending,
					CreatedAt: time.Now().UTC().Add(-25 * time.Hour),
				},
			},
		}
		tg := &mockTelegramClient{}

		agent, err := NewRateDispatchAgent(tg, repo)
		require.NoError(t, err)

		require.NoError(t, agent.Run(t.Context()))
		require.Len(t, repo.retained, 1)
		require.Equal(t, domain.RateUserEventStatusCanceled, repo.retained[0].Status)
		require.Equal(t, 0, tg.calls)
	})

	t.Run("unsupported user type returns error", func(t *testing.T) {
		t.Parallel()

		repo := &mockRateUserEventRepository{
			events: []domain.RateUserEvent{
				{
					UserType:  "bogus",
					UserID:    "123456",
					Message:   "msg",
					Status:    domain.RateUserEventStatusPending,
					CreatedAt: time.Now().UTC(),
				},
			},
		}

		agent, err := NewRateDispatchAgent(&mockTelegramClient{}, repo)
		require.NoError(t, err)

		err = agent.Run(t.Context())
		require.Error(t, err)
	})

	t.Run("empty event list returns nil error", func(t *testing.T) {
		t.Parallel()

		repo := &mockRateUserEventRepository{events: []domain.RateUserEvent{}}
		agent, err := NewRateDispatchAgent(&mockTelegramClient{}, repo)
		require.NoError(t, err)

		require.NoError(t, agent.Run(t.Context()))
	})
	t.Run("repo error is propagated", func(t *testing.T) {
		t.Parallel()

		repo := &mockRateUserEventRepository{obtainErr: errors.New("db down")}
		agent, err := NewRateDispatchAgent(&mockTelegramClient{}, repo)
		require.NoError(t, err)

		require.Error(t, agent.Run(t.Context()))
	})
}

func TestRateDispatchAgent_runUserTypeTelegram(t *testing.T) {
	t.Parallel()

	t.Run("valid chat ID sends message", func(t *testing.T) {
		t.Parallel()

		tg := &mockTelegramClient{}
		agent, err := NewRateDispatchAgent(tg, &mockRateUserEventRepository{})
		require.NoError(t, err)

		event := &domain.RateUserEvent{UserID: "123456", Message: "hi"}
		require.NoError(t, agent.runUserTypeTelegram(t.Context(), event))
		require.Equal(t, 1, tg.calls)
	})

	t.Run("non-numeric UserID returns error", func(t *testing.T) {
		t.Parallel()

		agent, err := NewRateDispatchAgent(&mockTelegramClient{}, &mockRateUserEventRepository{})
		require.NoError(t, err)

		event := &domain.RateUserEvent{UserID: "abc", Message: "hi"}
		require.Error(t, agent.runUserTypeTelegram(t.Context(), event))
	})

	t.Run("zero UserID returns error", func(t *testing.T) {
		t.Parallel()

		agent, err := NewRateDispatchAgent(&mockTelegramClient{}, &mockRateUserEventRepository{})
		require.NoError(t, err)

		event := &domain.RateUserEvent{UserID: "0", Message: "hi"}
		require.Error(t, agent.runUserTypeTelegram(t.Context(), event))
	})

	t.Run("telegram client error propagated", func(t *testing.T) {
		t.Parallel()

		tg := &mockTelegramClient{err: errors.New("network error")}
		agent, err := NewRateDispatchAgent(tg, &mockRateUserEventRepository{})
		require.NoError(t, err)

		event := &domain.RateUserEvent{UserID: "123456", Message: "hi"}
		require.Error(t, agent.runUserTypeTelegram(t.Context(), event))
	})

	t.Run("nil event returns error", func(t *testing.T) {
		t.Parallel()

		agent, err := NewRateDispatchAgent(&mockTelegramClient{}, &mockRateUserEventRepository{})
		require.NoError(t, err)

		require.Error(t, agent.runUserTypeTelegram(t.Context(), nil))
	})
}

func TestRateDispatchAgent_Vacuum(t *testing.T) {
	t.Parallel()

	t.Run("calls repo with 180-day duration", func(t *testing.T) {
		t.Parallel()

		repo := &mockRateUserEventRepository{}
		agent, err := NewRateDispatchAgent(&mockTelegramClient{}, repo)
		require.NoError(t, err)

		require.NoError(t, agent.Vacuum(t.Context()))
		// Spelled out rather than compared against defaultEventRetention, which would
		// assert the constant equals itself. This deletes user data irreversibly, so
		// the retention window is pinned here and changing it has to be deliberate.
		require.Equal(t, 180*24*time.Hour, repo.removedDuration)
	})

	t.Run("repo error is propagated", func(t *testing.T) {
		t.Parallel()

		repo := &mockRateUserEventRepository{retainErr: errors.New("db error")}
		repo.removeErr = errors.New("remove failed")
		agent, err := NewRateDispatchAgent(&mockTelegramClient{}, repo)
		require.NoError(t, err)

		require.Error(t, agent.Vacuum(t.Context()))
	})
}

// The two seams this agent takes, asserted against the fakes that stand in for them.
// Without this an interface change breaks the production implementation loudly and the
// fake silently. Pre-existing gap; the file changed, so the gate holds it to R1.
var (
	_ rateUserEventRepository = (*mockRateUserEventRepository)(nil)
	_ telegramClient          = (*mockTelegramClient)(nil)
)

type mockRateUserEventRepository struct {
	events          []domain.RateUserEvent
	retained        []*domain.RateUserEvent
	obtainErr       error
	retainErr       error
	removeErr       error
	removedDuration time.Duration
}

func (m *mockRateUserEventRepository) ObtainUnprocessedRateUserEvents(_ context.Context) ([]domain.RateUserEvent, error) {
	return m.events, m.obtainErr
}

func (m *mockRateUserEventRepository) RetainRateUserEvent(_ context.Context, e *domain.RateUserEvent) error {
	cp := *e
	m.retained = append(m.retained, &cp)
	return m.retainErr
}

func (m *mockRateUserEventRepository) RemoveRateUserEventOlderThan(_ context.Context, d time.Duration) error {
	m.removedDuration = d
	return m.removeErr
}

type mockTelegramClient struct {
	err   error
	calls int
}

func (m *mockTelegramClient) SendHTMLMessage(_ context.Context, _ integration.TelegramChatID, _ string) error {
	m.calls++
	return m.err
}
