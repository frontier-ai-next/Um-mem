import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { registerProjectTools } from '../integrations/dsh/project-tools/isolated-project.js'

const tools = {}
const hooks = {}
const session = {}
const exec = { agent: { session } }
const root = mkdtempSync(join(tmpdir(), 'project-source-check-'))
registerProjectTools({
  systemPrompt: { section() {} },
  tools: { register(tool) { tools[tool.name] = tool } },
  on(name, callback) { hooks[name] = callback },
}, root, process.execPath)

const message = (source, content) => hooks['session/event'](session, {
  type: 'user/message', data: { source, content: [{ type: 'text', text: content }] },
})
message({ kind: 'user' }, 'Start at 08:15. Use 7 seats.')
message({ kind: 'runtime-context', sections: [{ name: 'dsh:um-mem-focus', text:
  '- Прежний источник (только неизменившиеся условия): Rendezvous on 4 August, at Gate 2. (id=mem_earlier)\n' +
  '- Заметка: Rendezvous on 5 August. (id=mem_note rev=rev_note)',
}] }, '')

const plan = (date, opens) => [{ path: 'plan.json', text: JSON.stringify({ date, opens, groups: [{ size: 7 }] }) }]
const checks = [{ path: 'plan.json', from: ['user', 'mem_earlier'],
  values: { date: '4 August', opens: '08:15', '/groups/0/size': 7 } }]
const verify = async (values, from) => JSON.parse(await tools.project_check.execute({
  checks: [{ path: 'plan.json', values, from }],
}, exec))[0]

const wrong = await tools.project_save.execute({ files: plan('04.08', 'gate 08:15'), verify: { checks } }, exec)
assert.deepEqual(JSON.parse(wrong.split('\n')[1])[0].unequal.map(row => row.key), ['date', 'opens'])
assert.deepEqual(JSON.parse(wrong.split('\n').at(-1).replace('Date check: ', ''))[0],
  { path: 'plan.json', value: '04.08', ref: 'mem_earlier', expected: '4 August', ok: false })
assert.deepEqual((await verify({ date: '04.08' }, ['mem_earlier'])).unbacked, ['date'])
assert.deepEqual((await verify({ date: '04.08' }, ['file:plan.json'])).unavailable, ['file:plan.json'])
assert.deepEqual((await verify({ date: '5 August' }, ['mem_note'])).unavailable, ['mem_note'])
const right = await tools.project_save.execute({ files: plan('4 August', '08:15'), verify: { checks } }, exec)
assert.equal(JSON.parse(right.split('\n')[1])[0].ok, true)
assert.match(await tools.project_open.execute({ paths: ['plan.json'] }, exec), /already shown/)
const intact = readFileSync(join(root, 'plan.json'), 'utf8')
const badWrite = await tools.project_save.execute({ files: [
  { path: 'plan.json', text: intact + '\nDate check: []' },
  { path: 'other.md', text: 'would be a partial write' },
] }, exec)
assert.match(badWrite, /No files changed\. Invalid JSON/)
assert.equal(readFileSync(join(root, 'plan.json'), 'utf8'), intact)
assert.throws(() => readFileSync(join(root, 'other.md'), 'utf8'), { code: 'ENOENT' })
const notes = []
const agent = { session, steer: message => notes.push(message) }
const sample = JSON.stringify({ shipments: [{ ticket: 'sample' }] })
const cli = 'import {readFileSync} from "node:fs";\n' +
  'const input=JSON.parse(readFileSync(0,"utf8"));\n' +
  'if(!Array.isArray(input?.shipments)){ console.error("Expected an array"); process.exit(2); }\n' +
  'try { console.log(JSON.stringify({results:input.shipments.map(row=>row.ticket)})); } catch(error){ console.error(error.message); process.exit(2); }'
await tools.project_save.execute({ files: [{ path: 'queue.mjs', text: cli }] }, exec)
const invoke = async () => JSON.parse(await tools.project_check.execute({ script: 'queue.mjs', stdin: sample }, exec))
const bad = await invoke()
assert.equal(bad.repairs.length, 1)
assert.match(bad.repairs[0].expected, /Keep processing valid rows/)
assert.deepEqual((await invoke()).repairs, bad.repairs)
hooks['agent/turn-stopping']({ agent })
hooks['agent/turn-stopping']({ agent })
assert.equal(notes.length, 1)
assert.equal(notes[0].source.kind, 'project-verification')
await tools.project_save.execute({ edits: [{ path: 'queue.mjs', old: 'row=>row.ticket', new: 'row=>row?.ticket ?? null' }] }, exec)
hooks['agent/turn-stopping']({ agent })
const fixed = await invoke()
assert.equal(fixed.repeated, true)
assert.equal(fixed.repairs, undefined)
console.log('Exact date proof, a single repair pass, and automatic replay after the fix passed.')
