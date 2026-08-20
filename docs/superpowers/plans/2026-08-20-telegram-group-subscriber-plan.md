# Telegram Group Subscriber Support Implementation Plan

Goal: Allow Telegram groups and other non-private chats to be configured as Smart Router subscribers from the Web console.

Architecture: Keep the existing BIGINT database field and delivery path. Add pure normalization helpers on the backend and frontend, include chat_type in create/update payloads, and add a Web selector and type badge.

Tech Stack: Go, Gin, existing Telegram API handler, Vue 3, Node built-in test runner.

---

Task 1: Add failing normalization tests

Files:
- Modify: internal/api/telegram_test.go
- Modify: web/tests/telegram.test.js

- Add backend cases for positive auto to private, negative auto to group, -100 auto to supergroup, explicit channel, zero rejection, and private/negative mismatch.
- Add frontend cases for the same auto mapping and invalid zero/type combinations.
- Run the focused Go and Node tests and confirm the new cases fail because the helpers do not exist.

Task 2: Implement backend normalization and validation

Files:
- Modify: internal/api/telegram.go

- Add normalizeSubscriberChat(chatID int64, requestedType string) returning the normalized type or a validation error.
- Accept private, group, supergroup, and channel; infer private for positive IDs, group for other negative IDs, and supergroup for negative IDs beginning with -100.
- Use the helper in CreateSubscriber and UpdateSubscriber. For an update with a new chat_id and no chat_type, update both chat_id and inferred chat_type. For a chat_type-only update, validate it against the stored chat_id.
- Preserve partial update behavior for unrelated subscriber fields.

Task 3: Implement frontend normalization and form support

Files:
- Modify: web/src/telegram.js
- Modify: web/tests/telegram.test.js
- Modify: web/src/views/SettingsView.vue

- Add normalizeTelegramSubscriberChat(chatID, requestedType) returning numeric chat_id and normalized chat_type.
- Include chat_type in subscriber create/update payloads and permit negative IDs in saveSubscriber.
- Add a native chat type selector with Auto, Private, Group, Supergroup, and Channel options.
- Initialize the selector when editing an existing subscriber, show a type column/badge, and retain the existing per-row test button.

Task 4: Verify

- Run go test ./internal/api ./internal/telegram.
- Run npm test from web.
- Run npm run lint from web and confirm no errors.
- Run npm run build from web.
- Run git diff --check and inspect for unrelated changes.
