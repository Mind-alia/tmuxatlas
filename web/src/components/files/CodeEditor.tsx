import {
  forwardRef,
  memo,
  useCallback,
  useDeferredValue,
  useEffect,
  useImperativeHandle,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
} from 'react'
import { CaseSensitive, ChevronDown, ChevronRight, ChevronUp, Regex, Replace, ReplaceAll, WholeWord, X } from 'lucide-react'
import { cn } from '../../lib/utils'
import { commentToken, escapeHTML, highlightLines } from './highlight'
import {
  backspace,
  countLines,
  deleteLines,
  duplicateLines,
  enter,
  escapeRegExp,
  findMatches,
  indent,
  insertMarks,
  lineEndAt,
  lineIndexAt,
  lineStartAt,
  moveLines,
  offsetOfLine,
  outdent,
  replaceAllEdit,
  replacementFor,
  smartHome,
  toggleComment,
  typeCharacter,
  visualWidth,
  type Edit,
  type FindOptions,
  type MarkRange,
  type Match,
} from './editorCommands'

export interface CursorInfo {
  line: number
  column: number
  selected: number
  lines: number
}

export interface CodeEditorHandle {
  focus(): void
  revealLine(line: number): void
}

export interface CodeEditorProps {
  value: string
  onChange: (value: string) => void
  language: string | null
  readOnly?: boolean
  wrap?: boolean
  tabSize?: number
  insertSpaces?: boolean
  onSave?: () => void
  onCursorChange?: (info: CursorInfo) => void
  className?: string
  ariaLabel?: string
}

const LINE_HEIGHT = 20
const FONT_SIZE = 13
const PAD_TOP = 4
const PAD_LEFT = 12
// Extra right padding on the rendered code (not the textarea) so the textarea
// is always wider than its longest line and never scrolls internally.
const CODE_PAD_RIGHT = 48
const BOTTOM_SPACE = LINE_HEIGHT * 6
const HIGHLIGHT_MAX_BYTES = 400_000
const HIGHLIGHT_MAX_LINES = 20_000
const PLAIN_MAX_BYTES = 2_000_000
const PLAIN_MAX_LINES = 50_000
const DECORATE_MAX = 5000
const ZWSP = '​'

type PanelMode = 'find' | 'replace' | 'goto'

const EDITOR_CSS = `
.ce-textarea::selection { background: color-mix(in oklch, var(--primary) 32%, transparent); color: transparent; -webkit-text-fill-color: transparent; }
.ce-textarea::-moz-selection { background: color-mix(in oklch, var(--primary) 32%, transparent); color: transparent; }
.ce-rows mark { background: color-mix(in oklch, var(--warning) 28%, transparent); color: inherit; border-radius: 2px; }
.ce-rows mark.ce-cur { background: color-mix(in oklch, var(--warning) 55%, transparent); box-shadow: 0 0 0 1px var(--warning); }
`

const textMetrics = {
  fontFamily: 'var(--font-mono)',
  fontSize: FONT_SIZE,
  lineHeight: `${LINE_HEIGHT}px`,
  fontVariantLigatures: 'none',
  letterSpacing: 'normal',
} as const

interface RowProps {
  index: number
  html: string
  current: boolean
  wrap: boolean
  gutterWidth: string
}

const Row = memo(function Row({ index, html, current, wrap, gutterWidth }: RowProps) {
  return (
    <div
      className="flex"
      data-line={index}
      style={current ? { background: 'color-mix(in oklch, var(--foreground) 5%, transparent)' } : undefined}
    >
      <div
        data-gutter=""
        className={cn(
          'sticky left-0 z-[2] shrink-0 cursor-default select-none bg-card pr-3 pl-2 text-right tabular-nums',
          current ? 'text-foreground' : 'text-muted-foreground/70',
        )}
        style={{ width: gutterWidth, ...(current ? { backgroundImage: 'linear-gradient(color-mix(in oklch, var(--foreground) 5%, transparent), color-mix(in oklch, var(--foreground) 5%, transparent))' } : null) }}
      >
        {index + 1}
      </div>
      <div
        className={cn('min-w-0 flex-1 text-foreground', wrap ? 'whitespace-pre-wrap [overflow-wrap:anywhere]' : 'whitespace-pre')}
        style={{ paddingLeft: PAD_LEFT, paddingRight: wrap ? 0 : CODE_PAD_RIGHT }}
        dangerouslySetInnerHTML={{ __html: html || ZWSP }}
      />
    </div>
  )
})

