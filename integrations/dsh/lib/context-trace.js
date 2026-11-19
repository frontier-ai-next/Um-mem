import { createHash, randomUUID } from 'node:crypto'
import { mkdirSync, readdirSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

const hash = value => createHash('sha256').update(value).digest('hex')
export const turnID = (session, turn) => hash(session + '\0' + turn).slice(0, 16)

export function traceTurn(config, session, current) {
  if (!session?.id || !current?.trace) return
  const scope = hash(config.workspace + '\0' + config.profile)
  const dir = join(config.store, '.UM-MEM', 'memory', 'harness-context', scope)
  const prefix = current.trace.at.replace(/\D/g, '').slice(0, 17)
  const name = prefix + '-' + turnID(session.id, current.turn) + '.json'
  const temporary = join(dir, randomUUID() + '.tmp')
  try {
    mkdirSync(dir, { recursive: true })
    writeFileSync(temporary, JSON.stringify({
      id: turnID(session.id, current.turn), session: session.id, turn: String(current.turn),
      ...current.trace,
    }), 'utf8')
    renameSync(temporary, join(dir, name))
    const expired = readdirSync(dir).filter(file => /^\d{17}-[0-9a-f]{16}\.json$/.test(file)).sort().slice(0, -160)
    for (const file of expired) rmSync(join(dir, file), { force: true })
  } catch {
    try { rmSync(temporary, { force: true }) } catch { /* tracing is optional */ }
  }
}
