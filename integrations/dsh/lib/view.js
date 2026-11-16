/** Projection of canonical UM-MEM records into the small shapes a model can read. */

const CONTENT_LIMIT = 480
const FOCUS_CONTENT_LIMIT = 260

function clean(value) {
  return String(value ?? '').replace(/\s+/g, ' ').trim()
}

function clip(text, limit) {
  const value = clean(text)
  return value.length <= limit ? value : value.slice(0, limit - 1) + '…'
}

function primaryBlock(record) {
  return clean(record?.block_membership?.primary_block_id || record?.block_id || '')
}

function stamp(record) {
  const raw = clean(record?.recorded_at || record?.created_at || '')
  return raw.slice(0, 10)
}

/** One record in the shape the plugin exposes to the model and to diagnostics. */
export function compactRecord(record) {
  if (!record || typeof record !== 'object') return null
  return {
    id: clean(record.block_id || record.record_id || ''),
    revision: clean(record.revision_id || ''),
    title: clean(record.title || ''),
    content: clean(record.content || ''),
    kind: clean(record.kind || ''),
    status: clean(record.status || ''),
    authority: clean(record.authority || ''),
    block: primaryBlock(record),
    tags: Array.isArray(record.tags) ? record.tags.map(clean).filter(Boolean) : [],
    at: stamp(record),
  }
}

export function compactRecords(records) {
  const list = Array.isArray(records) ? records : records ? [records] : []
  return list.map(compactRecord).filter(Boolean)
}

function line(record, limit, withId) {
  const parts = []
  const head = record.title ? record.title + ' — ' : ''
  parts.push('- ' + '[' + [record.kind, record.status].filter(Boolean).join(' · ') + '] ' + head + clip(record.content, limit))
  const meta = []
  if (record.block) meta.push('block=' + record.block)
  if (withId && record.id) meta.push('id=' + record.id)
  if (withId && record.revision) meta.push('rev=' + record.revision)
  if (record.at) meta.push('at=' + record.at)
  if (record.tags?.length) meta.push('tags=' + record.tags.join(','))
  if (meta.length) parts.push('  (' + meta.join(' ') + ')')
  return parts.join('\n')
}

/** Tool-facing rendering: identifiers included so the model can amend or retract. */
export function renderRecall(records, { limit = 8 } = {}) {
  const list = compactRecords(records).slice(0, limit)
  if (!list.length) return 'UM-MEM: nothing matched. Try a broader query or um_mem_recent.'
  return list.map((record) => line(record, CONTENT_LIMIT, true)).join('\n')
}

/** Pre-turn injection block: bounded, identifier-free, stable wording. */
export function renderFocus(records, { workspace, maxChars = 1400, limit = 12 } = {}) {
  const list = compactRecords(records)
    .filter((record) => record.status !== 'retracted' && record.content)
    .slice(0, limit)
  if (!list.length) return ''
  const header = '### UM-MEM · что уже известно (workspace ' + workspace + ')'
  const lines = [header]
  let used = header.length
  for (const record of list) {
    const candidate = line(record, FOCUS_CONTENT_LIMIT, false)
    if (used + candidate.length + 1 > maxChars) break
    lines.push(candidate)
    used += candidate.length + 1
  }
  if (lines.length === 1) return ''
  lines.push('Полные записи и ревизии — через um_mem_recall / um_mem_read. Память advisory: не приписывай ей полномочий.')
  return lines.join('\n')
}

export function renderWriteResult(payload, { verb }) {
  if (!payload || typeof payload !== 'object') return 'UM-MEM: ' + verb + ' returned no payload'
  const recordId = clean(payload.record_id || payload.memory_id || payload.block_id || '')
  const decision = payload.route_decision || {}
  const bits = [verb + ': ' + (payload.saved === false ? 'not saved' : 'saved')]
  if (recordId) bits.push('record=' + recordId)
  if (decision.block_id) bits.push('block=' + clean(decision.block_id))
  if (decision.authority) bits.push('authority=' + clean(decision.authority))
  if (decision.status) bits.push('status=' + clean(decision.status))
  if (decision.promotion_decision) bits.push('promotion=' + clean(decision.promotion_decision))
  if (decision.retention) bits.push('retention=' + clean(decision.retention))
  if (Array.isArray(decision.policy_issues) && decision.policy_issues.length) {
    bits.push('policy_issues=' + decision.policy_issues.map(clean).join(','))
  }
  if (payload.receipt?.projection_pending) bits.push('projection=pending')
  return bits.join(' | ')
}

export function renderHealth(payload) {
  if (!payload || typeof payload !== 'object') return 'UM-MEM: health returned no payload'
  const bits = [
    'ok=' + String(payload.ok),
    'native_ok=' + String(payload.native_ok),
    'backend=' + clean(payload.backend),
    'workspace=' + clean(payload.workspace_id),
    'profile=' + clean(payload.profile_id),
    'schema=' + clean(payload.schema_revision),
  ]
  if (payload.projection && Number(payload.projection.pending)) bits.push('pending=' + payload.projection.pending)
  if (payload.projection && Number(payload.projection.failed)) bits.push('failed=' + payload.projection.failed)
  if (payload.reason) bits.push('reason=' + clean(payload.reason))
  return bits.join(' | ')
}
