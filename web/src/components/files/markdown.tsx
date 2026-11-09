import { Fragment, useEffect, useMemo, useRef, useState, type JSX, type MouseEvent, type ReactNode } from 'react'
import { cn } from '../../lib/utils'
import { rawURL, resolvePath } from './filesApi'
import { highlightToHTML, languageFor } from './highlight'

// ---------------------------------------------------------------------------
// AST

export type Align = 'left' | 'center' | 'right' | null

export type Inline =
  | { type: 'text'; text: string }
  | { type: 'code'; text: string }
  | { type: 'em' | 'strong' | 'del'; children: Inline[] }
  | { type: 'link'; href: string; title: string | null; children: Inline[] }
  | { type: 'image'; src: string; alt: string; title: string | null }
  | { type: 'html'; text: string }
  | { type: 'br' }
  | { type: 'softbreak' }
  | { type: 'footnoteRef'; label: string; index: number; first: boolean }

export interface ListItem {
  checked: boolean | null
  children: Block[]
}

export interface FootnoteItem {
  label: string
  index: number
  children: Block[]
}

export type Block =
  | { type: 'heading'; level: 1 | 2 | 3 | 4 | 5 | 6; content: Inline[]; id: string }
  | { type: 'paragraph'; content: Inline[] }
  | { type: 'code'; info: string; code: string }
  | { type: 'frontmatter'; code: string }
  | { type: 'blockquote'; children: Block[] }
  | { type: 'list'; ordered: boolean; start: number; loose: boolean; items: ListItem[] }
  | { type: 'table'; align: Align[]; header: Inline[][]; rows: Inline[][][] }
  | { type: 'hr' }
  | { type: 'html'; text: string }
  | { type: 'footnotes'; items: FootnoteItem[] }

// ---------------------------------------------------------------------------
// Text helpers

const MAX_DEPTH = 24

const ENTITIES: Record<string, string> = {
  amp: '&', lt: '<', gt: '>', quot: '"', apos: "'", nbsp: '\u00a0', ensp: '\u2002', emsp: '\u2003',
  thinsp: '\u2009', copy: '©', reg: '®', trade: '™', hellip: '…', mdash: '—', ndash: '–', minus: '−',
  laquo: '«', raquo: '»', lsaquo: '‹', rsaquo: '›', lsquo: '‘', rsquo: '’', sbquo: '‚', ldquo: '“',
  rdquo: '”', bdquo: '„', bull: '•', middot: '·', deg: '°', plusmn: '±', times: '×', divide: '÷',
  euro: '€', pound: '£', yen: '¥', cent: '¢', curren: '¤', sect: '§', para: '¶', shy: '\u00ad',
  larr: '←', rarr: '→', uarr: '↑', darr: '↓', harr: '↔', lArr: '⇐', rArr: '⇒', hArr: '⇔',
  le: '≤', ge: '≥', ne: '≠', asymp: '≈', infin: '∞', sum: '∑', prod: '∏', radic: '√', micro: 'µ',
  frac12: '½', frac14: '¼', frac34: '¾', sup1: '¹', sup2: '²', sup3: '³', iexcl: '¡', iquest: '¿',
  dagger: '†', Dagger: '‡', permil: '‰', prime: '′', Prime: '″', star: '☆', check: '✓', hearts: '♥',
  alpha: 'α', beta: 'β', gamma: 'γ', delta: 'δ', lambda: 'λ', mu: 'μ', pi: 'π', sigma: 'σ', omega: 'ω',
  Delta: 'Δ', Omega: 'Ω', agrave: 'à', aacute: 'á', acirc: 'â', auml: 'ä', atilde: 'ã', aring: 'å',
  aelig: 'æ', ccedil: 'ç', egrave: 'è', eacute: 'é', ecirc: 'ê', euml: 'ë', igrave: 'ì', iacute: 'í',
  icirc: 'î', iuml: 'ï', ntilde: 'ñ', ograve: 'ò', oacute: 'ó', ocirc: 'ô', ouml: 'ö', otilde: 'õ',
  oslash: 'ø', oelig: 'œ', ugrave: 'ù', uacute: 'ú', ucirc: 'û', uuml: 'ü', yuml: 'ÿ', szlig: 'ß',
  Agrave: 'À', Aacute: 'Á', Acirc: 'Â', Auml: 'Ä', Ccedil: 'Ç', Egrave: 'È', Eacute: 'É', Ecirc: 'Ê',
  Euml: 'Ë', Icirc: 'Î', Iuml: 'Ï', Ocirc: 'Ô', Ouml: 'Ö', OElig: 'Œ', Ugrave: 'Ù', Ucirc: 'Û',
  Uuml: 'Ü', Ntilde: 'Ñ', zwj: '\u200d', zwnj: '\u200c',
}

