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


// --- optional semantic judge -------------------------------------------------
// String matching says "the title is not in the list"; a judge says "no record
// actually answers this". The judge is a cheap model, configured outside the
// repository in ~/.um-mem/eval.json, so a full run costs almost nothing.

function judgeConfig() {
  const env = process.env
  let config = { url: env.UM_MEM_JUDGE_URL || '', key: env.UM_MEM_JUDGE_KEY || '', model: env.UM_MEM_JUDGE_MODEL || '' }
  if (!config.url) {
    try {
      const home = env.USERPROFILE || env.HOME
      const raw = readFileSync(join(home, '.um-mem', 'eval.json'), 'utf8')
      const file = JSON.parse(raw)
      config = {
        url: config.url || file.base_url || file.url || '',
        key: config.key || file.api_key || file.key || '',
        model: config.model || file.model || '',
      }
    } catch { /* judge stays unavailable */ }
  }
  return config
}

async function judge(question, candidates) {
  const config = judgeConfig()
  if (!config.url || !config.model || !candidates.length) return 0
  const listing = candidates.map((item, index) =>
    '[' + (index + 1) + '] ' + item.title + ' :: ' + String(item.content || '').slice(0, 400)).join('\n\n')
  const prompt = 'Вопрос: ' + question + '\n\nКандидаты из памяти:\n' + listing +
    '\n\nКакой номер кандидата реально отвечает на вопрос? Ответь только цифрой (0 — ни один не отвечает).'
  try {
    const response = await fetch(config.url.replace(/\/$/, '') + '/chat/completions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + config.key },
      body: JSON.stringify({ model: config.model, temperature: 0, max_tokens: 512,
        messages: [{ role: 'user', content: prompt }] }),
    })
    if (!response.ok) return 0
    const payload = await response.json()
    const text = String(payload?.choices?.[0]?.message?.content || '').trim()
    const match = text.match(/\d+/)
    const index = match ? Number(match[0]) : 0
    return index >= 1 && index <= candidates.length ? index : 0
  } catch {
    return 0
  }
}
const rows = []
let hit1 = 0
let hitK = 0
let errors = 0

for (const item of questions) {
  let titles = []
  let candidates = []
  try {
    const records = await recall(item.q)
    titles = (records || []).map((record) => record.title || '')
    candidates = (records || []).map((record) => ({ title: record.title || '', content: record.content || '' }))
  } catch (error) {
    errors += 1
    rows.push({ question: item.q, expect: item.expect, rank: null, error: String(error.message).slice(0, 120) })
    continue
  }
  const rank = titles.findIndex((title) => title.includes(item.expect))
  if (rank === 0) hit1 += 1
  if (rank >= 0 && rank < k) hitK += 1
  rows.push({ question: item.q, expect: item.expect, rank: rank < 0 ? null : rank + 1, titles, candidates })
}

const total = questions.length
let judged1 = 0
let judgedK = 0
const useJudge = flag('judge', false) === true
if (useJudge) {
  for (const row of rows) {
    if (row.error) continue
    row.judged = await judge(row.question, row.candidates || [])
    if (row.judged === 1) judged1 += 1
    if (row.judged >= 1) judgedK += 1
  }
}

const summary = {
  store, workspace, k, total,
  hit_at_1: hit1, hit_at_k: hitK, errors,
  hit_at_1_share: Number((hit1 / total).toFixed(3)),
  hit_at_k_share: Number((hitK / total).toFixed(3)),
}
if (useJudge) {
  summary.judged_at_1 = judged1
  summary.judged_at_k = judgedK
  summary.judged_at_1_share = Number((judged1 / total).toFixed(3))
  summary.judged_at_k_share = Number((judgedK / total).toFixed(3))
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
  if (useJudge) {
    console.log('judged@1 ' + judged1 + '/' + total + ' (' + Math.round(summary.judged_at_1_share * 100) + '%) · ' +
      'judged@' + k + ' ' + judgedK + '/' + total + ' (' + Math.round(summary.judged_at_k_share * 100) + '%)')
  }
}

if (!allowMiss && (errors > 0 || hitK < total)) process.exit(1)
