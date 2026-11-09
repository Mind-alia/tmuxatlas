import './highlight.css'

// Dependency-free syntax highlighter. Scanners emit ordered, non-overlapping
// [start, end, kind] tokens; the renderer escapes everything and splits spans
// at newlines so every output line is balanced.

type Tok = [number, number, string]
type Emit = (s: number, e: number, kind: string) => void

// ---------------------------------------------------------------------------
// HTML helpers

const ESC: Record<string, string> = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }
const ESC_RE = /[&<>"']/g

export function escapeHTML(text: string): string {
  for (let i = 0; i < text.length; i++) {
    const k = text.charCodeAt(i)
    if (k === 38 || k === 60 || k === 62 || k === 34 || k === 39) return text.replace(ESC_RE, (c) => ESC[c])
  }
  return text
}

// ---------------------------------------------------------------------------
// Language tables

const NAMES: Record<string, string> = {
  makefile: 'makefile', gnumakefile: 'makefile', dockerfile: 'dockerfile', containerfile: 'dockerfile',
  caddyfile: 'conf', 'nginx.conf': 'conf', '.gitignore': 'ignore', '.dockerignore': 'ignore',
  '.npmignore': 'ignore', '.gitattributes': 'ignore', '.eslintignore': 'ignore', '.prettierignore': 'ignore',
  'go.mod': 'gomod', 'go.sum': null as unknown as string, 'cargo.lock': 'toml', 'poetry.lock': 'toml',
  'uv.lock': 'toml', 'composer.lock': 'json', '.bashrc': 'shell', '.zshrc': 'shell', '.profile': 'shell',
  '.bash_profile': 'shell', '.zprofile': 'shell', '.zshenv': 'shell', '.bash_aliases': 'shell',
  '.bash_logout': 'shell', '.envrc': 'shell', '.gitconfig': 'ini', '.editorconfig': 'ini',
  gemfile: 'ruby', rakefile: 'ruby', '.babelrc': 'json', '.prettierrc': 'json', 'tsconfig.json': 'json',
}

const EXTS: Record<string, string> = {
  js: 'javascript', jsx: 'javascript', mjs: 'javascript', cjs: 'javascript',
  ts: 'typescript', tsx: 'typescript', mts: 'typescript', cts: 'typescript',
  json: 'json', jsonc: 'json', json5: 'json', webmanifest: 'json', geojson: 'json',
  go: 'go', py: 'python', pyw: 'python', pyi: 'python', rs: 'rust', c: 'c', h: 'c',
  cc: 'cpp', cpp: 'cpp', cxx: 'cpp', hpp: 'cpp', hh: 'cpp', hxx: 'cpp', ino: 'cpp',
  java: 'java', kt: 'kotlin', kts: 'kotlin', cs: 'csharp', php: 'php',
  rb: 'ruby', rake: 'ruby', gemspec: 'ruby', lua: 'lua',
  sh: 'shell', bash: 'shell', zsh: 'shell', fish: 'shell', ksh: 'shell', env: 'shell',
  sql: 'sql', css: 'css', scss: 'scss', less: 'less',
  html: 'html', htm: 'html', xhtml: 'html', vue: 'html',
  xml: 'xml', svg: 'xml', xsd: 'xml', xsl: 'xml', xslt: 'xml', plist: 'xml', rss: 'xml', atom: 'xml',
  yaml: 'yaml', yml: 'yaml', toml: 'toml',
  ini: 'ini', cfg: 'ini', properties: 'ini', service: 'ini', timer: 'ini', socket: 'ini', mount: 'ini',
  desktop: 'ini', conf: 'conf', md: 'markdown', markdown: 'markdown', mdx: 'markdown',
  diff: 'diff', patch: 'diff', mk: 'makefile', mak: 'makefile', dockerfile: 'dockerfile',
}

const LABELS: Record<string, string> = {
  javascript: 'JavaScript', typescript: 'TypeScript', json: 'JSON', go: 'Go', python: 'Python', rust: 'Rust',
  c: 'C', cpp: 'C++', java: 'Java', kotlin: 'Kotlin', csharp: 'C#', php: 'PHP', ruby: 'Ruby', lua: 'Lua',
  shell: 'Shell', sql: 'SQL', css: 'CSS', scss: 'SCSS', less: 'Less', html: 'HTML', xml: 'XML',
  yaml: 'YAML', toml: 'TOML', ini: 'INI', conf: 'Config', markdown: 'Markdown', dockerfile: 'Dockerfile',
  makefile: 'Makefile', diff: 'Diff', ignore: 'Ignore', gomod: 'Go module',
}

/** Language id for a file name, e.g. 'main.go' -> 'go', 'Dockerfile' -> 'dockerfile', unknown -> null. */
export function languageFor(fileName: string): string | null {
  const base = (fileName.split('/').pop() ?? '').toLowerCase()
  if (!base) return null
  if (Object.prototype.hasOwnProperty.call(NAMES, base)) return NAMES[base] ?? null
  if (base === '.env' || base.startsWith('.env.')) return 'shell'
  if (base.startsWith('dockerfile.') || base.startsWith('makefile.')) return base.startsWith('d') ? 'dockerfile' : 'makefile'
  const dot = base.lastIndexOf('.')
  if (dot < 0) return null
  const ext = base.slice(dot + 1)
  if (ext === 'lock') return null
  return Object.prototype.hasOwnProperty.call(EXTS, ext) ? EXTS[ext] : null
}

/** Human label for the status bar, e.g. 'go' -> 'Go', null -> 'Texte brut'. */
export function languageLabel(lang: string | null): string {
  if (!lang) return 'Texte brut'
  return LABELS[lang] ?? lang
}

const SLASH = { line: '//', block: ['/*', '*/'] as [string, string] }
const COMMENTS: Record<string, { line?: string; block?: [string, string] }> = {
  javascript: SLASH, typescript: SLASH, go: SLASH, rust: SLASH, c: SLASH, cpp: SLASH, java: SLASH,
  kotlin: SLASH, csharp: SLASH, php: SLASH, scss: SLASH, less: SLASH, gomod: { line: '//' },
  css: { block: ['/*', '*/'] },
  python: { line: '#' }, ruby: { line: '#' }, shell: { line: '#' }, yaml: { line: '#' }, toml: { line: '#' },
  conf: { line: '#' }, dockerfile: { line: '#' }, makefile: { line: '#' }, ignore: { line: '#' },
  ini: { line: ';' },
  lua: { line: '--', block: ['--[[', ']]'] },
  sql: { line: '--', block: ['/*', '*/'] },
  html: { block: ['<!--', '-->'] }, xml: { block: ['<!--', '-->'] }, markdown: { block: ['<!--', '-->'] },
}

/** Comment tokens for toggle-comment, e.g. 'go' -> line '//' plus a C-style block pair. */
export function commentToken(lang: string | null): { line?: string; block?: [string, string] } {
  return (lang && COMMENTS[lang]) || {}
}

// ---------------------------------------------------------------------------
// Generic scanner

type Hook = [string, RegExp] // [possible first characters, sticky regexp]

interface Str {
  q: string
  multi?: boolean
  raw?: boolean
  interp?: 'tpl' | 'var'
}

interface Cfg {
  line?: string[]
  block?: [string, string][]
  str?: Str[]
  kw?: string
  types?: string
  consts?: string
  vars?: string
  nocase?: boolean
  caps?: boolean
  macro?: boolean
  nofn?: boolean
  nonum?: boolean
  dollarIdent?: boolean
  regexp?: boolean
  json?: boolean
  charLit?: boolean
  hashPrev?: boolean
  preproc?: boolean
  meta?: Hook[]
  vhooks?: Hook[]
  lineStart?: (code: string, i: number) => [string, number] | null
}

interface Prep {
  cfg: Cfg
  kw: Set<string>
  types: Set<string>
  consts: Set<string>
  vars: Set<string>
  strs: Str[]
  line: string[]
  block: [string, string][]
  starters: string
}

const Y = (src: string) => new RegExp(src, 'y')
const set = (s?: string) => new Set(s ? s.split(/\s+/).filter(Boolean) : [])

const NUM = Y(
  String.raw`0[xX][\da-fA-F_]+[a-zA-Z]*|0[bB][01_]+[a-zA-Z]*|0[oO][0-7_]+[a-zA-Z]*|\d[\d_]*(?:\.(?!\.)[\d_]*)?(?:[eE][+-]?\d+)?[a-zA-Z]*|\.\d[\d_]*(?:[eE][+-]?\d+)?[a-zA-Z]*`,
)
const REGEXP = Y(String.raw`/(?:\\.|\[(?:\\.|[^\]\n\\])*\]|[^/\n\\\[])+/[a-z]*`)
const CHAR_LIT = Y(String.raw`'(?:\\.[^'\n]{0,8}|[^'\\\n])'`)
const LIFETIME = Y(String.raw`'[A-Za-z_]\w*`)
const INCLUDE_ARG = Y(String.raw`[ \t]*<[^>\n]*>`)
const PRE = Y(String.raw`#[ \t]*[A-Za-z_]\w*`)
const REGEX_PREV_WORDS = new Set('return typeof case in of delete void throw yield await do else'.split(' '))
const OPS = '+-*/%=<>!&|^~?:\\'
const PUNCT = '()[];,.'
const SHELL_VAR = Y(String.raw`\$(?:\{[^}\n]*\}|[A-Za-z_]\w*|[0-9@*#?$!-]|\()`)

const cache = new Map<string, Prep>()
function prep(lang: string, cfg: Cfg): Prep {
  let p = cache.get(lang)
  if (!p) {
    p = {
      cfg,
      kw: set(cfg.kw),
      types: set(cfg.types),
      consts: set(cfg.consts),
      vars: set(cfg.vars),
      strs: [...(cfg.str ?? [])].sort((a, b) => b.q.length - a.q.length),
      line: [...(cfg.line ?? [])].sort((a, b) => b.length - a.length),
      block: [...(cfg.block ?? [])].sort((a, b) => b[0].length - a[0].length),
      starters: '',
    }
    p.starters = [...p.line, ...p.block.map((b) => b[0]), ...p.strs.map((x) => x.q)].map((x) => x[0]).join('')
    cache.set(lang, p)
  }
  return p
}

function isWordCh(c: string): boolean {
  if (c === '') return false
  const k = c.charCodeAt(0)
  return (k >= 97 && k <= 122) || (k >= 65 && k <= 90) || (k >= 48 && k <= 57) || k === 95 || k === 36
}
function isIdentPart(k: number, dollar?: boolean): boolean {
  return (k >= 97 && k <= 122) || (k >= 65 && k <= 90) || (k >= 48 && k <= 57) || k === 95 || k >= 0xa0 || (dollar === true && k === 36)
}
function isIdentStart(c: string): boolean {
  if (c === '') return false
  const k = c.charCodeAt(0)
  return (k >= 97 && k <= 122) || (k >= 65 && k <= 90) || k === 95 || k === 36 || k >= 0xa0
}
const isSpace = (c: string) => c === ' ' || c === '\n' || c === '\t' || c === '\r'

function lineStartWS(code: string, i: number): boolean {
  let j = i - 1
  while (j >= 0 && (code.charAt(j) === ' ' || code.charAt(j) === '\t')) j--
  return j < 0 || code.charAt(j) === '\n'
}

function scanGeneric(code: string, p: Prep, emit: Emit, start = 0, braceStop = false): number {
  const { cfg } = p
  const n = code.length
  let i = start
  let depth = 0

  const commentAt = (j: number): boolean => {
    for (const l of p.line) if (code.startsWith(l, j)) return true
    for (const b of p.block) if (code.startsWith(b[0], j)) return true
    return false
  }

  scan: while (i < n) {
    const ch = code.charAt(i)
    if (isSpace(ch)) {
      i++
      continue
    }
    if (cfg.lineStart && (i === 0 || code.charAt(i - 1) === '\n')) {
      const r = cfg.lineStart(code, i)
      if (r) {
        emit(i, i + r[1], r[0])
        i += r[1]
        continue
      }
    }
    // Identifiers
    if (isIdentStart(ch) && (cfg.dollarIdent || ch !== '$')) {
      let j = i + 1
      while (j < n && isIdentPart(code.charCodeAt(j), cfg.dollarIdent)) j++
      const word = code.slice(i, j)
      const w = cfg.nocase ? word.toLowerCase() : word
      const afterDot = code.charAt(i - 1) === '.' && code.charAt(i - 2) !== '.'
      let kind: string | null = null
      if (!afterDot) {
        if (p.kw.has(w)) kind = 'keyword'
        else if (p.consts.has(w)) kind = 'constant'
        else if (p.types.has(w)) kind = 'type'
        else if (p.vars.has(w)) kind = 'variable'
      }
      if (!kind && !cfg.nofn) {
        if (code.charAt(j) === '(') kind = 'function'
        else if (cfg.macro && code.charAt(j) === '!' && code.charAt(j + 1) !== '=') {
          emit(i, j + 1, 'function')
          i = j + 1
          continue
        }
      }
      if (!kind && cfg.caps && !afterDot && ch >= 'A' && ch <= 'Z') {
        kind = /^[A-Z][A-Z0-9_]+$/.test(word) ? 'constant' : 'type'
      }
      if (kind) emit(i, j, kind)
      i = j
      continue
    }
    // Variables, symbols
    if (cfg.vhooks) {
      for (const [first, re] of cfg.vhooks) {
        if (!first.includes(ch)) continue
        if (ch === ':' && (isWordCh(code.charAt(i - 1)) || code.charAt(i - 1) === ':')) continue
        re.lastIndex = i
        const m = re.exec(code)
        if (m) {
          emit(i, i + m[0].length, 'variable')
          i += m[0].length
          continue scan
        }
      }
    }
    // Meta: decorators, attributes, tags
    if (cfg.meta) {
      for (const [first, re] of cfg.meta) {
        if (!first.includes(ch)) continue
        re.lastIndex = i
        const m = re.exec(code)
        if (m) {
          emit(i, i + m[0].length, 'meta')
          i += m[0].length
          continue scan
        }
      }
    }
    // Preprocessor
    if (cfg.preproc && ch === '#' && lineStartWS(code, i)) {
      PRE.lastIndex = i
      const m = PRE.exec(code)
      if (m) {
        emit(i, i + m[0].length, 'meta')
        i += m[0].length
        if (/include|import/.test(m[0])) {
          INCLUDE_ARG.lastIndex = i
          const a = INCLUDE_ARG.exec(code)
          if (a) {
            const off = a[0].indexOf('<')
            emit(i + off, i + a[0].length, 'string')
            i += a[0].length
          }
        }
        continue
      }
    }
    // Comments
    const special = p.starters.includes(ch)
    for (let b = 0; special && b < p.block.length; b++) {
      const open = p.block[b][0]
      if (code.startsWith(open, i)) {
        const close = p.block[b][1]
        const e = code.indexOf(close, i + open.length)
        const end = e < 0 ? n : e + close.length
        emit(i, end, 'comment')
        i = end
        continue scan
      }
    }
    for (let b = 0; special && b < p.line.length; b++) {
      const l = p.line[b]
      if (!code.startsWith(l, i)) continue
      if (cfg.hashPrev && l === '#' && i > 0 && !/[\s;&|(]/.test(code.charAt(i - 1))) continue
      let e = code.indexOf('\n', i)
      if (e < 0) e = n
      emit(i, e, l === '#' && i === 0 && code.charAt(1) === '!' && cfg.hashPrev ? 'meta' : 'comment')
      i = e
      continue scan
    }
    // Strings
    if (cfg.charLit && ch === "'") {
      CHAR_LIT.lastIndex = i
      let m = CHAR_LIT.exec(code)
      if (m) {
        emit(i, i + m[0].length, 'string')
        i += m[0].length
        continue
      }
      LIFETIME.lastIndex = i
      m = LIFETIME.exec(code)
      if (m) {
        emit(i, i + m[0].length, 'type')
        i += m[0].length
        continue
      }
      i++
      continue
    }
    for (let b = 0; special && b < p.strs.length; b++) {
      const s = p.strs[b]
      if (code.startsWith(s.q, i)) {
        i = scanString(code, i, s, p, emit)
        continue scan
      }
    }
    // Numbers
    if (!cfg.nonum && ((ch >= '0' && ch <= '9') || (ch === '.' && /\d/.test(code.charAt(i + 1)) && !isWordCh(code.charAt(i - 1))))) {
      NUM.lastIndex = i
      const m = NUM.exec(code)
      if (m) {
        emit(i, i + m[0].length, 'number')
        i += m[0].length
        continue
      }
    }
    // Regexp literal
    if (cfg.regexp && ch === '/') {
      let k = i - 1
      while (k >= 0 && (code.charAt(k) === ' ' || code.charAt(k) === '\t')) k--
      const pc = k < 0 ? '' : code.charAt(k)
      let ok = pc === '' || pc === '\n' || '([{,;:=!&|?+-*%<>~^'.includes(pc)
      if (!ok && isWordCh(pc)) {
        let s = k
        while (s > 0 && isWordCh(code.charAt(s - 1))) s--
        ok = REGEX_PREV_WORDS.has(code.slice(s, k + 1))
      }
      if (ok) {
        REGEXP.lastIndex = i
        const m = REGEXP.exec(code)
        if (m) {
          emit(i, i + m[0].length, 'regexp')
          i += m[0].length
          continue
        }
      }
    }
    // Braces
    if (ch === '{') {
      depth++
      emit(i, i + 1, 'punct')
      i++
      continue
    }
    if (ch === '}') {
      if (braceStop && depth === 0) return i
      depth--
      emit(i, i + 1, 'punct')
      i++
      continue
    }
    // Operator / punctuation runs
    if (OPS.includes(ch)) {
      let j = i + 1
      while (j < n && OPS.includes(code.charAt(j)) && !commentAt(j)) j++
      emit(i, j, 'operator')
      i = j
      continue
    }
    if (PUNCT.includes(ch)) {
      let j = i + 1
      while (j < n && PUNCT.includes(code.charAt(j))) j++
      emit(i, j, 'punct')
      i = j
      continue
    }
    i++
  }
  return n
}

/** Scans a string starting at i; returns the index after it. */
function scanString(code: string, i: number, s: Str, p: Prep, emit: Emit): number {
  const n = code.length
  const q = s.q
  let j = i + q.length
  let seg = i
  let closed = false
  while (j < n) {
    const c = code.charAt(j)
    if (c === '\\' && !s.raw) {
      if (!s.multi && code.charAt(j + 1) === '\n') break
      j += 2
      continue
    }
    if (c === '\n' && !s.multi) break
    if (c === '\r' && !s.multi) break
    if (code.startsWith(q, j)) {
      j += q.length
      closed = true
      break
    }
    if (s.interp === 'tpl' && c === '$' && code.charAt(j + 1) === '{') {
      emit(seg, j, 'string')
      emit(j, j + 2, 'punct')
      const close = scanGeneric(code, p, emit, j + 2, true)
      if (close < n) emit(close, close + 1, 'punct')
      j = close + 1
      seg = j
      continue
    }
    if (s.interp === 'var' && c === '$') {
      SHELL_VAR.lastIndex = j
      const m = SHELL_VAR.exec(code)
      if (m) {
        emit(seg, j, 'string')
        emit(j, j + m[0].length, 'variable')
        j += m[0].length
        seg = j
        continue
      }
    }
    j++
  }
  if (j > n) j = n
  let kind = 'string'
  if (p.cfg.json && closed) {
    let k = j
    while (code.charAt(k) === ' ' || code.charAt(k) === '\t') k++
    if (code.charAt(k) === ':') kind = 'property'
  }
  emit(seg, j, kind)
  return j
}

// ---------------------------------------------------------------------------
// Language configs

const SQ: Str = { q: "'" }
const DQ: Str = { q: '"' }
const cLike: Cfg = { line: ['//'], block: [['/*', '*/']], str: [DQ, SQ] }

const CFG: Record<string, Cfg> = {
  javascript: {
    ...cLike,
    str: [DQ, SQ, { q: '`', multi: true, interp: 'tpl' }],
    kw: 'break case catch class const continue debugger default delete do else export extends finally for function if import in instanceof new of return static super switch this throw try typeof var let void while with yield async await',
    types: 'Array Object String Number Boolean Map Set Promise Date Error RegExp Symbol BigInt JSON Math Record',
    consts: 'true false null undefined NaN Infinity',
    caps: true, regexp: true, dollarIdent: true,
    meta: [['@', Y(String.raw`@[A-Za-z_$][\w$.]*`)]],
  },
  json: {
    line: ['//'], block: [['/*', '*/']], str: [DQ], consts: 'true false null', json: true, nofn: true,
  },
  go: {
    ...cLike,
    str: [DQ, SQ, { q: '`', multi: true, raw: true }],
    kw: 'break default func interface select case defer go map struct chan else goto package switch const fallthrough if range type continue for import return var',
    types: 'bool byte complex64 complex128 error float32 float64 int int8 int16 int32 int64 rune string uint uint8 uint16 uint32 uint64 uintptr any comparable',
    consts: 'true false nil iota',
  },
  python: {
    line: ['#'],
    str: [{ q: '"""', multi: true }, { q: "'''", multi: true }, DQ, SQ],
    kw: 'and as assert async await break class continue def del elif else except finally for from global if import in is lambda nonlocal not or pass raise return try while with yield match case',
    types: 'int str float bool list dict set tuple bytes object type complex frozenset bytearray range print len Exception',
    consts: 'True False None Ellipsis NotImplemented',
    vars: 'self cls',
    caps: true,
    meta: [['@', Y(String.raw`@[A-Za-z_][\w.]*`)]],
  },
  rust: {
    ...cLike,
    str: [{ q: '"', multi: true }],
    charLit: true, macro: true, caps: true,
    kw: 'as async await break const continue crate dyn else enum extern fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait type unsafe use where while',
    types: 'i8 i16 i32 i64 i128 isize u8 u16 u32 u64 u128 usize f32 f64 bool char str String Vec Option Result Box Rc Arc',
    consts: 'true false None Some Ok Err',
    meta: [['#', Y(String.raw`#!?\[[^\]\n]*\]`)]],
  },
  c: {
    ...cLike, preproc: true, caps: true,
    kw: 'auto break case const continue default do else enum extern for goto if inline register restrict return sizeof static struct switch typedef union volatile while',
    types: 'int char short long float double void signed unsigned size_t ssize_t bool uint8_t uint16_t uint32_t uint64_t int8_t int16_t int32_t int64_t FILE',
    consts: 'true false NULL nullptr',
  },
  cpp: {
    ...cLike, preproc: true, caps: true,
    kw: 'alignas auto break case catch class const constexpr continue default delete do else enum explicit export extern for friend goto if inline namespace new noexcept operator override private protected public register return sizeof static struct switch template this throw try typedef typename union using virtual volatile while final',
    types: 'int char short long float double void signed unsigned size_t bool string vector map set unique_ptr shared_ptr wchar_t int8_t int16_t int32_t int64_t uint8_t uint16_t uint32_t uint64_t',
    consts: 'true false NULL nullptr',
  },
  java: {
    ...cLike, caps: true,
    str: [{ q: '"""', multi: true }, DQ, SQ],
    kw: 'abstract assert break case catch class const continue default do else enum extends final finally for goto if implements import instanceof interface native new package private protected public return static strictfp super switch synchronized this throw throws transient try volatile while var record',
    types: 'int long short byte float double boolean char void String Object Integer Long List Map Set',
    consts: 'true false null',
    meta: [['@', Y(String.raw`@[A-Za-z_][\w.]*`)]],
  },
  kotlin: {
    ...cLike, caps: true,
    str: [{ q: '"""', multi: true, raw: true }, DQ, SQ],
    kw: 'as break class continue do else for fun if in interface is object package return super this throw try typealias typeof val var when while by catch constructor delegate dynamic field file finally get import init param property receiver set setparam where abstract annotation companion const crossinline data enum external final infix inline inner internal lateinit noinline open operator out override private protected public reified sealed suspend tailrec vararg',
    types: 'Int Long Short Byte Float Double Boolean Char String Any Unit Nothing List Map Set Array',
    consts: 'true false null',
    meta: [['@', Y(String.raw`@[A-Za-z_][\w.]*`)]],
  },
  csharp: {
    ...cLike, preproc: true, caps: true,
    str: [{ q: '"""', multi: true }, DQ, SQ],
    kw: 'abstract as base break case catch checked class const continue default delegate do else enum event explicit extern finally fixed for foreach goto if implicit in interface internal is lock namespace new operator out override params private protected public readonly ref return sealed sizeof stackalloc static struct switch this throw try typeof unchecked unsafe using virtual volatile while async await var record init get set',
    types: 'bool byte char decimal double float int long object sbyte short string uint ulong ushort void dynamic',
    consts: 'true false null',
  },
  php: {
    line: ['//', '#'], block: [['/*', '*/']], str: [{ q: '"', interp: 'var' }, SQ], caps: true,
    nocase: false,
    kw: 'abstract and as break case catch class clone const continue declare default do echo else elseif empty enddeclare endfor endforeach endif endswitch endwhile extends final finally fn for foreach function global goto if implements include include_once instanceof insteadof interface isset list match namespace new or print private protected public readonly require require_once return static switch throw trait try unset use var while xor yield',
    types: 'int float string bool array object void mixed callable iterable self parent never',
    consts: 'true false null TRUE FALSE NULL',
    meta: [['#', Y(String.raw`#\[[^\]\n]*\]`)], ['<?', Y(String.raw`<\?(?:php|=)?|\?>`)]],
    vhooks: [['$', Y(String.raw`\$[A-Za-z_]\w*`)]],
  },
  ruby: {
    line: ['#'], str: [DQ, SQ, { q: '`' }],
    kw: 'alias and begin break case class def defined? do else elsif end ensure for if in module next not or redo rescue retry return self super then undef unless until when while yield require require_relative include extend attr_accessor attr_reader attr_writer private protected public lambda proc puts raise loop',
    consts: 'true false nil',
    caps: true,
    vhooks: [['@', Y(String.raw`@@?[A-Za-z_]\w*`)], [':', Y(String.raw`:[A-Za-z_]\w*[?!]?`)]],
  },
  lua: {
    line: ['--'], block: [['--[[', ']]']], str: [{ q: '[[', multi: true, raw: true }, DQ, SQ],
    kw: 'and break do else elseif end for function goto if in local not or repeat return then until while',
    consts: 'true false nil',
    vars: 'self',
  },
  shell: {
    line: ['#'], hashPrev: true,
    str: [{ q: '"', interp: 'var' }, { q: "'", raw: true }, { q: '`', raw: true }],
    kw: 'if then else elif fi for while until do done case esac in function select time return exit break continue local export readonly declare typeset unset source alias set shift trap eval exec cd echo',
    consts: 'true false',
    vhooks: [['$', SHELL_VAR]],
  },
  sql: {
    line: ['--'], block: [['/*', '*/']], str: [SQ, DQ, { q: '`' }], nocase: true,
    kw: 'select from where and or not in is as on join inner left right full outer cross group by order having limit offset insert into values update set delete create alter drop table index view database schema add column primary key foreign references unique default constraint union all distinct case when then else end exists between like ilike with returning begin commit rollback grant revoke if cascade asc desc using explain',
    types: 'int integer bigint smallint tinyint serial varchar char text boolean bool date time timestamp timestamptz datetime decimal numeric float double real json jsonb uuid blob bytea',
    consts: 'true false null',
  },
  gomod: {
    line: ['//'], str: [DQ, { q: '`', raw: true }], nonum: true, nofn: true,
    kw: 'module go require replace exclude retract toolchain godebug use',
  },
  conf: {
    line: ['#'], hashPrev: true, str: [DQ, SQ], consts: 'on off true false yes no', nofn: true,
    vhooks: [['$', Y(String.raw`\$(?:\{[^}\n]*\}|\w+)`)]],
    lineStart: (code, i) => {
      const m = /[A-Za-z_][\w.-]*/y
      m.lastIndex = i
      const r = m.exec(code)
      return r ? ['keyword', r[0].length] : null
    },
  },
  dockerfile: {
    line: ['#'], hashPrev: true, nofn: true,
    str: [{ q: '"', interp: 'var' }, SQ],
    kw: 'FROM RUN CMD LABEL MAINTAINER EXPOSE ENV ADD COPY ENTRYPOINT VOLUME USER WORKDIR ARG ONBUILD STOPSIGNAL HEALTHCHECK SHELL AS',
    vhooks: [['$', Y(String.raw`\$(?:\{[^}\n]*\}|\w+)`)]],
  },
  makefile: {
    line: ['#'], hashPrev: true, str: [DQ, SQ], nofn: true,
    kw: 'ifeq ifneq ifdef ifndef else endif include sinclude -include define endef export unexport override vpath',
    vhooks: [['$', Y(String.raw`\$(?:\([^)\n]*\)|\{[^}\n]*\}|[@<^?*%+|$a-zA-Z])`)]],
    lineStart: (code, i) => {
      const a = /[A-Za-z_][\w.-]*(?=[ \t]*[:+?!]?=)/y
      a.lastIndex = i
      let r = a.exec(code)
      if (r) return ['property', r[0].length]
      const t = /[^\s#:=$\t][^:=#\n]*?(?=::?(?!=))/y
      t.lastIndex = i
      r = t.exec(code)
      return r && !/^(ifeq|ifneq|ifdef|ifndef|include|define|export)\b/.test(r[0]) ? ['function', r[0].trimEnd().length] : null
    },
  },
  ignore: { line: ['#'], hashPrev: true, nonum: true, nofn: true },
  ini: { line: ['#', ';'], str: [DQ], consts: 'true false on off yes no', nofn: true },
  toml: { line: ['#'], str: [{ q: '"""', multi: true }, { q: "'''", multi: true, raw: true }, DQ, { q: "'", raw: true }], consts: 'true false inf nan', nofn: true },
}

// ---------------------------------------------------------------------------
// CSS / SCSS / Less

const PROP_END = /[;{}]/g

function scanCss(code: string, emit: Emit, slashComments: boolean): void {
  const n = code.length
  let i = 0
  let depth = 0
  let paren = 0
  let inValue = false
  let inAt = false
  const re = (r: RegExp, at: number) => ((r.lastIndex = at), r.exec(code))
  const NUMBER = Y(String.raw`[+-]?(?:\d*\.)?\d+(?:[eE][+-]?\d+)?(?:%|[a-zA-Z]+)?`)
  const WORD = Y(String.raw`-{0,2}[A-Za-z_ -￿][\w -￿-]*|--[\w-]+`)
  const HEX = Y(String.raw`#[0-9a-fA-F]{3,8}(?![\w-])`)

  while (i < n) {
    const ch = code.charAt(i)
    if (isSpace(ch)) {
      i++
      continue
    }
    if (code.startsWith('/*', i) || (slashComments && code.startsWith('//', i))) {
      const block = code.charAt(i + 1) === '*'
      let e = block ? code.indexOf('*/', i + 2) : code.indexOf('\n', i)
      e = e < 0 ? n : block ? e + 2 : e
      emit(i, e, 'comment')
      i = e
      continue
    }
    if (ch === '"' || ch === "'") {
      let j = i + 1
      while (j < n && code.charAt(j) !== ch && code.charAt(j) !== '\n') j += code.charAt(j) === '\\' ? 2 : 1
      j = Math.min(code.charAt(j) === ch ? j + 1 : j, n)
      emit(i, j, 'string')
      i = j
      continue
    }
    if (ch === '@') {
      const m = re(Y(String.raw`@[\w-]+`), i)
      if (m) {
        emit(i, i + m[0].length, 'keyword')
        inAt = true
        i += m[0].length
        continue
      }
    }
    if (ch === '$' && slashComments) {
      const m = re(Y(String.raw`\$[\w-]+`), i)
      if (m) {
        emit(i, i + m[0].length, 'variable')
        i += m[0].length
        continue
      }
    }
    if (ch === '{' || ch === '}') {
      depth = ch === '{' ? depth + 1 : Math.max(0, depth - 1)
      inValue = false
      inAt = false
      emit(i, i + 1, 'punct')
      i++
      continue
    }
    if (ch === ';') {
      inValue = false
      inAt = false
      emit(i, i + 1, 'punct')
      i++
      continue
    }
    if (ch === '(' || ch === ')') {
      paren = ch === '(' ? paren + 1 : Math.max(0, paren - 1)
      emit(i, i + 1, 'punct')
      i++
      continue
    }
    if (ch === '[' || ch === ']' || ch === ',') {
      emit(i, i + 1, 'punct')
      i++
      continue
    }
    if (ch === '#') {
      const m = re(HEX, i)
      if (m && (inValue || inAt)) {
        emit(i, i + m[0].length, 'number')
        i += m[0].length
        continue
      }
      const id = re(Y(String.raw`#[\w-]+`), i)
      if (id) {
        emit(i, i + id[0].length, inValue || inAt ? 'plain' : 'type')
        i += id[0].length
        continue
      }
    }
    if (ch === '.' && !inValue && !inAt && /[A-Za-z_-]/.test(code.charAt(i + 1))) {
      const m = re(Y(String.raw`\.[\w-]+`), i)
      if (m) {
        emit(i, i + m[0].length, 'type')
        i += m[0].length
        continue
      }
    }
    if (ch === '!') {
      const m = re(Y(String.raw`!\s*(?:important|default)`), i)
      if (m) {
        emit(i, i + m[0].length, 'keyword')
        i += m[0].length
        continue
      }
    }
    if (/\d/.test(ch) || ((ch === '.' || ch === '-' || ch === '+') && /[\d.]/.test(code.charAt(i + 1)) && (inValue || inAt || paren > 0))) {
      const m = re(NUMBER, i)
      if (m) {
        emit(i, i + m[0].length, 'number')
        i += m[0].length
        continue
      }
    }
    if (ch === ':' && !inValue && !inAt) {
      const m = re(Y(String.raw`::?[\w-]+`), i)
      if (m) {
        emit(i, i + m[0].length, 'attr')
        i += m[0].length
        continue
      }
    }
    if (/[A-Za-z_\- -￿]/.test(ch)) {
      const m = re(WORD, i)
      if (m) {
        const word = m[0]
        const j = i + word.length
        let k = j
        while (code.charAt(k) === ' ' || code.charAt(k) === '\t') k++
        let isProp = false
        if (code.charAt(k) === ':' && code.charAt(k + 1) !== ':' && !inValue) {
          if (paren > 0 && inAt) isProp = true
          else if (depth > 0 && !inAt && paren === 0) {
            PROP_END.lastIndex = k + 1
            const e = PROP_END.exec(code)
            isProp = !e || e[0] !== '{'
          }
        }
        if (isProp) {
          emit(i, j, 'property')
          emit(k, k + 1, 'punct')
          inValue = true
          i = k + 1
          continue
        }
        if (code.charAt(j) === '(') emit(i, j, 'function')
        else if (word.startsWith('--')) emit(i, j, 'variable')
        else if (!inValue && !inAt) emit(i, j, 'tag')
        i = j
        continue
      }
    }
    if ('>+~*=|&:'.includes(ch)) {
      emit(i, i + 1, 'operator')
      i++
      continue
    }
    i++
  }
}

// ---------------------------------------------------------------------------
// Markup (HTML / XML / Vue)

function scanMarkup(code: string, emit: Emit, lang: string): void {
  const n = code.length
  const find = /[<&]/g
  const ENT = Y(String.raw`&(?:#\d+|#[xX][\da-fA-F]+|[A-Za-z][\w]*);`)
  const OPEN = Y(String.raw`<(/?)([A-Za-z][\w:.-]*)`)
  const ATTR = Y(String.raw`[^\s=<>/"']+`)
  const UNQ = Y(String.raw`[^\s"'<>=\x60]+`)
  let i = 0
  while (i < n) {
    find.lastIndex = i
    const m = find.exec(code)
    if (!m) break
    const p = m.index
    if (code.charAt(p) === '&') {
      ENT.lastIndex = p
      const e = ENT.exec(code)
      if (e) emit(p, p + e[0].length, 'constant')
      i = p + (e ? e[0].length : 1)
      continue
    }
    if (code.startsWith('<!--', p)) {
      const e = code.indexOf('-->', p + 4)
      const end = e < 0 ? n : e + 3
      emit(p, end, 'comment')
      i = end
      continue
    }
    if (code.startsWith('<![CDATA[', p)) {
      const e = code.indexOf(']]>', p)
      const end = e < 0 ? n : e + 3
      emit(p, end, 'string')
      i = end
      continue
    }
    const nx = code.charAt(p + 1)
    if (nx === '!' || nx === '?') {
      const e = code.indexOf('>', p)
      const end = e < 0 ? n : e + 1
      emit(p, end, 'meta')
      i = end
      continue
    }
    OPEN.lastIndex = p
    const o = OPEN.exec(code)
    if (!o) {
      i = p + 1
      continue
    }
    const closing = o[1] === '/'
    emit(p, p + 1 + o[1].length, 'punct')
    emit(p + 1 + o[1].length, p + o[0].length, 'tag')
    let j = p + o[0].length
    let selfClose = false
    let ended = false
    let afterEq = false
    while (j < n) {
      const c = code.charAt(j)
      if (isSpace(c)) {
        j++
        continue
      }
      if (c === '>') {
        emit(j, j + 1, 'punct')
        j++
        ended = true
        break
      }
      if (c === '/') {
        if (code.charAt(j + 1) === '>') {
          emit(j, j + 2, 'punct')
          j += 2
          selfClose = true
          ended = true
          break
        }
        emit(j, j + 1, 'punct')
        j++
        continue
      }
      if (c === '<') break
      if (c === '=') {
        emit(j, j + 1, 'operator')
        j++
        afterEq = true
        continue
      }
      if (c === '"' || c === "'") {
        const e = code.indexOf(c, j + 1)
        if (e < 0) {
          j++
          afterEq = false
          continue
        }
        emit(j, e + 1, 'string')
        j = e + 1
        afterEq = false
        continue
      }
      if (afterEq) {
        UNQ.lastIndex = j
        const u = UNQ.exec(code)
        afterEq = false
        if (u) {
          emit(j, j + u[0].length, 'string')
          j += u[0].length
          continue
        }
      }
      ATTR.lastIndex = j
      const a = ATTR.exec(code)
      if (!a) {
        j++
        continue
      }
      emit(j, j + a[0].length, 'attr')
      j += a[0].length
    }
    i = j
    const name = o[2].toLowerCase()
    if (lang === 'html' && ended && !closing && !selfClose && (name === 'script' || name === 'style')) {
      const e = code.toLowerCase().indexOf('</' + name, j)
      const end = e < 0 ? n : e
      if (end > j) {
        const sub = code.slice(j, end)
        const inner: Tok[] = []
        tokenize(sub, name === 'script' ? 'javascript' : 'css', (s, en, k) => inner.push([s + j, en + j, k]))
        for (const t of inner) emit(t[0], t[1], t[2])
      }
      i = end
    }
  }
}

// ---------------------------------------------------------------------------
// YAML

const YAML_KEY = Y(String.raw`(?:"(?:[^"\\\n]|\\.)*"|'[^'\n]*'|[^\s#:'"\[\]{},&*!|>%@\x60-][^#\n]*?)(?=[ \t]*:(?:[ \t]|$))`)
const YAML_NUM = /^[-+]?(?:\d[\d_]*\.?\d*(?:[eE][-+]?\d+)?|0x[\da-fA-F]+|0o[0-7]+|\.inf|\.nan)$/i
const YAML_CONST = /^(?:true|false|yes|no|on|off|null|~)$/i

function scanYaml(code: string, emit: Emit, startAt = 0, endAt = code.length): void {
  let pos = startAt
  let blockIndent = -1
  while (pos < endAt) {
    let eol = code.indexOf('\n', pos)
    if (eol < 0 || eol > endAt) eol = endAt
    let ce = eol
    if (ce > pos && code.charAt(ce - 1) === '\r') ce--
    let k = pos
    while (k < ce && (code.charAt(k) === ' ' || code.charAt(k) === '\t')) k++
    const indent = k - pos
    const next = eol + 1
    if (k >= ce) {
      pos = next
      continue
    }
    if (blockIndent >= 0) {
      if (indent > blockIndent) {
        emit(k, ce, 'string')
        pos = next
        continue
      }
      blockIndent = -1
    }
    if (code.charAt(k) === '#') {
      emit(k, ce, 'comment')
      pos = next
      continue
    }
    if (indent === 0 && (code.startsWith('---', k) || code.startsWith('...', k)) && (k + 3 >= ce || code.charAt(k + 3) === ' ')) {
      emit(k, k + 3, 'meta')
      k += 3
    }
    while (code.charAt(k) === '-' && (k + 1 >= ce || code.charAt(k + 1) === ' ')) {
      emit(k, k + 1, 'punct')
      k++
      while (code.charAt(k) === ' ') k++
    }
    const line = code.slice(k, ce)
    if (yamlLine(line, k, emit)) blockIndent = indent
    pos = next
  }
}

/** Highlights one YAML line (without leading dash); returns true if it opens a block scalar. */
function yamlLine(line: string, base: number, emit: Emit): boolean {
  const n = line.length
  let p = 0
  let flow = 0
  let block = false
  YAML_KEY.lastIndex = 0
  const km = YAML_KEY.exec(line)
  if (km) {
    emit(base, base + km[0].length, km[0][0] === '"' || km[0][0] === "'" ? 'property' : 'property')
    p = km[0].length
    while (line.charAt(p) === ' ' || line.charAt(p) === '\t') p++
    emit(base + p, base + p + 1, 'punct')
    p++
  }
  while (p < n) {
    const c = line.charAt(p)
    if (c === ' ' || c === '\t') {
      p++
      continue
    }
    if (c === '#' && (p === 0 || /\s/.test(line.charAt(p - 1)))) {
      emit(base + p, base + n, 'comment')
      break
    }
    if (c === '"' || c === "'") {
      let j = p + 1
      while (j < n && line.charAt(j) !== c) j += c === '"' && line.charAt(j) === '\\' ? 2 : 1
      j = Math.min(j + 1, n)
      let kind = 'string'
      if (flow > 0) {
        let k = j
        while (line.charAt(k) === ' ') k++
        if (line.charAt(k) === ':') kind = 'property'
      }
      emit(base + p, base + j, kind)
      p = j
      continue
    }
    if (c === '&' || c === '*') {
      const m = /[&*][^\s,[\]{}]+/y
      m.lastIndex = p
      const r = m.exec(line)
      if (r) {
        emit(base + p, base + p + r[0].length, 'variable')
        p += r[0].length
        continue
      }
    }
    if (c === '!') {
      const m = /![^\s,[\]{}]*/y
      m.lastIndex = p
      const r = m.exec(line)
      if (r) {
        emit(base + p, base + p + r[0].length, 'meta')
        p += r[0].length
        continue
      }
    }
    if (c === '[' || c === '{') {
      flow++
      emit(base + p, base + p + 1, 'punct')
      p++
      continue
    }
    if (c === ']' || c === '}') {
      flow = Math.max(0, flow - 1)
      emit(base + p, base + p + 1, 'punct')
      p++
      continue
    }
    if (c === ',' || (c === ':' && (p + 1 >= n || line.charAt(p + 1) === ' '))) {
      emit(base + p, base + p + 1, 'punct')
      p++
      continue
    }
    if ((c === '|' || c === '>') && flow === 0) {
      const m = /[|>][+-]?\d?[+-]?/y
      m.lastIndex = p
      const r = m.exec(line)
      if (r) {
        emit(base + p, base + p + r[0].length, 'operator')
        p += r[0].length
        block = /^\s*(?:#.*)?$/.test(line.slice(p))
        continue
      }
    }
    // Plain scalar
    let end: number
    if (flow > 0) {
      const m = /[^\s,[\]{}#]+/y
      m.lastIndex = p
      const r = m.exec(line)
      end = r ? p + r[0].length : p + 1
    } else {
      const m = /\s#/g
      m.lastIndex = p
      const r = m.exec(line)
      end = r ? r.index : n
    }
    const text = line.slice(p, end).trimEnd()
    const e = p + text.length
    let kind: string | null = null
    if (flow > 0) {
      let k = e
      while (line.charAt(k) === ' ') k++
      if (line.charAt(k) === ':') kind = 'property'
    }
    if (!kind) kind = YAML_CONST.test(text) ? 'constant' : YAML_NUM.test(text) ? 'number' : null
    if (kind) emit(base + p, base + e, kind)
    p = Math.max(e, p + 1)
  }
  return block
}

// ---------------------------------------------------------------------------
// INI / TOML

function scanIni(code: string, emit: Emit, lang: string): void {
  const n = code.length
  const valueP = prep(lang + ':value', CFG[lang])
  const KEY = Y(String.raw`[^=:\s#;\[][^=:#\n]*?(?=[ \t]*[=:])`)
  const SECTION = Y(String.raw`\[\[?[^\]\n]*\]\]?`)
  const sub = (a: number, b: number) => {
    if (b <= a) return
    scanGeneric(code.slice(a, b), valueP, (s, e, k) => emit(s + a, e + a, k))
  }
  let pos = 0
  while (pos < n) {
    let eol = code.indexOf('\n', pos)
    if (eol < 0) eol = n
    let k = pos
    while (k < eol && (code.charAt(k) === ' ' || code.charAt(k) === '\t')) k++
    const c = code.charAt(k)
    if (k >= eol) {
      pos = eol + 1
      continue
    }
    if (c === '#' || c === ';') {
      emit(k, eol, 'comment')
      pos = eol + 1
      continue
    }
    if (c === '[') {
      SECTION.lastIndex = k
      const s = SECTION.exec(code)
      if (s) {
        emit(k, k + s[0].length, 'type')
        sub(k + s[0].length, eol)
        pos = eol + 1
        continue
      }
    }
    KEY.lastIndex = k
    const km = KEY.exec(code)
    let vs = k
    if (km && k + km[0].length < eol) {
      emit(k, k + km[0].length, 'property')
      let s = k + km[0].length
      while (code.charAt(s) === ' ' || code.charAt(s) === '\t') s++
      emit(s, s + 1, 'operator')
      vs = s + 1
      while (vs < eol && (code.charAt(vs) === ' ' || code.charAt(vs) === '\t')) vs++
      if (lang === 'toml') {
        const q = code.startsWith('"""', vs) ? '"""' : code.startsWith("'''", vs) ? "'''" : ''
        if (q) {
          const e = code.indexOf(q, vs + 3)
          const end = e < 0 ? n : e + 3
          emit(vs, end, 'string')
          const nl = code.indexOf('\n', end)
          const lineEnd = nl < 0 ? n : nl
          sub(end, lineEnd)
          pos = lineEnd + 1
          continue
        }
      }
    }
    sub(vs, eol)
    pos = eol + 1
  }
}

// ---------------------------------------------------------------------------
// Diff

function scanDiff(code: string, emit: Emit): void {
  const n = code.length
  let pos = 0
  while (pos < n) {
    let eol = code.indexOf('\n', pos)
    if (eol < 0) eol = n
    const c = code.charAt(pos)
    let kind: string | null = null
    if (code.startsWith('@@', pos)) kind = 'meta'
    else if (code.startsWith('+++', pos) || code.startsWith('---', pos) || code.startsWith('diff ', pos) || code.startsWith('index ', pos)) kind = 'heading'
    else if (c === '+') kind = 'string'
    else if (c === '-') kind = 'regexp'
    else if (c === '\\') kind = 'comment'
    if (kind && eol > pos) emit(pos, eol, kind)
    pos = eol + 1
  }
}

// ---------------------------------------------------------------------------
// Markdown

const MD_LINK = Y(String.raw`!?\[[^\]\n]*\]\([^)\n]*\)`)
const MD_URL = Y(String.raw`https?://[^\s<>)\]]+`)
const MD_AUTO = Y(String.raw`<(?:https?://|mailto:)[^>\s]+>`)
const MD_TAG = Y(String.raw`</?[A-Za-z][^<>\n]*>`)
const alnum = (c: string) => /[A-Za-z0-9]/.test(c)

function mdInline(s: string, base: number, emit: Emit): void {
  const n = s.length
  let i = 0
  while (i < n) {
    const c = s.charAt(i)
    if (c === '\\') {
      i += 2
      continue
    }
    if (c === '`') {
      let j = i
      while (s.charAt(j) === '`') j++
      const run = s.slice(i, j)
      const e = s.indexOf(run, j)
      if (e >= 0) {
        emit(base + i, base + e + run.length, 'code')
        i = e + run.length
      } else i = j
      continue
    }
    if (c === '[' || (c === '!' && s.charAt(i + 1) === '[')) {
      MD_LINK.lastIndex = i
      const m = MD_LINK.exec(s)
      if (m) {
        emit(base + i, base + i + m[0].length, 'link')
        i += m[0].length
        continue
      }
    }
    if (c === '*' || c === '_') {
      const prev = s.charAt(i - 1)
      const canOpen = c === '*' || !alnum(prev)
      if (canOpen && s.charAt(i + 1) === c && s.charAt(i + 2) !== '' && !isSpace(s.charAt(i + 2))) {
        const e = s.indexOf(c + c, i + 3)
        if (e > 0 && !isSpace(s.charAt(e - 1)) && (c === '*' || !alnum(s.charAt(e + 2)))) {
          emit(base + i, base + e + 2, 'strong')
          i = e + 2
          continue
        }
      } else if (canOpen && s.charAt(i + 1) !== c && s.charAt(i + 1) !== '' && !isSpace(s.charAt(i + 1))) {
        let e = s.indexOf(c, i + 2)
        while (e > 0 && s.charAt(e + 1) === c) e = s.indexOf(c, e + 2)
        if (e > 0 && !isSpace(s.charAt(e - 1)) && (c === '*' || !alnum(s.charAt(e + 1)))) {
          emit(base + i, base + e + 1, 'emphasis')
          i = e + 1
          continue
        }
      }
      while (s.charAt(i) === c) i++
      continue
    }
    if (c === '<') {
      MD_AUTO.lastIndex = i
      let m = MD_AUTO.exec(s)
      if (m) {
        emit(base + i, base + i + m[0].length, 'link')
        i += m[0].length
        continue
      }
      MD_TAG.lastIndex = i
      m = MD_TAG.exec(s)
      if (m) {
        emit(base + i, base + i + m[0].length, 'tag')
        i += m[0].length
        continue
      }
    }
    if (c === 'h' && s.startsWith('http', i) && !alnum(s.charAt(i - 1))) {
      MD_URL.lastIndex = i
      const m = MD_URL.exec(s)
      if (m) {
        emit(base + i, base + i + m[0].length, 'link')
        i += m[0].length
        continue
      }
    }
    if (c === '|') emit(base + i, base + i + 1, 'punct')
    i++
  }
}

function scanMarkdown(code: string, emit: Emit): void {
  const n = code.length
  let pos = 0
  let fence = ''
  // YAML front matter
  if (code.startsWith('---\n') || code.startsWith('---\r\n')) {
    const m = /\n---[ \t]*(?:\r?\n|$)/g
    m.lastIndex = 3
    const r = m.exec(code)
    if (r) {
      emit(0, 3, 'meta')
      scanYaml(code, emit, code.indexOf('\n') + 1, r.index + 1)
      const end = r.index + 1
      emit(end, end + 3, 'meta')
      pos = end + 3
    }
  }
  while (pos < n) {
    let eol = code.indexOf('\n', pos)
    if (eol < 0) eol = n
    let ce = eol
    if (ce > pos && code.charAt(ce - 1) === '\r') ce--
    const line = code.slice(pos, ce)
    const next = eol + 1
    if (fence) {
      const t = line.trim()
      if (t.startsWith(fence) && /^(.)\1*$/.test(t) && t.length >= fence.length) fence = ''
      if (ce > pos) emit(pos, ce, 'code')
      pos = next
      continue
    }
    let k = 0
    while (line.charAt(k) === ' ' && k < 3) k++
    const fm = /^(`{3,}|~{3,})/.exec(line.slice(k))
    if (fm && !(fm[1][0] === '`' && line.slice(k + fm[1].length).includes('`'))) {
      fence = fm[1]
      emit(pos, ce, 'code')
      pos = next
      continue
    }
    if (/^#{1,6}(?:\s|$)/.test(line.slice(k))) {
      emit(pos + k, ce, 'heading')
      pos = next
      continue
    }
    if (/^([-*_])(?: *\1){2,} *$/.test(line.slice(k))) {
      emit(pos + k, ce, 'punct')
      pos = next
      continue
    }
    if (/^\s*\|?\s*:?-{2,}:?\s*(?:\|\s*:?-{2,}:?\s*)*\|?\s*$/.test(line) && line.includes('|')) {
      emit(pos, ce, 'punct')
      pos = next
      continue
    }
    const ref = /^ {0,3}\[[^\]]+\]:\s*\S+/.exec(line)
    if (ref) {
      emit(pos, pos + ref[0].length, 'link')
      mdInline(line.slice(ref[0].length), pos + ref[0].length, emit)
      pos = next
      continue
    }
    k = 0
    while (line.charAt(k) === ' ') k++
    while (line.charAt(k) === '>') {
      emit(pos + k, pos + k + 1, 'punct')
      k++
      if (line.charAt(k) === ' ') k++
    }
    const lm = /^[ \t]*(?:[-*+]|\d{1,9}[.)])[ \t]+/.exec(line.slice(k))
    if (lm) {
      const ms = k + lm[0].search(/\S/)
      const me = k + lm[0].trimEnd().length
      emit(pos + ms, pos + me, 'punct')
      k += lm[0].length
      const task = /^\[[ xX]\](?=\s)/.exec(line.slice(k))
      if (task) {
        emit(pos + k, pos + k + 3, 'punct')
        k += 3
      }
    }
    mdInline(line.slice(k), pos + k, emit)
    pos = next
  }
}

// ---------------------------------------------------------------------------
// Dispatch and rendering

function tokenize(code: string, lang: string, emit: Emit): void {
  switch (lang) {
    case 'typescript': {
      const base = CFG.javascript
      const p = prep('typescript', {
        ...base,
        kw: base.kw + ' interface type enum implements namespace declare readonly abstract public private protected as is keyof infer satisfies override module',
        types: base.types + ' string number boolean any unknown never object bigint symbol Partial Readonly Pick Omit',
      })
      scanGeneric(code, p, emit)
      return
    }
    case 'css':
      return scanCss(code, emit, false)
    case 'scss':
    case 'less':
      return scanCss(code, emit, true)
    case 'html':
    case 'xml':
      return scanMarkup(code, emit, lang)
    case 'yaml':
      return scanYaml(code, emit)
    case 'toml':
    case 'ini':
      return scanIni(code, emit, lang)
    case 'markdown':
      return scanMarkdown(code, emit)
    case 'diff':
      return scanDiff(code, emit)
    default: {
      const cfg = CFG[lang]
      if (cfg) scanGeneric(code, prep(lang, cfg), emit)
    }
  }
}

function wrapSpan(text: string, kind: string): string {
  const open = '<span class="tok-' + kind + '">'
  if (text.indexOf('\n') < 0) return open + escapeHTML(text) + '</span>'
  let out = ''
  let from = 0
  for (;;) {
    const nl = text.indexOf('\n', from)
    const part = nl < 0 ? text.slice(from) : text.slice(from, nl)
    if (part) out += open + escapeHTML(part) + '</span>'
    if (nl < 0) return out
    out += '\n'
    from = nl + 1
  }
}

/**
 * HTML for `code`: every input character appears exactly once, HTML-escaped,
 * tokens wrapped in <span class="tok-KIND">. Spans never contain '\n'.
 */
export function highlightToHTML(code: string, lang: string | null): string {
  if (!lang || !code) return escapeHTML(code)
  const parts: string[] = []
  let pos = 0
  tokenize(code, lang, (s0, e, kind) => {
    const s = s0 < pos ? pos : s0
    if (e <= s || kind === 'plain') return
    if (s > pos) parts.push(escapeHTML(code.slice(pos, s)))
    parts.push(wrapSpan(code.slice(s, e), kind))
    pos = e
  })
  parts.push(escapeHTML(code.slice(pos)))
  return parts.join('')
}

/** One balanced HTML string per source line (exactly code.split('\n').length entries). */
export function highlightLines(code: string, lang: string | null): string[] {
  return highlightToHTML(code, lang).split('\n')
}
