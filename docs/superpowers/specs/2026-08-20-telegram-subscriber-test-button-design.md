# Telegram Subscriber Test Button Design

Date: 2026-08-20

## Goal

Expose the existing per-subscriber Telegram test-message endpoint in the Web subscriber list so an administrator can verify one subscriber's delivery path without sending a full report.

## Current state

- The backend endpoint already exists: POST /admin/telegram/subscribers/:id/test.
- The frontend API wrapper already exposes sendTelegramSubscriberTest(id).
- SettingsView renders subscriber row actions but does not render or call a test action.

## Design

Add a Test button to each Telegram subscriber row. The button calls the existing API wrapper with the row id, disables only that row while the request is active, and shows the server success message through the existing toast flow. Errors continue through the existing API error handling. The button receives an accessible aria-label.

No backend, database, or Telegram protocol changes are required.

## Acceptance criteria

1. Every subscriber row has a visible test action next to the existing enable, edit, and delete actions.
2. Clicking the action calls POST /admin/telegram/subscribers/:id/test exactly once for that click.
3. Only the clicked row is disabled while its request is pending.
4. A successful response displays a success toast.
5. A failed response does not leave the row stuck in a loading state.
6. A frontend regression test covers the subscriber test endpoint path construction.
7. Existing Telegram configuration behavior remains unchanged.
