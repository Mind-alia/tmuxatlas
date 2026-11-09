// Pure text transforms for the CodeEditor. Every command takes the current
// text and selection and returns an Edit (a single range replacement plus the
// selection to restore afterwards), or null to let the browser handle the key.

export interface EditorState {
  text: string
  start: number
  end: number
}

export interface Edit {
  from: number
  to: number
  insert: string
  /** Selection in the text after the replacement. */
  selStart: number
  selEnd: number
}

export interface IndentOptions {
  tabSize: number
  insertSpaces: boolean
}

export interface EnterOptions extends IndentOptions {
  language?: string | null
}

export interface CommentToken {
  line?: string
  block?: [string, string]
}

const OPENERS: Record<string, string> = { '(': ')', '[': ']', '{': '}', '"': '"', "'": "'", '`': '`' }
const CLOSERS = new Set([')', ']', '}'])
const QUOTES = new Set(['"', "'", '`'])
const WORD_CHAR = /[\p{L}\p{N}_]/u

// ---------------------------------------------------------------------------
// Line helpers

export function lineStartAt(text: string, pos: number): number {
  return pos <= 0 ? 0 : text.lastIndexOf('\n', pos - 1) + 1
}

export function lineEndAt(text: string, pos: number): number {
  const i = text.indexOf('\n', pos)
  return i === -1 ? text.length : i
}

/** 0-based line index of `pos`. */
export function lineIndexAt(text: string, pos: number): number {
  let line = 0
  let i = text.indexOf('\n')
  while (i !== -1 && i < pos) {
    line++
    i = text.indexOf('\n', i + 1)
  }
  return line
}

/** Offset of the start of the 0-based line `line` (clamped). */
export function offsetOfLine(text: string, line: number): number {
  let pos = 0
  for (let n = 0; n < line; n++) {
    const i = text.indexOf('\n', pos)
    if (i === -1) return lineStartAt(text, text.length)
    pos = i + 1
  }
  return pos
}

export function countLines(text: string): number {
  let n = 1
  let i = text.indexOf('\n')
  while (i !== -1) {
    n++
    i = text.indexOf('\n', i + 1)
  }
  return n
}

/** Full lines touched by the selection. A selection ending at column 0 of a line excludes that line. */
export function selectedLineRange(text: string, start: number, end: number): { from: number; to: number } {
  let last = end
  if (end > start && lineStartAt(text, end) === end) last = end - 1
  return { from: lineStartAt(text, start), to: lineEndAt(text, Math.max(start, last)) }
}

function leadingWhitespace(line: string): string {
  return /^[ \t]*/.exec(line)![0]
}

/** Visual width of a string with tabs expanded. */
export function visualWidth(s: string, tabSize: number): number {
  let w = 0
  for (const ch of s) w = ch === '\t' ? w + tabSize - (w % tabSize) : w + 1
  return w
}

export function indentUnit(opts: IndentOptions): string {
  return opts.insertSpaces ? ' '.repeat(opts.tabSize) : '\t'
}

// One prefix splice per line: remove `remove` chars at `col` and insert `insert`.
interface LineSplice {
  col: number
  remove: number
  insert: string
}

function mapColumn(c: number, s: LineSplice | null, keepBefore: boolean): number {
  if (!s) return c
  if (c < s.col || (keepBefore && c === s.col && s.remove === 0)) return c
  if (c >= s.col + s.remove) return c - s.remove + s.insert.length
  return s.col + s.insert.length
}

