import { describe, expect, it } from 'vitest'
import { routingFormatEdits } from './routing-format'

function format(source: string, start?: number, end?: number) {
  return routingFormatEdits(source, start, end).reduceRight(
    (text, edit) => text.slice(0, edit.start) + edit.text + text.slice(edit.end),
    source,
  )
}

describe('routing rule formatting', () => {
  it('keeps comments, literal values and rule boundaries intact', () => {
    const before = [
      '/*  do not format',
      'domain(fake.com)->direct',
      '*/',
      'domain(suffix: example.com,regexp:"a  b-># c",full:foo#bar)&&! pname(curl)->AI #  keep  this',
      "dip('2001:db8::/32')->direct",
      'fallback:proxy',
    ].join('\r\n')

    expect(format(before)).toBe(
      [
        '/*  do not format',
        'domain(fake.com)->direct',
        '*/',
        'domain(suffix:example.com, regexp:"a  b-># c", full:foo#bar) && !pname(curl) -> AI #  keep  this',
        "dip('2001:db8::/32') -> direct",
        'fallback: proxy',
      ].join('\r\n'),
    )
  })

  it('formats only the pasted range even when surrounding rules also need spacing', () => {
    const head = 'domain(example.com)->proxy\n'
    const paste = 'dport(80,443)->direct'
    const tail = '\nfallback:proxy'
    expect(format(`${head}${paste}${tail}`, head.length, head.length + paste.length))
      .toBe(`${head}dport(80, 443) -> direct${tail}`)

    const comment = '/* open comment\n'
    expect(format(`${comment}${paste}`, comment.length)).toBe(`${comment}${paste}`)
  })
})
