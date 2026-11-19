const months = new Map()
for (const locale of new Set([Intl.DateTimeFormat().resolvedOptions().locale, 'en', 'ru'])) {
  for (const options of [{ month: 'long' }, { month: 'long', day: 'numeric' }]) {
    const format = new Intl.DateTimeFormat(locale, { ...options, timeZone: 'UTC' })
    for (let month = 1; month <= 12; month++) {
      const name = format.formatToParts(Date.UTC(2000, month - 1, 1)).find(part => part.type === 'month').value
      months.set(name.toLowerCase(), month)
    }
  }
}

const names = [...months.keys()].filter(name => /^\p{L}+$/u.test(name)).sort((a, b) => b.length - a.length).join('|')
const dates = new RegExp(
  `(?<![\\p{L}\\p{N}])(?:([0-9]{1,2}) +(${names})(?:[ ,]+[0-9]{4}(?: +года)?)?|` +
  `(${names}) +([0-9]{1,2})(?:[ ,]+[0-9]{4})?|` +
  '[0-9]{4}-([0-9]{2})-([0-9]{2})|([0-9]{1,2})[./]([0-9]{1,2})(?:[./][0-9]{2,4})?)(?![\\p{L}\\p{N}])',
  'giu',
)

function dateLiterals(text) {
  return [...text.matchAll(dates)].flatMap(match => {
    const [value, dayFirst, namedMonth, monthFirst, namedDay, isoMonth, isoDay, shortDay, shortMonth] = match
    const named = namedMonth ?? monthFirst
    const month = named ? months.get(named.toLowerCase()) : Number(isoMonth ?? shortMonth)
    const day = Number(dayFirst ?? namedDay ?? isoDay ?? shortDay)
    if (month < 1 || month > 12 || day < 1 || day > 31) return []
    return [{ value, key: `${month}/${day}` }]
  })
}

export function compareDates(text, quotes) {
  const proof = new Map()
  for (const [ref, quote] of quotes ?? []) {
    for (const { value, key } of dateLiterals(quote)) {
      if (!proof.has(key)) proof.set(key, { ref, expected: value })
    }
  }
  const result = new Map()
  for (const { value, key } of dateLiterals(text)) {
    const source = proof.get(key)
    if (source) result.set(value, { value, ...source, ok: value === source.expected })
  }
  return [...result.values()]
}
