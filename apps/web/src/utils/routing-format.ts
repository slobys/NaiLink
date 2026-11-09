// Keep literals and comments opaque, including block comments and multiline strings.
// The bare token pattern follows the dae lexer: e.g. `#` and `!` can occur inside a name.
const ROUTING_TOKEN =
  /\/\*[\s\S]*?(?:\*\/|$)|#[^\r\n]*|"(?:[^"\\]|\\+[^\\])*(?:"|\\*$)|'(?:[^'\\]|\\+[^\\])*(?:'|\\*$)|->|&&|[\w/\\^*.+-][\w/\\^*.+=@$!#%-]*|\S/g
const HORIZONTAL_SPACE = /^[ \t]*$/

function spacingBetween(left: string, right: string, beforeLeft: string) {
  if (left.startsWith('/*') || right.startsWith('/*') || right.startsWith('#')) return
  if (left === ':') return beforeLeft === 'fallback' ? ' ' : ''
  if (left === ',' || left === '->' || right === '->' || left === '&&' || right === '&&') return ' '
  if (right === ':' || right === ',' || right === ')' || right === '(' || left === '(' || left === '!') return ''
}

export function routingFormatEdits(text: string, start = 0, end = text.length) {
  const edits: { start: number; end: number; text: string }[] = []
  let previousEnd = 0
  let left = ''
  let beforeLeft = ''

  // Scan from the start so a paste inside an existing string/comment stays untouched.
  for (const token of text.matchAll(ROUTING_TOKEN)) {
    const offset = token.index ?? 0
    if (offset >= end) break

    if (previousEnd > start) {
      const gap = text.slice(previousEnd, offset)
      if (HORIZONTAL_SPACE.test(gap)) {
        const spacing = spacingBetween(left, token[0], beforeLeft)
        if (spacing !== undefined && spacing !== gap) {
          edits.push({ start: previousEnd, end: offset, text: spacing })
        }
      }
    }
    previousEnd = offset + token[0].length
    beforeLeft = left
    left = token[0]
  }

  return edits
}
