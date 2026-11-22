import { createContext, useContext, useEffect, useMemo, useRef, useState, type MouseEvent, type ReactNode } from 'react'
import { Orbit } from 'lucide-react'
import { cn } from '../../lib/utils'

/** An unfinished sub-agent as GET /api/agent-teams/summary lists it. */
export interface SummaryAgent {
  id: string
  description: string
  action: string
  state: 'running' | 'quiet'
}

/** The sub-agents of the Claude Code sessions that run in one tmux session of the hub. */
export interface AgentSessionSummary {
  tmux_session: string
  running: number
  recent_done: number
  total: number
  agents: SummaryAgent[]
}

export const SUMMARY_POLL_MS = 5000
/** Sent when the Agents page is asked for a session while it may already be open. */
export const AGENTS_SESSION_EVENT = 'atlas:agents-session'

const NO_SESSIONS: AgentSessionSummary[] = []

/**
 * Polls the summary while `enabled` and the tab is visible. It never reports an
 * error: a refusal (401, 403) or a hub without the route (404) ends the polling,
 * anything else is retried at the next tick.
 */
export function useAgentSummaryPoll(enabled: boolean): AgentSessionSummary[] {
  const [sessions, setSessions] = useState(NO_SESSIONS)

  useEffect(() => {
    if (!enabled) {
      setSessions(NO_SESSIONS)
      return
    }
    let cancelled = false
    let busy = false
    let previous = ''
    let timer: ReturnType<typeof setInterval> | null = null
    const stop = () => {
      if (timer !== null) clearInterval(timer)
      timer = null
    }
    const end = () => {
      cancelled = true
      stop()
      document.removeEventListener('visibilitychange', onVisibility)
    }
    const fetchOnce = async () => {
      if (busy || cancelled) return
      busy = true
      try {
        const res = await fetch('/api/agent-teams/summary', { cache: 'no-store' })
        if (cancelled) return
        if (res.status === 401 || res.status === 403 || res.status === 404) {
          end()
          setSessions(NO_SESSIONS)
          return
        }
        if (!res.ok) return
        const text = await res.text()
        if (cancelled || text === previous) return
        const body = JSON.parse(text) as { sessions?: AgentSessionSummary[] }
        previous = text
        setSessions(Array.isArray(body.sessions) ? body.sessions : NO_SESSIONS)
      } catch {
        // Keep what is shown; the next tick tries again.
      } finally {
        busy = false
      }
    }
    const start = () => {
      if (timer === null) timer = setInterval(() => void fetchOnce(), SUMMARY_POLL_MS)
    }
    function onVisibility() {
      if (document.hidden) stop()
      else {
        void fetchOnce()
        start()
      }
    }
    document.addEventListener('visibilitychange', onVisibility)
    if (!document.hidden) {
      void fetchOnce()
      start()
    }
    return end
  }, [enabled])

  return sessions
}

interface AgentSummaryValue {
  localHostId: string | null
  bySession: ReadonlyMap<string, AgentSessionSummary>
  open: (session: string) => void
}

const AgentSummaryContext = createContext<AgentSummaryValue>({
  localHostId: null,
  bySession: new Map(),
  open: () => {},
})

/**
 * The single poll of the app. Sub-agents are only read on the hub, so only the
 * sessions of `localHostId` get a summary.
 */
export function AgentSummaryProvider({
  enabled,
  localHostId,
  onOpen,
  children,
}: {
  enabled: boolean
  localHostId: string | null
  /** Shows the Agents page; the URL already names the session. */
  onOpen: () => void
  children: ReactNode
}) {
  const sessions = useAgentSummaryPoll(enabled)
  const onOpenRef = useRef(onOpen)
  useEffect(() => {
    onOpenRef.current = onOpen
  }, [onOpen])
  const value = useMemo<AgentSummaryValue>(
    () => ({
      localHostId,
      bySession: new Map(sessions.map(summary => [summary.tmux_session, summary])),
      open: session => {
        const url = `/agents?session=${encodeURIComponent(session)}`
        if (window.location.pathname + window.location.search !== url) window.history.pushState(null, '', url)
        window.dispatchEvent(new CustomEvent(AGENTS_SESSION_EVENT, { detail: session }))
        onOpenRef.current()
      },
    }),
    [localHostId, sessions],
  )
  return <AgentSummaryContext.Provider value={value}>{children}</AgentSummaryContext.Provider>
}

