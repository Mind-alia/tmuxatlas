import { describe, expect, it } from 'vitest'
import {
  backspace,
  countLines,
  deleteLines,
  detectIndent,
  duplicateLines,
  enter,
  findMatches,
  indent,
  insertMarks,
  lineColumn,
  moveLines,
  outdent,
  replaceAllEdit,
  replacementFor,
  selectedLineRange,
  smartHome,
  toggleComment,
  typeCharacter,
  type Edit,
  type EditorState,
} from './editorCommands'

// Parses "a|b" (caret) or "a[b]c" (selection) into a state.
function st(marked: string): EditorState {
  const caret = marked.indexOf('|')
  if (caret !== -1) {
    const text = marked.slice(0, caret) + marked.slice(caret + 1)
    return { text, start: caret, end: caret }
  }
  const start = marked.indexOf('[')
  const end = marked.indexOf(']') - 1
  const text = marked.replace('[', '').replace(']', '')
  return { text, start, end }
}

// Applies an edit and renders the result with the same markers.
function apply(state: EditorState, edit: Edit | null): string {
  if (!edit) return 'null'
  const text = state.text.slice(0, edit.from) + edit.insert + state.text.slice(edit.to)
  if (edit.selStart === edit.selEnd) return text.slice(0, edit.selStart) + '|' + text.slice(edit.selStart)
  return text.slice(0, edit.selStart) + '[' + text.slice(edit.selStart, edit.selEnd) + ']' + text.slice(edit.selEnd)
}

const spaces2 = { tabSize: 2, insertSpaces: true }
const spaces4 = { tabSize: 4, insertSpaces: true }
const tabs = { tabSize: 4, insertSpaces: false }

describe('line helpers', () => {
  it('counts lines and maps offsets', () => {
    expect(countLines('')).toBe(1)
    expect(countLines('a\nb\n')).toBe(3)
    expect(lineColumn('ab\ncd', 4)).toEqual({ line: 2, column: 2 })
  })

  it('excludes a trailing line when the selection ends at column 0', () => {
    expect(selectedLineRange('aa\nbb\ncc', 0, 6)).toEqual({ from: 0, to: 5 })
    expect(selectedLineRange('aa\nbb\ncc', 4, 4)).toEqual({ from: 3, to: 5 })
  })
})

describe('indent / outdent', () => {
  it.each([
    ['caret inserts spaces to the next tab stop', 'a|b', spaces2, 'a |b'],
    ['caret at a tab stop inserts a full unit', 'ab|', spaces4, 'ab  |'],
    ['tabs mode inserts a tab', 'x|', tabs, 'x\t|'],
    ['single-line selection is replaced', 'a[bc]d', spaces2, 'a |d'],
  ])('%s', (_name, input, opts, expected) => {
    const s = st(input)
    expect(apply(s, indent(s, opts))).toBe(expected)
  })

  it('indents every selected line and skips blank lines', () => {
    const s = st('[one\n\ntwo]\nthree')
    expect(apply(s, indent(s, spaces2))).toBe('[  one\n\n  two]\nthree')
  })

  it('does not indent a line where the selection ends at column 0', () => {
    const s = st('[a\nb\n]c')
    expect(apply(s, indent(s, tabs))).toBe('[\ta\n\tb\n]c')
  })

  it('outdents the current line to the previous tab stop', () => {
    const s = st('     x|')
    expect(apply(s, outdent(s, spaces4))).toBe('    x|')
    const t = st('\t\tfoo|')
    expect(apply(t, outdent(t, tabs))).toBe('\tfoo|')
  })

  it('outdents a multi-line selection, leaving unindented lines alone', () => {
    const s = st('  [a\nb\n    c]')
    expect(apply(s, outdent(s, spaces2))).toBe('[a\nb\n  c]')
  })

  it('returns null when nothing can be outdented', () => {
    expect(outdent(st('abc|'), spaces2)).toBeNull()
  })
})

describe('detectIndent', () => {
  it('detects tabs', () => {
    expect(detectIndent('a\n\tb\n\tc\n\t\td')).toEqual({ insertSpaces: false, tabSize: 4 })
  })
  it('detects 4 spaces', () => {
    expect(detectIndent('def f():\n    if x:\n        y\n    z\n')).toEqual({ insertSpaces: true, tabSize: 4 })
  })
  it('detects 2 spaces', () => {
    expect(detectIndent('{\n  "a": {\n    "b": 1\n  }\n}')).toEqual({ insertSpaces: true, tabSize: 2 })
  })
  it('defaults to 2 spaces', () => {
    expect(detectIndent('plain text')).toEqual({ insertSpaces: true, tabSize: 2 })
    expect(detectIndent('')).toEqual({ insertSpaces: true, tabSize: 2 })
  })
})

