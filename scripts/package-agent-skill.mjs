import { copyFileSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'

const name = 'organize-memory-topology'
const source = new URL('../.agents/skills/' + name + '/SKILL.md', import.meta.url)
const destination = join(process.cwd(), 'skills', name)
mkdirSync(destination, { recursive: true })
copyFileSync(source, join(destination, 'SKILL.md'))