// Applies one splice per selected line and maps the selection through it.
function spliceLines(
  state: EditorState,
  splice: (line: string, index: number) => LineSplice | null,
  keepStartBeforeInsert: boolean,
): Edit | null {
  const { text, start, end } = state
  const { from, to } = selectedLineRange(text, start, end)
  const lines = text.slice(from, to).split('\n')
  const splices = lines.map(splice)
  if (splices.every((s) => !s || (s.remove === 0 && s.insert === ''))) return null

  const mapPos = (pos: number, keepBefore: boolean): number => {
    if (pos < from) return pos
    if (pos > to) return pos + delta
    let lineStart = from
    let shift = 0
    for (let i = 0; i < lines.length; i++) {
      const lineEnd = lineStart + lines[i].length
      const s = splices[i]
      if (pos <= lineEnd) return lineStart + shift + mapColumn(pos - lineStart, s, keepBefore)
      if (s) shift += s.insert.length - s.remove
      lineStart = lineEnd + 1
    }
    return pos + shift
  }

  const out = lines.map((line, i) => {
    const s = splices[i]
    return s ? line.slice(0, s.col) + s.insert + line.slice(s.col + s.remove) : line
  })
  const insert = out.join('\n')
  const delta = insert.length - (to - from)
  const collapsed = start === end
  const selStart = mapPos(start, keepStartBeforeInsert && !collapsed)
  const selEnd = collapsed ? selStart : mapPos(end, false)
  return { from, to, insert, selStart, selEnd }
}

// ---------------------------------------------------------------------------
// Indentation

export function indent(state: EditorState, opts: IndentOptions): Edit {
  const { text, start, end } = state
  const multiLine = text.slice(start, end).includes('\n')
  if (!multiLine) {
    let insert = '\t'
    if (opts.insertSpaces) {
      const col = visualWidth(text.slice(lineStartAt(text, start), start), opts.tabSize)
      insert = ' '.repeat(opts.tabSize - (col % opts.tabSize))
    }
    const pos = start + insert.length
    return { from: start, to: end, insert, selStart: pos, selEnd: pos }
  }
  const unit = indentUnit(opts)
  return spliceLines(state, (line) => (line.trim() === '' ? null : { col: 0, remove: 0, insert: unit }), true)!
}

function outdentRemoval(line: string, tabSize: number): number {
  if (line.startsWith('\t')) return 1
  let n = 0
  while (n < line.length && line[n] === ' ') n++
  if (n === 0) return 0
  // Spaces followed by a tab within the first stop: remove both.
  if (n < tabSize && line[n] === '\t') return n + 1
  return Math.min(n, n % tabSize || tabSize)
}

export function outdent(state: EditorState, opts: IndentOptions): Edit | null {
  return spliceLines(
    state,
    (line) => {
      const remove = outdentRemoval(line, opts.tabSize)
      return remove ? { col: 0, remove, insert: '' } : null
    },
    false,
  )
}

/** Guesses the indentation style of a document. */
export function detectIndent(text: string): { insertSpaces: boolean; tabSize: number } {
  let tabs = 0
  let spaces = 0
  const deltas = new Map<number, number>()
  let prev = 0
  let scanned = 0
  let pos = 0
  while (pos <= text.length && scanned < 10000) {
    const nl = text.indexOf('\n', pos)
    const end = nl === -1 ? text.length : nl
    const line = text.slice(pos, end)
    pos = end + 1
    scanned++
    if (line.trim() === '') continue
    if (line[0] === '\t') {
      tabs++
      continue
    }
    let n = 0
    while (line[n] === ' ') n++
    if (n > 0 && line[n] !== '\t') spaces++
    const d = Math.abs(n - prev)
    if (d >= 2 && d <= 8) deltas.set(d, (deltas.get(d) ?? 0) + 1)
    prev = n
    if (nl === -1) break
  }
  if (tabs > spaces) return { insertSpaces: false, tabSize: 4 }
  let best = 2
  let bestCount = 0
  for (const size of [2, 4, 8, 3]) {
    const count = deltas.get(size) ?? 0
    if (count > bestCount) {
      best = size
      bestCount = count
    }
  }
  return { insertSpaces: true, tabSize: best }
}

// ---------------------------------------------------------------------------
// Enter

const LIST_ITEM = /^(\s*)([-*+]|(\d+)([.)]))(\s+)(\[[ xX]\]\s+)?/

