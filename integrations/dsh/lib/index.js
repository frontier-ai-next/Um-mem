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

import { commonArgs, runCli, runWithRequest } from './cli.js'
import { resolveConfig } from './config.js'
import { compactRecords, renderFocus, renderHealth, renderRecall, renderWriteResult } from './view.js'

export const name = 'um-mem'
export const inject = ['tools', 'systemPrompt']

/** Static rules must stay byte-stable: they are the prefix-cache anchor. */
const RULES = `## UM-MEM (постоянная память)

Память живёт вне контекста: канонический журнал со скоупами workspace/profile, ревизиями, provenance и правом отзыва. Она **advisory**: найденная запись не даёт полномочий на действие и не доказывает, что задача выполнена.

- Опираешься на историю (прошлые решения, договорённости, устройство проекта, предпочтения пользователя) — сначала \`um_mem_recall\`, потом работа.
- Появился факт, решение или вывод, который понадобится в следующий раз — \`um_mem_remember\`. Болтовню, одноразовые пути и пересказ кода не записывать.
- Новый факт отменяет старый — \`um_mem_amend\` с \`rev\` из recall; запись была ошибочной — \`um_mem_forget\`.
- Пустой recall — это ответ «в памяти нет». Не выдумывай записи и не ссылайся на память, которой не видел.`

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
  const isMarker = (part) => /^(current runtime context|\[|<|#|\||-{3,}|\*{3,})/i.test(part)
  const plain = paragraphs.filter((part) => !isMarker(part))
  const last = plain.length ? plain[plain.length - 1] : (paragraphs[paragraphs.length - 1] || '')
  return last.slice(0, 400)
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

function scopeChain(config) {
  const chain = ['workspace:' + config.workspace]
  if (config.userId) chain.push('user_global:' + config.userId)
  return chain
}

export function apply(ctx, rawConfig = {}) {
  const config = resolveConfig(rawConfig)
  const disposers = []

  // ---------- pre-turn focus block ----------
  let focusText = ''
  let focusAt = 0
  let focusSource = ''
  let refreshing = false
  let lastRefreshStart = 0
  // Per-turn trace: what was asked and which tools actually ran. Without it the
  // store only grows when the model decides to write, which is why a session of
  // real work could leave no trace at all.
  let turn = { user: '', tools: [], startedAt: 0 }

  function renderFocusFrom(records) {
    if (!config.inject) return ''
    return renderFocus(records, {
      workspace: config.workspace,
      maxChars: config.focusMaxChars,
      limit: config.focusLimit,
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
    const now = Date.now()
    if (refreshing || now - lastRefreshStart < REFRESH_GAP_MS) return
    refreshing = true
    lastRefreshStart = now
    try {
      const workspaceRecords = await runCli(config, [
        'recent',
        ...commonArgs(config),
        '--limit', String(config.focusLimit),
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
      if (!event || event.type !== 'user/message') return
      const text = extractText(event.data?.content ?? event.data?.message?.content) || extractText(event.message?.content)
      if (!text) return
      turn = { user: userTail(text), tools: [], startedAt: Date.now() }
      void refreshFocus()
      if (!config.inject) return
      // Best-effort semantic recall for this turn; the block shows up from the
      // next assembly onward and is deduplicated by the host while it stays stable.
      void (async () => {
        try {
          const records = await runWithRequest(config, 'recall', {
            query: text.slice(0, 600),
            workspace_id: config.workspace,
            profile_id: config.profile,
            scope_chain: scopeChain(config),
            source_horizon: 'durable',
            temporal_mode: 'current',
            retrieval_mode: 'semantic',
            search_all: true,
            include_candidates: true,
            limit: config.limit,
            max_tokens: 1800,
          })
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

  // Warm the block before the next request assembles: the sync `text()` renderer
  // cannot await a recall, so the turn that arrives right after a user message
  // would otherwise show the previous turn's snapshot.
  const warm = () => {
    if (!config.inject) return
    if (Date.now() - focusAt < 60000) return
    void refreshFocus()
  }
  try {
    ctx.on('agent/pre-step', (...args) => {
      try {
        warm()
      } catch {
        /* warm-up must never break the step */
      }
      const next = args[args.length - 1]
      return typeof next === 'function' ? next() : undefined
    })
  } catch (error) {
    ctx.logger?.debug?.('[dsh-um-mem] no pre-step hook: ' + String(error))
  }

  async function recordEpisode() {
    const snapshot = turn
    turn = { user: '', tools: [], startedAt: 0 }
    if (!snapshot.user || snapshot.tools.length === 0) return
    const minutes = snapshot.startedAt ? Math.max(1, Math.round((Date.now() - snapshot.startedAt) / 60000)) : 0
    const content = 'Раунд: запрос «' + snapshot.user.slice(0, 220) + '»; инструменты: ' +
      snapshot.tools.slice(0, 8).join(', ') + (minutes ? '; ~' + minutes + ' мин' : '')
    try {
      await runWithRequest(config, 'remember-candidate', {
        operation_id: operationId(),
        content,
        title: 'Эпизод: ' + snapshot.user.slice(0, 60),
        kind: 'episode',
        tags: ['episode', 'turn'],
        requested_block_id: 'workspace_episodes',
        evidence_kind: 'tool_output',
        ...(config.userId ? { user_id: config.userId } : {}),
      })
      void refreshFocus()
    } catch (error) {
      ctx.logger?.debug?.('[dsh-um-mem] episode capture failed: ' + String(error?.message || error))
    }
  }

  try {
    ctx.on('tools/result', (exec) => {
      const toolName = typeof exec?.name === 'string' ? exec.name : ''
      if (!toolName || toolName.startsWith('um_mem_')) return
      if (!turn.tools.includes(toolName)) turn.tools.push(toolName)
    })
  } catch {
    /* host without tool results */
  }

  try {
    ctx.on('agent/turn-stopping', (...args) => {
      try {
        void recordEpisode()
      } catch {
        /* capture must never break the turn */
      }
      const next = args[args.length - 1]
      return typeof next === 'function' ? next() : undefined
    })
  } catch (error) {
    ctx.logger?.debug?.('[dsh-um-mem] no turn-stopping hook: ' + String(error))
  }

  disposers.push(ctx.systemPrompt.section({ name: 'dsh:um-mem-rules', order: SECTION_ORDER, text: () => RULES }))
  disposers.push(ctx.systemPrompt.context({
    name: 'dsh:um-mem-focus',
    order: SECTION_ORDER + 1,
    text: () => {
      if (!focusText) return ''
      if (Date.now() - focusAt > FOCUS_TTL_MS) return ''
      return focusText
    },
  }))

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
        },
        required: ['query'],
      },
      output: toolOutput(),
      async execute(args = {}) {
        const query = String(args.query ?? '').trim()
        if (!query) throw new Error('um_mem_recall: query is required')
        const limit = Number.isFinite(Number(args.limit)) && Number(args.limit) > 0 ? Math.trunc(Number(args.limit)) : config.limit
        const plan = {
          query,
          workspace_id: config.workspace,
          profile_id: config.profile,
          scope_chain: scopeChain(config),
          source_horizon: 'durable',
          temporal_mode: 'current',
          retrieval_mode: 'semantic',
          include_candidates: true,
          limit,
          max_tokens: 1800,
        }
        const block = String(args.block ?? '').trim()
        if (block) plan.logical_block_ids = [block]
        else plan.search_all = true
        const facet = String(args.facet ?? '').trim()
        if (facet && !facet.includes('=')) throw new Error('um_mem_recall: facet must look like um.key=value')
        const records = await runWithRequest(config, 'recall', plan, facet ? ['--facet', facet] : [])
        const compact = compactRecords(records)
        let text = renderRecall(records, { limit })
        // Links were stored but nothing read them, so a connected record stayed
        // invisible after a recall. Pull one hop for the top hits and mark it.
        const known = new Set(compact.map((record) => record.id))
        const linked = []
        for (const record of compact.slice(0, 2)) {
          if (!record.id) continue
          try {
            const payload = await runCli(config, ['neighbors', ...commonArgs(config), '--record-id', record.id, '--depth', '1'])
            for (const neighbor of payload.neighbors || []) {
              if (known.has(neighbor.record_id)) continue
              known.add(neighbor.record_id)
              linked.push({ ...neighbor, via: record.title })
              if (linked.length >= 4) break
            }
          } catch {
            /* a broken graph must not fail the recall itself */
          }
          if (linked.length >= 4) break
        }
        if (linked.length) {
          text += '\n\nСвязано ребром:\n' + linked
            .map((neighbor) => '- [' + neighbor.edge_type + '] ' + neighbor.title +
              ' (id=' + neighbor.record_id + ', ' + neighbor.logical_block_id + ')' +
              (neighbor.via ? ' — через «' + neighbor.via + '»' : ''))
            .join('\n')
        }
        return text
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
          kind: { type: 'string', description: 'Тип: fact, preference, rule, decision, task, lesson. По умолчанию fact.' },
          tags: { type: 'string', description: 'Теги через запятую (опционально).' },
          block: { type: 'string', description: 'Логический блок (по умолчанию stable_project_facts).' },
          evidence_kind: { type: 'string', description: 'Откуда факт: user_statement, tool_output, file_read, agent_inference. По умолчанию agent_inference.' },
          metadata: { type: 'string', description: 'Опциональные метки записи как JSON-объект, например {"source":"repo","confidence":"high"}. Служебные ключи (status, authority, scope, retention…) зарезервированы и будут отклонены.' },
        },
        required: ['content'],
      },
      output: toolOutput(),
      async execute(args = {}) {
        const content = String(args.content ?? '').trim()
        if (!content) throw new Error('um_mem_remember: content is required')
        const tags = String(args.tags ?? '')
          .split(',')
          .map((tag) => tag.trim())
          .filter(Boolean)
        const payload = {
          operation_id: operationId(),
          content,
          title: String(args.title ?? '').trim() || content.slice(0, 80),
          kind: String(args.kind ?? '').trim() || 'fact',
          tags,
          requested_block_id: String(args.block ?? '').trim() || 'stable_project_facts',
          evidence_kind: String(args.evidence_kind ?? '').trim() || 'agent_inference',
        }
        if (config.userId) payload.user_id = config.userId
        const rawMeta = String(args.metadata ?? '').trim()
        if (rawMeta) {
          try {
            const parsed = JSON.parse(rawMeta)
            if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) payload.metadata = parsed
          } catch {
            throw new Error('um_mem_remember: metadata must be a JSON object')
          }
        }
        const result = await runWithRequest(config, 'remember-candidate', payload)
        void refreshFocus()
        return renderWriteResult(result, { verb: 'remember' })
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
          reason: { type: 'string', description: 'Почему запись меняется.' },
        },
        required: ['record_id', 'expected_revision', 'content'],
      },
      output: toolOutput(),
      async execute(args = {}) {
        const result = await runWithRequest(config, 'amend', {
          memory_id: String(args.record_id ?? '').trim(),
          expected_revision: String(args.expected_revision ?? '').trim(),
          reason: String(args.reason ?? '').trim() || 'amended through dsh-um-mem',
          content: String(args.content ?? ''),
          operation_id: operationId(),
        })
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
        void refreshFocus()
        return renderWriteResult(result, { verb: 'forget' })
      },
    },
    {
      name: 'um_mem_read',
      description: 'Прочитать одну запись памяти целиком по record_id (включая метаданные, статус и ревизию).',
      parameters: {
        type: 'object',
        properties: { record_id: { type: 'string', description: 'id записи (mem_…).' } },
        required: ['record_id'],
      },
      output: toolOutput(),
      async execute(args = {}) {
        const recordId = String(args.record_id ?? '').trim()
        if (!recordId) throw new Error('um_mem_read: record_id is required')
        const record = await runCli(config, ['read-record', ...commonArgs(config), '--record-id', recordId])
        const [compact] = compactRecords(record)
        if (!compact) return 'UM-MEM: record not found: ' + recordId
        return [
          compact.title || '(no title)',
          'id=' + compact.id + ' rev=' + compact.revision,
          'kind=' + compact.kind + ' status=' + compact.status + ' authority=' + compact.authority,
          'block=' + compact.block + (compact.tags.length ? ' tags=' + compact.tags.join(',') : ''),
          'at=' + compact.at,
          '',
          compact.content,
        ].join('\n')
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

  for (const tool of tools) disposers.push(ctx.tools.register(tool))

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
