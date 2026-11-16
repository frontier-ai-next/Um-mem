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
  return ['workspace:' + config.workspace]
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
      const records = await runCli(config, [
        'recent',
        ...commonArgs(config),
        '--limit', String(config.focusLimit),
        '--scope', 'workspace',
        '--scope-id', config.workspace,
      ])
      scheduleFocus(records)
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
        const records = await runWithRequest(config, 'recall', plan)
        return renderRecall(records, { limit })
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
        const result = await runWithRequest(config, 'remember-candidate', payload)
        void refreshFocus()
        return renderWriteResult(result, { verb: 'remember' })
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