function isMarkdown(lang: string | null | undefined): boolean {
  return lang === 'markdown' || lang === 'md' || lang === 'mdx'
}

export function enter(state: EditorState, opts: EnterOptions): Edit {
  const { text, start, end } = state
  const lineStart = lineStartAt(text, start)
  const before = text.slice(lineStart, start)
  const lineIndent = leadingWhitespace(before)

  if (isMarkdown(opts.language)) {
    const m = LIST_ITEM.exec(before)
    if (m) {
      const markerEnd = lineStart + m[0].length
      const rest = text.slice(markerEnd, lineEndAt(text, end))
      if (start === end && start >= markerEnd && rest.trim() === '' && text.slice(markerEnd, start).trim() === '') {
        // Empty list item: drop the marker and end the list.
        const to = lineEndAt(text, start)
        return { from: lineStart, to, insert: '', selStart: lineStart, selEnd: lineStart }
      }
      if (start >= markerEnd) {
        const bullet = m[3] !== undefined ? `${Number(m[3]) + 1}${m[4]}` : m[2]
        const insert = '\n' + m[1] + bullet + m[5] + (m[6] ? '[ ] ' : '')
        const pos = start + insert.length
        return { from: start, to: end, insert, selStart: pos, selEnd: pos }
      }
    }
  }

  const unit = indentUnit(opts)
  const trimmed = before.trimEnd()
  const last = trimmed.slice(-1)
  const colonIndents = opts.language === 'python' || opts.language === 'yaml'
  const opensBlock = last === '{' || last === '[' || last === '(' || trimmed.endsWith('=>') || (colonIndents && last === ':')

  // Caret between a bracket pair: expand into an indented block.
  const after = text.slice(end, lineEndAt(text, end))
  const afterTrim = after.replace(/^[ \t]+/, '')
  const pairClose = OPENERS[last]
  if (CLOSERS.has(pairClose ?? '') && afterTrim[0] === pairClose) {
    const skip = after.length - afterTrim.length
    const first = '\n' + lineIndent + unit
    const insert = first + '\n' + lineIndent
    const from = start - (before.length - trimmed.length)
    const pos = from + first.length
    return { from, to: end + skip, insert, selStart: pos, selEnd: pos }
  }

  const insert = '\n' + lineIndent + (opensBlock ? unit : '')
  const pos = start + insert.length
  return { from: start, to: end, insert, selStart: pos, selEnd: pos }
}

// ---------------------------------------------------------------------------
// Backspace

export function backspace(state: EditorState, opts: IndentOptions): Edit | null {
  const { text, start, end } = state
  if (start !== end || start === 0) return null
  const prev = text[start - 1]
  const next = text[start]
  if (OPENERS[prev] !== undefined && OPENERS[prev] === next) {
    return { from: start - 1, to: start + 1, insert: '', selStart: start - 1, selEnd: start - 1 }
  }
  if (!opts.insertSpaces) return null
  const before = text.slice(lineStartAt(text, start), start)
  if (before.length < 2 || !/^ +$/.test(before)) return null
  const remove = Math.min(before.length, before.length % opts.tabSize || opts.tabSize)
  if (remove < 2) return null
  return { from: start - remove, to: start, insert: '', selStart: start - remove, selEnd: start - remove }
}

// ---------------------------------------------------------------------------
// Auto-closing pairs

