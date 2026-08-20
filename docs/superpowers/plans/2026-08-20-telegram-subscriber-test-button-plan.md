# Telegram Subscriber Test Button Implementation Plan

Goal: Add a per-subscriber Telegram test-message button to the Web settings page.

Architecture: Reuse the existing backend endpoint and frontend API surface. Extract the endpoint path construction into a small tested helper, then add a row-local pending id and action handler in SettingsView.vue.

Tech Stack: Vue 3, Vite, Node built-in test runner, existing API client and toast store.

---

Task 1: Add the failing regression test

Files:
- Modify: web/tests/telegram.test.js
- Modify: web/src/telegram.js

- Add a test asserting that subscriber id 42 maps to /admin/telegram/subscribers/42/test.
- Run node --test tests/telegram.test.js and confirm it fails because the helper is not defined.

Task 2: Implement the API path helper

Files:
- Modify: web/src/telegram.js
- Modify: web/src/api.js

- Add telegramSubscriberTestPath(id) that accepts a positive integer and returns /admin/telegram/subscribers/{id}/test.
- Make api.sendTelegramSubscriberTest(id) use that helper and preserve the empty JSON POST body.
- Run the focused Telegram test and confirm it passes.

Task 3: Add the subscriber-row action

Files:
- Modify: web/src/views/SettingsView.vue

- Add a telegramTestSendingId ref initialized to null.
- Add async function testSubscriber(s) that sets the row id, calls api.sendTelegramSubscriberTest(s.id), displays the response message as a success toast, and clears the id in finally.
- Add a row action button labeled 测试, disabled when its row id is pending, with an accessible label.

Task 4: Verify the feature

- Run npm test from web and confirm all tests pass.
- Run npm run lint and confirm there are no errors; retain only the pre-existing warnings if present.
- Run npm run build and confirm the production build succeeds.
- Run git diff --check and inspect the final diff for unrelated changes.
