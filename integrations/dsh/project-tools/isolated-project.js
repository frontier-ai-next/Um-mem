import { spawnSync } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { existsSync, readFileSync, readdirSync, realpathSync, writeFileSync } from 'node:fs'
import { dirname, isAbsolute, relative, resolve, sep } from 'node:path'
import { isDeepStrictEqual } from 'node:util'
import { probeJSON } from './cli-probes.js'
import { compareDates } from './date-checks.js'

const textOf = blocks => (blocks ?? []).map(part => part.type === 'text' ? part.text : '').filter(Boolean).join('\n')

function quotedSources(text, sources) {
  for (const match of text.matchAll(/^- (?:Первоисточник|Прежний источник \(только неизменившиеся условия\)|Цитата): (.+?) \(id=(mem_\w+)(?: rev=rev_\w+)?\)$/gmu)) {
    sources.set(match[2], match[1])
  }
  const full = /^.*\nid=(mem_\w+) rev=rev_\w+\nkind=source_excerpt [^\n]*\n[^]*?\n\n([^]+)$/u.exec(text)
  if (full) sources.set(full[1], full[2])
  for (const match of text.matchAll(/^(?:Более поздняя реплика по теме[^\n]*?|Запрошенная более ранняя реплика) \(id=(mem_\w+)\)\.\n([^\n]+)/gmu)) {
    if (match[2] !== 'Цитата уже показана.') sources.set(match[1], match[2])
  }
}

