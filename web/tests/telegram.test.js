import test from 'node:test'
import assert from 'node:assert/strict'
import { telegramTokenUpdatePayload, telegramSubscriberTestPath, normalizeTelegramSubscriberChat } from '../src/telegram.js'

test('does not create an empty Telegram config update payload', () => {
  assert.equal(telegramTokenUpdatePayload(''), null)
  assert.equal(telegramTokenUpdatePayload('   '), null)
})

test('creates a bot token update payload when a token is entered', () => {
  assert.deepEqual(telegramTokenUpdatePayload('  123:ABC  '), {
    bot_token: '123:ABC',
  })
})

test('builds the Telegram subscriber test endpoint path', () => {
  assert.equal(telegramSubscriberTestPath(42), '/admin/telegram/subscribers/42/test')
})


test('normalizes a negative Telegram group chat id', () => {
  assert.deepEqual(normalizeTelegramSubscriberChat('-1001234567890', 'auto'), {
    chat_id: '-1001234567890',
    chat_type: 'supergroup',
  })
})

test('rejects a private type for a negative Telegram chat id', () => {
  assert.throws(() => normalizeTelegramSubscriberChat('-123', 'private'), /private/i)
})


test('normalizes a positive Telegram private chat id', () => {
  assert.deepEqual(normalizeTelegramSubscriberChat('123456789', 'auto'), {
    chat_id: '123456789',
    chat_type: 'private',
  })
})

test('accepts an explicit Telegram channel type', () => {
  assert.deepEqual(normalizeTelegramSubscriberChat('-1001234567890', 'channel'), {
    chat_id: '-1001234567890',
    chat_type: 'channel',
  })
})

test('preserves leading zeros in a chat id', () => {
  assert.deepEqual(normalizeTelegramSubscriberChat('00123456789', 'auto'), {
    chat_id: '00123456789',
    chat_type: 'private',
  })
})

test('trims surrounding whitespace but keeps inner zeros', () => {
  assert.deepEqual(normalizeTelegramSubscriberChat('  00123  ', 'auto'), {
    chat_id: '00123',
    chat_type: 'private',
  })
})

test('preserves leading zeros for a supergroup chat id', () => {
  assert.deepEqual(normalizeTelegramSubscriberChat('-001001234567890', 'auto'), {
    chat_id: '-001001234567890',
    chat_type: 'supergroup',
  })
})

test('rejects a non-integer chat id', () => {
  assert.throws(() => normalizeTelegramSubscriberChat('00123abc', 'auto'), TypeError)
})

test('rejects a zero Telegram chat id', () => {
  assert.throws(() => normalizeTelegramSubscriberChat('0', 'auto'), TypeError)
})