/** The summary of a tmux session, or null when it has no known sub-agent. */
export function useSessionAgents(hostId: string | undefined, session: string) {
  const { localHostId, bySession, open } = useContext(AgentSummaryContext)
  const summary = localHostId !== null && hostId === localHostId ? (bySession.get(session) ?? null) : null
  return { summary, open }
}

const plural = (count: number) => (count > 1 ? 's' : '')

/**
 * A pill that opens the Agents page on the session. `count` shows the agents in
 * progress, or the ones finished within the hour when none is; `link` is the
 * shortcut of a session view, shown as soon as the session has sub-agents.
 */
export function SessionAgentsBadge({
  hostId,
  session,
  variant = 'count',
  className,
}: {
  hostId: string | undefined
  session: string
  variant?: 'count' | 'link'
  className?: string
}) {
  const { summary, open } = useSessionAgents(hostId, session)
  if (!summary) return null
  const { running, recent_done: done } = summary
  if (variant === 'count' && running === 0 && done === 0) return null
  if (variant === 'link' && summary.total === 0) return null
  const state =
    running > 0
      ? `${running} sous-agent${plural(running)} en cours`
      : done > 0
        ? `${done} sous-agent${plural(done)} terminé${plural(done)}`
        : 'aucun sous-agent en cours'
  const onClick = (event: MouseEvent) => {
    event.stopPropagation()
    open(session)
  }
  return (
    <button
      type="button"
      data-testid="session-agents-badge"
      aria-label={`Agents de la session ${session} : ${state}`}
      title={`${state} : ouvrir la page Agents`}
      onClick={onClick}
      className={cn(
        'ui-focus-ring relative inline-flex h-6 shrink-0 items-center gap-1 whitespace-nowrap rounded-full px-2 text-2xs font-medium tabular-nums after:absolute after:-inset-2',
        running > 0
          ? 'bg-primary/15 text-primary hover:bg-primary/25'
          : 'border border-border text-muted-foreground hover:bg-secondary hover:text-foreground',
        className,
      )}
    >
      <Orbit size={12} aria-hidden="true" />
      {variant === 'link' ? (
        <>
          <span className="hidden md:inline">Agents</span>
          {running > 0 && <span>{running}</span>}
        </>
      ) : running > 0 ? (
        `${running} agent${plural(running)}`
      ) : (
        `${done} terminé${plural(done)}`
      )}
    </button>
  )
}

/** How many agents in progress a session row lists before « +N ». */
const SIDEBAR_AGENT_LINES = 3

/** Under a session of the sidebar: its sub-agents in progress and what each one is doing. */
export function SidebarSessionAgents({ hostId, session }: { hostId: string; session: string }) {
  const { summary, open } = useSessionAgents(hostId, session)
  if (!summary || summary.agents.length === 0) return null
  const shown = summary.agents.slice(0, SIDEBAR_AGENT_LINES)
  const more = summary.agents.length - shown.length
  return (
    <button
      type="button"
      data-testid="sidebar-session-agents"
      title="Ouvrir la page Agents sur cette session"
      onClick={() => open(session)}
      className="ui-focus-ring ml-3 mt-0.5 flex w-[calc(100%-0.75rem)] min-w-0 flex-col rounded border-l border-sidebar-border py-0.5 pl-2 pr-1.5 text-left text-2xs leading-4 text-muted-foreground hover:bg-sidebar-accent"
    >
      <span className="sr-only">Sous-agents en cours de {session} : </span>
      {shown.map(agent => (
        <span key={agent.id} className="flex w-full min-w-0 items-center gap-1.5">
          <span
            aria-hidden="true"
            className={cn('size-1.5 shrink-0 rounded-full', agent.state === 'quiet' ? 'bg-warning' : 'bg-primary')}
          />
          <span className="max-w-[55%] shrink-0 truncate font-medium text-sidebar-foreground">
            {agent.description || 'Sous-agent'}
          </span>
          <span className="min-w-0 flex-1 truncate">
            {agent.action || (agent.state === 'quiet' ? 'silencieux' : 'démarre')}
          </span>
        </span>
      ))}
      {more > 0 && (
        <span className="pl-3">
          +{more}
          <span className="sr-only"> autre{plural(more)}</span>
        </span>
      )}
    </button>
  )
}
