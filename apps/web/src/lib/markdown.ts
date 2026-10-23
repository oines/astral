import type { MarkdownIt } from 'markstream-vue'

function closingDelimiter(source: string, delimiter: string, start: number) {
  for (let index = source.indexOf(delimiter, start); index >= 0; index = source.indexOf(delimiter, index + delimiter.length)) {
    let backslashes = 0
    for (let previous = index - 1; source[previous] === '\\'; previous--) backslashes++
    if (backslashes % 2 === 0) return index
  }
  return -1
}

function numericFormula(value: string) {
  if (/\\[a-z]+/i.test(value)) return true
  const text = value.trim()
  if (!text || /[+\-*/=^_,]$/.test(text) || !/^[\d\s.,+\-*/=^_(){}[\]|<>!:a-z%]+$/i.test(text)) return false
  return (text.match(/[a-z]+/gi) || []).every(word => word.length === 1 || ['sin', 'cos', 'tan', 'log', 'ln', 'exp', 'pi', 'lim', 'max', 'min', 'sqrt'].includes(word))
}

// Protect unambiguous dollar amounts before the renderer’s eager math rule can
// pair a price with the next price. Work on inline source only; stored messages
// and copy/export text remain unchanged.
export function protectCurrency(source: string) {
  let result = ''
  for (let index = 0; index < source.length;) {
    const url = source.startsWith('http', index) ? source.slice(index).match(/^https?:\/\/[^\s<>]+/) : undefined
    if (url) { result += url[0]; index += url[0].length; continue }
    if (source[index] === '`') {
      const marker = source.slice(index).match(/^`+/)![0]
      let end = source.indexOf(marker, index + marker.length)
      while (end >= 0 && (source[end - 1] === '`' || source[end + marker.length] === '`')) end = source.indexOf(marker, end + marker.length)
      const next = end < 0 ? source.length : end + marker.length
      result += source.slice(index, next); index = next; continue
    }
    if (source[index] === '\\') {
      const marker = source[index + 1] === '(' ? '\\)' : source[index + 1] === '[' ? '\\]' : ''
      const end = marker ? closingDelimiter(source, marker, index + 2) : -1
      const next = end >= 0 ? end + 2 : Math.min(source.length, index + 2)
      result += source.slice(index, next); index = next; continue
    }
    if (source.startsWith('](', index)) {
      let next = index + 2
      let depth = 1
      for (; next < source.length && depth; next++) {
        if (source[next] === '\\') next++
        else if (source[next] === '(') depth++
        else if (source[next] === ')') depth--
      }
      result += source.slice(index, next); index = next; continue
    }
    if (source[index] !== '$') { result += source[index++]; continue }
    const rest = source.slice(index)
    const marker = rest.startsWith('$$') ? '$$' : '$'
    const end = closingDelimiter(source, marker, index + marker.length)
    const amount = /^\$[+-]?\d/.test(rest)
    const formula = end >= 0 && !/\d/.test(source[end + 1] || '') && numericFormula(source.slice(index + 1, end))
    if (marker === '$' && amount && !formula) { result += '\\$'; index++; continue }
    const next = end < 0 ? source.length : end + marker.length
    result += source.slice(index, next); index = next
  }
  return result
}

const configured = new WeakSet<MarkdownIt>()

export function configureMarkdown(md: MarkdownIt) {
  if (configured.has(md)) return md
  configured.add(md)
  md.core.ruler.before('inline', 'astral_currency', (state: { tokens: Array<{ type: string; content: string }> }) => {
    for (const token of state.tokens) if (token.type === 'inline') token.content = protectCurrency(token.content)
  })
  return md
}
