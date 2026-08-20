export function telegramTokenUpdatePayload(tokenDraft) {
  const token = String(tokenDraft || '').trim()
  return token ? { bot_token: token } : null
}

export function telegramSubscriberTestPath(id) {
  if (!Number.isInteger(id) || id <= 0) {
    throw new TypeError('subscriber id must be a positive integer')
  }
  return `/admin/telegram/subscribers/${id}/test`
}

export function normalizeTelegramSubscriberChat(chatIDInput, requestedType = 'auto') {
  const chatID = Number(chatIDInput)
  if (!Number.isSafeInteger(chatID) || chatID === 0) {
    throw new TypeError('Chat ID 必须是非零整数')
  }

  let chatType = String(requestedType || 'auto').trim().toLowerCase()
  if (!chatType || chatType === 'auto') {
    if (chatID > 0) chatType = 'private'
    else if (String(chatID).startsWith('-100')) chatType = 'supergroup'
    else chatType = 'group'
  }

  if (!['private', 'group', 'supergroup', 'channel'].includes(chatType)) {
    throw new TypeError('不支持的 Telegram 会话类型')
  }
  if (chatType === 'private' && chatID < 0) {
    throw new TypeError('private Chat ID 必须为正数')
  }
  if (chatType !== 'private' && chatID > 0) {
    throw new TypeError(chatType + ' Chat ID 必须为负数')
  }

  return { chat_id: chatID, chat_type: chatType }
}
