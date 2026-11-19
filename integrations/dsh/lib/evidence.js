import { createHash } from 'node:crypto'
import { turnID } from './context-trace.js'

function backsTurn(record, sourceTurn) {
  return (record.evidence_refs ?? []).some(ref => {
    if (ref.ref_type !== 'message' || ref.produced_by !== 'human' || ref.producer_id !== 'harness-user') return false
    const match = /^(.*):([^:]+):[0-9a-f]{64}$/.exec(ref.ref_id)
    return Boolean(match && turnID(match[1], match[2]) === sourceTurn)
  })
}

function hostBacked(record) {
  return (record.evidence_refs ?? []).some(ref =>
    (ref.ref_type === 'message' && ref.produced_by === 'human' && ref.producer_id === 'harness-user') ||
    (ref.ref_type === 'tool_result' && ref.produced_by === 'harness' && ref.producer_id && ref.hash))
}

export function preferBackedRevision(records, maxChars, proofMaxChars) {
  const [first, ...remaining] = records ?? []
  const sourceTurn = first?.metadata?.harness_turn
  const block = first?.block_membership?.primary_block_id
  if (!sourceTurn || !block || (first.revision_of && backsTurn(first, sourceTurn))) return { records, maxChars }
  const revision = remaining.find(record => record.revision_of &&
    record.block_membership?.primary_block_id === block && backsTurn(record, sourceTurn))
  if (!revision) return { records, maxChars }
  const length = String(revision.content ?? '').replace(/\s+/g, ' ').trim().length
  const overhead = String(revision.block_id ?? '').length + String(revision.revision_id ?? '').length + 55
  const room = Math.max(maxChars, Math.min(proofMaxChars, length + overhead))
  return { records: [revision, ...remaining.filter(record => record !== revision)], maxChars: room }
}

const terms = text => new Set(String(text ?? '').normalize('NFKC').toLowerCase().match(/[\p{L}\p{N}]{4,}/gu) ?? [])
const literals = text => new Set((String(text ?? '').normalize('NFKC').toLowerCase()
  .match(/[\p{L}\p{N}]+(?:[-./:][\p{L}\p{N}]+)*/gu) ?? []).filter(word => /\p{N}/u.test(word)))

export function revisionUsesSource(before, after, source) {
  const incoming = literals(source)
  const retired = literals(before)
  if ([...literals(after)].some(value => incoming.has(value) && !retired.has(value))) return true
  const oldTerms = terms(before)
  const sourceTerms = terms(source)
  return [...terms(after)].filter(term => sourceTerms.has(term) && !oldTerms.has(term)).length >= 2
}

export function revisionLabels(record, content, args) {
  const before = literals(record?.content)
  const current = literals(content)
  const obsolete = value => [...literals(value)].some(word => before.has(word) && !current.has(word))
  const title = typeof args.title === 'string' && args.title.trim() || record?.title
  const tags = typeof args.tags === 'string' ? authoredTags(args.tags) : record?.tags
  const facets = args.facets == null ? record?.metadata?.memory_facets : authoredFacets(args.facets)
  const safeTitle = title && obsolete(title) ? title.split(/[,;|]/u).map(part => part.trim())
    .filter(part => !obsolete(part)).join(', ') || content.trim().slice(0, 80) : title
  return {
    ...(safeTitle && (safeTitle !== record?.title || args.title) ? { title: safeTitle } : {}),
    ...(tags && (args.tags != null || tags.some(obsolete)) ? { tags: tags.filter(tag => !obsolete(tag)) } : {}),
    ...(facets && (args.facets != null || Object.values(facets).some(obsolete)) ? {
      facets: Object.fromEntries(Object.entries(facets).filter(([, value]) => !obsolete(value))),
    } : {}),
  }
}

// The first complete ranked record supplies the answer. A second record
// earns space only if it covers a part of this request absent from the first.
export function adaptiveFocus(records, query) {
  const ranked = records ?? []
  const [primary, ...rest] = ranked
  if (!primary) return []
  const backed = preferBackedRevision(ranked, Infinity, Infinity)
  if (backed.records !== ranked) return [backed.records[0]]
  const known = terms(primary.content)
  const uncovered = [...terms(query)].filter(term => !known.has(term))
  const owner = primary.block_membership?.primary_block_id
  const confirmed = hostBacked(primary)
  const secondary = rest.find(record => {
    if (record.metadata?.retrieval_truncated) return false
    const delivered = terms(record.content)
    if (confirmed && owner && record.block_membership?.primary_block_id === owner && !hostBacked(record) &&
        [...delivered].filter(term => known.has(term)).length >= 3) return false
    return uncovered.filter(term => delivered.has(term)).length >= 2
  })
  return secondary ? [primary, secondary] : [primary]
}