function splitLines(text: string): string[] {
  return text.split('\n')
}

// Groups matches into per-line mark ranges (columns relative to each line).
function marksByLine(text: string, matches: Match[], current: number): Map<number, MarkRange[]> {
  const map = new Map<number, MarkRange[]>()
  let line = 0
  let lineStart = 0
  const limit = Math.min(matches.length, DECORATE_MAX)
  const add = (l: number, r: MarkRange) => {
    const list = map.get(l)
    if (list) list.push(r)
    else map.set(l, [r])
  }
  for (let i = 0; i < limit; i++) {
    const m = matches[i]
    let nl = text.indexOf('\n', lineStart)
    while (nl !== -1 && nl < m.start) {
      line++
      lineStart = nl + 1
      nl = text.indexOf('\n', lineStart)
    }
    // A match may span lines: split it into one segment per line.
    let segStart = m.start
    let l = line
    let ls = lineStart
    while (segStart < m.end) {
      const le = lineEndAt(text, segStart)
      const segEnd = Math.min(m.end, le)
      if (segEnd > segStart) add(l, { start: segStart - ls, end: segEnd - ls, current: i === current })
      if (segEnd >= m.end) break
      segStart = le + 1
      ls = segStart
      l++
    }
  }
  return map
}

export const CodeEditor = forwardRef<CodeEditorHandle, CodeEditorProps>(function CodeEditor(
  {
    value,
    onChange,
    language,
    readOnly = false,
    wrap: wrapProp = false,
    tabSize = 2,
    insertSpaces = true,
    onSave,
    onCursorChange,
    className,
    ariaLabel,
  },
  ref,
) {
  const scrollerRef = useRef<HTMLDivElement>(null)
  const rowsRef = useRef<HTMLDivElement>(null)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const measureRef = useRef<HTMLSpanElement>(null)
  const charWidthRef = useRef(7.8)
  const rafRef = useRef(0)
  const revealPendingRef = useRef(false)
  const tabEscapeRef = useRef(false)

  const [caretLine, setCaretLine] = useState(0)
  const [panel, setPanel] = useState<PanelMode | null>(null)
  const [panelFocusTick, setPanelFocusTick] = useState(0)
  const [query, setQuery] = useState('')
  const [replacement, setReplacement] = useState('')
  const [findOpts, setFindOpts] = useState<FindOptions>({ caseSensitive: false, regex: false, wholeWord: false })
  const [current, setCurrent] = useState(-1)
  const [gotoText, setGotoText] = useState('')
  const searchFromRef = useRef(0)
  const searchPendingRef = useRef(false)

  const lineCount = useMemo(() => countLines(value), [value])
  const plain = value.length > PLAIN_MAX_BYTES || lineCount > PLAIN_MAX_LINES
  const wrap = wrapProp && !plain
  const canHighlight = value.length <= HIGHLIGHT_MAX_BYTES && lineCount <= HIGHLIGHT_MAX_LINES
  const digits = Math.max(2, String(lineCount).length)
  const gutterWidth = `calc(${digits}ch + 20px)`

  // --- Highlighted rows ---------------------------------------------------
  // The deferred value is highlighted in the background; rows whose text does
  // not match the live value yet fall back to escaped plain text, so the layer
  // always stays aligned with the textarea.
  const deferredValue = useDeferredValue(value)
  const highlighted = useMemo(() => {
    if (plain || !canHighlight) return null
    const raw = splitLines(deferredValue)
    let html: string[]
    try {
      html = highlightLines(deferredValue, language)
    } catch {
      html = raw.map(escapeHTML)
    }
    return { raw, html }
  }, [deferredValue, language, plain, canHighlight])

  const liveLines = useMemo(() => (plain ? null : splitLines(value)), [value, plain])

  const findActive = panel === 'find' || panel === 'replace'
  const findResult = useMemo(
    () => (findActive ? findMatches(value, query, findOpts) : { matches: [] as Match[] }),
    [findActive, value, query, findOpts],
  )
  const matches = findResult.matches
  const currentIndex = matches.length === 0 ? -1 : Math.min(Math.max(current, 0), matches.length - 1)

  const marks = useMemo(
    () => (plain || matches.length === 0 ? null : marksByLine(value, matches, currentIndex)),
    [plain, value, matches, currentIndex],
  )

  const rowsHtml = useMemo(() => {
    if (!liveLines) return null
    return liveLines.map((line, i) => {
      let html: string
      if (highlighted && highlighted.raw[i] === line && highlighted.html[i] !== undefined) html = highlighted.html[i]
      else html = escapeHTML(line)
      const ranges = marks?.get(i)
      return ranges ? insertMarks(html, ranges) : html
    })
  }, [liveLines, highlighted, marks])

  const gutterText = useMemo(() => {
    if (!plain) return ''
    const parts = new Array<string>(lineCount)
    for (let i = 0; i < lineCount; i++) parts[i] = String(i + 1)
    return parts.join('\n')
  }, [plain, lineCount])

  // --- Measurement & scrolling -------------------------------------------
  useLayoutEffect(() => {
    const span = measureRef.current
    if (!span) return
    const w = span.getBoundingClientRect().width / 100
    if (w > 0) charWidthRef.current = w
  }, [])

  const gutterPx = useCallback(() => digits * charWidthRef.current + 20, [digits])

  const revealRange = useCallback(
    (pos: number, center = false) => {
      const scroller = scrollerRef.current
      const ta = textareaRef.current
      if (!scroller || !ta) return
      const text = ta.value
      const line = lineIndexAt(text, pos)
      let top = PAD_TOP + line * LINE_HEIGHT
      let height = LINE_HEIGHT
      if (wrap) {
        const row = rowsRef.current?.children[line] as HTMLElement | undefined
        if (row) {
          top = row.offsetTop
          height = row.offsetHeight
        }
      }
      const viewH = scroller.clientHeight
      if (center) {
        scroller.scrollTop = Math.max(0, top - viewH / 3)
      } else if (top < scroller.scrollTop) {
        scroller.scrollTop = top
      } else if (top + height > scroller.scrollTop + viewH) {
        scroller.scrollTop = Math.min(top, top + height - viewH)
      }
      if (!wrap) {
        const lineStart = lineStartAt(text, pos)
        const col = visualWidth(text.slice(lineStart, pos), tabSize)
        const cw = charWidthRef.current
        const gutter = gutterPx()
        const x = gutter + PAD_LEFT + col * cw
        const viewW = scroller.clientWidth
        if (x - cw * 4 < scroller.scrollLeft + gutter) {
          scroller.scrollLeft = Math.max(0, x - gutter - PAD_LEFT - cw * 8)
        } else if (x + cw * 2 > scroller.scrollLeft + viewW) {
          scroller.scrollLeft = x + cw * 8 - viewW
        }
      }
    },
    [wrap, tabSize, gutterPx],
  )

  const onCursorChangeRef = useRef(onCursorChange)
  onCursorChangeRef.current = onCursorChange

  const syncCursor = useCallback(() => {
    rafRef.current = 0
    const ta = textareaRef.current
    if (!ta) return
    const { selectionStart: start, selectionEnd: end, value: text } = ta
    const head = ta.selectionDirection === 'backward' ? start : end
    const line = lineIndexAt(text, head)
    setCaretLine(line)
    if (revealPendingRef.current) {
      revealPendingRef.current = false
      revealRange(head)
    }
    onCursorChangeRef.current?.({
      line: line + 1,
      column: head - lineStartAt(text, head) + 1,
      selected: Math.abs(end - start),
      lines: countLines(text),
    })
  }, [revealRange])

  const scheduleSync = useCallback(
    (reveal: boolean) => {
      if (reveal) revealPendingRef.current = true
      if (rafRef.current) return
      if (typeof requestAnimationFrame === 'function') rafRef.current = requestAnimationFrame(syncCursor)
      else syncCursor()
    },
    [syncCursor],
  )

  useEffect(() => () => {
    if (rafRef.current && typeof cancelAnimationFrame === 'function') cancelAnimationFrame(rafRef.current)
  }, [])

  // Report initial cursor info and line count changes.
  useEffect(() => {
    scheduleSync(false)
  }, [lineCount, scheduleSync])

  const setSelection = useCallback(
    (start: number, end: number, reveal = true, center = false) => {
      const ta = textareaRef.current
      if (!ta) return
      ta.setSelectionRange(start, end)
      if (reveal) revealRange(end, center)
      scheduleSync(false)
    },
    [revealRange, scheduleSync],
  )

  useImperativeHandle(
    ref,
    () => ({
      focus() {
        textareaRef.current?.focus({ preventScroll: true })
      },
      revealLine(line: number) {
        const ta = textareaRef.current
        if (!ta) return
        const pos = offsetOfLine(ta.value, Math.max(0, line - 1))
        setSelection(pos, pos, true, true)
      },
    }),
    [setSelection],
  )

  // --- Editing ------------------------------------------------------------
  const applyEdit = useCallback(
    (edit: Edit) => {
      const ta = textareaRef.current
      if (!ta || readOnly) return
      const old = ta.value
      const changes = edit.insert !== '' || edit.from !== edit.to
      const prevFocus = document.activeElement as HTMLElement | null
      if (changes) {
        const expected = old.slice(0, edit.from) + edit.insert + old.slice(edit.to)
        if (document.activeElement !== ta) ta.focus({ preventScroll: true })
        ta.setSelectionRange(edit.from, edit.to)
        let ok = false
        try {
          ok = typeof document.execCommand === 'function' &&
            document.execCommand(edit.insert === '' ? 'delete' : 'insertText', false, edit.insert)
        } catch {
          ok = false
        }
        if (!ok || ta.value !== expected) {
          // Fallback (no execCommand): direct splice, then notify React.
          if (ta.value !== old) ta.setRangeText(expected, 0, ta.value.length)
          else ta.setRangeText(edit.insert, edit.from, edit.to, 'end')
          ta.dispatchEvent(new Event('input', { bubbles: true }))
        }
        // execCommand needs focus on the textarea; give it back (e.g. to the replace input).
        if (prevFocus && prevFocus !== ta && prevFocus !== document.body) prevFocus.focus({ preventScroll: true })
      }
      ta.setSelectionRange(edit.selStart, edit.selEnd)
      revealPendingRef.current = true
      scheduleSync(true)
    },
    [readOnly, scheduleSync],
  )

  const state = () => {
    const ta = textareaRef.current!
    return { text: ta.value, start: ta.selectionStart, end: ta.selectionEnd }
  }

  // --- Find panel -----------------------------------------------------------
  const openPanel = useCallback(
    (mode: PanelMode) => {
      const ta = textareaRef.current
      const next: PanelMode = mode === 'replace' && readOnly ? 'find' : mode
      if (ta && next !== 'goto') {
        searchFromRef.current = ta.selectionStart
        const selected = ta.value.slice(ta.selectionStart, ta.selectionEnd)
        if (selected && !selected.includes('\n') && selected.length < 200) {
          setQuery(findOpts.regex ? escapeRegExp(selected) : selected)
        }
        searchPendingRef.current = true
      }
      if (next === 'goto') setGotoText('')
      setPanel(next)
      setPanelFocusTick((t) => t + 1)
    },
    [readOnly, findOpts.regex],
  )

  const closePanel = useCallback(() => {
    setPanel(null)
    textareaRef.current?.focus({ preventScroll: true })
  }, [])

  // After a query/option change (not a text edit), jump to the match nearest
  // the search origin.
  useEffect(() => {
    if (!findActive || !searchPendingRef.current) return
    searchPendingRef.current = false
    if (matches.length === 0) return
    const idx = matches.findIndex((m) => m.start >= searchFromRef.current)
    const i = idx === -1 ? 0 : idx
    setCurrent(i)
    setSelection(matches[i].start, matches[i].end, true)
  }, [findActive, matches, setSelection])

  const updateQuery = (q: string) => {
    searchPendingRef.current = true
    setQuery(q)
  }
  const toggleOpt = (opt: keyof FindOptions) => {
    searchPendingRef.current = true
    setFindOpts((o) => ({ ...o, [opt]: !o[opt] }))
  }

  const step = useCallback(
    (dir: 1 | -1) => {
      if (matches.length === 0) return
      const next = (currentIndex + dir + matches.length) % matches.length
      searchFromRef.current = matches[next].start
      setCurrent(next)
      setSelection(matches[next].start, matches[next].end, true)
    },
    [matches, currentIndex, setSelection],
  )

  const replaceOne = useCallback(() => {
    if (readOnly || currentIndex < 0) return
    const ta = textareaRef.current
    if (!ta) return
    const m = matches[currentIndex]
    const insert = replacementFor(ta.value, m, query, replacement, findOpts)
    searchFromRef.current = m.start + insert.length
    applyEdit({ from: m.start, to: m.end, insert, selStart: m.start, selEnd: m.start + insert.length })
    // Select the next match after the replaced text, computed on the new text.
    const after = findMatches(ta.value, query, findOpts).matches
    if (after.length === 0) return
    const idx = after.findIndex((x) => x.start >= m.start + insert.length)
    const i = idx === -1 ? 0 : idx
    setCurrent(i)
    setSelection(after[i].start, after[i].end, true)
  }, [readOnly, currentIndex, matches, query, replacement, findOpts, applyEdit, setSelection])

  const replaceAll = useCallback(() => {
    if (readOnly) return
    const ta = textareaRef.current
    if (!ta) return
    const result = replaceAllEdit(ta.value, query, replacement, findOpts)
    if (!result) return
    applyEdit({ ...result.edit, selEnd: result.edit.selStart })
    setCurrent(0)
  }, [readOnly, query, replacement, findOpts, applyEdit])

  const goToLine = useCallback(() => {
    const ta = textareaRef.current
    const m = /^\s*(\d+)\s*(?:[:,]\s*(\d+))?\s*$/.exec(gotoText)
    if (!ta || !m) return
    const line = Math.min(Math.max(Number(m[1]), 1), countLines(ta.value))
    const start = offsetOfLine(ta.value, line - 1)
    const col = m[2] ? Math.max(Number(m[2]), 1) - 1 : 0
    const pos = Math.min(start + col, lineEndAt(ta.value, start))
    setPanel(null)
    ta.focus({ preventScroll: true })
    setSelection(pos, pos, true, true)
  }, [gotoText, setSelection])

  // --- Keyboard -------------------------------------------------------------
  const onRootKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const mod = e.ctrlKey || e.metaKey
    const key = e.key.length === 1 ? e.key.toLowerCase() : e.key
    if (mod && !e.altKey && key === 's') {
      e.preventDefault()
      onSave?.()
      return
    }
    if (mod && !e.altKey && !e.shiftKey && key === 'f') {
      e.preventDefault()
      openPanel('find')
      return
    }
    if ((mod && !e.altKey && key === 'h') || (e.ctrlKey && e.altKey && (key === 'f' || e.code === 'KeyF'))) {
      e.preventDefault()
      openPanel('replace')
      return
    }
    if (mod && !e.altKey && key === 'g') {
      e.preventDefault()
      openPanel('goto')
      return
    }
    if (key === 'F3') {
      e.preventDefault()
      if (!findActive) openPanel('find')
      else step(e.shiftKey ? -1 : 1)
      return
    }
    if (key === 'Escape' && panel) {
      e.preventDefault()
      e.stopPropagation()
      closePanel()
    }
  }

  const onTextareaKeyDown = (e: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    if (e.nativeEvent.isComposing || e.key === 'Process' || e.key === 'Unidentified') return
    const ta = e.currentTarget
    const mod = e.ctrlKey || e.metaKey
    const key = e.key.length === 1 ? e.key.toLowerCase() : e.key
    const opts = { tabSize, insertSpaces }

    if (key === 'Escape' && !panel) {
      // Escape releases the Tab trap for one keypress (keyboard accessibility).
      tabEscapeRef.current = true
      return
    }
    if (key === 'Tab' && tabEscapeRef.current) {
      tabEscapeRef.current = false
      return
    }
    tabEscapeRef.current = false

    if (key === 'Home' && !mod && !e.altKey) {
      e.preventDefault()
      const backward = ta.selectionDirection === 'backward'
      const head = backward ? ta.selectionStart : ta.selectionEnd
      const anchor = backward ? ta.selectionEnd : ta.selectionStart
      const target = smartHome(ta.value, head)
      if (e.shiftKey) {
        if (target < anchor) ta.setSelectionRange(target, anchor, 'backward')
        else ta.setSelectionRange(anchor, target, 'forward')
      } else ta.setSelectionRange(target, target)
      scheduleSync(true)
      return
    }

    if (readOnly) return

    let edit: Edit | null = null
    if (key === 'Tab' && !mod && !e.altKey) {
      edit = e.shiftKey ? outdent(state(), opts) : indent(state(), opts)
      e.preventDefault()
      if (!edit) return
    } else if (key === 'Enter' && !mod && !e.altKey && !e.shiftKey) {
      edit = enter(state(), { ...opts, language })
    } else if (key === 'Backspace' && !mod && !e.altKey && !e.shiftKey) {
      edit = backspace(state(), opts)
    } else if (mod && !e.altKey && (key === '/' || e.code === 'Slash' || e.code === 'NumpadDivide')) {
      e.preventDefault()
      edit = toggleComment(state(), commentToken(language))
      if (!edit) return
    } else if (mod && !e.altKey && !e.shiftKey && key === 'd') {
      edit = duplicateLines(state())
    } else if (e.altKey && !mod && (key === 'ArrowUp' || key === 'ArrowDown')) {
      e.preventDefault()
      edit = e.shiftKey ? duplicateLines(state()) : moveLines(state(), key === 'ArrowUp' ? 'up' : 'down')
      if (!edit) return
      if (e.shiftKey && key === 'ArrowUp') {
        // Copy up: keep the selection on the original (upper) copy.
        const s = state()
        edit = { ...edit, selStart: s.start, selEnd: s.end }
      }
    } else if (mod && e.shiftKey && !e.altKey && key === 'k') {
      edit = deleteLines(state())
    } else if (e.key.length === 1 && !e.metaKey && (!e.ctrlKey || e.altKey)) {
      // Allows AltGr / Option layouts (e.g. AZERTY braces).
      edit = typeCharacter(state(), e.key)
    }
    if (edit) {
      e.preventDefault()
      applyEdit(edit)
    }
  }

  const onGutterMouseDown = (e: ReactMouseEvent<HTMLDivElement>) => {
    const target = e.target as HTMLElement
    const gutter = target.closest('[data-gutter]')
    if (!gutter) return
    const ta = textareaRef.current
    if (!ta) return
    e.preventDefault()
    let line: number
    const row = gutter.closest('[data-line]') as HTMLElement | null
    if (row) line = Number(row.dataset.line)
    else {
      const rect = gutter.getBoundingClientRect()
      line = Math.floor((e.clientY - rect.top) / LINE_HEIGHT)
    }
    const start = offsetOfLine(ta.value, Math.max(0, line))
    const end = lineEndAt(ta.value, start)
    ta.focus({ preventScroll: true })
    setSelection(start, Math.min(end + 1, ta.value.length), false)
  }

  // Focus the panel input when the panel opens (or is re-requested).
  const findInputRef = useRef<HTMLInputElement>(null)
  const gotoInputRef = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (!panel) return
    const input = panel === 'goto' ? gotoInputRef.current : findInputRef.current
    input?.focus()
    input?.select()
  }, [panel, panelFocusTick])

  const onFindKeyDown = (e: ReactKeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      step(e.shiftKey ? -1 : 1)
    } else if (e.altKey && !e.ctrlKey && !e.metaKey) {
      const opt = e.code === 'KeyC' ? 'caseSensitive' : e.code === 'KeyR' ? 'regex' : e.code === 'KeyW' ? 'wholeWord' : null
      if (opt) {
        e.preventDefault()
        toggleOpt(opt)
      }
    }
  }

  const onReplaceKeyDown = (e: ReactKeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      if ((e.ctrlKey || e.metaKey) && e.altKey) replaceAll()
      else replaceOne()
    }
  }

  const textareaStyle = {
    ...textMetrics,
    left: gutterWidth,
    width: `calc(100% - ${gutterWidth})`,
    paddingTop: PAD_TOP,
    paddingLeft: PAD_LEFT,
    tabSize,
    MozTabSize: tabSize,
    caretColor: 'var(--foreground)',
    WebkitTextFillColor: 'transparent',
  } as React.CSSProperties

  const panelInput =
    'h-7 min-w-0 flex-1 rounded-sm border border-border bg-background px-2 text-xs text-foreground placeholder:text-muted-foreground ui-focus-ring'

  let counter: React.ReactNode = null
  if (findActive && query) {
    if (findResult.error) counter = <span className="text-destructive">Regex invalide</span>
    else if (matches.length === 0) counter = <span className="text-destructive">Aucun résultat</span>
    else counter = `${currentIndex + 1} / ${matches.length}${findResult.truncated ? '+' : ''}`
  }

  return (
    <div className={cn('relative h-full w-full overflow-hidden bg-card', className)} onKeyDown={onRootKeyDown}>
      <style>{EDITOR_CSS}</style>
      <div ref={scrollerRef} className="absolute inset-0 overflow-auto overscroll-contain">
        <div
          className="relative"
          style={{
            ...textMetrics,
            tabSize,
            minWidth: wrap ? undefined : 'max-content',
            width: wrap ? '100%' : undefined,
            minHeight: '100%',
            paddingBottom: BOTTOM_SPACE,
          }}
          onMouseDown={onGutterMouseDown}
        >
          <span
            ref={measureRef}
            aria-hidden="true"
            className="pointer-events-none invisible absolute top-0 left-0 whitespace-pre"
          >
            {'0'.repeat(100)}
          </span>
          {plain ? (
            <div aria-hidden="true" className="relative flex" style={{ paddingTop: PAD_TOP }}>
              <div
                className="pointer-events-none absolute right-0 left-0"
                style={{ top: PAD_TOP + caretLine * LINE_HEIGHT, height: LINE_HEIGHT, background: 'color-mix(in oklch, var(--foreground) 5%, transparent)' }}
              />
              <pre
                data-gutter=""
                className="sticky left-0 z-[2] m-0 shrink-0 cursor-default select-none bg-card pr-3 pl-2 text-right text-muted-foreground/70 tabular-nums"
                style={{ ...textMetrics, width: gutterWidth }}
              >
                {gutterText}
              </pre>
              <pre
                className="relative m-0 whitespace-pre text-foreground"
                style={{ ...textMetrics, paddingLeft: PAD_LEFT, paddingRight: CODE_PAD_RIGHT }}
              >
                {value || ZWSP}
              </pre>
            </div>
          ) : (
            <div ref={rowsRef} aria-hidden="true" className="ce-rows" style={{ paddingTop: PAD_TOP }}>
              {rowsHtml!.map((html, i) => (
                <Row key={i} index={i} html={html} current={i === caretLine} wrap={wrap} gutterWidth={gutterWidth} />
              ))}
            </div>
          )}
          <textarea
            ref={textareaRef}
            className={cn(
              'ce-textarea absolute top-0 z-[1] m-0 h-full resize-none overflow-hidden border-0 bg-transparent p-0 text-transparent outline-none',
              wrap ? 'whitespace-pre-wrap [overflow-wrap:anywhere]' : 'whitespace-pre',
            )}
            style={textareaStyle}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            onKeyDown={onTextareaKeyDown}
            onSelect={() => scheduleSync(true)}
            onInput={() => scheduleSync(true)}
            onKeyUp={() => scheduleSync(false)}
            onMouseUp={() => scheduleSync(false)}
            onFocus={() => scheduleSync(false)}
            onScroll={(e) => {
              // The outer container scrolls; keep the textarea itself pinned.
              const t = e.currentTarget
              if (t.scrollTop) t.scrollTop = 0
              if (t.scrollLeft) t.scrollLeft = 0
            }}
            readOnly={readOnly}
            wrap={wrap ? 'soft' : 'off'}
            spellCheck={false}
            autoCapitalize="off"
            autoCorrect="off"
            autoComplete="off"
            enterKeyHint="enter"
            aria-label={ariaLabel ?? 'Éditeur de texte'}
            aria-multiline="true"
            aria-readonly={readOnly || undefined}
          />
        </div>
      </div>

      {panel && (
        <div
          role="search"
          aria-label={panel === 'goto' ? 'Aller à la ligne' : 'Rechercher dans le fichier'}
          className="absolute top-2 right-3 z-10 flex w-[min(28rem,calc(100%-1.5rem))] flex-col gap-1 rounded-md border border-border bg-card p-1.5 text-xs shadow-lg"
        >
          {panel === 'goto' ? (
            <div className="flex items-center gap-1">
              <input
                ref={gotoInputRef}
                className={panelInput}
                inputMode="numeric"
                placeholder={`Aller à la ligne (1 – ${lineCount})`}
                aria-label="Aller à la ligne"
                value={gotoText}
                onChange={(e) => setGotoText(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault()
                    goToLine()
                  }
                }}
              />
              <PanelButton label="Fermer" onClick={closePanel}>
                <X size={14} />
              </PanelButton>
            </div>
          ) : (
            <>
              <div className="flex items-center gap-1">
                {!readOnly && (
                  <PanelButton
                    label={panel === 'replace' ? 'Masquer le remplacement' : 'Afficher le remplacement'}
                    onClick={() => setPanel(panel === 'replace' ? 'find' : 'replace')}
                  >
                    {panel === 'replace' ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                  </PanelButton>
                )}
                <input
                  ref={findInputRef}
                  className={cn(panelInput, findResult.error && 'border-destructive')}
                  placeholder="Rechercher"
                  aria-label="Rechercher"
                  value={query}
                  onChange={(e) => updateQuery(e.target.value)}
                  onKeyDown={onFindKeyDown}
                  spellCheck={false}
                  autoCapitalize="off"
                  autoCorrect="off"
                  autoComplete="off"
                />
                <PanelToggle
                  label="Respecter la casse"
                  pressed={!!findOpts.caseSensitive}
                  onClick={() => toggleOpt('caseSensitive')}
                >
                  <CaseSensitive size={15} />
                </PanelToggle>
                <PanelToggle
                  label="Expression régulière"
                  pressed={!!findOpts.regex}
                  onClick={() => toggleOpt('regex')}
                >
                  <Regex size={14} />
                </PanelToggle>
                <PanelToggle
                  label="Mot entier"
                  pressed={!!findOpts.wholeWord}
                  onClick={() => toggleOpt('wholeWord')}
                >
                  <WholeWord size={15} />
                </PanelToggle>
              </div>
              <div className="flex items-center gap-1">
                {panel === 'replace' && !readOnly ? (
                  <>
                    <span className="w-6 shrink-0" />
                    <input
                      className={panelInput}
                      placeholder="Remplacer"
                      aria-label="Remplacer"
                      value={replacement}
                      onChange={(e) => setReplacement(e.target.value)}
                      onKeyDown={onReplaceKeyDown}
                      spellCheck={false}
                      autoCapitalize="off"
                      autoCorrect="off"
                      autoComplete="off"
                    />
                    <PanelButton label="Remplacer" onClick={replaceOne} disabled={currentIndex < 0}>
                      <Replace size={14} />
                    </PanelButton>
                    <PanelButton label="Tout remplacer" onClick={replaceAll} disabled={matches.length === 0}>
                      <ReplaceAll size={14} />
                    </PanelButton>
                  </>
                ) : (
                  <span className="flex-1" />
                )}
                <span
                  role="status"
                  aria-live="polite"
                  className="min-w-[5.5rem] shrink-0 px-1 text-right whitespace-nowrap text-muted-foreground tabular-nums"
                >
                  {counter}
                </span>
                <PanelButton label="Résultat précédent" onClick={() => step(-1)} disabled={matches.length === 0}>
                  <ChevronUp size={14} />
                </PanelButton>
                <PanelButton label="Résultat suivant" onClick={() => step(1)} disabled={matches.length === 0}>
                  <ChevronDown size={14} />
                </PanelButton>
                <PanelButton label="Fermer" onClick={closePanel}>
                  <X size={14} />
                </PanelButton>
              </div>
            </>
          )}
        </div>
      )}
    </div>
  )
})

interface PanelButtonProps {
  label: string
  onClick: () => void
  disabled?: boolean
  children: React.ReactNode
}

function PanelButton({ label, onClick, disabled, children }: PanelButtonProps) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      disabled={disabled}
      onClick={onClick}
      className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-sm text-muted-foreground hover:bg-secondary hover:text-foreground disabled:pointer-events-none disabled:opacity-40 ui-focus-ring"
    >
      {children}
    </button>
  )
}

function PanelToggle({ label, pressed, onClick, children }: PanelButtonProps & { pressed: boolean }) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      aria-pressed={pressed}
      onClick={onClick}
      className={cn(
        'inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-sm ui-focus-ring',
        pressed ? 'bg-primary/20 text-primary' : 'text-muted-foreground hover:bg-secondary hover:text-foreground',
      )}
    >
      {children}
    </button>
  )
}
