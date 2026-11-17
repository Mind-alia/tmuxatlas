import { ArrowDown, Eraser, Pause, Play, Search } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button } from '../../ui'
import { fetchLogs } from './api'
import { InlineError } from './ContainersList'
import type { DockerLogLine } from './types'

export const LOGS_POLL_MS = 2000
/** Lines kept in the browser; older ones scroll away. */
export const MAX_CLIENT_LINES = 5000
const FIRST_TAIL = 500

function lineKey(line: DockerLogLine) {
  return `${line.t ?? ''}\u0000${line.s}\u0000${line.m}`
}

/** Adds a batch, skipping the lines a since boundary may send twice. */
export function mergeLines(previous: DockerLogLine[], batch: DockerLogLine[]): DockerLogLine[] {
  if (batch.length === 0) return previous
  const tail = new Set(previous.slice(-50).map(lineKey))
  const fresh = batch.filter(line => !line.t || !tail.has(lineKey(line)))
  const next = previous.concat(fresh)
  return next.length > MAX_CLIENT_LINES ? next.slice(next.length - MAX_CLIENT_LINES) : next
}

function clock(t?: string) {
  if (!t) return ''
  const date = new Date(t)
  if (Number.isNaN(date.getTime())) return ''
  return `${date.toLocaleDateString('fr-FR', { day: '2-digit', month: '2-digit' })} ${date.toLocaleTimeString('fr-FR', { hour: '2-digit', minute: '2-digit', second: '2-digit' })}`
}

function Highlight({ text, query }: { text: string; query: string }) {
  if (!query) return <>{text}</>
  const lower = text.toLowerCase()
  const needle = query.toLowerCase()
  const parts: { text: string; hit: boolean }[] = []
  let at = 0
  for (let i = lower.indexOf(needle); i >= 0 && parts.length < 200; i = lower.indexOf(needle, at)) {
    if (i > at) parts.push({ text: text.slice(at, i), hit: false })
    parts.push({ text: text.slice(i, i + needle.length), hit: true })
    at = i + needle.length
  }
  parts.push({ text: text.slice(at), hit: false })
  return (
    <>
      {parts.map((part, i) =>
        part.hit ? (
          <mark key={i} className="rounded-sm bg-warning/40 text-foreground">
            {part.text}
          </mark>
        ) : (
          <span key={i}>{part.text}</span>
        ),
      )}
    </>
  )
}