// The host archives user statements in the project block. A verified quote can
// qualify a note from another topic when both belong to the same workspace.
export function sourceForFocus(primary, history, sourceBlock = primary?.block_membership?.primary_block_id, { newerOnly = false } = {}) {
  if (!primary) return null
  const prior = Date.parse(primary.recorded_at)
  if (!Number.isFinite(prior)) return null
  const known = terms(primary.content)
  const knownLiterals = literals(primary.content)
  let best
  let coverage = 0
  let backed
  let backedHasLiteral = false
  for (const record of history) {
    const turn = record.metadata?.harness_turn
    if (!sourceBlock || record.block_id === primary.block_id || record.block_membership?.primary_block_id !== sourceBlock ||
      record.scope !== primary.scope || record.workspace_id !== primary.workspace_id ||
      record.profile_id !== primary.profile_id ||
      record.kind !== 'source_excerpt' || record.metadata?.source !== 'dsh-user' ||
      !turn || !backsTurn(record, turn) ||
      (Date.parse(record.recorded_at) <= prior && (newerOnly || !backsTurn(primary, turn)))) continue
    if (!newerOnly && backsTurn(primary, turn)) {
      const hasLiteral = [...literals(record.content)].some(value => knownLiterals.has(value))
      if (!backed || (hasLiteral && !backedHasLiteral) ||
          (hasLiteral === backedHasLiteral && Date.parse(record.recorded_at) > Date.parse(backed.recorded_at))) {
        backed = record
        backedHasLiteral = hasLiteral
      }
    }
    const shared = [...terms(record.content)].filter(term => known.has(term)).length
    if (shared >= 3 && shared > coverage) {
      best = record
      coverage = shared
    }
  }
  return backed ?? best ?? null
}

export function earlierProof(primary, source, history) {
  if (!source) return null
  const sourceTurn = source.metadata?.harness_turn
  if (!sourceTurn || !backsTurn(primary, sourceTurn)) return null
  const sourceValues = literals(source.content)
  const carried = [...literals(primary.content)].filter(value => !sourceValues.has(value))
  const known = terms(primary.content)
  let best, score = 0
  for (const record of history) {
    const turn = record.metadata?.harness_turn
    if (!turn || record.kind !== 'source_excerpt' || record.metadata?.source !== 'dsh-user' ||
        record.block_membership?.primary_block_id !== source.block_membership?.primary_block_id || record.scope !== source.scope ||
        record.workspace_id !== source.workspace_id || record.profile_id !== source.profile_id ||
        !backsTurn(record, turn) || Date.parse(record.recorded_at) >= Date.parse(source.recorded_at)) continue
    const values = literals(record.content)
    const matches = carried.filter(value => values.has(value)).length
    if (!matches) continue
    const linked = backsTurn(primary, turn)
    const shared = [...terms(record.content)].filter(term => known.has(term)).length
    if (!linked && shared < 3) continue
    const covered = matches * 2 + (linked ? 1 : 0)
    if (covered > score) {
      best = record
      score = covered
    }
  }
  return best ?? null
}

const exactTerms = query => query.normalize('NFKC').toLowerCase().trim().replace(/[?!]+$/u, '').replace(/\s+/gu, ' ')
export function recallFingerprint(query) {
  const normalized = exactTerms(query)
  const keywords = normalized.split(',').map(part => part.trim())
  // Reorder a bare keyword list only. Phrases retain order and negation.
  if (keywords.length >= 2 && keywords.every(part => /^[\p{L}\p{N}_-]+$/u.test(part))) {
    return keywords.sort().join(',')
  }
  return normalized
}

export function authoredTags(value) {
  const tags = new Map()
  for (const item of String(value ?? '').split(',')) {
    const tag = item.normalize('NFKC').trim().replace(/\s+/gu, ' ')
    if (tag && !tags.has(tag.toLowerCase())) tags.set(tag.toLowerCase(), tag)
  }
  return [...tags.values()]
}

export function authoredFacets(value) {
  return Object.fromEntries(['purpose', 'applies_when', 'review_when'].flatMap(key => {
    const text = typeof value?.[key] === 'string' ? value[key].trim().replace(/\s+/gu, ' ').slice(0, 320) : ''
    return text ? [[key, text]] : []
  }))
}

export function sourceExcerpts(sessionId, turn, text) {
  const value = String(text ?? '').trim()
  if (!value) return []
  const points = Array.from(value)
  const source = createHash('sha256').update(JSON.stringify([sessionId, turn, value])).digest('hex')
  const heading = value.split(/\r?\n/).find(line =>
    line.length <= 110 && /\d/.test(line) && /\b\d{4}\b/.test(line))?.trim() ?? ''
  const result = []
  let start = 0
  while (start < points.length) {
    let end = Math.min(start + 740, points.length)
    if (end < points.length) {
      const floor = start + 490
      for (let cursor = end; cursor > floor; cursor--) {
        if (points[cursor - 1] === '\n' || points[cursor - 1] === ' ' &&
            /[.!?]/.test(points[cursor - 2] ?? '')) {
          end = cursor
          break
        }
      }
    }
    const quote = points.slice(start, end).join('').trim()
    if (quote) {
      const index = result.length + 1
      result.push({
        operation_id: 'dsh-source-' + source + '-' + index,
        title: heading || value.slice(0, 72).replace(/\s+/g, ' '),
        content: (heading && !quote.includes(heading) ? heading + '\n' : '') + quote,
        kind: 'source_excerpt',
        evidence_kind: 'user_statement',
        source_id: 'dsh-user:' + source,
        metadata: { source: 'dsh-user', excerpt: index },
        evidence_refs: [{ ref_type: 'message', ref_id: sessionId + ':' + turn + ':' + source,
          produced_by: 'human', producer_id: 'harness-user' }],
      })
    }
    if (end === points.length) break
    start = end - 110
  }
  return result
}
