---
name: beacon-data-privacy
description: Beacon's personal-data policy in full — which fields are pre-approved for user-scoped tables (Telegram chat_id, IANA timezone, BCP-47 locale, user-selected city coordinates), which are off limits without an explicit policy change, how to classify a borderline field, and what may appear in logs. Load before adding any column to a user-scoped table, writing a migration that touches rate_user_* or weather_user_*, capturing anything new from a Telegram update, or adding a field to a log line or access-log format.
---

# Beacon data and privacy policy

This project stores the **minimum personal data required** to function as a Telegram bot.
The stance is not "zero PII" — that ship sailed when we started keying subscriptions by
Telegram `chat_id`. The stance is "no PII beyond what is strictly necessary for the bot to
deliver notifications."

## Pre-approved fields

These may be stored in user-scoped tables without further discussion:

- **Telegram `chat_id`** (column: `user_id` in `rate_user_subscriptions`,
  `rate_user_events`, `rate_user_profiles`). Unavoidable — the bot has no other way to
  address a user. Already PII under GDPR (stable persistent identifier), but the cost of
  avoiding it is "no bot."
- **IANA timezone** (e.g. `Asia/Almaty`, `Europe/Moscow`). Low-sensitivity: one of ~400
  values, weak identifying power on its own.
- **BCP-47 locale** (e.g. `ru-RU`, `kk-KZ`, `en-US`). Same as timezone — low-sensitivity,
  useful for future localisation of notification text.
- **City coordinates** (`latitude`, `longitude` in `weather_user_cities`). These are
  user-volunteered preferences — the user explicitly searches for and selects a named city
  from a geocoding result list. They are not device-collected, geolocation API, or
  IP-derived coordinates. The coordinates are stored to request weather data for the chosen
  city and carry no more identifying power than the city name itself. Guardrails: values are
  server-re-validated (lat ∈ [-90,90], lng ∈ [-180,180]) before persistence; the Open-Meteo
  geocoding call is the only source of coordinate values.

## Off-limits fields

Do **not** add any of these to user-scoped tables without an explicit policy change. If a
feature request seems to require one, push back on the design before writing SQL — there is
usually a way to achieve the same UX without persisting the field:

- Telegram `@username` / display name / first name / last name.
- Phone, email, or any other contact channel.
- Photo URL or any biometric.
- Device-collected or IP-derived precise location (lat/lng). Note: city coordinates
  explicitly chosen by the user from a geocoding search result are **pre-approved** (see
  above) — this prohibition is about coordinates obtained without the user's active
  selection (geolocation API, IP geolocation, etc.).
- IP address, device fingerprint, browser user-agent string.

## When a request looks borderline

If asked to add a field that is not on either list above, classify it first and surface the
trade-off before persisting it. Examples of borderline cases that warrant a sanity check:

- Subscription notes / tags entered by the user (free text → may contain PII).
- Last-active timestamp at high precision (the bot already has chat_id; do we also need to
  know exactly when each user opens the Mini App?).
- Per-user notification preferences beyond the minimal set already stored.

The default for "I'm not sure if this is OK" is **don't persist it yet, ask first**. Schema
changes that add identity-adjacent columns are easier to prevent than to revert from a
production database.

## Logs

The same policy applies to log output, with one practical relaxation: the bot's existing log
lines already include `chat=<chat_id>` for observability and that is fine. Do not log
`@username`, message body content, or any other off-limits field. The access-log format
`middleware [200] GET /api/v1/me/subscriptions` intentionally omits the
`X-Telegram-Init-Data` header for the same reason.
