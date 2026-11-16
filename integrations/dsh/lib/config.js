/** Resolve the plugin configuration: environment > patch-row config > config file > defaults. */

import { existsSync, readFileSync, statSync } from 'node:fs'
import { basename, dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { homedir } from 'node:os'

const DEFAULT_BACKEND = 'mnemosyne'
const EXE = process.platform === 'win32' ? 'um-memory.exe' : 'um-memory'

/** The package directory (`.../integrations/dsh`), where a local config may live. */
function packageRoot() {
  return resolve(dirname(fileURLToPath(import.meta.url)), '..')
}

function text(...values) {
  for (const value of values) {
    if (typeof value === 'string' && value.trim()) return value.trim()
  }
  return ''
}

function positive(value, fallback) {
  const number = Number(value)
  return Number.isFinite(number) && number > 0 ? Math.trunc(number) : fallback
}

/** A workspace id that survives being used as a directory name and a SQLite key. */
export function slugFor(value) {
  const slug = String(value || '')
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 64)
  return slug || 'workspace'
}

function binaryFor(candidate) {
  if (!candidate) return ''
  try {
    if (existsSync(candidate) && statSync(candidate).isDirectory()) return join(candidate, EXE)
  } catch {
    /* fall through to the literal value */
  }
  return candidate
}

/** Optional per-machine config: `~/.um-mem/dsh.json` (or UM_MEM_DSH_CONFIG)
 *  and `um-mem.config.json` next to this package. */
export function readConfigFile() {
  const explicit = text(process.env.UM_MEM_DSH_CONFIG)
  const candidates = [
    explicit,
    join(homedir(), '.um-mem', 'dsh.json'),
    join(packageRoot(), 'um-mem.config.json'),
  ].filter(Boolean)
  for (const candidate of candidates) {
    try {
      if (!existsSync(candidate)) continue
      const parsed = JSON.parse(readFileSync(candidate, 'utf8'))
      if (parsed && typeof parsed === 'object') return parsed
    } catch {
      /* a broken config file must not take the plugin down */
    }
  }
  return {}
}

export function resolveConfig(raw = {}) {
  const env = process.env
  const file = readConfigFile()
  const home = homedir()
  const homeBin = join(home, '.um-mem', 'bin')
  const configured = binaryFor(text(env.UM_MEM_BIN, raw.binary, file.binary))
  const binary = configured || join(homeBin, EXE)
  return {
    binary: binaryFor(binary) || join(binary, EXE),
    store: resolve(text(env.UM_MEM_STORE, raw.store, file.store) || join(home, '.um-mem', 'store')),
    workspace: text(env.UM_MEM_WORKSPACE, raw.workspace, file.workspace) || slugFor(basename(process.cwd())),
    profile: text(env.UM_MEM_PROFILE, raw.profile, file.profile) || 'default',
    userId: text(env.UM_MEM_USER_ID, raw.userId, file.userId),
    backend: text(env.UM_MEM_BACKEND, raw.backend, file.backend) || DEFAULT_BACKEND,
    limit: positive(raw.limit, positive(file.limit, 8)),
    focusLimit: positive(raw.focusLimit, positive(file.focusLimit, 12)),
    focusMaxChars: positive(raw.focusMaxChars, positive(file.focusMaxChars, 1400)),
    timeoutMs: positive(raw.timeoutMs, positive(file.timeoutMs, 30000)),
    inject: raw.inject ?? file.inject ?? true,
  }
}