const ENTITY_RE = /^&(?:#[xX]([0-9a-fA-F]{1,6})|#([0-9]{1,7})|([A-Za-z][A-Za-z0-9]{1,31}));/

/** Decodes the entity at the start of s; returns [text, length] or null. */
function matchEntity(s: string): [string, number] | null {
  const m = ENTITY_RE.exec(s)
  if (!m) return null
  if (m[3] !== undefined) {
    const value = ENTITIES[m[3]]
    return value === undefined ? null : [value, m[0].length]
  }
  const code = m[1] !== undefined ? parseInt(m[1], 16) : parseInt(m[2], 10)
  const valid = code > 0 && code <= 0x10ffff && (code < 0xd800 || code > 0xdfff)
  return [valid ? String.fromCodePoint(code) : '\ufffd', m[0].length]
}

const ASCII_PUNCT = '!"#$%&\'()*+,-./:;<=>?@[\\]^_`{|}~'
const isAsciiPunct = (c: string) => c.length === 1 && ASCII_PUNCT.includes(c)

/** Resolves backslash escapes and entities (link destinations, titles, info strings). */
function unescapeString(s: string): string {
  if (!s.includes('\\') && !s.includes('&')) return s
  let out = ''
  for (let i = 0; i < s.length; i++) {
    const c = s[i]
    if (c === '\\' && isAsciiPunct(s[i + 1] ?? '')) {
      out += s[++i]
    } else if (c === '&') {
      const ent = matchEntity(s.slice(i, i + 40))
      if (ent) {
        out += ent[0]
        i += ent[1] - 1
      } else out += c
    } else out += c
  }
  return out
}

const isBlank = (line: string) => !/\S/.test(line)

/** Indentation in columns (tabs advance to the next multiple of 4). */
function indentOf(line: string): number {
  let col = 0
  for (let i = 0; i < line.length; i++) {
    const c = line[i]
    if (c === ' ') col++
    else if (c === '\t') col += 4 - (col % 4)
    else break
  }
  return col
}

/** Removes the first `cols` columns of a line, splitting tabs when needed. */
function stripColumns(line: string, cols: number): string {
  let col = 0
  let i = 0
  while (i < line.length && col < cols) {
    col += line[i] === '\t' ? 4 - (col % 4) : 1
    i++
  }
  return (col > cols ? ' '.repeat(col - cols) : '') + line.slice(i)
}

/** GitHub-like heading slug (without deduplication). */
export function slugify(text: string): string {
  return text
    .trim()
    .toLowerCase()
    .replace(/[^\p{L}\p{M}\p{N}\s_-]/gu, '')
    .replace(/\s/g, '-')
}

const normalizeLabel = (label: string) => label.trim().replace(/\s+/g, ' ').toLowerCase()

function plainText(nodes: Inline[]): string {
  let out = ''
  for (const node of nodes) {
    switch (node.type) {
      case 'text':
      case 'code':
      case 'html':
        out += node.text
        break
      case 'image':
        out += node.alt
        break
      case 'br':
      case 'softbreak':
        out += ' '
        break
      case 'footnoteRef':
        break
      default:
        out += plainText(node.children)
    }
  }
  return out
}

// ---------------------------------------------------------------------------
// Block parser

interface Definition {
  href: string
  title: string | null
}

interface Context {
  refs: Map<string, Definition>
  footnoteDefs: Map<string, Block[]>
  footnoteOrder: Map<string, number>
  slugs: Map<string, number>
  /** Inline parsing runs after all reference definitions are known. */
  pending: Array<() => void>
}

const ATX_RE = /^ {0,3}(#{1,6})(?=[ \t]|$)/
const FENCE_RE = /^( {0,3})(`{3,}|~{3,})(.*)$/
const HR_RE = /^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$/
const SETEXT_RE = /^ {0,3}(=+|-+)[ \t]*$/
const QUOTE_RE = /^ {0,3}>/
const FOOTNOTE_DEF_RE = /^ {0,3}\[\^([^\]\s]{1,100})\]:[ \t]?/
const REF_DEF_RE =
  /^\[((?:[^\\[\]]|\\.){1,999})\]:[ \t]*(<[^<>\n]*>|\S+)(?:[ \t]+("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|\((?:[^()\\]|\\.)*\)))?[ \t]*$/
const HTML_BLOCK_TAGS = new Set(
  (
    'address article aside base basefont blockquote body caption center col colgroup dd details dialog dir div dl ' +
    'dt fieldset figcaption figure footer form frame frameset h1 h2 h3 h4 h5 h6 head header hr html iframe legend ' +
    'li link main menu menuitem nav noframes ol optgroup option p param picture search section summary table ' +
    'tbody td tfoot th thead title tr track ul'
  ).split(' '),
)
const HTML_RAW_TAGS = new Set(['script', 'pre', 'style', 'textarea'])

interface Marker {
  ordered: boolean
  bullet: string
  start: number
  /** Content column of the item. */
  width: number
  empty: boolean
  first: string
}

function listMarker(line: string): Marker | null {
  const indent = indentOf(line)
  if (indent > 3) return null
  const s = line.trimStart()
  const m = /^(?:([-+*])|(\d{1,9})([.)]))(?=[ \t]|$)/.exec(s)
  if (!m) return null
  const markerEnd = indent + m[0].length
  const afterMarker = stripColumns(line, markerEnd)
  const empty = isBlank(afterMarker)
  const spaces = indentOf(afterMarker)
  const width = empty || spaces >= 5 ? markerEnd + 1 : markerEnd + spaces
  return {
    ordered: !m[1],
    bullet: m[1] ?? m[3],
    start: m[2] ? parseInt(m[2], 10) : 1,
    width,
    empty,
    first: empty ? '' : stripColumns(line, width),
  }
}

function fenceStart(line: string) {
  const m = FENCE_RE.exec(line)
  if (!m) return null
  if (m[2][0] === '`' && m[3].includes('`')) return null
  return { indent: m[1].length, fence: m[2], info: m[3].trim() }
}

function isAtx(line: string) {
  return ATX_RE.test(line)
}

function htmlStart(line: string): { kind: 'raw' | 'comment' | 'other' | 'blank'; tag?: string } | null {
  if (indentOf(line) > 3) return null
  const s = line.trimStart()
  if (s[0] !== '<') return null
  if (s.startsWith('<!--')) return { kind: 'comment' }
  if (/^<[?!]/.test(s)) return { kind: 'blank' }
  const m = /^<\/?([A-Za-z][A-Za-z0-9-]*)(?=[\s/>]|$)/.exec(s)
  if (!m) return null
  const tag = m[1].toLowerCase()
  if (HTML_RAW_TAGS.has(tag) && s[1] !== '/') return { kind: 'raw', tag }
  if (HTML_BLOCK_TAGS.has(tag)) return { kind: 'blank' }
  // Any other tag only when it is alone on its line.
  if (/^<\/?[A-Za-z][A-Za-z0-9-]*(?:\s+[^<>]*)?\/?>\s*$/.test(s)) return { kind: 'other' }
  return null
}

function splitRow(line: string): string[] {
  let s = line.trim()
  if (s.startsWith('|')) s = s.slice(1)
  if (s.endsWith('|') && !s.endsWith('\\|')) s = s.slice(0, -1)
  const cells: string[] = []
  let cur = ''
  for (let i = 0; i < s.length; i++) {
    const c = s[i]
    if (c === '\\' && s[i + 1] === '|') {
      cur += '|'
      i++
    } else if (c === '|') {
      cells.push(cur.trim())
      cur = ''
    } else cur += c
  }
  cells.push(cur.trim())
  return cells
}

function delimiterRow(line: string): Align[] | null {
  if (!line.includes('-') || indentOf(line) > 3) return null
  if (!line.includes('|') && !/^\s*:?-+:?\s*$/.test(line)) return null
  const cells = splitRow(line)
  const align: Align[] = []
  for (const cell of cells) {
    if (!/^:?-+:?$/.test(cell)) return null
    const left = cell.startsWith(':')
    const right = cell.endsWith(':')
    align.push(left && right ? 'center' : right ? 'right' : left ? 'left' : null)
  }
  return align
}

function tableStart(lines: string[], i: number): Align[] | null {
  const head = lines[i]
  if (!head.includes('|') || i + 1 >= lines.length || indentOf(head) > 3) return null
  const align = delimiterRow(lines[i + 1])
  if (!align || splitRow(head).length !== align.length) return null
  return align
}

/** Whether a line starts a block that ends a paragraph or a lazy continuation. */
function interrupts(lines: string[], i: number): boolean {
  const line = lines[i]
  if (isBlank(line)) return true
  if (indentOf(line) > 3) return false
  if (isAtx(line) || fenceStart(line) || QUOTE_RE.test(line) || HR_RE.test(line)) return true
  const h = htmlStart(line)
  if (h && h.kind !== 'other') return true
  const m = listMarker(line)
  if (m && !m.empty && (!m.ordered || m.start === 1)) return true
  return tableStart(lines, i) !== null
}

/** Whether a collected line can be continued lazily (it likely ends a paragraph). */
function paragraphLike(line: string): boolean {
  return (
    !isBlank(line) &&
    indentOf(line) < 4 &&
    !isAtx(line) &&
    !fenceStart(line) &&
    !HR_RE.test(line) &&
    !QUOTE_RE.test(line) &&
    !line.includes('|')
  )
}

function parseBlocks(lines: string[], ctx: Context, depth: number): { blocks: Block[]; loose: boolean } {
  const blocks: Block[] = []
  const n = lines.length
  let i = 0
  let loose = false
  let sawBlank = false

  const push = (block: Block) => {
    if (sawBlank && blocks.length > 0) loose = true
    sawBlank = false
    blocks.push(block)
  }

  while (i < n) {
    const line = lines[i]

    if (isBlank(line)) {
      sawBlank = true
      i++
      continue
    }

    const indent = indentOf(line)

    // Indented code.
    if (indent >= 4) {
      const code: string[] = []
      while (i < n && (isBlank(lines[i]) || indentOf(lines[i]) >= 4)) {
        code.push(isBlank(lines[i]) ? stripColumns(lines[i], 4).replace(/^\s+$/, '') : stripColumns(lines[i], 4))
        i++
      }
      while (code.length && code[code.length - 1] === '') {
        code.pop()
        i--
      }
      push({ type: 'code', info: '', code: code.join('\n') })
      continue
    }

    // Fenced code.
    const fence = fenceStart(line)
    if (fence) {
      const code: string[] = []
      i++
      while (i < n) {
        const close = /^ {0,3}(`{3,}|~{3,})[ \t]*$/.exec(lines[i])
        if (close && close[1][0] === fence.fence[0] && close[1].length >= fence.fence.length) {
          i++
          break
        }
        code.push(stripColumns(lines[i], Math.min(fence.indent, indentOf(lines[i]))))
        i++
      }
      push({ type: 'code', info: unescapeString(fence.info), code: code.join('\n') })
      continue
    }

    // ATX heading.
    const atx = ATX_RE.exec(line)
    if (atx) {
      let text = line.slice(atx[0].length).trim()
      const closing = /(?:^|[ \t])#+$/.exec(text)
      if (closing) text = text.slice(0, closing.index).trim()
      push(heading(atx[1].length, text, ctx))
      i++
      continue
    }

    if (HR_RE.test(line)) {
      push({ type: 'hr' })
      i++
      continue
    }

    // Blockquote.
    if (QUOTE_RE.test(line) && depth < MAX_DEPTH) {
      const inner: string[] = []
      while (i < n) {
        const l = lines[i]
        if (QUOTE_RE.test(l)) {
          const rest = l.slice(l.indexOf('>') + 1)
          inner.push(rest[0] === ' ' ? rest.slice(1) : rest[0] === '\t' ? stripColumns(rest, 1) : rest)
          i++
        } else if (inner.length && paragraphLike(inner[inner.length - 1]) && !interrupts(lines, i)) {
          inner.push(l) // lazy continuation
          i++
        } else break
      }
      const block: Block = { type: 'blockquote', children: [] }
      push(block)
      block.children = parseBlocks(inner, ctx, depth + 1).blocks
      continue
    }

    // List.
    const first = listMarker(line)
    if (first && depth < MAX_DEPTH) {
      const list: Block & { type: 'list' } = {
        type: 'list',
        ordered: first.ordered,
        start: first.start,
        loose: false,
        items: [],
      }
      push(list)
      while (i < n) {
        const m = listMarker(lines[i])
        if (!m || m.ordered !== first.ordered || m.bullet !== first.bullet || HR_RE.test(lines[i])) break
        const itemLines = [m.first]
        i++
        while (i < n) {
          const l = lines[i]
          if (isBlank(l)) {
            if (m.empty && itemLines.length === 1) break
            itemLines.push('')
            i++
          } else if (indentOf(l) >= m.width) {
            itemLines.push(stripColumns(l, m.width))
            i++
          } else if (paragraphLike(itemLines[itemLines.length - 1]) && !interrupts(lines, i) && !listMarker(l)) {
            itemLines.push(l.trimStart()) // lazy continuation
            i++
          } else break
        }
        while (itemLines.length > 1 && isBlank(itemLines[itemLines.length - 1])) {
          itemLines.pop()
          i--
        }
        let checked: boolean | null = null
        const task = /^\[([ xX])\](?=[ \t]|$)/.exec(itemLines[0])
        if (task) {
          checked = task[1] !== ' '
          itemLines[0] = itemLines[0].slice(3).replace(/^[ \t]/, '')
        }
        const item: ListItem = { checked, children: [] }
        list.items.push(item)
        const parsed = parseBlocks(itemLines, ctx, depth + 1)
        item.children = parsed.blocks
        if (parsed.loose) list.loose = true

        let j = i
        while (j < n && isBlank(lines[j])) j++
        const next = j < n ? listMarker(lines[j]) : null
        if (!next || next.ordered !== first.ordered || next.bullet !== first.bullet || HR_RE.test(lines[j])) break
        if (j > i) list.loose = true
        i = j
      }
      continue
    }

    // Footnote definition.
    const fn = FOOTNOTE_DEF_RE.exec(line)
    if (fn && depth < MAX_DEPTH) {
      const inner = [line.slice(fn[0].length)]
      i++
      while (i < n) {
        const l = lines[i]
        if (isBlank(l)) {
          let j = i
          while (j < n && isBlank(lines[j])) j++
          if (j < n && indentOf(lines[j]) >= 4) {
            for (; i < j; i++) inner.push('')
          } else break
        } else if (indentOf(l) >= 4) {
          inner.push(stripColumns(l, 4))
          i++
        } else if (paragraphLike(inner[inner.length - 1]) && !interrupts(lines, i) && !FOOTNOTE_DEF_RE.test(l)) {
          inner.push(l)
          i++
        } else break
      }
      const label = normalizeLabel(fn[1])
      if (!ctx.footnoteDefs.has(label)) ctx.footnoteDefs.set(label, parseBlocks(inner, ctx, depth + 1).blocks)
      continue
    }

    // Raw HTML block: kept as literal text.
    const html = htmlStart(line)
    if (html) {
      const raw: string[] = []
      if (html.kind === 'raw' || html.kind === 'comment') {
        const end = html.kind === 'comment' ? '-->' : `</${html.tag}>`
        while (i < n) {
          raw.push(lines[i])
          i++
          if (raw[raw.length - 1].toLowerCase().includes(end)) break
        }
      } else {
        while (i < n && !isBlank(lines[i])) raw.push(lines[i++])
      }
      push({ type: 'html', text: raw.join('\n') })
      continue
    }

    // Table.
    const align = tableStart(lines, i)
    if (align) {
      const cols = align.length
      const headerCells = splitRow(line)
      const rowCells: string[][] = []
      i += 2
      while (i < n && !isBlank(lines[i]) && !(interrupts(lines, i) && !lines[i].includes('|'))) {
        if (QUOTE_RE.test(lines[i]) || fenceStart(lines[i]) || isAtx(lines[i])) break
        const cells = splitRow(lines[i])
        while (cells.length < cols) cells.push('')
        cells.length = cols
        rowCells.push(cells)
        i++
      }
      const table: Block & { type: 'table' } = { type: 'table', align, header: [], rows: [] }
      push(table)
      ctx.pending.push(() => {
        table.header = headerCells.map(c => parseInline(c, ctx))
        table.rows = rowCells.map(r => r.map(c => parseInline(c, ctx)))
      })
      continue
    }

    // Paragraph (with reference definitions and setext headings).
    const para: string[] = [line.trimStart()]
    i++
    let setext: 1 | 2 | 0 = 0
    while (i < n) {
      const l = lines[i]
      const sx = indentOf(l) < 4 ? SETEXT_RE.exec(l) : null
      if (sx) {
        setext = sx[1][0] === '=' ? 1 : 2
        i++
        break
      }
      if (interrupts(lines, i)) break
      para.push(l.trimStart())
      i++
    }
    let start = 0
    while (start < para.length) {
      const def = REF_DEF_RE.exec(para[start])
      if (!def) break
      const label = normalizeLabel(def[1])
      let href = def[2]
      if (href.startsWith('<')) href = href.slice(1, -1)
      if (label && !ctx.refs.has(label)) {
        ctx.refs.set(label, {
          href: unescapeString(href),
          title: def[3] ? unescapeString(def[3].slice(1, -1)) : null,
        })
      }
      start++
    }
    const text = para.slice(start).join('\n').replace(/[ \t]+$/, '')
    if (!text) {
      if (setext === 2) push({ type: 'hr' })
      else if (setext === 1) push(paragraph('=', ctx))
      continue
    }
    push(setext ? heading(setext, text, ctx) : paragraph(text, ctx))
  }

  return { blocks, loose }
}