describe('enter', () => {
  it.each([
    ['copies the leading whitespace', '    foo|', null, '    foo\n    |'],
    ['indents after {', 'if (x) {|', null, 'if (x) {\n  |'],
    ['indents after =>', 'const f = () =>|', null, 'const f = () =>\n  |'],
    ['indents after : in python', 'def f():|', 'python', 'def f():\n  |'],
    ['does not indent after : elsewhere', 'label:|', 'javascript', 'label:\n|'],
    ['expands a bracket pair', '  f({|})', null, '  f({\n    |\n  })'],
    ['expands brackets with spaces inside', 'x = [ | ]', null, 'x = [\n  |\n]'],
    ['replaces the selection', 'a[bc]d', null, 'a\n|d'],
  ])('%s', (_name, input, language, expected) => {
    const s = st(input)
    expect(apply(s, enter(s, { ...spaces2, language }))).toBe(expected)
  })

  it('uses tabs when configured', () => {
    const s = st('\tfn() {|}')
    expect(apply(s, enter(s, { ...tabs, language: 'go' }))).toBe('\tfn() {\n\t\t|\n\t}')
  })

  describe('markdown lists', () => {
    const md = { ...spaces2, language: 'markdown' }
    it.each([
      ['continues a dash list', '- item|', '- item\n- |'],
      ['continues a star list with indent', '  * item|', '  * item\n  * |'],
      ['increments numbered lists', '1. one|', '1. one\n2. |'],
      ['increments parenthesis lists', '9) nine|', '9) nine\n10) |'],
      ['continues task lists unchecked', '- [x] done|', '- [x] done\n- [ ] |'],
      ['ends the list on an empty item', 'a\n- |', 'a\n|'],
      ['ends an empty task item', '- [ ] |', '|'],
      ['ends an empty numbered item', '3. |', '|'],
    ])('%s', (_name, input, expected) => {
      const s = st(input)
      expect(apply(s, enter(s, md))).toBe(expected)
    })

    it('does not treat emphasis as a list', () => {
      const s = st('**bold**|')
      expect(apply(s, enter(s, md))).toBe('**bold**\n|')
    })
  })
})

describe('backspace', () => {
  it('deletes back to the previous tab stop in leading spaces', () => {
    const s = st('      |x')
    expect(apply(s, backspace(s, spaces4))).toBe('    |x')
    const t = st('    |')
    expect(apply(t, backspace(t, spaces4))).toBe('|')
  })

  it('leaves single characters and non-indent text to the browser', () => {
    expect(backspace(st(' |x'), spaces4)).toBeNull()
    expect(backspace(st('a   |'), spaces4)).toBeNull()
    expect(backspace(st('    |'), tabs)).toBeNull()
    expect(backspace(st('|abc'), spaces2)).toBeNull()
    expect(backspace(st('[ab]'), spaces2)).toBeNull()
  })

  it('deletes an empty pair', () => {
    for (const pair of ['()', '[]', '{}', '""', "''", '``']) {
      const s = st(`x${pair[0]}|${pair[1]}y`)
      expect(apply(s, backspace(s, spaces2))).toBe('x|y')
    }
  })
})

describe('typeCharacter (auto-closing pairs)', () => {
  it.each([
    ['closes a paren at the end', 'f|', '(', 'f(|)'],
    ['closes before whitespace', '|  x', '{', '{|}  x'],
    ['closes before a closer', '(|)', '[', '([|])'],
    ['does not close before a word', '|abc', '(', 'null'],
    ['closes quotes after a space', 'a |', '"', 'a "|"'],
    ['does not close a quote after a word char', "it|", "'", 'null'],
    ['does not close a quote after an accented letter', 'é|', "'", 'null'],
    ['steps over an existing closer', 'f(a|)', ')', 'f(a)|'],
    ['steps over an existing quote', '"abc|"', '"', '"abc"|'],
    ['wraps the selection in brackets', 'x [abc] y', '(', 'x ([abc]) y'],
    ['wraps the selection in quotes', '[word]', '`', '`[word]`'],
    ['leaves other characters alone', 'a|', 'b', 'null'],
    ['leaves a closer with a selection to the browser', '[a]', ')', 'null'],
  ])('%s', (_name, input, ch, expected) => {
    const s = st(input)
    expect(apply(s, typeCharacter(s, ch))).toBe(expected)
  })
})

