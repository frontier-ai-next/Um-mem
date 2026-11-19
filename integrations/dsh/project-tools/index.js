import { registerProjectTools } from './isolated-project.js'

export const name = 'um-mem-project-tools'
export const inject = ['tools', 'systemPrompt']

export function apply(ctx, config = {}) {
  if (config.directory) return registerProjectTools(ctx, config.directory, config.node ?? process.execPath, config.sources)
}
