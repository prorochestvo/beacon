// Package profile is the application service behind a user's own profile: the
// timezone and locale that decide how a notification is rendered for them.
//
// It is free of HTTP concerns, and it is deliberately this small. What a user
// may store about themselves is a policy question rather than a feature one —
// see the project's data-privacy rules before adding a field here.
package profile

import (
	"context"
	"fmt"
	"strings"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
)

// Profile is what a user may record about themselves: an IANA timezone and a
// BCP-47 locale, both of which exist so a notification reads correctly at the
// other end.
type Profile struct {
	Timezone string
	Locale   string
}

// Service stores a user's profile. Construct it with NewService; it holds no
// mutable state and is safe for concurrent use.
type Service struct {
	profiles Store
}

// NewService constructs a Service over the profile store. In production that is
// a repository.
func NewService(profiles Store) *Service {
	return &Service{profiles: profiles}
}

// UpsertMeProfile records userID's timezone and locale, replacing whatever was
// stored before. Callers send it on every session start and discard the result,
// so it must be safe to repeat.
//
// A rejection the caller can act on is a *internal.PublicError. Whether the
// timezone actually loads is settled by the store, which answers the same way.
func (s *Service) UpsertMeProfile(ctx context.Context, userID string, p Profile) error {
	timezone := strings.TrimSpace(p.Timezone)
	locale := strings.TrimSpace(p.Locale)

	if timezone == "" {
		return internal.NewPublicError("timezone is required")
	}
	// BCP-47 tags top out around 35 characters in practice, so 64 rejects
	// nothing real while keeping a buggy client from writing megabytes into the
	// column.
	if len(locale) > maxLocaleLength {
		return internal.NewPublicError("locale too long")
	}

	record := &domain.RateUserProfile{
		UserType: domain.UserTypeTelegram,
		UserID:   userID,
		Timezone: timezone,
		Locale:   locale,
	}
	if err := s.profiles.UpsertRateUserProfile(ctx, record); err != nil {
		return fmt.Errorf("upsert profile: %w", err)
	}
	return nil
}

// Store persists user profiles. Satisfied by
// *repository.RateUserProfileRepository.
//
// It reports a *internal.PublicError for a timezone that will not load — the one
// check this service leaves to it, because the store is what has to read the
// value back.
type Store interface {
	UpsertRateUserProfile(ctx context.Context, record *domain.RateUserProfile) error
}

// maxLocaleLength bounds the stored locale tag.
const maxLocaleLength = 64
