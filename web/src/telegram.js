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
  // 保留用户输入的原样字符串（如 00123456789 的前导零），
  // 数值仅用于校验与正负判断，不做去零转换。
  const raw = String(chatIDInput ?? '').trim()
  let chatID
  try {
    chatID = BigInt(raw)
  } catch {
    throw new TypeError('Chat ID 必须是非零整数')
  }
  if (chatID === 0n) {
    throw new TypeError('Chat ID 必须是非零整数')
  }

  let chatType = String(requestedType || 'auto').trim().toLowerCase()
  if (!chatType || chatType === 'auto') {
    if (chatID > 0n) chatType = 'private'
    // 按数值判断（String(chatID) 无前导零）：与后端数值语义一致
    else if (String(chatID).startsWith('-100')) chatType = 'supergroup'
    else chatType = 'group'
  }

  if (!['private', 'group', 'supergroup', 'channel'].includes(chatType)) {
    throw new TypeError('不支持的 Telegram 会话类型')
  }
  if (chatType === 'private' && chatID < 0n) {
    throw new TypeError('private Chat ID 必须为正数')
  }
  if (chatType !== 'private' && chatID > 0n) {
    throw new TypeError(chatType + ' Chat ID 必须为负数')
  }

  return { chat_id: raw, chat_type: chatType }
}
