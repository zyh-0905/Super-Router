import test from 'node:test'
import assert from 'node:assert/strict'
import { parseSSEChunk, mergeQualityEvent, sanitizeQualityDetails, isTerminalStatus, qualityLabel, stageLabel } from '../src/quality.js'

test('parses split SSE frames', () => {
  const first = parseSSEChunk('', 'event: stage_result\ndata: {"stage":"stream"')
  assert.equal(first.events.length, 0)
  const second = parseSSEChunk(first.buffer, '}\n\n')
  assert.equal(second.events[0].event, 'stage_result')
})

test('merges stage result without losing earlier stages', () => {
  const state = mergeQualityEvent({ stages: { connectivity: { status: 'passed' } } }, {
    event: 'stage_result', data: { stage: 'stream', status: 'passed' }
  })
  assert.equal(state.stages.connectivity.status, 'passed')
  assert.equal(state.stages.stream.status, 'passed')
})

test('removes credential-shaped fields from details', () => {
  const safe = sanitizeQualityDetails({ latency_ms: 12, authorization: 'Bearer secret', api_key: 'secret' })
  assert.deepEqual(safe, { latency_ms: 12 })
})

test('sanitizeQualityDetails handles nested objects', () => {
  const safe = sanitizeQualityDetails({
    ok: 1,
    headers: { authorization: 'Bearer x', accept: 'application/json' },
    body: { api_key: 'k', prompt: 'hi' },
  })
  assert.deepEqual(safe, { ok: 1, body: { prompt: 'hi' } })
})

test('sanitizeQualityDetails tolerates non-objects', () => {
  assert.deepEqual(sanitizeQualityDetails(null), null)
  assert.deepEqual(sanitizeQualityDetails('x'), 'x')
  assert.deepEqual(sanitizeQualityDetails([1, 2]), [1, 2])
})

test('isTerminalStatus', () => {
  for (const s of ['completed', 'failed', 'cancelled', 'expired']) {
    assert.equal(isTerminalStatus(s), true, s)
  }
  for (const s of ['queued', 'running', 'cancel_requested']) {
    assert.equal(isTerminalStatus(s), false, s)
  }
})

test('qualityLabel maps overall statuses', () => {
  assert.equal(qualityLabel('good'), '良好')
  assert.equal(qualityLabel('attention'), '需要关注')
  assert.equal(qualityLabel('failed'), '异常')
  assert.equal(qualityLabel('unknown'), '无法判断')
})

test('stageLabel maps stages', () => {
  assert.equal(stageLabel('connectivity'), '连接性')
  assert.equal(stageLabel('protocol'), '协议一致性')
  assert.equal(stageLabel('stream'), '流式响应')
  assert.equal(stageLabel('usage'), 'Usage/计费')
  assert.equal(stageLabel('behavior'), '模型行为')
})

test('mergeQualityEvent handles progress events', () => {
  const state = mergeQualityEvent({}, { event: 'task_progress', data: { progress: 42, current_stage: 'stream' } })
  assert.equal(state.progress, 42)
  assert.equal(state.currentStage, 'stream')
})

test('mergeQualityEvent ignores unknown events', () => {
  const state = mergeQualityEvent({ x: 1 }, { event: 'something_else', data: {} })
  assert.equal(state.x, 1)
})