export function typeCharacter(state: EditorState, ch: string): Edit | null {
  const { text, start, end } = state
  const close = OPENERS[ch]
  const next = text[end]

  if (start !== end) {
    if (close === undefined) return null
    const inner = text.slice(start, end)
    return { from: start, to: end, insert: ch + inner + close, selStart: start + 1, selEnd: end + 1 }
  }

  // Typing the closer that is already next: step over it.
  if ((CLOSERS.has(ch) || QUOTES.has(ch)) && next === ch) {
    return { from: start, to: start, insert: '', selStart: start + 1, selEnd: start + 1 }
  }

  if (close === undefined) return null
  const nextOk = next === undefined || /\s/.test(next) || CLOSERS.has(next) || next === ',' || next === ';' || next === ':'
  if (!nextOk) return null
  if (QUOTES.has(ch)) {
    const prev = text[start - 1]
    if (prev !== undefined && (WORD_CHAR.test(prev) || prev === ch || prev === '\\')) return null
  }
  return { from: start, to: start, insert: ch + close, selStart: start + 1, selEnd: start + 1 }
}

// ---------------------------------------------------------------------------
// Comments

export function toggleComment(state: EditorState, token: CommentToken): Edit | null {
  if (token.line) return toggleLineComment(state, token.line)
  if (token.block) return toggleBlockComment(state, token.block)
  return null
}

function toggleLineComment(state: EditorState, tok: string): Edit | null {
  const { text, start, end } = state
  const { from, to } = selectedLineRange(text, start, end)
  const lines = text.slice(from, to).split('\n')
  const nonBlank = lines.filter((l) => l.trim() !== '')
  const candidates = nonBlank.length ? nonBlank : lines
  const commented = nonBlank.length > 0 && nonBlank.every((l) => l.trimStart().startsWith(tok))

  if (commented) {
    return spliceLines(
      state,
      (line) => {
        const ws = leadingWhitespace(line)
        if (!line.slice(ws.length).startsWith(tok)) return null
        const space = line[ws.length + tok.length] === ' ' ? 1 : 0
        return { col: ws.length, remove: tok.length + space, insert: '' }
      },
      false,
    )
  }
  const col = Math.min(...candidates.map((l) => leadingWhitespace(l).length))
  return spliceLines(
    state,
    (line) => (nonBlank.length && line.trim() === '' ? null : { col, remove: 0, insert: tok + ' ' }),
    true,
  )
}

function toggleBlockComment(state: EditorState, [open, close]: [string, string]): Edit | null {
  const { text, start, end } = state
  const { from, to } = selectedLineRange(text, start, end)
  const block = text.slice(from, to)
  const ws = leadingWhitespace(block)
  const body = block.slice(ws.length)
  const bodyTrim = body.trimEnd()
  const tail = body.length - bodyTrim.length
  const contentFrom = from + ws.length
  const contentTo = to - tail
  const clamp = (pos: number, lo: number, hi: number) => Math.min(Math.max(pos, lo), hi)

  if (bodyTrim.startsWith(open) && bodyTrim.endsWith(close) && bodyTrim.length >= open.length + close.length) {
    let inner = bodyTrim.slice(open.length, bodyTrim.length - close.length)
    const lead = inner.startsWith(' ') ? 1 : 0
    const trail = inner.length > lead && inner.endsWith(' ') ? 1 : 0
    inner = inner.slice(lead, inner.length - trail)
    const removedBefore = open.length + lead
    const mapPos = (pos: number) => {
      if (pos <= contentFrom) return pos
      if (pos >= contentTo) return pos - (bodyTrim.length - inner.length)
      return clamp(pos - removedBefore, contentFrom, contentFrom + inner.length)
    }
    return { from: contentFrom, to: contentTo, insert: inner, selStart: mapPos(start), selEnd: mapPos(end) }
  }

  const prefix = open + ' '
  const insert = prefix + text.slice(contentFrom, contentTo) + ' ' + close
  const mapPos = (pos: number) => {
    if (pos < contentFrom) return pos
    if (pos > contentTo) return pos + insert.length - (contentTo - contentFrom)
    return pos + prefix.length
  }
  return { from: contentFrom, to: contentTo, insert, selStart: mapPos(start), selEnd: mapPos(end) }
}

// ---------------------------------------------------------------------------
// Line operations