function paragraph(text: string, ctx: Context): Block {
  const block: Block & { type: 'paragraph' } = { type: 'paragraph', content: [] }
  ctx.pending.push(() => {
    block.content = parseInline(text, ctx)
  })
  return block
}

function heading(level: number, text: string, ctx: Context): Block {
  const block: Block & { type: 'heading' } = {
    type: 'heading',
    level: level as 1 | 2 | 3 | 4 | 5 | 6,
    content: [],
    id: '',
  }
  ctx.pending.push(() => {
    block.content = parseInline(text, ctx)
    const base = slugify(plainText(block.content)) || 'section'
    const seen = ctx.slugs.get(base)
    let id = base
    if (seen !== undefined) {
      let k = seen
      do id = `${base}-${++k}`
      while (ctx.slugs.has(id))
      ctx.slugs.set(base, k)
    } else ctx.slugs.set(base, 0)
    if (id !== base) ctx.slugs.set(id, 0)
    block.id = id
  })
  return block
}

/** Parses Markdown into a block AST (inline content fully parsed). */
export function parseMarkdown(source: string): Block[] {
  const ctx: Context = {
    refs: new Map(),
    footnoteDefs: new Map(),
    footnoteOrder: new Map(),
    slugs: new Map(),
    pending: [],
  }
  const lines = source.replace(/\r\n?/g, '\n').replace(/\u0000/g, '\ufffd').split('\n')
  const blocks: Block[] = []
  let offset = 0

  // YAML front matter at the very top.
  if (/^---[ \t]*$/.test(lines[0] ?? '')) {
    let end = 1
    while (end < lines.length && !/^(?:---|\.\.\.)[ \t]*$/.test(lines[end])) end++
    if (end < lines.length && end > 1) {
      blocks.push({ type: 'frontmatter', code: lines.slice(1, end).join('\n') })
      offset = end + 1
    }
  }

  blocks.push(...parseBlocks(offset ? lines.slice(offset) : lines, ctx, 0).blocks)
  for (let k = 0; k < ctx.pending.length; k++) ctx.pending[k]()

  if (ctx.footnoteOrder.size) {
    const items = [...ctx.footnoteOrder].map(([label, index]) => ({
      label,
      index,
      children: ctx.footnoteDefs.get(label) ?? [],
    }))
    blocks.push({ type: 'footnotes', items })
  }
  return blocks
}