/** Live tail of a container's logs: polls with since while visible. */
export function LogsPanel({ containerId }: { containerId: string }) {
  const [lines, setLines] = useState<DockerLogLine[]>([])
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [skipped, setSkipped] = useState(false)
  const [follow, setFollow] = useState(true)
  const [paused, setPaused] = useState(false)
  const [timestamps, setTimestamps] = useState(true)
  const [filter, setFilter] = useState('')
  const sinceRef = useRef<string | undefined>(undefined)
  const scrollRef = useRef<HTMLDivElement>(null)

  const poll = useCallback(
    async (signal: AbortSignal) => {
      const first = sinceRef.current === undefined
      try {
        const result = await fetchLogs(containerId, first ? { tail: FIRST_TAIL } : { tail: 2000, since: sinceRef.current }, signal)
        if (signal.aborted) return
        const batch = result.lines ?? []
        setLines(previous => (first ? batch.slice(-MAX_CLIENT_LINES) : mergeLines(previous, batch)))
        sinceRef.current = result.next_since ?? sinceRef.current ?? ''
        if (!first && result.truncated) setSkipped(true)
        setError(null)
        setLoaded(true)
      } catch (err) {
        if (signal.aborted) return
        setError(err instanceof Error ? err.message : 'Lecture des journaux impossible')
        setLoaded(true)
      }
    },
    [containerId],
  )

  useEffect(() => {
    sinceRef.current = undefined
    setLines([])
    setLoaded(false)
    setSkipped(false)
  }, [containerId])

  useEffect(() => {
    if (paused) return
    // Same loop as the containers list: one request at a time, none while
    // hidden, and a generation so a visibility change never doubles it.
    const hidden = () => document.visibilityState === 'hidden'
    let generation = 0
    let stopped = false
    let timer: ReturnType<typeof setTimeout> | null = null
    let controller: AbortController | null = null
    const run = async (loop: number) => {
      if (stopped || loop !== generation || hidden()) return
      controller?.abort()
      controller = new AbortController()
      await poll(controller.signal)
      if (!stopped && loop === generation && !hidden()) timer = setTimeout(() => void run(loop), LOGS_POLL_MS)
    }
    const onVisibility = () => {
      generation++
      if (timer) clearTimeout(timer)
      timer = null
      if (hidden()) controller?.abort()
      else void run(generation)
    }
    void run(generation)
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      stopped = true
      generation++
      if (timer) clearTimeout(timer)
      controller?.abort()
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [poll, paused])

  const shown = useMemo(() => {
    const q = filter.trim().toLowerCase()
    return q ? lines.filter(line => line.m.toLowerCase().includes(q)) : lines
  }, [lines, filter])

  useEffect(() => {
    if (!follow) return
    const element = scrollRef.current
    if (element) element.scrollTop = element.scrollHeight
  }, [shown, follow])

  const onScroll = () => {
    const element = scrollRef.current
    if (!element) return
    const atBottom = element.scrollHeight - element.scrollTop - element.clientHeight < 24
    if (atBottom !== follow) setFollow(atBottom)
  }

  return (
    <div className="flex min-h-0 flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <label className="relative flex min-w-0 flex-1 basis-48 items-center sm:max-w-xs">
          <span className="sr-only">Filtrer les journaux</span>
          <Search size={14} aria-hidden className="pointer-events-none absolute left-2.5 text-muted-foreground" />
          <input
            type="search"
            value={filter}
            onChange={event => setFilter(event.target.value)}
            placeholder="Filtrer les journaux"
            className="ui-focus-ring h-9 w-full min-w-0 rounded-md border border-border bg-input pl-8 pr-2 text-xs text-foreground placeholder:text-muted-foreground"
          />
        </label>
        <label className="flex min-h-9 cursor-pointer items-center gap-1.5 text-xs">
          <input type="checkbox" checked={follow} onChange={event => setFollow(event.target.checked)} className="accent-[var(--primary)]" />
          Défilement auto
        </label>
        <label className="flex min-h-9 cursor-pointer items-center gap-1.5 text-xs">
          <input type="checkbox" checked={timestamps} onChange={event => setTimestamps(event.target.checked)} className="accent-[var(--primary)]" />
          Horodatage
        </label>
        <div className="ml-auto flex items-center gap-1">
          <Button
            size="sm"
            variant="ghost"
            leadingIcon={paused ? <Play size={14} aria-hidden /> : <Pause size={14} aria-hidden />}
            onClick={() => setPaused(value => !value)}
            aria-pressed={paused}
          >
            {paused ? 'Reprendre' : 'Pause'}
          </Button>
          <Button size="sm" variant="ghost" leadingIcon={<Eraser size={14} aria-hidden />} onClick={() => {
            setLines([])
            setSkipped(false)
          }}>
            Effacer
          </Button>
        </div>
      </div>
      {error && <InlineError message={error} />}
      {skipped && (
        <p className="text-2xs text-warning">Des lignes ont été sautées : le conteneur écrit plus vite que l’affichage.</p>
      )}
      <div className="relative min-h-0">
        <div
          ref={scrollRef}
          onScroll={onScroll}
          role="log"
          aria-label="Journaux du conteneur"
          aria-live="off"
          tabIndex={0}
          className="ui-focus-ring h-[min(60vh,36rem)] overflow-auto rounded-lg border border-border bg-muted p-2 font-mono text-[11px] leading-[1.45]"
        >
          {!loaded ? (
            <p className="text-muted-foreground">Chargement…</p>
          ) : shown.length === 0 ? (
            <p className="text-muted-foreground">{lines.length === 0 ? 'Aucune ligne pour l’instant.' : 'Aucune ligne ne correspond au filtre.'}</p>
          ) : (
            shown.map((line, i) => (
              <div key={i} className="flex min-w-0 gap-2 whitespace-pre-wrap break-all" data-stream={line.s}>
                {timestamps && line.t && <span className="shrink-0 select-none text-muted-foreground">{clock(line.t)}</span>}
                {line.s === 'err' && (
                  <span className="shrink-0 select-none font-sans text-2xs font-medium leading-[1.6] text-destructive" title="Sortie d’erreur (stderr)">
                    err
                  </span>
                )}
                <span className="min-w-0 text-foreground">
                  <Highlight text={line.m} query={filter.trim()} />
                </span>
              </div>
            ))
          )}
        </div>
        {!follow && shown.length > 0 && (
          <Button
            size="sm"
            variant="secondary"
            className="absolute bottom-3 right-4 shadow-[var(--elevation-md)]"
            leadingIcon={<ArrowDown size={14} aria-hidden />}
            onClick={() => setFollow(true)}
          >
            Suivre
          </Button>
        )}
      </div>
      <p className="text-2xs text-muted-foreground">
        {paused ? 'En pause.' : 'Actualisé toutes les 2 s.'} {lines.length} ligne{lines.length > 1 ? 's' : ''} en mémoire
        {lines.length >= MAX_CLIENT_LINES ? ' (les plus anciennes sont retirées)' : ''}.
      </p>
    </div>
  )
}
