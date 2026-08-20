# Telegram Group Subscriber Support Design

Date: 2026-08-20

## Goal

Allow the Web console to register Telegram private chats, groups, supergroups, and channels as alert subscribers, including negative Telegram chat IDs.

## Current state

The database already stores chat_id as BIGINT and chat_type as VARCHAR. The backend and Web form currently reject all non-positive IDs, and the Web form does not send chat_type. The existing send-message endpoint and subscriber worker can already use the stored chat_id.

## Design

Add one shared frontend normalization helper for form values and one backend normalization helper for API defense in depth. A positive ID is private. A negative ID is a group; IDs beginning with -100 are auto-labeled supergroup. Channels remain available through an explicit type selection because their numeric ID shape can overlap with supergroups.

The Web subscriber form gains a chat type select with Auto, Private chat, Group, Supergroup, and Channel. The payload includes chat_type. The subscriber table displays the normalized type. Create and update APIs accept chat_type values private, group, supergroup, and channel, reject zero IDs and sign/type mismatches, and infer a type when chat_type is omitted.

No database migration is required.

## Acceptance criteria

1. A negative group or supergroup Chat ID can be created from the Web form.
2. Existing positive private Chat IDs continue to work.
3. Explicit channel type is accepted for a negative Chat ID.
4. Zero IDs and sign/type mismatches are rejected with a clear validation error.
5. Existing subscriber test-message and scheduled-report delivery use the stored negative ID unchanged.
6. The subscriber list shows the stored chat type.
7. Backend and frontend regression tests cover inference and validation.