describe('toggleComment', () => {
  const js = { line: '//' }
  it('comments the current line at its indent', () => {
    const s = st('  foo|()')
    expect(apply(s, toggleComment(s, js))).toBe('  // foo|()')
  })

  it('uncomments the current line', () => {
    const s = st('  // foo|()')
    expect(apply(s, toggleComment(s, js))).toBe('  foo|()')
    const t = st('#x|')
    expect(apply(t, toggleComment(t, { line: '#' }))).toBe('x|')
  })

  it('comments selected lines at the minimum indent, skipping blank lines', () => {
    const s = st('[  a\n\n    b]')
    expect(apply(s, toggleComment(s, js))).toBe('[  // a\n\n  //   b]')
  })

  it('handles mixed indentation', () => {
    const s = st('[\tif x\n\t\ty]')
    expect(apply(s, toggleComment(s, { line: '#' }))).toBe('[\t# if x\n\t# \ty]')
  })

  it('uncomments when every non-blank line is commented', () => {
    const s = st('[// a\n  //b\n]c')
    expect(apply(s, toggleComment(s, js))).toBe('[a\n  b\n]c')
  })

  it('comments all lines when only some are commented', () => {
    const s = st('[// a\nb]')
    expect(apply(s, toggleComment(s, js))).toBe('[// // a\n// b]')
  })

  it('wraps lines in a block comment when there is no line token', () => {
    const html = { block: ['<!--', '-->'] as [string, string] }
    const s = st('  <p>|hi</p>')
    expect(apply(s, toggleComment(s, html))).toBe('  <!-- <p>|hi</p> -->')
    const t = st('  <!-- <p>|hi</p> -->')
    expect(apply(t, toggleComment(t, html))).toBe('  <p>|hi</p>')
  })

  it('wraps multiple lines in a css block comment', () => {
    const css = { block: ['/*', '*/'] as [string, string] }
    const s = st('[a {\n  color: red;\n}]')
    expect(apply(s, toggleComment(s, css))).toBe('/* [a {\n  color: red;\n}] */')
  })

  it('returns null without a comment token', () => {
    expect(toggleComment(st('a|'), {})).toBeNull()
  })
})

describe('line operations', () => {
  it('duplicates the current line below and moves the caret', () => {
    const s = st('a\nb|c\nd')
    expect(apply(s, duplicateLines(s))).toBe('a\nbc\nb|c\nd')
  })

  it('duplicates a multi-line selection', () => {
    const s = st('[a\nb]')
    expect(apply(s, duplicateLines(s))).toBe('a\nb\n[a\nb]')
  })

  it('moves lines up and down', () => {
    const s = st('one\ntw|o\nthree')
    expect(apply(s, moveLines(s, 'up'))).toBe('tw|o\none\nthree')
    expect(apply(s, moveLines(s, 'down'))).toBe('one\nthree\ntw|o')
  })

  it('moves a selection block', () => {
    const s = st('x\n[a\nb]\ny')
    expect(apply(s, moveLines(s, 'down'))).toBe('x\ny\n[a\nb]')
    expect(apply(s, moveLines(s, 'up'))).toBe('[a\nb]\nx\ny')
  })

  it('does not move past the edges', () => {
    expect(moveLines(st('a|\nb'), 'up')).toBeNull()
    expect(moveLines(st('a\nb|'), 'down')).toBeNull()
  })

  it('deletes the current line keeping the column', () => {
    const s = st('abc\nde|f\nghi')
    expect(apply(s, deleteLines(s))).toBe('abc\ngh|i')
  })

  it('deletes the last line', () => {
    const s = st('abc\nde|f')
    expect(apply(s, deleteLines(s))).toBe('|abc')
    const t = st('on|ly')
    expect(apply(t, deleteLines(t))).toBe('|')
  })

  it('deletes selected lines', () => {
    const s = st('a\n[b\nc]\nd')
    expect(apply(s, deleteLines(s))).toBe('a\n|d')
  })

  it('smart home toggles between first non-blank and column 0', () => {
    const text = 'x\n    foo'
    expect(smartHome(text, 9)).toBe(6)
    expect(smartHome(text, 6)).toBe(2)
    expect(smartHome(text, 2)).toBe(6)
    expect(smartHome('abc', 2)).toBe(0)
  })
})

