/**
 * dsh-um-mem — UM-MEM memory for DeepSeek Harness (host half).
 *
 * Two surfaces:
 *   1. explicit tools (recall / remember / amend / forget / read / recent / health)
 *      backed by the local `um-memory` CLI;
 *   2. a bounded pre-turn memory block, refreshed from the last user message,
 *      injected through `systemPrompt.context` (user-role tail, deduplicated by
 *      the host while its bytes stay unchanged).
 *
 * Records stay advisory: a retrieved record never authorises an action, and a
 * failed write is reported as an error instead of a silent success.
 */

import { createHash } from 'node:crypto'
import { appendFileSync, existsSync, mkdirSync, readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { commonArgs, runCli, runWithRequest } from './cli.js'
import { resolveConfig } from './config.js'
import { adaptiveFocus, authoredFacets, authoredTags, earlierProof, preferBackedRevision, recallFingerprint, revisionLabels, revisionUsesSource, sourceExcerpts, sourceForFocus } from './evidence.js'
import { traceTurn, turnID } from './context-trace.js'
import { compactRecords, renderFocus, renderHealth, renderRecall, renderWriteResult } from './view.js'

export const name = 'um-mem'
export const inject = ['tools', 'systemPrompt']

// Capture used to fail silently: an episode simply never appeared and there was
// no way to tell a host that never called the hook from a filter that rejected the
// text. This log answers that question without guessing.
function logEvent(kind, data) {
  try {
    const dir = join(homedir(), '.um-mem')
    mkdirSync(dir, { recursive: true })
    appendFileSync(join(dir, 'plugin-events.log'),
      JSON.stringify({ at: new Date().toISOString(), kind, ...data }) + '\n', 'utf8')
  } catch {
    /* diagnostics must never break the turn */
  }
}

/** Static rules must stay byte-stable: they are the prefix-cache anchor. */
const RULES = `## UM-MEM (постоянная память)

Память живёт вне контекста: канонический журнал со скоупами workspace/profile, ревизиями, provenance и правом отзыва. Она **advisory**: найденная запись не даёт полномочий на действие и не доказывает, что задача выполнена.

- Опираешься на историю (прошлые решения, договорённости, устройство проекта, предпочтения пользователя) — сначала \`um_mem_recall\`, потом работа.
- Появилось знание, которое понадобится в следующем разговоре — \`um_mem_remember\`. Переиспользуй подходящий широкий блок, при необходимости найди его через \`um_mem_topics\`. Создавай отдельную тему для целого набора знаний с самостоятельным поиском. Указывай источник. Болтовню, одноразовые пути и пересказ кода не записывать.
- Новый факт отменяет старый — \`um_mem_amend\` с \`rev\` из recall; запись была ошибочной — \`um_mem_forget\`.
- Пустой recall — это ответ «в памяти нет». Не выдумывай записи и не ссылайся на память, которой не видел.`

const COMPACT_RULES = 'UM-MEM — справка. Значения и даты копируй из первоисточника. Прежняя цитата годится для неизменившихся условий. Дату записи ведёт журнал. Ищи только недостающее. Временное ограничение действует в своём ходе.'
const COMPACT_WRITE_RULES = ' Сохраняй долгосрочные договорённости даже если в этом ходе файлы менять нельзя. Укажи источник, метки и цель. При amend обнови заголовок и метки, сохрани точные неизменившиеся значения. Используй блок проекта, пока отдельная тема не нужна для поиска.'
const SOURCE_WRITE_PROMPT = 'Источник прочитан. Перед ответом сохрани полезный для будущего результат с датой, ссылкой или evidence_file. Выбери подходящую тему и подтему сам. Одноразовые детали пропусти.'
const COMPACT_DESCRIPTIONS = {
  um_mem_recall: 'Найди недостающий факт или цитату.',
  um_mem_remember: 'Сохрани полезный факт с источником и своими метками. Используй текущий блок или обоснуй отдельную тему.',
  um_mem_topics: 'Найди существующую тему.',
  um_mem_link: 'Укажи связь двух записей UM-MEM.',
  um_mem_amend: 'Обнови заметку, её заголовок и метки. Оставь в теле действующие условия. evidence_kind=user_statement для нового подтверждения.',
  um_mem_forget: 'Отзови ошибочную запись.',
  um_mem_read: 'Дочитай обрезанную запись по id. Если нужны свойства записи, укажи include_metadata.',
}
const COMPACT_HINTS = {
  kind: 'Вид записи. Тему не меняет.', topic: 'Путь новой самостоятельной темы.', topic_reason: 'Что здесь будут искать отдельно.',
  block: 'ID показанного блока. Для текущего проекта пропусти.',
  tags: 'Свои поисковые метки через запятую.', facets: 'Цель, граница применимости и повод пересмотра.',
  facet: 'Фильтр key=value.', metadata: 'JSON с дополнительными метками.', edge_type: 'Тип подтверждённой связи.',
  evidence_kind: 'user_statement, tool_output, file_read или agent_inference.', disposition: 'history для отозванных условий.',
}
const AUTHORED_FACETS = { type: 'object', properties: {
  purpose: { type: 'string', description: 'Зачем агенту будет нужна именно эта запись.' },
  applies_when: { type: 'string', description: 'Конкретная ситуация или граница применимости, если известна.' },
  review_when: { type: 'string', description: 'Что должно произойти, чтобы решение пересмотреть, если известно.' },
} }

function compactTool(tool) {
  const properties = Object.fromEntries(Object.entries(tool.parameters.properties).map(([name, field]) => {
    const { description, ...schema } = field
    return [name, COMPACT_HINTS[name] ? { ...schema, description: COMPACT_HINTS[name] } : schema]
  }))
  return { ...tool, description: COMPACT_DESCRIPTIONS[tool.name], parameters: { ...tool.parameters, properties } }
}

const SECTION_ORDER = 240
const FOCUS_TTL_MS = 10 * 60 * 1000
const REFRESH_GAP_MS = 1500

/** Flatten a DSH message content (string, parts array or nested object) into text. */
function extractText(content) {
  if (typeof content === 'string') return content.trim()
  if (Array.isArray(content)) {
    return content
      .map((part) => {
        if (typeof part === 'string') return part
        if (part && typeof part === 'object') return String(part.text ?? part.content ?? '')
        return ''
      })
      .join('\n')
      .trim()
  }
  if (content && typeof content === 'object') {
    return extractText(content.text ?? content.content ?? content.message?.content)
  }
  return ''
}

/** The host appends its own context blocks to a user message; the question is
 *  the last plain paragraph, not the injected snapshot above it. */
function userTail(text) {
  const paragraphs = String(text || '').split(/\n{2,}/).map((part) => part.trim()).filter(Boolean)
  const isMarker = (part) =>
    /^(current |approval prompts|the runtime context|you are interacting|tokens prefixed|non-zero exits|use the |track every|web_search|web_fetch|cite the url|start independent|when only waiting|create_goal|only create|use the workflow|time sampled|browser time zone|elapsed since|<|\[|#|\||-{3,}|\*{3,}|-\s|\*\s)/i.test(part) ||
    /runtime-context snapshots|this snapshot supersedes|deepseek harness web gui url|\(block=|tags=|\bblock=/i.test(part)
  const plain = paragraphs.filter((part) => !isMarker(part))
  // No plain paragraph means the message carried only harness context: better to
  // record nothing than to store the envelope as if it were the question.
  const first = plain.length ? plain[0] : ''
  return first.slice(0, 400)
}

function toolOutput() {
  return {
    schema: { type: 'string' },
    render(_args, value) {
      return [{ type: 'text', text: String(value ?? '') }]
    },
  }
}

function operationId() {
  return 'dsh-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 10)
}

function observedEvidence(toolName, value) {
  const body = typeof value === 'string' ? value : JSON.stringify(value)
  if (!body) return null
  let result
  try { result = JSON.parse(body) } catch { return null }
  const ref = result?.evidence
  if (typeof ref !== 'string' || !ref.trim() || ref.length > 240 || result.error === true) return null
  return { ref_type: 'tool_result', ref_id: ref, produced_by: 'harness', producer_id: toolName,
    hash: createHash('sha256').update(body).digest('hex') }
}

function scopeChain(config) {
  const chain = ['workspace:' + config.workspace]
  if (config.userId) chain.push('user_global:' + config.userId)
  return chain
}

async function findExactNote(config, payload) {
  if (!config.exactReuse) return null
  const rows = await runCli(config, ['recent', ...commonArgs(config), '--limit', '24', '--scope', 'workspace',
    '--scope-id', config.workspace, '--logical-block-id', payload.requested_block_id])
  const now = Date.now()
  return rows.find(row => row.content === payload.content && row.kind === payload.kind &&
    ['candidate', 'accepted', 'active'].includes(row.status) && row.metadata?.evidence_kind === payload.evidence_kind &&
    !(Date.parse(row.valid_from) > now) &&
    ![row.valid_to, row.expires_at].some(value => Date.parse(value) <= now)) || null
}

async function findSimilarNote(config, payload, fileRef) {
  if (!config.exactReuse || !config.embedUrl || !fileRef) return null
  const rows = await runWithRequest(config, 'recall', {
    query: payload.title + '\n' + payload.content.slice(0, 1600),
    workspace_id: config.workspace,
    profile_id: config.profile,
    scope_chain: ['workspace:' + config.workspace],
    source_horizon: 'durable',
    temporal_mode: 'current',
    retrieval_mode: 'hybrid',
    logical_block_ids: [payload.requested_block_id],
    include_candidates: true,
    limit: 4,
    max_tokens: 1200,
  })
  return rows.find(row => row.kind === payload.kind &&
    row.metadata?.evidence_kind === payload.evidence_kind &&
    Number(row.metadata?.retrieval_dense_similarity) >= 0.82 &&
    row.evidence_refs?.some(ref => ref.ref_type === 'file_snapshot' && ref.ref_id === fileRef)) || null
}

export function apply(ctx, rawConfig = {}) {
  const config = resolveConfig(rawConfig)
  const disposers = []
  ctx.inject(['skills'], (host) => {
    if (!host.skills) return
    const resource = 'skills/organize-memory-topology/SKILL.md'
    const bundled = new URL('../' + resource, import.meta.url)
    const path = fileURLToPath(existsSync(bundled) ? bundled : new URL('../../../.agents/' + resource, import.meta.url))
    const text = readFileSync(path, 'utf8')
    const [, name, description, body] = /^---\r?\nname: ([^\r\n]+)\r?\ndescription: ([^\r\n]+)\r?\n---\r?\n([\s\S]*)$/.exec(text)
    host.skills.register({
      name, content: body.trim(), source: 'bundled', path,
      description: config.compact ? 'Review or reorganize the memory tree: topic boundaries, splits, merges, or links.' : description,
    })
  })

  // ---------- pre-turn focus block ----------
  let focusText = ''
  let focusAt = 0
  let focusSource = ''
  let refreshing = false
  let lastRefreshStart = 0
  const activeSessions = new WeakMap()
  const sourcePrompts = new WeakMap()
  const turnsBySession = new WeakMap()
  const lookupLimits = { um_mem_recall: 2, um_mem_read: 2, um_mem_topics: 1 }
  // Per-turn trace: what was asked and which tools actually ran. Without it the
  // store only grows when the model decides to write, which is why a session of
  // real work could leave no trace at all.
  let projectTopic
  let memoryGeneration = 0
  function projectBlock() {
    projectTopic ??= runWithRequest(config, 'agent-topics', { path: [config.projectName] })
      .then(topic => topic.block_id)
      .catch(error => { projectTopic = undefined; throw error })
    return projectTopic
  }

  function recentSources(block) {
    return runCli(config, ['recent', ...commonArgs(config), '--limit', '8', '--scope', 'workspace',
      '--scope-id', config.workspace, '--logical-block-id', block], { timeout: Math.min(config.timeoutMs, 2000) })
      .catch(() => [])
  }

  function traceSearch(session, operation, records, text, skipped) {
    const current = session && turnsBySession.get(session)
    const projected = compactRecords(records)
    for (const record of records) if (!record.metadata?.retrieval_truncated && record.block_id) current?.records.set(record.block_id, record)
    if (!current?.trace || current.trace.searches.length >= 32) return text
    current.trace.searches.push({ operation, chars: Array.from(text).length, skipped,
      records: projected.map(({ id, revision }) => ({ id, revision })) })
    traceTurn(config, session, current)
    return text
  }

  function reserveLookup(current, name) {
    if (!config.compact || !current) return true
    const used = current.calls[name] ?? 0
    if (used >= lookupLimits[name]) return false
    current.calls[name] = used + 1
    return true
  }

  function lookupClosed(session, operation) {
    return traceSearch(session, operation, [],
      'UM-MEM: поиск на этот ход завершён. Продолжай работу по найденным сведениям.', 'budget')
  }

  function renderFocusFrom(records, delivered, displayed, maxChars = config.focusMaxChars, sourceID, earlierID) {
    if (!config.inject) return ''
    return renderFocus(records, {
      workspace: config.workspace,
      maxChars,
      limit: config.focusLimit,
      compact: config.compact,
      delivered,
      displayed,
      sourceID,
      earlierID,
    })
  }

  function scheduleFocus(records) {
    const rendered = renderFocusFrom(records)
    if (rendered && rendered !== focusText) {
      focusText = rendered
      focusAt = Date.now()
    }
  }

  async function refreshFocus() {
    if (config.activeMask) return
    const now = Date.now()
    if (refreshing || now - lastRefreshStart < REFRESH_GAP_MS) return
    refreshing = true
    lastRefreshStart = now
    try {
      const workspaceRecords = await runCli(config, [
        'recent',
        ...commonArgs(config),
        '--limit', String(config.compact ? config.focusLimit * 2 : config.focusLimit),
        '--scope', 'workspace',
        '--scope-id', config.workspace,
      ])
      let userRecords = []
      if (config.userId) {
        try {
          userRecords = await runCli(config, [
            'recent',
            ...commonArgs(config),
            '--limit', '6',
            '--scope', 'user_global',
            '--scope-id', config.userId,
          ])
        } catch {
          userRecords = []
        }
      }
      scheduleFocus([
        ...(Array.isArray(userRecords) ? userRecords : []),
        ...(Array.isArray(workspaceRecords) ? workspaceRecords : []),
      ])
      focusSource = 'recent'
    } catch (error) {
      ctx.logger?.debug?.('[dsh-um-mem] focus refresh failed: ' + String(error?.message || error))
    } finally {
      refreshing = false
    }
  }

  const onSessionEvent = (session, event) => {
    try {
      if (session && event?.type === 'turn/start') sourcePrompts.delete(session)
      if (!event || event.type !== 'user/message') {
        logEvent('session/event', { type: event?.type || 'unknown' })
        return
      }
      const text = extractText(event.data?.content ?? event.data?.message?.content) || extractText(event.message?.content)
      if (!text) {
        logEvent('session/event', { type: 'user/message', text: 0 })
        return
      }
      const tailText = userTail(text)
      if (/^you are the (?:root|child) agent\.\s+your name is\b/i.test(tailText)) return
      logEvent('session/event', { type: 'user/message', text: text.length, captured: tailText.length })
      void refreshFocus()
      if (!config.inject || config.activeMask) return
      // Best-effort semantic recall for this turn; the block shows up from the
      // next assembly onward and is deduplicated by the host while it stays stable.
      void (async () => {
        try {
          const records = await runWithRequest(config, 'recall', {
            query: tailText || text.slice(0, 600),
            workspace_id: config.workspace,
            profile_id: config.profile,
            scope_chain: scopeChain(config),
            source_horizon: 'durable',
            temporal_mode: 'current',
            retrieval_mode: config.embedUrl ? 'hybrid' : 'semantic',
            search_all: true,
            include_candidates: true,
            limit: config.limit,
            max_tokens: 1800,
          })
          logEvent('recall', { source: 'focus', hits: Array.isArray(records) ? records.length : 0, q: String(tailText || text).slice(0, 90) })
          if (Array.isArray(records) && records.length) {
            scheduleFocus(records)
            focusSource = 'recall'
          }
        } catch (error) {
          ctx.logger?.debug?.('[dsh-um-mem] turn recall failed: ' + String(error?.message || error))
        }
      })()
    } catch (error) {
      ctx.logger?.debug?.('[dsh-um-mem] session event failed: ' + String(error))
    }
  }
  try {
    ctx.on('session/event', onSessionEvent)
  } catch (error) {
    ctx.logger?.warn?.('[dsh-um-mem] cannot observe session events: ' + String(error))
  }

  // Accepted user messages reach pre-step before prompt assembly. Session
  // events are recorded after assembly, too late for the first request.
  try {
    ctx.on('agent/pre-step', async (step, next) => {
      const decision = await next()
      if (decision?.kind === 'reject') return decision
      try {
        if (config.inject && !config.activeMask && Date.now() - focusAt >= 60000) void refreshFocus()
        const session = step?.agent?.session
        if (session) {
          const accepted = decision?.messages ?? step.messages ?? []
          const userMessages = accepted.filter(message => message.source?.kind === 'user')
          const newest = userMessages.at(-1)
          const previousTurn = turnsBySession.get(session)
          if (!previousTurn || previousTurn.turn !== step.turn) {
            turnsBySession.set(session, { turn: step.turn, sources: [], tools: new Set(), calls: {}, seen: new Set(),
              full: new Map(), queries: new Set(), records: new Map(), toolEvidence: new Map() })
          }
          const current = turnsBySession.get(session)
          const latestSource = extractText(newest?.content)
          if (latestSource) current.latestSource = latestSource
          if (config.preserveEvidence) {
            for (const message of userMessages) {
              const source = extractText(message.content)
              if (source && !current.sources.includes(source)) current.sources.push(source)
            }
          }
          if (config.activeMask && config.inject) {
            const query = userTail(extractText(newest?.content))
            const previous = activeSessions.get(session)
            if (previous?.turn !== step.turn) activeSessions.delete(session)
            if (session.id && query && (!previous || previous.turn !== step.turn || previous.query !== query)) {
              const state = { turn: step.turn, query, text: '' }
              activeSessions.set(session, state)
              const result = await runWithRequest(config, 'dsh-focus', {
                session_id: session.id,
                ...(config.userId ? { user_id: config.userId } : {}),
                query,
                limit: Math.min(16, config.focusLimit),
                max_tokens: config.focusMaxChars ? Math.min(2048, Math.ceil(config.focusMaxChars / 2)) : 2048,
                hybrid: Boolean(config.embedUrl),
              }, [], { timeout: Math.min(config.timeoutMs, 5000) }).catch(() => ({ status: 'unavailable' }))
              const displayed = []
              if (result.status === 'resolved') {
                const selected = config.compact && !config.focusMaxChars
                  ? { records: adaptiveFocus(result.records, query), maxChars: Infinity }
                  : config.compact
                    ? preferBackedRevision(result.records, config.focusMaxChars, config.focusProofMaxChars)
                    : { records: result.records, maxChars: config.focusMaxChars }
                let source, earlier
                const block = selected.records[0]?.block_membership?.primary_block_id
                if (config.compact && !config.focusMaxChars && config.preserveEvidence && block) {
                  const primary = selected.records[0]
                  const owner = primary.scope === 'workspace' && primary.workspace_id === config.workspace
                    ? await projectBlock().catch(() => block) : block
                  if (primary.kind === 'source_excerpt') source = primary
                  else {
                    const history = await recentSources(owner)
                    source = sourceForFocus(primary, history, owner)
                    earlier = earlierProof(primary, source, history)
                  }
                }
                const records = source && source.block_id !== selected.records[0].block_id
                  ? [source, ...(earlier ? [earlier] : []), selected.records[0]] : selected.records
                state.text = renderFocusFrom(records, current.seen, displayed, selected.maxChars, source?.block_id, earlier?.block_id)
                for (const record of records) if (!record.metadata?.retrieval_truncated && record.block_id) current.records.set(record.block_id, record)
              }
              for (const record of displayed) if (!record.clipped) current.full.set(record.id, record.revision)
              current.trace = { at: current.trace?.at ?? new Date().toISOString(), status: result.status,
                revision: result.revision, automatic: displayed, chars: Array.from(state.text).length,
                searches: current.trace?.searches ?? [] }
              traceTurn(config, session, current)
              logEvent('active-mask', { status: result.status, records: result.records?.length ?? 0, revision: result.revision ?? null })
            }
          }
        }
      } catch (error) {
        ctx.logger?.debug?.('[dsh-um-mem] active focus unavailable: ' + String(error?.message || error))
      }
      return decision
    })
  } catch (error) {
    ctx.logger?.debug?.('[dsh-um-mem] no pre-step hook: ' + String(error))
  }

  try {
    ctx.on('tools/result', (exec, result) => {
      const toolName = typeof exec?.name === 'string' ? exec.name : ''
      const session = exec?.agent?.session
      const current = session && turnsBySession.get(session)
      if (current && result?.isError === false && config.evidenceTools.has(toolName)) {
        const evidence = observedEvidence(toolName, result.value)
        if (evidence) {
          const recent = current.toolEvidence
          recent.set(evidence.ref_id, evidence)
          if (recent.size > 24) recent.delete(recent.keys().next().value)
        }
      }
      if (config.compact && config.exactReuse && session && result?.isError === false) {
        if (toolName === 'um_mem_remember') sourcePrompts.set(session, 'handled')
        if (!sourcePrompts.has(session) && (toolName === 'web_fetch' ||
          (toolName === 'read' && /\.(?:md|txt|rst)$/i.test(result.value?.path ?? '')))) {
          sourcePrompts.set(session, 'pending')
        }
      }
      if (toolName && !toolName.startsWith('um_mem_')) current?.tools.add(toolName)
    })
  } catch {
    /* host without tool results */
  }

  try {
    ctx.on('agent/turn-stopping', async (...args) => {
      try {
        const session = args[0]?.agent?.session
        const current = session && turnsBySession.get(session)
        const sources = current?.sources.splice(0) ?? []
        if (current && !sources.length && !config.preserveEvidence && current.tools.size && current.latestSource) {
          sources.push(current.latestSource.slice(0, 220))
        }
        if (config.captureSources && session?.id && sources.length) {
          const block = await projectBlock()
          for (const source of sources) {
            for (const payload of sourceExcerpts(session.id, current.turn, source)) {
              payload.requested_block_id = block
              payload.metadata.harness_turn = turnID(session.id, current.turn)
              if (payload.metadata.excerpt === 1) payload.metadata.tools = [...current.tools].slice(0, 12)
              const saved = await runWithRequest(config, 'remember-candidate', payload, ['--auto-link=false'])
              logEvent('source-excerpt', { saved: saved.saved === true, replayed: saved.replayed === true })
            }
          }
        }
      } catch (error) {
        ctx.logger?.debug?.('[dsh-um-mem] source capture unavailable: ' + String(error?.message || error))
      }
      const next = args[args.length - 1]
      return typeof next === 'function' ? next() : undefined
    })
  } catch (error) {
    ctx.logger?.debug?.('[dsh-um-mem] no turn-stopping hook: ' + String(error))
  }

  disposers.push(ctx.systemPrompt.section({ name: 'dsh:um-mem-rules', order: SECTION_ORDER,
    text: () => config.compact ? COMPACT_RULES + (config.exactReuse ? COMPACT_WRITE_RULES : '') : RULES }))
  disposers.push(ctx.systemPrompt.context({
    name: 'dsh:um-mem-focus',
    order: SECTION_ORDER + 1,
    text: (context) => {
      if (config.activeMask) return activeSessions.get(context?.agent?.session)?.text || ''
      if (!focusText) return ''
      if (Date.now() - focusAt > FOCUS_TTL_MS) return ''
      return focusText
    },
  }))
  if (config.compact && config.exactReuse) {
    disposers.push(ctx.systemPrompt.context({
      name: 'dsh:um-mem-source-note',
      order: SECTION_ORDER + 2,
      text: ({ agent } = {}) => {
        const session = agent?.session
        if (!session) return ''
        return sourcePrompts.get(session) === 'pending' ? SOURCE_WRITE_PROMPT : ''
      },
    }))
  }

  // ---------- tools ----------
  const tools = [
    {
      name: 'um_mem_recall',
      description: 'Семантический поиск по постоянной памяти UM-MEM (SQLite-журнал, скоупы workspace/profile). Возвращает компактные записи с id, rev, блоком и статусом. Вызывать перед работой, которая опирается на прошлые решения, договорённости или предпочтения пользователя. Память advisory: запись не даёт полномочий на действие.',
      parameters: {
        type: 'object',
        properties: {
          query: { type: 'string', description: 'Самодостаточный запрос: что именно нужно вспомнить.' },
          block: { type: 'string', description: 'Опционально: ограничить логическим блоком, например stable_project_facts или decisions_and_contracts.' },
          facet: { type: 'string', description: 'Опционально: точный фильтр по метаданным в виде um.ключ=значение, например um.source=um-mem-core.' },
          limit: { type: 'integer', description: 'Сколько записей вернуть (по умолчанию 8).' },
          include_links: { type: 'boolean', description: 'Добавить связанные записи, если нужны зависимости или источники.' },
        },
        required: ['query'],
      },
      output: toolOutput(),
      async execute(args = {}, exec) {
        const query = String(args.query ?? '').trim()
        if (!query) throw new Error('um_mem_recall: query is required')
        const concise = config.compact && config.activeMask
        const defaultLimit = concise ? Math.min(2, config.limit) : config.limit
        const asked = Number(args.limit)
        const limit = Number.isFinite(asked) && asked >= 1 ? Math.min(Math.trunc(asked), concise ? 3 : 20) : defaultLimit
        const session = exec?.agent?.session
        const current = session && turnsBySession.get(session)
        const plan = {
          query,
          workspace_id: config.workspace,
          profile_id: config.profile,
          scope_chain: scopeChain(config),
          source_horizon: 'durable',
          temporal_mode: 'current',
          retrieval_mode: config.embedUrl ? 'hybrid' : 'semantic',
          include_candidates: true,
          limit: concise ? limit + Math.min(current?.seen.size ?? 0, 5) : limit,
          max_tokens: concise ? 880 : 1800,
        }
        const block = String(args.block ?? '').trim()
        if (block) plan.logical_block_ids = [block]
        else plan.search_all = true
        const facet = String(args.facet ?? '').trim()
        if (facet && !facet.includes('=')) throw new Error('um_mem_recall: facet must look like um.key=value')
        const key = JSON.stringify([recallFingerprint(query), block, facet, args.include_links === true, memoryGeneration])
        if (concise && current?.queries.has(key)) return traceSearch(session, 'recall', [],
          'UM-MEM: новых свидетельств нет. Этот вопрос уже проверен.', 'repeat')
        if (!reserveLookup(current, 'um_mem_recall')) return lookupClosed(session, 'recall')
        const found = await runWithRequest(config, 'recall', plan, facet ? ['--facet', facet] : [])
        const records = current && concise ? found.filter(record => !current.seen.has(record.revision_id || record.block_id)) : found
        if (current && concise) {
          current.queries.add(key)
          if (!records.length) return traceSearch(session, 'recall', [], 'UM-MEM: новых свидетельств нет. Используй уже найденное или укажи, какой детали не хватает.')
        }
        logEvent('recall', { source: 'tool', hits: Array.isArray(records) ? records.length : 0, q: String(plan.query || '').slice(0, 90) })
        const compact = compactRecords(records)
        const shown = []
        let text = renderRecall(records, { limit, compact: config.compact, displayed: shown })
        if (concise) {
          for (const record of shown) {
            current?.seen.add(record.revision || record.id)
            if (record.full) current?.full.set(record.id, record.revision)
          }
          if (args.include_links !== true) {
            return traceSearch(session, 'recall', records.slice(0, shown.length), text)
          }
        }
        // Links were stored but nothing read them, so a connected record stayed
        // invisible after a recall. Pull one hop for the top hits and mark it.
        const known = new Set(compact.map((record) => record.id))
        const linked = []
        for (const record of compact.slice(0, concise ? 1 : 2)) {
          if (!record.id) continue
          try {
            const payload = await runCli(config, ['neighbors', ...commonArgs(config), '--record-id', record.id, '--depth', concise ? '1' : '2'])
            for (const neighbor of payload.neighbors || []) {
              if (known.has(neighbor.record_id)) continue
              known.add(neighbor.record_id)
              linked.push({ ...neighbor, via: record.title })
            }
          } catch {
            /* a broken graph must not fail the recall itself */
          }
        }
        // Two hops are allowed, but only the strongest four survive: a budget of
        // links is what keeps the block small enough to stay useful.
        linked.sort((left, right) => {
          if (left.hop !== right.hop) return left.hop - right.hop
          return Number(right.weight || 1) - Number(left.weight || 1)
        })
        linked.splice(concise ? 2 : 4)
        if (linked.length) {
          text += '\n\nСвязано ребром:\n' + linked
            .map((neighbor) => '- [' + neighbor.edge_type + ' w=' + Number(neighbor.weight || 1).toFixed(2) +
              ' h' + (neighbor.hop || 1) + '] ' + neighbor.title +
              ' (id=' + neighbor.record_id + ', ' + neighbor.logical_block_id + ')' +
              (neighbor.via ? ' — через «' + neighbor.via + '»' : ''))
            .join('\n')
        }
        return traceSearch(session, 'recall', records.slice(0, concise ? shown.length : limit), text)
      },
    },
    {
      name: 'um_mem_topics',
      description: COMPACT_DESCRIPTIONS.um_mem_topics,
      parameters: { type: 'object', properties: { query: { type: 'string' } } },
      output: toolOutput(),
      async execute({ query = '' } = {}, exec) {
        const session = exec?.agent?.session
        if (!reserveLookup(session && turnsBySession.get(session), 'um_mem_topics')) return lookupClosed(session, 'topics')
        const topics = await runWithRequest(config, 'agent-topics', {
          query: String(query).slice(0, 160), limit: 8,
        })
        if (!topics.length) return 'В этом пространстве пока нет тем. Задай путь topic при сохранении.'
        return topics.map(topic => topic.path.join(' › ') +
          (topic.index ? ' (раздел)' : ' | block=' + topic.block_id)).join('\n')
      },
    },
    {
      name: 'um_mem_remember',
      description: 'Записать в постоянную память UM-MEM факт, решение, правило или предпочтение, которое понадобится в следующих сессиях. Запись создаётся как кандидат (advisory), её статус возвращается в ответе. Не записывать болтовню, одноразовые пути и то, что уже есть в памяти.',
      parameters: {
        type: 'object',
        properties: {
          content: { type: 'string', description: 'Одна самодостаточная мысль, без «см. выше».' },
          title: { type: 'string', description: 'Короткий заголовок записи.' },
          kind: { type: 'string', description: 'Необязательная метка формата записи. Используй подходящее слово. На ветку не влияет.' },
          topic: { type: 'array', items: { type: 'string' }, minItems: 1, maxItems: 5, description: 'Своё название широкого блока или путь для нескольких самостоятельных подтем. Последний элемент хранит много связанных записей.' },
          topic_reason: { type: 'string', description: 'Чем эта область самостоятельна и что в ней будут искать отдельно. Без причины новая запись сохраняется в блоке проекта.' },
          disposition: { type: 'string', enum: ['current', 'history'], description: 'Используй history для отвергнутого черновика или исторического эпизода.' },
          save_distinct: { type: 'boolean', description: 'Сохрани отдельно после предупреждения о сходстве, если это самостоятельный факт или наблюдение.' },
          tags: { type: 'string', description: 'Свои термины, по которым эту запись будут искать. Перечисли через запятую.' },
          facets: AUTHORED_FACETS,
          block: { type: 'string', description: 'ID существующего блока. Без block и topic запись относится к текущему проекту.' },
          evidence_kind: { type: 'string', description: 'Откуда факт: user_statement, tool_output, file_read, agent_inference. По умолчанию agent_inference.' },
          evidence_file: { type: 'string', description: 'Путь к файлу или id теста, который служит вторым свидетельством записи.' },
          metadata: { type: 'string', description: 'Опциональные метки записи как JSON-объект, например {"source":"repo","confidence":"high"}. Служебные ключи (status, authority, scope, retention…) зарезервированы и будут отклонены.' },
        },
        required: ['content'],
      },
      output: toolOutput(),
      async execute(args = {}, exec) {
        const content = String(args.content ?? '').trim()
        if (!content) throw new Error('um_mem_remember: content is required')
        const tags = authoredTags(args.tags)
        const historical = args.disposition === 'history'
        const kind = historical ? 'episode' : String(args.kind ?? '').trim().toLowerCase().slice(0, 64) || 'note'
        const explicitBlock = String(args.block ?? '').trim()
        if (explicitBlock && args.topic != null) return 'remember: укажи либо block, либо topic.'
        const payload = {
          operation_id: operationId(),
          content,
          title: String(args.title ?? '').trim() || content.slice(0, 80),
          kind,
          tags,
          requested_block_id: explicitBlock,
          evidence_kind: String(args.evidence_kind ?? '').trim() || 'agent_inference',
        }
        if (config.userId) payload.user_id = config.userId
        // Provenance: the write itself is the first witness; a named file or
        // test becomes the second. Without refs a record can only ever satisfy
        // "the agent said so".
        const writeRef = { ref_type: 'tool_result', ref_id: operationId(), produced_by: 'agent', producer_id: 'dsh-um-mem' }
        const fileRef = String(args.evidence_file ?? '').trim()
        const session = exec?.agent?.session
        const current = session && turnsBySession.get(session)
        const observed = payload.evidence_kind === 'tool_output' ? current?.toolEvidence.get(fileRef) : undefined
        if (!observed && fileRef && ['tool_output', 'file_read'].includes(payload.evidence_kind)) payload.evidence_kind = 'agent_tool_claim'
        const attachment = observed ?? { ref_type: 'file_snapshot', ref_id: fileRef, produced_by: 'agent', producer_id: 'dsh-um-mem' }
        payload.evidence_refs = fileRef
          ? [writeRef, attachment]
          : [writeRef]
        const rawMeta = String(args.metadata ?? '').trim()
        const labels = { source: 'dsh' }
        if (rawMeta) {
          try {
            const parsed = JSON.parse(rawMeta)
            if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) Object.assign(labels, parsed)
            else throw new Error('not an object')
          } catch {
            throw new Error('um_mem_remember: metadata must be a JSON object')
          }
        }
        if (session?.id && current) labels.harness_turn = turnID(session.id, current.turn)
        const facets = authoredFacets(args.facets)
        if (Object.keys(facets).length) labels.memory_facets = facets
        if (payload.evidence_kind === 'user_statement' && session?.id && current?.latestSource) {
          payload.evidence_refs.push(...sourceExcerpts(session.id, current.turn, current.latestSource)[0].evidence_refs)
        }
        payload.metadata = labels
        let chosenTopic
        const topicReason = String(args.topic_reason ?? '').trim()
        const suggestedTopic = Array.isArray(args.topic)
        if (suggestedTopic && (!config.compact || topicReason)) {
          chosenTopic = await runWithRequest(config, 'agent-topics', { path: args.topic, about: topicReason.slice(0, 240) })
          payload.requested_block_id = chosenTopic.block_id
        } else if (!explicitBlock) {
          payload.requested_block_id = await projectBlock()
        }
        try {
          const existing = await findExactNote(config, payload)
          if (existing) {
            const newEvidence = fileRef && !existing.evidence_refs?.some(ref =>
              ref.ref_type === attachment.ref_type && ref.ref_id === fileRef && (ref.hash ?? null) === (attachment.hash ?? null))
            return 'remember: already saved | record=' + existing.block_id + ' | rev=' + existing.revision_id +
              (newEvidence ? '\nAmend this record with evidence_file to attach the new source to its history.'
                : '\nUse this record. Amend it if its title, labels, or applicability needs an update.')
          }
          if (!historical && !args.save_distinct) {
            const similar = await findSimilarNote(config, payload, fileRef)
            if (similar) return 'remember: similar note, no write | record=' + similar.block_id +
              ' | rev=' + similar.revision_id + ' | ' + similar.title +
              '\nUse the existing note, amend it, or retry with save_distinct=true for a separate fact.'
          }
        } catch (error) {
          ctx.logger?.debug?.('[dsh-um-mem] duplicate lookup failed: ' + String(error?.message || error))
        }
        const extra = config.exactReuse ? ['--link-threshold', '0.28'] : []
        const result = await runWithRequest(config, 'remember-candidate', payload, extra)
        if (result.saved === true) memoryGeneration++
        void refreshFocus()
        return renderWriteResult(result, { verb: 'remember' }) +
          (result.saved && chosenTopic ? ' | topic=' + chosenTopic.path.join(' › ') : '') +
          (result.saved && suggestedTopic && !chosenTopic ? ' | сохранено в блоке проекта' : '')
      },
    },
    {
      name: 'um_mem_link',
      description: 'Связать две записи типизированным ребром. Граф памяти держится на таких рёбрах: derived_from (вывод из записи), supports (подтверждает), implements (реализует), cites (ссылается), related_to (по теме), verified_by (проверено), tests (тестирует), contains (входит в). Нужны id обеих записей; свидетельство подставляется автоматически.',
      parameters: {
        type: 'object',
        properties: {
          src_id: { type: 'string', description: 'id записи-источника (mem_…).' },
          dst_id: { type: 'string', description: 'id записи-цели (mem_…).' },
          edge_type: { type: 'string', description: 'Тип ребра: derived_from, supports, implements, cites, related_to, verified_by, tests, contains.' },
          reason: { type: 'string', description: 'Почему эти записи связаны.' },
          evidence_id: { type: 'string', description: 'Что служит свидетельством: id вызова инструмента, теста или файла.' },
        },
        required: ['src_id', 'dst_id', 'edge_type'],
      },
      output: toolOutput(),
      async execute(args = {}) {
        const src = String(args.src_id ?? '').trim()
        const dst = String(args.dst_id ?? '').trim()
        const edgeType = String(args.edge_type ?? '').trim()
        if (!src || !dst || !edgeType) throw new Error('um_mem_link: src_id, dst_id and edge_type are required')
        if (src === dst) throw new Error('um_mem_link: src and dst must be different records')
        const payload = {
          src_id: src,
          dst_id: dst,
          edge_type: edgeType,
          reason: String(args.reason ?? '').trim() || 'linked through dsh-um-mem',
          weight: 1,
          evidence_refs: [{
            ref_type: 'tool_result',
            ref_id: String(args.evidence_id ?? '').trim() || operationId(),
            produced_by: 'agent',
            producer_id: 'dsh-um-mem',
          }],
        }
        const result = await runWithRequest(config, 'add-edge', payload)
        return 'link: ' + (result.edge_type || edgeType) + ' | ' + src + ' → ' + dst + ' | ok=' + String(result.ok)
      },
    },
    {
      name: 'um_mem_amend',
      description: 'Заменить содержимое существующей записи новой ревизией (старый факт устарел). Нужны record_id и rev, которые вернул um_mem_recall или um_mem_read.',
      parameters: {
        type: 'object',
        properties: {
          record_id: { type: 'string', description: 'id записи (mem_…).' },
          expected_revision: { type: 'string', description: 'rev записи (rev_…) для защиты от гонки.' },
          content: { type: 'string', description: 'Новое содержимое целиком.' },
          title: { type: 'string', description: 'Актуальный заголовок, если старый больше не подходит.' },
          reason: { type: 'string', description: 'Почему запись меняется.' },
          tags: { type: 'string', description: 'Уточнённые тематические метки через запятую. Можно опустить.' },
          facets: { ...AUTHORED_FACETS, description: 'Новый полный набор авторских меток. Пропусти, чтобы сохранить прежний.' },
          evidence_kind: { type: 'string', enum: ['user_statement'], description: 'Условия явно сообщены или подтверждены пользователем в текущей реплике.' },
          evidence_file: { type: 'string', description: 'Добавь новое свидетельство к существующей записи. Укажи путь к источнику или ID теста.' },
        },
        required: ['record_id', 'expected_revision', 'content'],
      },
      output: toolOutput(),
      async execute(args = {}, exec) {
        const session = exec?.agent?.session
        const turn = session && turnsBySession.get(session)
        const recordID = String(args.record_id ?? '').trim()
        if (!recordID) throw new Error('um_mem_amend: record_id is required')
        const previous = turn?.records.get(recordID) ??
          await runCli(config, ['read-record', ...commonArgs(config), '--record-id', recordID])
        if (previous?.kind === 'source_excerpt') {
          return 'Это исходная цитата. Сохрани вывод через um_mem_remember или обнови существующее решение.'
        }
        const content = String(args.content ?? '')
        const labels = revisionLabels(previous, content, args)
        const metadata = labels.facets ? { memory_facets: labels.facets } : {}
        const refs = []
        const source = turn?.latestSource
        const confirmed = args.evidence_kind === 'user_statement'
        if (confirmed && (!session?.id || !source)) return 'amend: для user_statement нужна исходная реплика текущего хода.'
        if (session?.id && source && (confirmed || revisionUsesSource(previous?.content, content, source))) {
          refs.push(...sourceExcerpts(session.id, turn.turn, source)[0].evidence_refs)
          metadata.harness_turn = turnID(session.id, turn.turn)
          if (confirmed) metadata.evidence_kind = 'user_statement'
        }
        const fileRef = String(args.evidence_file ?? '').trim()
        if (fileRef) {
          const observed = turn?.toolEvidence.get(fileRef)
          refs.push(observed ?? { ref_type: 'file_snapshot', ref_id: fileRef, produced_by: 'agent', producer_id: 'dsh-um-mem' })
          if (observed && metadata.evidence_kind == null) metadata.evidence_kind = 'tool_output'
        }
        const result = await runWithRequest(config, 'amend', {
          memory_id: recordID,
          expected_revision: String(args.expected_revision ?? '').trim(),
          reason: String(args.reason ?? '').trim() || 'amended through dsh-um-mem',
          content,
          ...(labels.title ? { title: labels.title } : {}),
          ...(labels.tags ? { tags: labels.tags } : {}),
          ...(Object.keys(metadata).length ? { metadata_patch: metadata } : {}),
          ...(refs.length ? { evidence_refs: refs } : {}),
          operation_id: operationId(),
        })
        if (result.ok === true || result.saved === true) memoryGeneration++
        void refreshFocus()
        return renderWriteResult(result, { verb: 'amend' })
      },
    },
    {
      name: 'um_mem_forget',
      description: 'Отозвать (retract) запись постоянной памяти, если она была ошибочной. Требует record_id; rev желателен для защиты от гонки.',
      parameters: {
        type: 'object',
        properties: {
          record_id: { type: 'string', description: 'id записи (mem_…).' },
          expected_revision: { type: 'string', description: 'rev записи (rev_…), опционально.' },
          reason: { type: 'string', description: 'Почему запись отзывается.' },
        },
        required: ['record_id'],
      },
      output: toolOutput(),
      async execute(args = {}) {
        const result = await runWithRequest(config, 'forget', {
          memory_id: String(args.record_id ?? '').trim(),
          reason: String(args.reason ?? '').trim() || 'retracted through dsh-um-mem',
          expected_revision: String(args.expected_revision ?? '').trim(),
        })
        if (result.ok === true || result.saved === true) memoryGeneration++
        void refreshFocus()
        return renderWriteResult(result, { verb: 'forget' })
      },
    },
    {
      name: 'um_mem_read',
      description: 'Прочитать одну запись памяти целиком по record_id (включая метаданные, статус и ревизию).',
      parameters: {
        type: 'object',
        properties: {
          record_id: { type: 'string', description: 'id записи (mem_…).' },
          include_metadata: { type: 'boolean', description: 'Сведения о записи, даже если её текст уже показан.' },
        },
        required: ['record_id'],
      },
      output: toolOutput(),
      async execute(args = {}, exec) {
        const recordId = String(args.record_id ?? '').trim()
        if (!recordId) throw new Error('um_mem_read: record_id is required')
        const session = exec?.agent?.session
        const current = session && turnsBySession.get(session)
        const alreadyShown = config.compact && current?.full.has(recordId)
        if (alreadyShown && args.include_metadata !== true) {
          return traceSearch(session, 'read', [], 'Запись уже показана целиком: id=' + recordId + ' rev=' + current.full.get(recordId), 'repeat')
        }
        if (!reserveLookup(current, 'um_mem_read')) return lookupClosed(session, 'read')
        const record = await runCli(config, ['read-record', ...commonArgs(config), '--record-id', recordId])
        const [compact] = compactRecords(record)
        if (!compact) return 'UM-MEM: record not found: ' + recordId
        if (config.compact) current?.full.set(recordId, compact.revision)
        if (config.compact && config.preserveEvidence && compact.kind === 'source_excerpt' &&
          record.scope === 'workspace' && record.workspace_id === config.workspace) {
          const newer = sourceForFocus(record, await recentSources(compact.block), compact.block, { newerOnly: true })
          if (newer) {
            const [source] = compactRecords(newer)
            const seen = current?.full.get(source.id) === source.revision
            current?.full.set(source.id, source.revision)
            if (!seen) current?.seen.add(source.revision)
            return traceSearch(session, 'read', [newer, record], [
              'Более поздняя реплика по теме. Сначала проверь её изменения (id=' + source.id + ').',
              seen ? 'Цитата уже показана.' : source.content,
              'Запрошенная более ранняя реплика (id=' + compact.id + ').',
              alreadyShown ? 'Цитата уже показана.' : compact.content,
            ].join('\n'))
          }
        }
        const text = [
          compact.title || '(no title)',
          'id=' + compact.id + ' rev=' + compact.revision,
          'kind=' + compact.kind + ' status=' + compact.status + ' authority=' + compact.authority,
          'block=' + compact.block + (compact.tags.length ? ' tags=' + compact.tags.join(',') : ''),
          ...(config.compact ? (args.include_metadata === true ? ['saved_at=' + compact.at] : []) : ['at=' + compact.at]),
          '',
          alreadyShown ? '' : compact.content,
        ].join('\n')
        return traceSearch(session, 'read', [record], text)
      },
    },
    {
      name: 'um_mem_recent',
      description: 'Последние записи рабочего пространства без семантического поиска — быстрый взгляд «что я вообще помню».',
      parameters: {
        type: 'object',
        properties: { limit: { type: 'integer', description: 'Сколько записей вернуть (по умолчанию 10).' } },
      },
      output: toolOutput(),
      async execute(args = {}) {
        const limit = Number.isFinite(Number(args.limit)) && Number(args.limit) > 0 ? Math.trunc(Number(args.limit)) : 10
        const records = await runCli(config, [
          'recent',
          ...commonArgs(config),
          '--limit', String(limit),
          '--scope', 'workspace',
          '--scope-id', config.workspace,
        ])
        return renderRecall(records, { limit })
      },
    },
    {
      name: 'um_mem_contradictions',
      description: 'Пары записей на одну тему, которые могут противоречить друг другу: похожий текст, разные canonical_key, нет связи supersedes. Ничего не меняет — только предлагает ревью, чтобы память не обрастала противоречиями молча.',
      parameters: {
        type: 'object',
        properties: {
          threshold: { type: 'number', description: 'Порог похожести (по умолчанию 0.55).' },
          limit: { type: 'integer', description: 'Сколько пар вернуть (по умолчанию 10).' },
        },
      },
      output: toolOutput(),
      async execute(args = {}) {
        const threshold = Number(args.threshold) > 0 ? Number(args.threshold) : 0.55
        const limit = Number.isFinite(Number(args.limit)) && Number(args.limit) > 0 ? Math.trunc(Number(args.limit)) : 10
        const payload = await runCli(config, ['contradictions', ...commonArgs(config),
          '--conflict-threshold', String(threshold), '--neighbor-limit', String(limit)])
        const pairs = payload.pairs || []
        if (!pairs.length) return 'UM-MEM: противоречий не найдено (порог ' + threshold + ').'
        return 'пар: ' + payload.count + '\n' + pairs
          .map((pair) => '- ' + Number(pair.similarity).toFixed(2) + ' ' + pair.a_title + ' <-> ' + pair.b_title)
          .join('\n')
      },
    },
    {
      name: 'um_mem_promotion_candidates',
      description: 'Записи, готовые к рассмотрению на подтверждение: несут больше одного свидетельства, подтверждены ребром supports/verified_by или имеют human_approval. Само подтверждение остаётся ревью, но очередь должна быть видна — иначе память навсегда остаётся черновиком.',
      parameters: {
        type: 'object',
        properties: { limit: { type: 'integer', description: 'Сколько кандидатов вернуть (по умолчанию 15).' } },
      },
      output: toolOutput(),
      async execute(args = {}) {
        const limit = Number.isFinite(Number(args.limit)) && Number(args.limit) > 0 ? Math.trunc(Number(args.limit)) : 15
        const payload = await runCli(config, ['promotion-candidates', ...commonArgs(config), '--neighbor-limit', String(limit)])
        const list = payload.candidates || []
        if (!list.length) return 'UM-MEM: очередь пуста — ни одна запись не набрала второго свидетельства.'
        return 'кандидатов: ' + payload.count + '\n' + list
          .map((item) => '- ' + item.title + ' (' + item.logical_block_id + ', ' + item.kind + ') — ' + (item.reasons || []).join(', '))
          .join('\n')
      },
    },
    {
      name: 'um_mem_approve',
      description: 'Подтвердить запись как человек: добавляет свидетельство human_approval через amend. Это сигнал для ревью промоушена, а не обход tribunal — директива по-прежнему требует отдельного решения.',
      parameters: {
        type: 'object',
        properties: {
          record_id: { type: 'string', description: 'id записи (mem_…).' },
          note: { type: 'string', description: 'Почему запись подтверждена.' },
        },
        required: ['record_id'],
      },
      output: toolOutput(),
      async execute(args = {}) {
        const recordId = String(args.record_id ?? '').trim()
        if (!recordId) throw new Error('um_mem_approve: record_id is required')
        const note = String(args.note ?? '').trim()
        const argv = ['approve', ...commonArgs(config), '--record-id', recordId, '--approver', config.userId || 'operator']
        if (note) argv.push('--note', note)
        const payload = await runCli(config, argv)
        return payload.approved
          ? 'approve: ok | record=' + payload.record_id + ' | evidence=' + payload.evidence_count
          : 'approve: уже подтверждено этим оператором'
      },
    },
    {
      name: 'um_mem_stats',
      description: 'Метрики зрелости памяти: сколько записей живёт, как они распределены по блокам и видам, какая доля подтверждена (promoted), связана рёбрами и размечена метками. Гигиену (дубли, мусор) показывает линтер, а это — «взрослеет ли память».',
      parameters: { type: 'object', properties: {} },
      output: toolOutput(),
      async execute() {
        const payload = await runCli(config, ['memory-stats', ...commonArgs(config)])
        const parts = [
          'records=' + payload.records,
          'retracted=' + payload.retracted,
          'promoted=' + payload.promoted + ' (' + Math.round((payload.promoted_share || 0) * 100) + '%)',
          'linked=' + payload.linked + ' (' + Math.round((payload.linked_share || 0) * 100) + '%)',
          'labeled=' + payload.labeled + ' (' + Math.round((payload.labeled_share || 0) * 100) + '%)',
          'promotion_candidates=' + payload.promotion_candidates,
          'edges=' + payload.edges,
        ]
        const blocks = Object.entries(payload.blocks || {}).map(([name, count]) => name + '=' + count).join(', ')
        return parts.join(' | ') + '\nблоки: ' + blocks
      },
    },
    {
      name: 'um_mem_health',
      description: 'Состояние хранилища UM-MEM: выбранный бэкенд, workspace/profile, прошедшая ли нативная проекция, есть ли зависшие записи.',
      parameters: { type: 'object', properties: {} },
      output: toolOutput(),
      async execute() {
        const payload = await runCli(config, ['health', ...commonArgs(config)])
        return renderHealth(payload)
      },
    },
  ]

  const exposedTools = config.compact ? tools.filter(tool => Object.hasOwn(COMPACT_DESCRIPTIONS, tool.name)).map(compactTool) : tools
  for (const tool of exposedTools) {
    disposers.push(ctx.tools.register(tool))
  }

  // ---------- loopback diagnostics ----------
  try {
    ctx.inject(['webServer'], (host) => {
      const route = {
        kind: 'exact',
        path: '/api/um-mem/health',
        handler: async (request, response) => {
          const address = request?.socket?.remoteAddress
          const loopback = address === '127.0.0.1' || address === '::1' || address === '::ffff:127.0.0.1'
          if (!loopback) {
            response.writeHead(403)
            response.end()
            return
          }
          try {
            const payload = await runCli(config, ['health', ...commonArgs(config)])
            response.writeHead(200, { 'content-type': 'application/json; charset=utf-8', 'cache-control': 'no-store' })
            response.end(JSON.stringify({
              config: { store: config.store, workspace: config.workspace, profile: config.profile, backend: config.backend },
              health: renderHealth(payload),
              focus: { source: focusSource, ageMs: focusAt ? Date.now() - focusAt : null, chars: focusText.length },
              raw: payload,
            }))
          } catch (error) {
            response.writeHead(503, { 'content-type': 'application/json; charset=utf-8' })
            response.end(JSON.stringify({ ok: false, error: String(error?.message || error) }))
          }
        },
      }
      try {
        host.effect(() => host.webServer.register(route), 'um-mem: route ' + route.path)
      } catch (error) {
        host.logger?.warn?.('[dsh-um-mem] route failed: ' + String(error))
      }
    })
  } catch (error) {
    ctx.logger?.debug?.('[dsh-um-mem] no webServer: ' + String(error))
  }

  ctx.effect(() => () => {
    for (const dispose of disposers) {
      try {
        dispose?.()
      } catch {
        /* one dead disposer must not block the rest */
      }
    }
  }, 'dsh-um-mem: surfaces')

  void refreshFocus()
  console.log('[dsh-um-mem] ready: store=' + config.store + ' workspace=' + config.workspace + ' backend=' + config.backend + ' tools=' + tools.length)
}
