/** Thin wrapper around the `um-memory` CLI: one JSON document in, one JSON document out. */

import { execFile } from 'node:child_process'
import { existsSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const MAX_BUFFER = 32 * 1024 * 1024

export function parseJsonDocument(text) {
  const raw = String(text ?? '').trim()
  if (!raw) throw new Error('empty output')
  try {
    return JSON.parse(raw)
  } catch {
    /* scan for the first JSON document below */
  }
  for (let index = 0; index < raw.length; index += 1) {
    const head = raw[index]
    if (head !== '[' && head !== '{') continue
    try {
      return JSON.parse(raw.slice(index))
    } catch {
      continue
    }
  }
  throw new Error('non-JSON output: ' + raw.slice(0, 300))
}

export function commonArgs(config) {
  return [
    '--repo-root', config.store,
    '--workspace', config.workspace,
    '--profile', config.profile,
    '--backend', config.backend,
  ]
}

export function runCli(config, args, options = {}) {
  const timeout = options.timeout ?? config.timeoutMs
  return new Promise((resolve, reject) => {
    if (!existsSync(config.binary)) {
      reject(new Error('um-memory binary not found at ' + config.binary + ' (build it with `go build -tags purego -o bin/um-memory.exe ./cmd/um-memory` or install the release)'))
      return
    }
    execFile(
      config.binary,
      args,
      { timeout, windowsHide: true, maxBuffer: MAX_BUFFER, encoding: 'utf8' },
      (error, stdout, stderr) => {
        if (error) {
          if (error.code === 'EPERM' || error.code === 'EACCES') {
            reject(new Error('cannot execute ' + config.binary + ' (' + error.code + '): check that the binary exists and the host is allowed to spawn it'))
            return
          }
          const detail = String(stderr || '').trim() || String(stdout || '').trim() || error.message
          reject(new Error(detail.replace(/^um-memory:\s*/, '')))
          return
        }
        try {
          resolve(parseJsonDocument(stdout))
        } catch (parseError) {
          reject(parseError)
        }
      },
    )
  })
}

/** Run a command that takes a single JSON request/plan file. */
export async function runWithRequest(config, command, payload, extraArgs = []) {
  const directory = mkdtempSync(join(tmpdir(), 'um-mem-dsh-'))
  try {
    const file = join(directory, 'request.json')
    // Node writes UTF-8 without a BOM, which is what `readJSONFile` expects.
    writeFileSync(file, JSON.stringify(payload), 'utf8')
    const flag = command === 'recall' ? '--plan-file' : '--request-file'
    // The `await` matters: without it the `finally` below deletes the request
    // file while the child process is still opening it.
    return await runCli(config, [command, ...commonArgs(config), flag, file, ...extraArgs])
  } finally {
    try {
      rmSync(directory, { recursive: true, force: true })
    } catch {
      /* temp cleanup is best effort */
    }
  }
}