describe('find / replace', () => {
  const text = 'Foo foo food\nFOO bar'

  it('finds case-insensitive by default', () => {
    expect(findMatches(text, 'foo', {}).matches.map((m) => m.start)).toEqual([0, 4, 8, 13])
  })

  it('respects case sensitivity', () => {
    expect(findMatches(text, 'foo', { caseSensitive: true }).matches.map((m) => m.start)).toEqual([4, 8])
  })

  it('matches whole words only', () => {
    expect(findMatches(text, 'foo', { wholeWord: true }).matches.map((m) => m.start)).toEqual([0, 4, 13])
    expect(findMatches('été étés', 'été', { wholeWord: true }).matches).toEqual([{ start: 0, end: 3 }])
  })

  it('treats special characters literally without regex', () => {
    expect(findMatches('a.b axb', 'a.b', {}).matches).toEqual([{ start: 0, end: 3 }])
  })

  it('supports regular expressions', () => {
    expect(findMatches(text, 'fo+d?', { regex: true, caseSensitive: true }).matches).toEqual([
      { start: 4, end: 7 },
      { start: 8, end: 12 },
    ])
    expect(findMatches(text, '^\\w+', { regex: true }).matches.map((m) => m.start)).toEqual([0, 13])
  })

  it('skips empty matches and reports invalid regexes', () => {
    expect(findMatches('abc', 'x*', { regex: true }).matches).toEqual([])
    const bad = findMatches('abc', '(', { regex: true })
    expect(bad.matches).toEqual([])
    expect(bad.error).toBeTruthy()
    expect(findMatches('abc', '', {}).matches).toEqual([])
  })

  it('truncates at the limit', () => {
    const r = findMatches('aaaa', 'a', {}, 2)
    expect(r.matches).toHaveLength(2)
    expect(r.truncated).toBe(true)
  })

  it('expands regex replacements for a single match', () => {
    const t = 'key=value'
    const m = findMatches(t, '(\\w+)=(\\w+)', { regex: true }).matches[0]
    expect(replacementFor(t, m, '(\\w+)=(\\w+)', '$2:$1 ($&) $$', { regex: true })).toBe('value:key (key=value) $')
    expect(replacementFor(t, m, 'key', '$1', {})).toBe('$1')
  })

  it('replaces all matches within the minimal range', () => {
    const r = replaceAllEdit('x foo y foo z', 'foo', 'bar', {})!
    expect(r.count).toBe(2)
    expect(r.edit).toMatchObject({ from: 2, to: 11, insert: 'bar y bar' })
  })

  it('replaces all with regex groups and named groups', () => {
    const r = replaceAllEdit('a1 b2', '(?<l>[a-z])(\\d)', '$2$<l>', { regex: true })!
    expect(r.edit.insert).toBe('1a 2b')
    expect(replaceAllEdit('abc', 'z', 'y', {})).toBeNull()
    expect(replaceAllEdit('a$b', 'b', '$&', {})!.edit.insert).toBe('$&')
  })
})

describe('insertMarks', () => {
  it('wraps plain text ranges', () => {
    expect(insertMarks('hello world', [{ start: 6, end: 11, current: true }])).toBe('hello <mark class="ce-cur">world</mark>')
  })

  it('counts entities as one character', () => {
    expect(insertMarks('a &lt; b', [{ start: 2, end: 3 }])).toBe('a <mark>&lt;</mark> b')
  })

  it('splits marks around tags to stay balanced', () => {
    const html = '<span class="k">const</span> x'
    const out = insertMarks(html, [{ start: 3, end: 7 }])
    expect(out).toBe('<span class="k">con<mark>st</mark></span><mark> x</mark>')
    const div = document.createElement('div')
    div.innerHTML = out
    expect(div.textContent).toBe('const x')
  })

  it('handles several ranges and no ranges', () => {
    expect(insertMarks('aXbXc', [{ start: 1, end: 2 }, { start: 3, end: 4 }])).toBe('a<mark>X</mark>b<mark>X</mark>c')
    expect(insertMarks('abc', [])).toBe('abc')
  })
})
