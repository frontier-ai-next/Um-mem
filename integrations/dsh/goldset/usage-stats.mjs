#!/usr/bin/env node
/**
 * What the memory actually did while you worked.
 *
 * Reads ~/.um-mem/plugin-events.log (written by the plugin) and reports recall
 * volume, empty recalls and episode capture. The gold set measures the ranking;
 * this measures whether memory helped in real turns.
 *
 * Usage: node usage-stats.mjs [--days 7] [--log <path>]
 */
import { readFileSync, existsSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'

const args = process.argv.slice(2)
const flag = (name, fallback) => {
  const index = args.indexOf('--' + name)
  if (index === -1) return fallback
  return args[index + 1] ?? fallback
}

const logPath = String(flag('log', join(homedir(), '.um-mem', 'plugin-events.log')))
const days = Number(flag('days', 7))
if (!existsSync(logPath)) {
  console.log('нет лога: ' + logPath)
  process.exit(0)
}

const since = Date.now() - days * 24 * 3600 * 1000
const events = []
for (const line of readFileSync(logPath, 'utf8').split('\n')) {
  if (!line.trim()) continue
  try {
    const event = JSON.parse(line)
    if (Date.parse(event.at) >= since) events.push(event)
  } catch {
    /* skip malformed line */
  }
}

const recalls = events.filter((event) => event.kind === 'recall')
const focus = recalls.filter((event) => event.source === 'focus')
const tool = recalls.filter((event) => event.source === 'tool')
const empty = recalls.filter((event) => !event.hits)
const episodes = events.filter((event) => event.kind === 'episode/write')
const skipped = events.filter((event) => event.kind === 'episode/skip')
const turnStops = events.filter((event) => event.kind === 'turn-stopping')

const share = (part, whole) => (whole ? Math.round((part / whole) * 100) + '%' : 'n/a')
const topQueries = new Map()
for (const event of empty) {
  const key = String(event.q || '').slice(0, 70)
  topQueries.set(key, (topQueries.get(key) || 0) + 1)
}

console.log('окно: последние ' + days + ' дн. · событий ' + events.length)
console.log('recall: всего ' + recalls.length + ' (фокус ' + focus.length + ', инструмент ' + tool.length + ')')
console.log('пустых recall: ' + empty.length + ' (' + share(empty.length, recalls.length) + ')')
console.log('turn-stopping: ' + turnStops.length + ' · эпизодов записано ' + episodes.length + ' · пропущено ' + skipped.length)
if (topQueries.size) {
  console.log('\nзапросы без результата:')
  for (const [query, count] of [...topQueries.entries()].sort((a, b) => b[1] - a[1]).slice(0, 10)) {
    console.log('  ' + count + '× ' + query)
  }
}
const skipReasons = new Map()
for (const event of skipped) {
  const key = String(event.reason || 'unknown')
  skipReasons.set(key, (skipReasons.get(key) || 0) + 1)
}
if (skipReasons.size) {
  console.log('\nпричины пропуска эпизода:')
  for (const [reason, count] of skipReasons) console.log('  ' + count + '× ' + reason)
}
