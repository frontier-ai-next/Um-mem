/** Resolve the plugin configuration from the patch-row config and the environment. */

import { existsSync, statSync } from 'node:fs'
import { basename, join, resolve } from 'node:path'
import { homedir } from 'node:os'

const DEFAULT_BACKEND = 'mnemosyne'
const EXE = process.platform === 'win32' ? 'um-memory.exe' : 'um-memory'

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

export function resolveConfig(raw = {}) {
  const env = process.env
  const home = homedir()
  const homeBin = join(home, '.um-mem', 'bin')
  const configured = binaryFor(text(raw.binary, env.UM_MEM_BIN))
  const binary = configured
    ? binaryFor(configured) || join(configured, EXE)
    : join(homeBin, EXE)
  return {
    binary: binary || join(homeBin, EXE),
    store: resolve(text(raw.store, env.UM_MEM_STORE) || join(home, '.um-mem', 'store')),
    workspace: text(raw.workspace, env.UM_MEM_WORKSPACE) || slugFor(basename(process.cwd())),
    profile: text(raw.profile, env.UM_MEM_PROFILE) || 'default',
    backend: text(raw.backend, env.UM_MEM_BACKEND) || DEFAULT_BACKEND,
    limit: positive(raw.limit, 8),
    focusLimit: positive(raw.focusLimit, 12),
    focusMaxChars: positive(raw.focusMaxChars, 1400),
    timeoutMs: positive(raw.timeoutMs, 30000),
    inject: raw.inject !== false,
  }
}