export function duplicateLines(state: EditorState): Edit {
  const { text, start, end } = state
  const { from, to } = selectedLineRange(text, start, end)
  const block = text.slice(from, to)
  const shift = block.length + 1
  return { from: to, to, insert: '\n' + block, selStart: start + shift, selEnd: end + shift }
}

export function moveLines(state: EditorState, direction: 'up' | 'down'): Edit | null {
  const { text, start, end } = state
  const { from, to } = selectedLineRange(text, start, end)
  const block = text.slice(from, to)
  if (direction === 'up') {
    if (from === 0) return null
    const prevStart = lineStartAt(text, from - 1)
    const prev = text.slice(prevStart, from - 1)
    const shift = prev.length + 1
    return { from: prevStart, to, insert: block + '\n' + prev, selStart: start - shift, selEnd: end - shift }
  }
  if (to >= text.length) return null
  const nextEnd = lineEndAt(text, to + 1)
  const next = text.slice(to + 1, nextEnd)
  const shift = next.length + 1
  return { from, to: nextEnd, insert: next + '\n' + block, selStart: start + shift, selEnd: end + shift }
}

export function deleteLines(state: EditorState): Edit {
  const { text, start, end } = state
  const { from, to } = selectedLineRange(text, start, end)
  if (to < text.length) {
    // Keep the caret column on the line that moves up.
    const col = start - lineStartAt(text, start)
    const nextLen = lineEndAt(text, to + 1) - (to + 1)
    const pos = from + Math.min(col, nextLen)
    return { from, to: to + 1, insert: '', selStart: pos, selEnd: pos }
  }
  const delFrom = from > 0 ? from - 1 : from
  const pos = lineStartAt(text, delFrom)
  return { from: delFrom, to, insert: '', selStart: pos, selEnd: pos }
}

/** Target of a smart Home press from `head`: first non-blank char, or column 0 when already there. */
export function smartHome(text: string, head: number): number {
  const lineStart = lineStartAt(text, head)
  const line = text.slice(lineStart, lineEndAt(text, head))
  const first = lineStart + leadingWhitespace(line).length
  return head === first ? lineStart : first
}

// ---------------------------------------------------------------------------
// Find / replace

export interface FindOptions {
  caseSensitive?: boolean
  regex?: boolean
  wholeWord?: boolean
}

export interface Match {
  start: number
  end: number
}

export interface FindResult {
  matches: Match[]
  error?: string
  /** True when the match list was cut at `limit`. */
  truncated?: boolean
}

export function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

export function buildSearchRegExp(query: string, opts: FindOptions): RegExp {
  let source = opts.regex ? query : escapeRegExp(query)
  if (opts.wholeWord) source = `(?<![\\p{L}\\p{N}_])(?:${source})(?![\\p{L}\\p{N}_])`
  const flags = 'gm' + (opts.caseSensitive ? '' : 'i') + (opts.wholeWord ? 'u' : '')
  return new RegExp(source, flags)
}

export function findMatches(text: string, query: string, opts: FindOptions, limit = 10000): FindResult {
  if (!query) return { matches: [] }
  let re: RegExp
  try {
    re = buildSearchRegExp(query, opts)
  } catch (err) {
    return { matches: [], error: err instanceof Error ? err.message : String(err) }
  }
  const matches: Match[] = []
  let m: RegExpExecArray | null
  while ((m = re.exec(text)) !== null) {
    if (m[0].length === 0) {
      re.lastIndex++
      if (re.lastIndex > text.length) break
      continue
    }
    matches.push({ start: m.index, end: m.index + m[0].length })
    if (matches.length >= limit) return { matches, truncated: true }
  }
  return { matches }
}

/** Expands `$&`, `$1`, `$<name>` and `$$` in a regex replacement for one match. */
export function expandReplacement(m: RegExpExecArray, replacement: string): string {
  return replacement.replace(/\$(\$|&|\d{1,2}|<([^>]*)>)/g, (all, tok: string, name?: string) => {
    if (tok === '$') return '$'
    if (tok === '&') return m[0]
    if (name !== undefined) return m.groups?.[name] ?? ''
    const n = Number(tok)
    if (n > 0 && n < m.length) return m[n] ?? ''
    // Two digits that are not a group: try one digit then a literal.
    if (tok.length === 2) {
      const one = Number(tok[0])
      if (one > 0 && one < m.length) return (m[one] ?? '') + tok[1]
    }
    return all
  })
}