function jsonValue(json, key) {
  let path = [key]
  if (key.startsWith('/')) path = key.slice(1).split('/').map(part => part.replaceAll('~1', '/').replaceAll('~0', '~'))
  if (/^\$[.[]/.test(key)) {
    const parts = [...key.matchAll(/\.([^.[\]]+)|\[(\d+)\]/gu)]
    if (parts.map(part => part[0]).join('') !== key.slice(1)) throw new Error('Use a simple JSON path.')
    path = parts.map(part => part[1] ?? part[2])
  }
  return path.reduce((object, part) => object != null && Object.hasOwn(object, part) ? object[part] : undefined, json)
}

function sourceContains(text, value) {
  if (value != null && typeof value === 'object') return Object.values(value).every(item => sourceContains(text, item))
  const literal = String(value)
  if (!literal) return false
  // Match the literal as a whole, so a shorter number or code cannot borrow proof from a longer one.
  const escaped = literal.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  return new RegExp('(?<![\\p{L}\\p{N}])' + escaped + '(?![\\p{L}\\p{N}])', 'u').test(text)
}

export function registerProjectTools(ctx, directory, nodeExecutable, sourcePaths = []) {
  const root = realpathSync(directory)
  ctx.systemPrompt?.section({
    name: 'isolated-project:quality',
    order: 220,
    text: () => 'Используй последнее подтверждение и точную дату из исходной цитаты. Поля времени содержат только время. Читай и сохраняй файлы пакетом. Запускай существующие тесты через scripts одним вызовом, вспомогательный запускатор не нужен. Для JSON CLI передай нормальный stdin с одной строкой массива. Если инструмент вернул repairs, исправь причину и проверь тем же вводом. Для чисел и дат укажи checks.values и from с id первоисточника. Проверку можно включить в project_save как verify. При нехватке источника укажи, что неизвестно.',
  })
  // Each benchmark stage starts a new Harness process and a fresh tool context.
  const shownFiles = new Map()
  const verified = new Map()
  let programRevision = 0
  const observed = new WeakMap()
  function sessionSources(session) {
    if (!session) return null
    if (!observed.has(session)) observed.set(session, { quotes: new Map(), calls: new Set(), dates: new Map(), failing: new Map(), reminded: false })
    return observed.get(session)
  }
  ctx.on?.('session/event', (session, event) => {
    if (!session) return
    if (event.type === 'turn/start') observed.delete(session)
    const { quotes, calls } = sessionSources(session)
    const data = event.data
    if (event.type === 'user/message') {
      if (data.source?.kind === 'user') quotes.set('user', textOf(data.content))
      for (const section of data.source?.sections ?? []) {
        if (section.name === 'dsh:um-mem-focus') quotedSources(section.text, quotes)
      }
    } else if (event.type === 'tool/call' && ['um_mem_read', 'um_mem_recall'].includes(data.name)) {
      calls.add(data.callId)
    } else if (event.type === 'tool/result' && calls.delete(data.message?.toolCallId)) {
      quotedSources(textOf(data.message.content), quotes)
    }
  })
  let shownListing
  const output = { schema: { type: 'string' }, render(_args, value) { return [{ type: 'text', text: value }] } }
  function checked(name, write = false) {
    if (typeof name !== 'string' || !name || isAbsolute(name) || name.includes(':')) {
      throw new Error('Use a path relative to the project.')
    }
    const target = resolve(root, name)
    const route = relative(root, target)
    if (!route || route === '..' || route.startsWith('..' + sep) || isAbsolute(route)) {
      throw new Error('The path is outside this project.')
    }
    const canonical = realpathSync(write && !existsSync(target) ? dirname(target) : target)
    const traced = relative(root, canonical)
    if (traced === '..' || traced.startsWith('..' + sep) || isAbsolute(traced)) {
      throw new Error('The path resolves outside this project.')
    }
    return target
  }
  const pathList = { type: 'array', items: { type: 'string' }, maxItems: 12 }
  const sourceFiles = new Map(sourcePaths.map(name => [checked(name), readFileSync(checked(name), 'utf8')]))
  function datesFor(path, text, exec) {
    const state = sessionSources(exec?.agent?.session)
    if (!state || !/\.(json|md)$/iu.test(path) || sourceFiles.has(checked(path))) return []
    const findings = compareDates(text, state.quotes)
    state.dates.set(relative(root, checked(path)), findings)
    return findings
  }
  ctx.on?.('agent/turn-stopping', ({ agent, signal }) => {
    const state = sessionSources(agent?.session)
    if (!state || typeof agent.steer !== 'function' || signal?.aborted) return
    for (const problem of [...state.failing.values()]) {
      if (problem.revision !== programRevision) runScript(problem.script, problem.stdin, state.failing)
    }
    if (state.reminded) return
    const failures = [...state.failing.values()].flatMap(problem => problem.repairs.map(repair => ({ script: problem.script, ...repair })))
    const dates = [...state.dates].flatMap(([path, findings]) => findings.filter(finding => !finding.ok).map(finding => ({ path, ...finding })))
    if (!failures.length && !dates.length) return
    state.reminded = true
    agent.steer({ id: randomUUID(), role: 'user', source: { kind: 'project-verification' }, content: [{ type: 'text', text:
      'Проверка обнаружила неисправленное расхождение. Исправь его точечно и повтори ту же проверку. Если исправление невозможно, сообщи об этом.\n' +
      JSON.stringify({ repairs: failures.slice(0, 4), dates: dates.slice(0, 4) }),
    }] })
  })
  const verification = { type: 'object', properties: {
    script: { type: 'string' }, stdin: { type: 'string' },
    scripts: { ...pathList, description: 'Run existing tests together. For a CLI with stdin, use script and stdin.' },
    checks: { type: 'array', maxItems: 12, items: { type: 'object', required: ['path'], properties: {
      path: { type: 'string' }, has: pathList, lacks: pathList,
      from: { ...pathList, description: 'Source refs for values: user, a visible quote ID, or file:path to an input.' },
      values: { type: 'object', additionalProperties: true, description: 'Only for JSON. Expected values from source by key or path like /items/0/value.' },
    } } },
  } }
  ctx.tools.register({
    name: 'project_open',
    description: 'List project files, or read related files together by passing their relative paths. Unchanged files already shown in this stage are summarized.',
    parameters: { type: 'object', properties: { paths: pathList } }, output,
    async execute({ paths = [] }, exec) {
      if (!paths.length) {
        const listing = readdirSync(root, { recursive: true }).join('\n')
        if (shownListing === listing) return 'The project file list is unchanged from the previous listing.'
        shownListing = listing
        return listing
      }
      return paths.map(name => {
        try {
          const file = checked(name)
          const content = readFileSync(file, 'utf8')
          if (shownFiles.get(file) === content) return '# ' + name + ' — already shown in this stage and unchanged.'
          shownFiles.set(file, content)
          const dates = datesFor(name, content, exec)
          const receipt = dates.length ? 'Date check: ' + JSON.stringify(dates) + '\n\n' : ''
          return receipt + '# ' + name + '\n' + content
        } catch (error) {
          const message = error.code === 'ENOENT' ? 'File not found. Use the project listing for available paths.'
            : error.code === 'EISDIR' ? 'This path is a directory. Use the project listing.' : 'Unable to read this project path.'
          return '# ' + JSON.stringify(name) + ' — ' + message
        }
      }).join('\n\n')
    },
  })
  ctx.tools.register({
    name: 'project_save',
    description: 'Save files or make unique replacements. Use verify in the same call to check saved work.',
    parameters: { type: 'object', properties: {
      files: { type: 'array', maxItems: 10, items: { type: 'object', required: ['path', 'text'],
        properties: { path: { type: 'string' }, text: { type: 'string' } } } },
      edits: { type: 'array', maxItems: 20, items: { type: 'object', required: ['path', 'old', 'new'],
        properties: { path: { type: 'string' }, old: { type: 'string' }, new: { type: 'string' } } } },
      verify: verification,
    } }, output,
    async execute({ files = [], edits = [], verify }, exec) {
      const targets = new Map(files.map(file => [checked(file.path, true), file.text]))
      const visible = new Set(targets.keys())
      for (const edit of edits) {
        const path = checked(edit.path)
        const text = targets.get(path) ?? readFileSync(path, 'utf8')
        const at = text.indexOf(edit.old)
        if (!edit.old || at < 0 || text.indexOf(edit.old, at + 1) !== -1) {
          return 'No files changed. Exact edit must match once in ' + edit.path
        }
        targets.set(path, text.slice(0, at) + edit.new + text.slice(at + edit.old.length))
      }
      if (!targets.size) return 'No files or edits supplied.'
      const checkedJSON = new Set((Array.isArray(verify?.checks) ? verify.checks : [])
        .filter(check => typeof check?.path === 'string' && /\.json$/iu.test(check.path))
        .map(check => checked(check.path, true)))
      for (const [path, text] of targets) {
        if (!/\.json$/iu.test(path)) continue
        // Existing JSON and explicitly checked JSON must stay parseable. A new
        // malformed input fixture may still be saved when it isn't a result.
        let cameFromJSON = false
        if (existsSync(path)) {
          try { JSON.parse(readFileSync(path, 'utf8')); cameFromJSON = true } catch { /* an existing invalid fixture */ }
        }
        if (!checkedJSON.has(path) && !cameFromJSON) continue
        try { JSON.parse(text) }
        catch (error) { return 'No files changed. Invalid JSON in ' + relative(root, path) + ': ' + error.message }
      }
      let changed = false
      let changedProgram = false
      for (const [path, text] of targets) {
        if (existsSync(path) && readFileSync(path, 'utf8') === text) continue
        writeFileSync(path, text, 'utf8')
        changed = true
        if (/\.[cm]?[jt]s$/iu.test(path)) changedProgram = true
        if (visible.has(path) || shownFiles.has(path)) shownFiles.set(path, text)
      }
      if (changedProgram) programRevision++
      if (changed) verified.clear()
      const names = [...targets.keys()].map(path => relative(root, path)).join(', ')
      const receipt = changed ? names : 'Files already matched, no change: ' + names
      const dates = [...targets].flatMap(([path, text]) => {
        const name = relative(root, path)
        return datesFor(name, text, exec).map(date => ({ path: name, ...date }))
      })
      return receipt + (verify ? '\n' + runVerification(verify, exec) : '') + (dates.length ? '\nDate check: ' + JSON.stringify(dates) : '')
    },
  })
  function checkFiles(checks, exec) {
    const quotes = sessionSources(exec?.agent?.session)?.quotes
    return checks.map(({ path, has = [], lacks = [], values = {}, from = [] }) => {
      try {
          const target = checked(path)
          const fields = Object.entries(values)
          if (!has.length && !lacks.length && !fields.length) return { path, ok: false, error: 'Supply source values.' }
          const proofs = from.map(ref => {
            if (!ref.startsWith('file:')) return quotes?.get(ref)
            const file = checked(ref.slice(5))
            return file !== target ? sourceFiles.get(file) : undefined
          })
          const unknown = from.filter((_, index) => !proofs[index])
          if (unknown.length) return { path, ok: false, unavailable: unknown }
          if (fields.length && !proofs.length) return { path, ok: false, error: 'Typed values require from with a source reference.' }
          const unbacked = fields.filter(([, value]) => !proofs.some(proof => sourceContains(proof, value))).map(([key]) => key)
          const text = readFileSync(target, 'utf8')
          let json
          if (fields.length || /\.json$/iu.test(target)) {
            try { json = JSON.parse(text) } catch { return { path, ok: false, error: 'Expected valid JSON.' } }
          }
          const dates = datesFor(path, text, exec)
          const unequal = []
          if (fields.length) {
            if (!json || typeof json !== 'object') return { path, ok: false, error: 'Expected a JSON object.' }
            for (const [key, expected] of fields) {
              const actual = jsonValue(json, key)
              if (!isDeepStrictEqual(actual, expected)) unequal.push({ key, ...(actual === undefined ? { missing: true } : { actual }), expected })
            }
          }
          const missing = has.filter(value => !text.includes(value))
          const stale = lacks.filter(value => text.includes(value))
          return { path, ok: !missing.length && !stale.length && !unequal.length && !unbacked.length && dates.every(date => date.ok),
            ...(dates.length ? { dates } : {}),
            ...(unbacked.length ? { unbacked } : {}),
            ...(missing.length ? { missing } : {}), ...(stale.length ? { stale } : {}), ...(unequal.length ? { unequal } : {}) }
      } catch (error) {
        return { path, ok: false, error: error.code === 'ENOENT' ? 'File not found.' : 'Invalid project path or check.' }
      }
    })
  }
  function runVerification({ script, stdin = '', checks, scripts = [] }, exec) {
    if (checks && !Array.isArray(checks)) return JSON.stringify({ error: 'checks must be an array.' })
    const files = checks?.length ? checkFiles(checks, exec) : undefined
    const failing = sessionSources(exec?.agent?.session)?.failing
    if (scripts.length) {
      const tests = [...new Set(scripts)].map(path => ({ path, ...runScript(path, '', failing) }))
      return JSON.stringify({ ...(files ? { files } : {}), tests, ...(script ? { script: runScript(script, stdin, failing) } : {}) })
    }
    if (files && !script) return JSON.stringify(files)
    const result = runScript(script ?? 'public-check.mjs', stdin, failing)
    return JSON.stringify(files ? { files, script: result } : result)
  }
  function runScript(script, stdin, failing) {
      if (!script.endsWith('.mjs')) throw new Error('Expected a project .mjs script.')
      let file
      try { file = checked(script) }
      catch (error) {
        if (error.code !== 'ENOENT') throw error
        return { error: 'No test file at this path.' }
      }
      const key = JSON.stringify([file, stdin])
      if (verified.has(key)) {
        const cached = verified.get(key)
        if (cached.repairs?.length) failing?.set(key, { script, stdin, revision: programRevision, repairs: cached.repairs })
        return { ...cached, repeated: true }
      }
      const deadline = Date.now() + 12000
      const invoke = input => spawnSync(nodeExecutable, ['--permission', '--allow-fs-read=' + root, file], {
        cwd: root, env: {}, encoding: 'utf8', input, timeout: Math.max(1, deadline - Date.now()), windowsHide: true,
      })
      const result = invoke(stdin)
      const { probes, repairs } = probeJSON(invoke, stdin, result, deadline)
      if (repairs.length) failing?.set(key, { script, stdin, revision: programRevision, repairs })
      else if (result.status === 0) failing?.delete(key)
      else if (failing?.has(key)) failing.get(key).revision = programRevision
      if (Number.isInteger(result.status) && !result.error) {
        verified.set(key, { exit: result.status, ...(repairs.length ? { repairs } : {}) })
      }
      return { exit: result.status, stdout: result.stdout?.slice(0, 3200),
        stderr: result.stderr?.slice(0, 1600), error: result.error?.message,
        ...(repairs.length ? { repairs } : {}), ...(probes.length ? { probes } : {}) }
  }
  ctx.tools.register({
    name: 'project_check',
    description: 'Run a script or verify files. JSON values use field names or JSON Pointers and from refs for source literals.',
    parameters: verification, output,
    execute: runVerification,
  })
}
