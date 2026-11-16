#!/usr/bin/env node
/**
 * Gold-set for UM-MEM recall.
 *
 * Runs a fixed list of questions against a store and reports whether the
 * expected record appears in the top k. Numbers, not impressions: without this
 * "the memory got better" is an opinion.
 *
 * Usage:
 *   node run.mjs <store> [--cli <path>] [--workspace deepseek-harness] [--profile default]
 *                 [--backend mnemosyne] [--k 3] [--json] [--allow-miss]
 */

import { execFile } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const args = process.argv.slice(2)
const store = args[0] && !args[0].startsWith('--') ? resolve(args[0]) : ''
if (!store) {
  console.error('usage: node run.mjs <store> [--cli <path>] [--workspace <id>] [--profile <id>] [--backend <id>] [--k 3] [--json] [--allow-miss]')
  process.exit(2)
}

function flag(name, fallback) {
  const index = args.indexOf('--' + name)
  if (index === -1) return fallback
  const value = args[index + 1]
  return value && !value.startsWith('--') ? value : true
}

const cli = flag('cli', 'C:/Users/polir/Documents/deepseek-harness/default-workspace/Um-mem/bin/um-memory.exe')
const workspace = flag('workspace', 'deepseek-harness')
const profile = flag('profile', 'default')
const backend = flag('backend', 'mnemosyne')
const k = Number(flag('k', 3))
const asJson = flag('json', false) === true
const allowMiss = flag('allow-miss', false) === true

const questions = JSON.parse(readFileSync(join(here, 'questions.json'), 'utf8'))

async function recall(query) {
  const dir = mkdtempSync(join(tmpdir(), 'goldset-'))
  try {
    const plan = join(dir, 'plan.json')
    writeFileSync(plan, JSON.stringify({
      query,
      workspace_id: workspace,
      profile_id: profile,
      scope_chain: ['workspace:' + workspace],
      source_horizon: 'durable',
      temporal_mode: 'current',
      retrieval_mode: 'semantic',
      include_candidates: true,
      limit: k,
    }), 'utf8')
    // `await` matters: the finally below removes the plan file, and without it
    // the child process would read a path that no longer exists.
    return await new Promise((resolvePromise, rejectPromise) => {
      execFile(cli, ['recall', '--repo-root', store, '--workspace', workspace,
        '--profile', profile, '--backend', backend, '--plan-file', plan],
      { timeout: 30000, windowsHide: true, maxBuffer: 32 * 1024 * 1024, encoding: 'utf8' },
      (error, stdout, stderr) => {
        if (error) {
          rejectPromise(new Error(String(stderr || stdout || error.message).slice(0, 300)))
          return
        }
        try {
          resolvePromise(JSON.parse(stdout))
        } catch {
          rejectPromise(new Error('non-JSON recall output'))
        }
      })
    })
  } finally {
    try { rmSync(dir, { recursive: true, force: true }) } catch { /* best effort */ }
  }
}

const rows = []
let hit1 = 0
let hitK = 0
let errors = 0

for (const item of questions) {
  let titles = []
  try {
    const records = await recall(item.q)
    titles = (records || []).map((record) => record.title || '')
  } catch (error) {
    errors += 1
    rows.push({ question: item.q, expect: item.expect, rank: null, error: String(error.message).slice(0, 120) })
    continue
  }
  const rank = titles.findIndex((title) => title.includes(item.expect))
  if (rank === 0) hit1 += 1
  if (rank >= 0 && rank < k) hitK += 1
  rows.push({ question: item.q, expect: item.expect, rank: rank < 0 ? null : rank + 1, titles })
}

const total = questions.length
const summary = {
  store, workspace, k, total,
  hit_at_1: hit1, hit_at_k: hitK, errors,
  hit_at_1_share: Number((hit1 / total).toFixed(3)),
  hit_at_k_share: Number((hitK / total).toFixed(3)),
}

if (asJson) {
  console.log(JSON.stringify({ summary, rows }, null, 2))
} else {
  for (const row of rows) {
    const mark = row.rank === null ? 'MISS' : (row.rank === 1 ? ' ok ' : 'top' + row.rank)
    console.log(mark.padEnd(5) + ' ' + row.question.slice(0, 44).padEnd(46) + ' -> ' +
      (row.error ? 'ERROR ' + row.error : (row.titles || []).join(' | ').slice(0, 70)))
  }
  console.log('\nhit@1 ' + hit1 + '/' + total + ' (' + Math.round(summary.hit_at_1_share * 100) + '%) · ' +
    'hit@' + k + ' ' + hitK + '/' + total + ' (' + Math.round(summary.hit_at_k_share * 100) + '%) · errors ' + errors)
}

if (!allowMiss && (errors > 0 || hitK < total)) process.exit(1)