/** Replacement string for the match at `match`, honouring regex substitutions. */
export function replacementFor(text: string, match: Match, query: string, replacement: string, opts: FindOptions): string {
  if (!opts.regex) return replacement
  const base = buildSearchRegExp(query, opts)
  const re = new RegExp(base.source, base.flags.replace('g', '') + 'y')
  re.lastIndex = match.start
  const m = re.exec(text)
  return m ? expandReplacement(m, replacement) : replacement
}

/** Replaces every match; returns the minimal changed range so the edit stays small. */
export function replaceAllEdit(text: string, query: string, replacement: string, opts: FindOptions): { edit: Edit; count: number } | null {
  const { matches, error } = findMatches(text, query, opts, Infinity)
  if (error || matches.length === 0) return null
  const re = buildSearchRegExp(query, opts)
  const next = text.replace(re, (...args: unknown[]) => {
    const whole = args[0] as string
    if (!opts.regex) return replacement
    if (whole.length === 0) return whole
    const last = args[args.length - 1]
    const hasGroups = typeof last === 'object' && last !== null
    const groupCount = args.length - (hasGroups ? 4 : 3)
    const m = Object.assign([whole, ...(args.slice(1, 1 + groupCount) as string[])], {
      index: args[1 + groupCount] as number,
      input: text,
      groups: hasGroups ? (last as Record<string, string>) : undefined,
    }) as unknown as RegExpExecArray
    return expandReplacement(m, replacement)
  })
  const from = matches[0].start
  const to = matches[matches.length - 1].end
  const insert = next.slice(from, next.length - (text.length - to))
  return { edit: { from, to, insert, selStart: from, selEnd: from + insert.length }, count: matches.length }
}

// ---------------------------------------------------------------------------
// Match decoration of highlighted HTML

export interface MarkRange {
  start: number
  end: number
  current?: boolean
}

/**
 * Wraps text ranges of a highlighted line (escaped HTML with balanced tags) in
 * <mark> elements. Marks are closed and reopened around tags so the output
 * stays balanced; entities count as one character.
 */
export function insertMarks(html: string, ranges: MarkRange[]): string {
  if (ranges.length === 0) return html
  let out = ''
  let textPos = 0
  let ri = 0
  let open: MarkRange | null = null
  let i = 0
  const openTag = (r: MarkRange) => (r.current ? '<mark class="ce-cur">' : '<mark>')
  while (i < html.length) {
    if (open && textPos >= open.end) {
      out += '</mark>'
      open = null
      ri++
    }
    const ch = html[i]
    if (ch === '<') {
      const close = html.indexOf('>', i)
      const tag = close === -1 ? html.slice(i) : html.slice(i, close + 1)
      out += open ? '</mark>' + tag + openTag(open) : tag
      i += tag.length
      continue
    }
    while (!open && ri < ranges.length && ranges[ri].end <= textPos) ri++
    if (!open && ri < ranges.length && textPos >= ranges[ri].start) {
      open = ranges[ri]
      out += openTag(open)
    }
    let len = 1
    if (ch === '&') {
      const semi = html.indexOf(';', i)
      if (semi > i && semi - i <= 10) len = semi - i + 1
    }
    out += html.slice(i, i + len)
    i += len
    textPos++
  }
  if (open) out += '</mark>'
  return out
}

/** 1-based line/column of `pos`. */
export function lineColumn(text: string, pos: number): { line: number; column: number } {
  return { line: lineIndexAt(text, pos) + 1, column: pos - lineStartAt(text, pos) + 1 }
}