// ---------------------------------------------------------------------------
// Inline parser (CommonMark delimiter algorithm, simplified)

interface Item {
  prev: Item | null
  next: Item | null
  text: string
  node: Inline | null
}

interface Delim {
  item: Item
  char: string
  count: number
  orig: number
  canOpen: boolean
  canClose: boolean
}

interface Bracket {
  item: Item
  image: boolean
  active: boolean
  /** Source index right after the opening bracket. */
  start: number
  delimBottom: number
}

const isSpace = (c: string) => c === '' || /\s/.test(c)
const isPunct = (c: string) => c !== '' && /[\p{P}\p{S}]/u.test(c)

const AUTOLINK_RE = /^<([A-Za-z][A-Za-z0-9+.-]{1,31}:[^\s<>]*)>/
const EMAIL_RE = /^<([A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*)>/
const INLINE_HTML_RE =
  /^(?:<\/?[A-Za-z][A-Za-z0-9-]*(?:\s+[A-Za-z_:][\w.:-]*(?:\s*=\s*(?:[^\s"'=<>`]+|'[^']*'|"[^"]*"))?)*\s*\/?>|<!--[\s\S]*?-->)/

function parseInline(src: string, ctx: Context): Inline[] {
  const head: Item = { prev: null, next: null, text: '', node: null }
  let tail = head
  const delims: Delim[] = []
  const brackets: Bracket[] = []
  let buf = ''
  /** Backtick run lengths known to have no closer further on. */
  const noCloser = new Set<number>()

  const append = (item: Item) => {
    item.prev = tail
    tail.next = item
    tail = item
    return item
  }
  const flush = () => {
    if (buf) {
      append({ prev: null, next: null, text: buf, node: null })
      buf = ''
    }
  }
  const pushNode = (node: Inline) => {
    flush()
    append({ prev: null, next: null, text: '', node })
  }
  const remove = (item: Item) => {
    if (item.prev) item.prev.next = item.next
    if (item.next) item.next.prev = item.prev
    if (tail === item) tail = item.prev ?? head
  }
  /** Detaches the items strictly between `from` and `to` (null = end) as nodes. */
  const collect = (from: Item, to: Item | null): Inline[] => {
    const out: Inline[] = []
    let cur = from.next
    while (cur && cur !== to) {
      if (cur.node) out.push(cur.node)
      else if (cur.text) {
        const last = out[out.length - 1]
        if (last && last.type === 'text') last.text += cur.text
        else out.push({ type: 'text', text: cur.text })
      }
      cur = cur.next
    }
    from.next = to
    if (to) to.prev = from
    else tail = from
    return out
  }

  const processEmphasis = (bottom: number) => {
    const openersBottom = new Map<string, number>()
    for (let ci = bottom; ci < delims.length; ci++) {
      const closer = delims[ci]
      if (!closer.canClose || closer.count === 0) continue
      const key = `${closer.char}${closer.canOpen ? 1 : 0}${closer.orig % 3}`
      const lower = Math.max(bottom, openersBottom.get(key) ?? bottom)
      let oi = ci - 1
      for (; oi >= lower; oi--) {
        const op = delims[oi]
        if (op.count === 0 || op.char !== closer.char || !op.canOpen) continue
        if (closer.char === '~') {
          if (op.count === closer.count) break
          continue
        }
        if ((op.canClose || closer.canOpen) && (op.orig + closer.orig) % 3 === 0 && !(op.orig % 3 === 0 && closer.orig % 3 === 0))
          continue
        break
      }
      if (oi < lower) {
        openersBottom.set(key, ci)
        continue
      }
      const op = delims[oi]
      const use = closer.char === '~' ? closer.count : op.count >= 2 && closer.count >= 2 ? 2 : 1
      const type = closer.char === '~' ? 'del' : use === 2 ? 'strong' : 'em'
      op.count -= use
      closer.count -= use
      op.item.text = op.char.repeat(op.count)
      closer.item.text = closer.char.repeat(closer.count)
      const children = collect(op.item, closer.item)
      const wrapped: Item = { prev: op.item, next: closer.item, text: '', node: { type, children } }
      op.item.next = wrapped
      closer.item.prev = wrapped
      for (let k = oi + 1; k < ci; k++) delims[k].count = 0
      if (op.count === 0) remove(op.item)
      if (closer.count === 0) remove(closer.item)
      else ci-- // the closer may close another opener
    }
    delims.length = bottom
  }

  const parseDestination = (p: number): { href: string; title: string | null; end: number } | null => {
    // p points right after '('.
    let q = p
    const skipWs = () => {
      while (q < src.length && /[ \t\n]/.test(src[q])) q++
    }
    skipWs()
    let href = ''
    if (src[q] === '<') {
      const close = src.indexOf('>', q + 1)
      if (close < 0) return null
      const inner = src.slice(q + 1, close)
      if (/[\n<]/.test(inner)) return null
      href = inner
      q = close + 1
    } else {
      let depthParen = 0
      const begin = q
      while (q < src.length) {
        const c = src[q]
        if (c === '\\' && isAsciiPunct(src[q + 1] ?? '')) {
          q += 2
          continue
        }
        if (/\s/.test(c)) break
        if (c === '(') depthParen++
        else if (c === ')') {
          if (depthParen === 0) break
          depthParen--
        }
        q++
      }
      if (depthParen !== 0) return null
      href = src.slice(begin, q)
    }
    const beforeTitle = q
    skipWs()
    let title: string | null = null
    const open = src[q]
    if (q > beforeTitle && (open === '"' || open === "'" || open === '(')) {
      const closeChar = open === '(' ? ')' : open
      let r = q + 1
      while (r < src.length && src[r] !== closeChar) r += src[r] === '\\' ? 2 : 1
      if (r >= src.length) return null
      title = unescapeString(src.slice(q + 1, r))
      q = r + 1
      skipWs()
    }
    if (src[q] !== ')') return null
    return { href: unescapeString(href), title, end: q + 1 }
  }

  const closeBracket = (p: number): number => {
    // p points at ']'. Returns the new position.
    const opener = brackets[brackets.length - 1]
    if (!opener) {
      buf += ']'
      return p + 1
    }
    if (!opener.active) {
      brackets.pop()
      buf += ']'
      return p + 1
    }
    let target: Definition | null = null
    let end = p + 1
    if (src[p + 1] === '(') {
      const dest = parseDestination(p + 2)
      if (dest) {
        target = { href: dest.href, title: dest.title }
        end = dest.end
      }
    }
    if (!target) {
      const label = src.slice(opener.start, p)
      if (src[p + 1] === '[') {
        const close = src.indexOf(']', p + 2)
        const ref = close >= 0 ? src.slice(p + 2, close) : null
        if (ref !== null && !ref.includes('[') && ref.length < 1000) {
          const def = ctx.refs.get(normalizeLabel(ref || label))
          if (def) {
            target = def
            end = close + 1
          }
        }
      }
      if (!target && label.length < 1000) {
        const def = ctx.refs.get(normalizeLabel(label))
        if (def) {
          target = def
          end = p + 1
        }
      }
    }
    brackets.pop()
    if (!target) {
      buf += ']'
      return p + 1
    }
    flush()
    processEmphasis(opener.delimBottom)
    const children = collect(opener.item, null)
    remove(opener.item)
    if (opener.image) {
      pushNode({ type: 'image', src: target.href, alt: plainText(children), title: target.title })
    } else {
      pushNode({ type: 'link', href: target.href, title: target.title, children: unlink(children) })
      for (const b of brackets) if (!b.image) b.active = false
    }
    return end
  }

  let p = 0
  while (p < src.length) {
    const c = src[p]
    switch (c) {
      case '\\': {
        const next = src[p + 1] ?? ''
        if (next === '\n') {
          pushNode({ type: 'br' })
          p += 2
          while (src[p] === ' ' || src[p] === '\t') p++
        } else if (isAsciiPunct(next)) {
          buf += next
          p += 2
        } else {
          buf += '\\'
          p++
        }
        break
      }
      case '`': {
        let run = 1
        while (src[p + run] === '`') run++
        const fence = '`'.repeat(run)
        let search = p + run
        let close = -1
        while (!noCloser.has(run)) {
          const at = src.indexOf(fence, search)
          if (at < 0) break
          let after = at + run
          while (src[after] === '`') after++
          if (after - at === run) {
            close = at
            break
          }
          search = after
        }
        if (close < 0) {
          noCloser.add(run)
          buf += fence
          p += run
          break
        }
        let code = src.slice(p + run, close).replace(/\n/g, ' ')
        if (code.length > 2 && code[0] === ' ' && code[code.length - 1] === ' ' && /[^ ]/.test(code)) {
          code = code.slice(1, -1)
        }
        pushNode({ type: 'code', text: code })
        p = close + run
        break
      }
      case '*':
      case '_':
      case '~': {
        let run = 1
        while (src[p + run] === c) run++
        const before = p > 0 ? src[p - 1] : ''
        const after = src[p + run] ?? ''
        const left = !isSpace(after) && (!isPunct(after) || isSpace(before) || isPunct(before))
        const right = !isSpace(before) && (!isPunct(before) || isSpace(after) || isPunct(after))
        let canOpen = left
        let canClose = right
        if (c === '_') {
          canOpen = left && (!right || isPunct(before))
          canClose = right && (!left || isPunct(after))
        }
        if (c === '~' && run > 2) canOpen = canClose = false
        flush()
        const item = append({ prev: null, next: null, text: c.repeat(run), node: null })
        if (canOpen || canClose) delims.push({ item, char: c, count: run, orig: run, canOpen, canClose })
        p += run
        break
      }
      case '!':
      case '[': {
        if (c === '!' && src[p + 1] !== '[') {
          buf += c
          p++
          break
        }
        if (c === '[' && src[p + 1] === '^') {
          const m = /^\[\^([^\]\s]{1,100})\]/.exec(src.slice(p, p + 104))
          const label = m ? normalizeLabel(m[1]) : ''
          if (m && ctx.footnoteDefs.has(label)) {
            let index = ctx.footnoteOrder.get(label)
            const first = index === undefined
            if (index === undefined) {
              index = ctx.footnoteOrder.size + 1
              ctx.footnoteOrder.set(label, index)
            }
            pushNode({ type: 'footnoteRef', label, index, first })
            p += m[0].length
            break
          }
        }
        const image = c === '!'
        flush()
        const item = append({ prev: null, next: null, text: image ? '![' : '[', node: null })
        p += image ? 2 : 1
        brackets.push({ item, image, active: true, start: p, delimBottom: delims.length })
        break
      }
      case ']':
        flush()
        p = closeBracket(p)
        break
      case '<': {
        const rest = src.slice(p, p + 2048)
        const auto = AUTOLINK_RE.exec(rest)
        if (auto) {
          pushNode({ type: 'link', href: auto[1], title: null, children: [{ type: 'text', text: auto[1] }] })
          p += auto[0].length
          break
        }
        const email = EMAIL_RE.exec(rest)
        if (email) {
          pushNode({ type: 'link', href: `mailto:${email[1]}`, title: null, children: [{ type: 'text', text: email[1] }] })
          p += email[0].length
          break
        }
        const html = INLINE_HTML_RE.exec(rest)
        if (html) {
          if (/^<br\s*\/?>$/i.test(html[0])) pushNode({ type: 'br' })
          else pushNode({ type: 'html', text: html[0] })
          p += html[0].length
          break
        }
        buf += c
        p++
        break
      }
      case '&': {
        const ent = matchEntity(src.slice(p, p + 40))
        if (ent) {
          buf += ent[0]
          p += ent[1]
        } else {
          buf += c
          p++
        }
        break
      }
      case '\n': {
        const hard = / {2,}$/.test(buf)
        buf = buf.replace(/[ \t]+$/, '')
        pushNode(hard ? { type: 'br' } : { type: 'softbreak' })
        p++
        while (src[p] === ' ' || src[p] === '\t') p++
        break
      }
      default: {
        // Fast path: copy plain characters up to the next special one.
        let q = p + 1
        while (q < src.length && !'\\`*_~![]<&\n'.includes(src[q])) q++
        buf += src.slice(p, q)
        p = q
      }
    }
  }
  flush()
  processEmphasis(0)
  return linkify(collect(head, null))
}

/** Links cannot contain links: keep their text only. */
function unlink(nodes: Inline[]): Inline[] {
  const out: Inline[] = []
  for (const node of nodes) {
    if (node.type === 'link') out.push(...unlink(node.children))
    else if (node.type === 'em' || node.type === 'strong' || node.type === 'del')
      out.push({ ...node, children: unlink(node.children) })
    else out.push(node)
  }
  return out
}

const BARE_URL_RE = /(?:https?:\/\/|www\.)[^\s<]+/g

/** GFM autolink extension: bare http(s):// and www. URLs in text outside links. */
function linkify(nodes: Inline[]): Inline[] {
  const out: Inline[] = []
  for (const node of nodes) {
    if (node.type === 'em' || node.type === 'strong' || node.type === 'del') {
      out.push({ ...node, children: linkify(node.children) })
      continue
    }
    if (node.type !== 'text' || (!node.text.includes('://') && !node.text.includes('www.'))) {
      out.push(node)
      continue
    }
    const text = node.text
    let last = 0
    BARE_URL_RE.lastIndex = 0
    let m: RegExpExecArray | null
    while ((m = BARE_URL_RE.exec(text))) {
      const before = m.index > 0 ? text[m.index - 1] : ''
      if (before && !/[\s*_~(]/.test(before)) continue
      let url = m[0]
      for (;;) {
        const trimmed = url.replace(/[?!.,:*_~'"]+$/, '').replace(/&[A-Za-z0-9]+;$/, '')
        let next = trimmed
        if (next.endsWith(')')) {
          const opens = next.split('(').length - 1
          const closes = next.split(')').length - 1
          if (closes > opens) next = next.slice(0, -1)
        }
        if (next === url) break
        url = next
      }
      const minLength = url.startsWith('www.') ? 5 : url.indexOf('://') + 4
      if (url.length < minLength) continue
      if (m.index > last) out.push({ type: 'text', text: text.slice(last, m.index) })
      const href = url.startsWith('www.') ? `http://${url}` : url
      out.push({ type: 'link', href, title: null, children: [{ type: 'text', text: url }] })
      last = m.index + url.length
      BARE_URL_RE.lastIndex = last
    }
    if (last === 0) out.push(node)
    else if (last < text.length) out.push({ type: 'text', text: text.slice(last) })
  }
  return out
}

// ---------------------------------------------------------------------------
// URL policy

type LinkTarget =
  | { kind: 'external'; href: string; blank: boolean }
  | { kind: 'anchor'; id: string }
  | { kind: 'local'; path: string }
  | { kind: 'unsafe' }

function scheme(url: string): string | null {
  // Browsers ignore control characters and whitespace inside schemes.
  const compact = url.replace(/[\u0000-\u0020\u007f]/g, '')
  const m = /^([A-Za-z][A-Za-z0-9+.-]*):/.exec(compact)
  return m ? m[1].toLowerCase() : null
}

function localPath(url: string): string {
  const cut = url.search(/[?#]/)
  const path = cut >= 0 ? url.slice(0, cut) : url
  try {
    return decodeURIComponent(path)
  } catch {
    return path
  }
}

function decodeFragment(hash: string): string {
  try {
    return decodeURIComponent(hash)
  } catch {
    return hash
  }
}

export function classifyLink(href: string, baseDir: string): LinkTarget {
  const url = href.trim()
  if (url.startsWith('#')) return { kind: 'anchor', id: decodeFragment(url.slice(1)) }
  if (url.startsWith('//')) return { kind: 'external', href: url, blank: true }
  const s = scheme(url)
  if (s === 'http' || s === 'https' || s === 'mailto') return { kind: 'external', href: url, blank: true }
  if (s === 'tel') return { kind: 'external', href: url, blank: false }
  if (s) return { kind: 'unsafe' }
  const path = localPath(url)
  if (!path) {
    const hash = url.indexOf('#')
    return hash >= 0 ? { kind: 'anchor', id: decodeFragment(url.slice(hash + 1)) } : { kind: 'unsafe' }
  }
  return { kind: 'local', path: resolvePath(baseDir, path) }
}

export function imageSource(src: string, baseDir: string): string | null {
  const url = src.trim()
  if (!url) return null
  if (url.startsWith('//')) return url
  const s = scheme(url)
  if (s === 'http' || s === 'https') return url
  if (s === 'data') return /^data:image\/[a-z0-9.+-]+[;,]/i.test(url) ? url : null
  if (s) return null
  const path = localPath(url)
  return path ? rawURL(resolvePath(baseDir, path)) : null
}

// ---------------------------------------------------------------------------
// Code highlighting

/** Fence names that are not file extensions, mapped to an extension. */
const ALIAS_EXTENSIONS: Record<string, string> = {
  bash: 'sh', shell: 'sh', zsh: 'sh', console: 'sh', shellsession: 'sh', javascript: 'js', typescript: 'ts',
  python: 'py', python3: 'py', golang: 'go', rust: 'rs', yaml: 'yml', markdown: 'md', ruby: 'rb',
  kotlin: 'kt', 'c++': 'cpp', docker: 'dockerfile', make: 'makefile', patch: 'diff', jsonc: 'json', json5: 'json',
}

const LANGUAGE_ALIASES: Record<string, string> = {
  bash: 'shell', sh: 'shell', shell: 'shell', zsh: 'shell', console: 'shell', shellsession: 'shell',
  js: 'javascript', javascript: 'javascript', jsx: 'javascript', mjs: 'javascript', cjs: 'javascript',
  ts: 'typescript', typescript: 'typescript', tsx: 'typescript',
  py: 'python', python: 'python', python3: 'python',
  yml: 'yaml', yaml: 'yaml', golang: 'go', go: 'go',
  rs: 'rust', rust: 'rust', html: 'html', xml: 'html', svg: 'html',
  json: 'json', jsonc: 'json', json5: 'json', css: 'css', scss: 'css',
  sql: 'sql', md: 'markdown', markdown: 'markdown', toml: 'toml', ini: 'ini',
  dockerfile: 'dockerfile', docker: 'dockerfile', makefile: 'makefile', make: 'makefile',
  c: 'c', h: 'c', cpp: 'cpp', 'c++': 'cpp', cc: 'cpp', java: 'java', kotlin: 'kotlin', kt: 'kotlin',
  rb: 'ruby', ruby: 'ruby', php: 'php', lua: 'lua', diff: 'diff', patch: 'diff',
}

/** Maps a fence info string (first word) to a highlighter language. */
export function fenceLanguage(info: string): string | null {
  const key = info.split(/\s+/)[0]?.replace(/^\{\.?|\}$/g, '').toLowerCase() ?? ''
  if (!key) return null
  const lookup = (name: string) => {
    try {
      return languageFor(name)
    } catch {
      return null
    }
  }
  const ext = ALIAS_EXTENSIONS[key]
  return (
    lookup(`x.${key}`) ??
    (ext ? lookup(`x.${ext}`) : null) ??
    lookup(key) ??
    lookup(key[0].toUpperCase() + key.slice(1)) ??
    LANGUAGE_ALIASES[key] ??
    null
  )
}

// ---------------------------------------------------------------------------
// Rendering

interface RenderContext {
  baseDir: string
  openPath: (path: string) => void
  scrollTo: (id: string) => void
}

const linkClass = 'text-primary underline-offset-2 hover:underline'

function renderInlines(nodes: Inline[], rc: RenderContext): ReactNode[] {
  return nodes.map((node, k) => renderInline(node, rc, k))
}

function renderInline(node: Inline, rc: RenderContext, key: number): ReactNode {
  switch (node.type) {
    case 'text':
      return node.text
    case 'softbreak':
      return '\n'
    case 'br':
      return <br key={key} />
    case 'code':
      return (
        <code key={key} className="rounded bg-secondary px-1 py-0.5 font-mono text-[0.9em]">
          {node.text}
        </code>
      )
    case 'html':
      return (
        <span key={key} className="font-mono text-[0.85em] text-muted-foreground">
          {node.text}
        </span>
      )
    case 'em':
      return <em key={key}>{renderInlines(node.children, rc)}</em>
    case 'strong':
      return (
        <strong key={key} className="font-semibold">
          {renderInlines(node.children, rc)}
        </strong>
      )
    case 'del':
      return <del key={key}>{renderInlines(node.children, rc)}</del>
    case 'image':
      return <MarkdownImage key={key} node={node} baseDir={rc.baseDir} />
    case 'footnoteRef':
      return (
        <sup key={key}>
          <a
            href={`#fn-${node.label}`}
            id={node.first ? `fnref-${node.label}` : undefined}
            className={linkClass}
            onClick={event => {
              event.preventDefault()
              rc.scrollTo(`fn-${node.label}`)
            }}
          >
            [{node.index}]
          </a>
        </sup>
      )
    case 'link':
      return renderLink(node, rc, key)
  }
}

function renderLink(node: Inline & { type: 'link' }, rc: RenderContext, key: number): ReactNode {
  const children = renderInlines(node.children, rc)
  const target = classifyLink(node.href, rc.baseDir)
  const title = node.title ?? undefined
  switch (target.kind) {
    case 'unsafe':
      return (
        <span key={key} title={title}>
          {children}
        </span>
      )
    case 'external':
      return (
        <a
          key={key}
          href={target.href}
          title={title}
          className={linkClass}
          {...(target.blank ? { target: '_blank', rel: 'noopener noreferrer' } : {})}
        >
          {children}
        </a>
      )
    case 'anchor':
      return (
        <a
          key={key}
          href={`#${target.id}`}
          title={title}
          className={linkClass}
          onClick={(event: MouseEvent) => {
            event.preventDefault()
            rc.scrollTo(target.id)
          }}
        >
          {children}
        </a>
      )
    case 'local':
      return (
        <a
          key={key}
          href="#"
          title={title ?? target.path}
          className={linkClass}
          onClick={(event: MouseEvent) => {
            event.preventDefault()
            rc.openPath(target.path)
          }}
        >
          {children}
        </a>
      )
  }
}

function MarkdownImage({ node, baseDir }: { node: Inline & { type: 'image' }; baseDir: string }) {
  const [broken, setBroken] = useState(false)
  const src = imageSource(node.src, baseDir)
  if (!src || broken) {
    return (
      <span
        className="inline-flex items-center rounded border border-dashed border-border px-1.5 py-0.5 text-[0.85em] text-muted-foreground italic"
        title={node.src}
      >
        {node.alt || 'Image indisponible'}
      </span>
    )
  }
  return (
    <img
      src={src}
      alt={node.alt}
      title={node.title ?? undefined}
      loading="lazy"
      decoding="async"
      onError={() => setBroken(true)}
      className="inline-block h-auto max-w-full rounded-md align-middle"
    />
  )
}

function CodeBlock({ code, info }: { code: string; info: string }) {
  const [copied, setCopied] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current)
  }, [])
  const label = info.split(/\s+/)[0] ?? ''
  const html = useMemo(() => {
    try {
      return highlightToHTML(code, fenceLanguage(info))
    } catch {
      return null
    }
  }, [code, info])

  const copy = () => {
    void navigator.clipboard?.writeText(code).then(
      () => {
        setCopied(true)
        if (timer.current) clearTimeout(timer.current)
        timer.current = setTimeout(() => setCopied(false), 1500)
      },
      () => undefined,
    )
  }

  return (
    <div className="group relative my-4">
      <div className="absolute top-1.5 right-1.5 flex items-center gap-2">
        {label && (
          <span className="pointer-events-none font-mono text-[11px] text-muted-foreground select-none">{label}</span>
        )}
        <button
          type="button"
          onClick={copy}
          className="rounded border border-border bg-secondary px-1.5 py-0.5 text-[11px] text-muted-foreground opacity-70 transition-opacity group-hover:opacity-100 hover:text-foreground focus-visible:opacity-100"
        >
          {copied ? 'Copié' : 'Copier'}
        </button>
      </div>
      <pre className="overflow-x-auto rounded-md border border-border bg-muted p-3 pt-7 font-mono text-[13px] leading-relaxed">
        {html === null ? <code>{code}</code> : <code dangerouslySetInnerHTML={{ __html: html }} />}
      </pre>
    </div>
  )
}

const headingClass: Record<number, string> = {
  1: 'mt-8 mb-4 border-b border-border pb-2 text-[1.85em] font-semibold leading-tight',
  2: 'mt-8 mb-4 border-b border-border pb-1.5 text-[1.45em] font-semibold leading-tight',
  3: 'mt-6 mb-3 text-[1.2em] font-semibold leading-snug',
  4: 'mt-6 mb-3 text-[1em] font-semibold',
  5: 'mt-5 mb-2 text-[0.9em] font-semibold',
  6: 'mt-5 mb-2 text-[0.85em] font-semibold text-muted-foreground',
}

const flow = '[&>*:first-child]:mt-0 [&>*:last-child]:mb-0'
const bulletStyles = ['list-disc', 'list-[circle]', 'list-[square]']

interface BlockOptions {
  tight?: boolean
  listDepth: number
  /** Element placed at the start of the first paragraph (task checkbox). */
  prefix?: ReactNode
}

function renderBlocks(blocks: Block[], rc: RenderContext, options: BlockOptions): ReactNode[] {
  return blocks.map((block, k) =>
    renderBlock(block, rc, k === 0 ? options : { ...options, prefix: undefined }, k),
  )
}

function renderBlock(block: Block, rc: RenderContext, options: BlockOptions, key: number): ReactNode {
  switch (block.type) {
    case 'heading': {
      const Tag = `h${block.level}` as const
      return (
        <Tag key={key} id={block.id} className={cn('scroll-mt-4 text-foreground', headingClass[block.level])}>
          {renderInlines(block.content, rc)}
        </Tag>
      )
    }
    case 'paragraph': {
      const content = renderInlines(block.content, rc)
      if (options.tight) {
        return (
          <Fragment key={key}>
            {options.prefix}
            {content}
          </Fragment>
        )
      }
      return (
        <p key={key} className="my-3">
          {options.prefix}
          {content}
        </p>
      )
    }
    case 'code':
      return <CodeBlock key={key} code={block.code} info={block.info} />
    case 'frontmatter':
      return (
        <pre
          key={key}
          className="mb-6 overflow-x-auto rounded-md border border-dashed border-border bg-muted/60 px-3 py-2 font-mono text-xs leading-relaxed text-muted-foreground"
        >
          <code>{block.code}</code>
        </pre>
      )
    case 'html':
      return (
        <div
          key={key}
          className="my-3 rounded border border-dashed border-border px-3 py-2 font-mono text-xs break-words whitespace-pre-wrap text-muted-foreground"
        >
          {block.text}
        </div>
      )
    case 'hr':
      return <hr key={key} className="my-6 border-0 border-t border-border" />
    case 'blockquote':
      return (
        <blockquote key={key} className={cn('my-4 border-l-4 border-primary/40 pl-4 text-muted-foreground', flow)}>
          {renderBlocks(block.children, rc, { listDepth: options.listDepth })}
        </blockquote>
      )
    case 'list': {
      const ListTag = block.ordered ? 'ol' : 'ul'
      const style = block.ordered ? 'list-decimal' : bulletStyles[options.listDepth % bulletStyles.length]
      return (
        <ListTag
          key={key}
          start={block.ordered && block.start !== 1 ? block.start : undefined}
          className={cn(
            'pl-7 marker:text-muted-foreground',
            options.listDepth > 0 ? 'my-1' : 'my-3',
            style,
            block.loose ? 'space-y-2' : 'space-y-0.5',
          )}
        >
          {block.items.map((item, k) => {
            const checkbox =
              item.checked === null ? undefined : (
                <input
                  type="checkbox"
                  checked={item.checked}
                  disabled
                  readOnly
                  className="mr-2 size-3.5 translate-y-[2px] accent-primary"
                />
              )
            const startsWithParagraph = item.children[0]?.type === 'paragraph'
            return (
              <li
                key={k}
                className={cn('pl-1', item.checked !== null && '-ml-5 list-none', block.loose && flow)}
              >
                {!startsWithParagraph && checkbox}
                {renderBlocks(item.children, rc, {
                  tight: !block.loose,
                  listDepth: options.listDepth + 1,
                  prefix: startsWithParagraph ? checkbox : undefined,
                })}
              </li>
            )
          })}
        </ListTag>
      )
    }
    case 'table':
      return (
        <div key={key} className="my-4 overflow-x-auto">
          <table className="border-collapse text-[0.92em]">
            <thead>
              <tr>
                {block.header.map((cell, k) => (
                  <th
                    key={k}
                    style={{ textAlign: block.align[k] ?? 'left' }}
                    className="border border-border bg-secondary px-3 py-1.5 font-semibold"
                  >
                    {renderInlines(cell, rc)}
                  </th>
                ))}
              </tr>
            </thead>
            {block.rows.length > 0 && (
              <tbody>
                {block.rows.map((row, r) => (
                  <tr key={r} className="even:bg-muted/40">
                    {row.map((cell, k) => (
                      <td
                        key={k}
                        style={block.align[k] ? { textAlign: block.align[k] ?? undefined } : undefined}
                        className="border border-border px-3 py-1.5 align-top"
                      >
                        {renderInlines(cell, rc)}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            )}
          </table>
        </div>
      )
    case 'footnotes':
      return (
        <section key={key} aria-label="Notes" className="mt-10 border-t border-border pt-4 text-[0.9em] text-muted-foreground">
          <ol className="list-decimal space-y-1 pl-6">
            {block.items.map(item => (
              <li key={item.label} id={`fn-${item.label}`} className={cn('scroll-mt-4 pl-1', flow)}>
                {renderBlocks(item.children, rc, { listDepth: 1, tight: item.children.length === 1 })}{' '}
                <a
                  href={`#fnref-${item.label}`}
                  aria-label="Retour au texte"
                  className={linkClass}
                  onClick={event => {
                    event.preventDefault()
                    rc.scrollTo(`fnref-${item.label}`)
                  }}
                >
                  ↩{'\ufe0e'}
                </a>
              </li>
            ))}
          </ol>
        </section>
      )
  }
}

export function MarkdownView(props: {
  source: string
  /** Absolute folder of the .md file, for resolving relative links/images. */
  baseDir: string
  /** Called when a relative link to a local file/folder is clicked (path is absolute, resolved). */
  onOpenPath?: (path: string) => void
  className?: string
}): JSX.Element {
  const { source, baseDir, onOpenPath, className } = props
  const blocks = useMemo(() => parseMarkdown(source), [source])
  const rootRef = useRef<HTMLDivElement>(null)
  const openRef = useRef(onOpenPath)
  openRef.current = onOpenPath

  const rc = useMemo<RenderContext>(
    () => ({
      baseDir,
      openPath: path => openRef.current?.(path),
      scrollTo: id => {
        const root = rootRef.current
        if (!root) return
        const wanted = [id, slugify(id)]
        let found: Element | undefined
        for (const candidate of wanted) {
          found = Array.from(root.querySelectorAll('[id]')).find(el => el.id === candidate)
          if (found) break
        }
        found?.scrollIntoView?.({ behavior: 'smooth', block: 'start' })
      },
    }),
    [baseDir],
  )

  const tree = useMemo(() => renderBlocks(blocks, rc, { listDepth: 0 }), [blocks, rc])

  return (
    <div
      ref={rootRef}
      className={cn(
        'mx-auto w-full max-w-[52rem] px-5 py-6 text-[15px] leading-7 break-words text-foreground sm:px-8',
        flow,
        className,
      )}
    >
      {tree}
    </div>
  )
}
