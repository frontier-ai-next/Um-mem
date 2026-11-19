const readJSON = text => { try { return JSON.parse(text) } catch { return undefined } }

function responseArrays(value, path = []) {
  if (Array.isArray(value)) return [{ path, rows: value }]
  if (!value || typeof value !== 'object') return []
  return Object.entries(value).flatMap(([key, child]) => responseArrays(child, [...path, key]))
}

export function probeJSON(invoke, stdin, result, deadline) {
  const input = readJSON(stdin)
  const probes = [], repairs = []
  if (result.status !== 0 || !input || typeof input !== 'object') return { probes, repairs }
  const arrays = Array.isArray(input) ? [['$', input]] : Object.entries(input).filter(([, value]) => Array.isArray(value))
  const normal = responseArrays(readJSON(result.stdout))
  for (const [path, items] of arrays) {
    const withValue = value => JSON.stringify(Array.isArray(input) ? value : { ...input, [path]: value })
    for (const [variant, value] of [['null container', null], ['null row beside input', [null, ...items]]]) {
      if (Date.now() >= deadline) return { probes, repairs }
      const run = invoke(withValue(value))
      const output = (run.stderr || run.stdout || run.error?.message || '').trim().slice(0, 280)
      probes.push({ path, variant, exit: run.status, output })
      if (value === null || run.status === 0) continue

      const crashes = /(?:Cannot (?:read|set) properties of null|Cannot destructure[^\n]*null|\bTypeError\b)/iu.test(output)
      const candidate = items.length > 0 && normal.some(view => view.rows.length === items.length)
      let mapped = false
      if (candidate && Date.now() < deadline) {
        const empty = invoke(withValue([]))
        const emptyOutput = readJSON(empty.stdout)
        mapped = empty.status === 0 && normal.some(({ path, rows }) => {
          const reduced = path.reduce((value, key) => value?.[key], emptyOutput)
          return rows.length === items.length && Array.isArray(reduced) && reduced.length === 0
        })
      }
      if (crashes || mapped) repairs.push({ path, variant, exit: run.status, diagnostic: output,
        expected: mapped ? 'Keep processing valid rows in this batch. Handle the malformed row according to the input contract.'
          : 'Handle malformed input explicitly before dereferencing it. Preserve the documented error behavior.' })
    }
  }
  return { probes, repairs }
}
