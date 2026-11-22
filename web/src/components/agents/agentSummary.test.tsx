import { act, cleanup, fireEvent, render, renderHook, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Host } from '../../hooks/useHosts'
import type { Session } from '../../hooks/useSessions'
import { buildWorkspaceViewModel } from '../../workspace/model'
import { SessionCard } from '../SessionCard'
import { Sidebar } from '../Sidebar'
import {
  AGENTS_SESSION_EVENT,
  AgentSummaryProvider,
  SUMMARY_POLL_MS,
  SessionAgentsBadge,
  useAgentSummaryPoll,
  type AgentSessionSummary,
} from './agentSummary'

const SUMMARY_URL = '/api/agent-teams/summary'

function session(host: string, name: string): Session {
  return { id: `${host}-${name}`, host, host_online: true, name, created: '', attached: false, last_activity: '', windows: [] }
}

const busy: AgentSessionSummary = {
  tmux_session: 'chantier',
  running: 5,
  recent_done: 1,
  total: 7,
  agents: [
    { id: 'a1', description: 'Tests Go', action: 'Bash go test ./pkg/server/', state: 'running' },
    { id: 'a2', description: 'Relecture de la doc', action: 'Read docs/ops.md', state: 'running' },
    { id: 'a3', description: 'Longue compilation', action: '', state: 'quiet' },
    { id: 'a4', description: 'Quatrième', action: 'Grep TODO', state: 'running' },
    { id: 'a5', description: 'Cinquième', action: 'Grep FIXME', state: 'running' },
  ],
}
const finished: AgentSessionSummary = { tmux_session: 'calme', running: 0, recent_done: 2, total: 2, agents: [] }
const old: AgentSessionSummary = { tmux_session: 'ancienne', running: 0, recent_done: 0, total: 3, agents: [] }

/** Answers the summary route; every other request (session previews) gets a 404. */
function stubSummary(answer: () => Response) {
  const calls = { count: 0 }
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      if (String(input) !== SUMMARY_URL) return new Response('', { status: 404 })
      calls.count += 1
      return answer()
    }),
  )
  return calls
}

const json = (sessions: AgentSessionSummary[]) => () =>
  new Response(JSON.stringify({ sessions }), { status: 200, headers: { 'Content-Type': 'application/json' } })

function setHidden(hidden: boolean) {
  Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden })
  document.dispatchEvent(new Event('visibilitychange'))
}

const tick = (ms: number) => act(() => vi.advanceTimersByTimeAsync(ms))

beforeEach(() => {
  vi.useFakeTimers()
  window.history.replaceState(null, '', '/')
})

afterEach(() => {
  cleanup()
  setHidden(false)
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('useAgentSummaryPoll', () => {
  it('lit le résumé, puis le relit toutes les 5 secondes', async () => {
    const calls = stubSummary(json([busy]))
    const { result } = renderHook(() => useAgentSummaryPoll(true))
    await tick(0)
    expect(calls.count).toBe(1)
    expect(result.current).toEqual([busy])
    await tick(SUMMARY_POLL_MS - 1)
    expect(calls.count).toBe(1)
    await tick(1)
    expect(calls.count).toBe(2)
  })

  it('garde la même liste tant que la réponse ne change pas', async () => {
    stubSummary(json([busy]))
    const { result } = renderHook(() => useAgentSummaryPoll(true))
    await tick(0)
    const first = result.current
    await tick(SUMMARY_POLL_MS)
    expect(result.current).toBe(first)
  })

  it('se met en pause quand l’onglet est caché et reprend à son retour', async () => {
    const calls = stubSummary(json([busy]))
    renderHook(() => useAgentSummaryPoll(true))
    await tick(0)
    setHidden(true)
    await tick(SUMMARY_POLL_MS * 4)
    expect(calls.count).toBe(1)
    setHidden(false)
    await tick(0)
    expect(calls.count).toBe(2)
    await tick(SUMMARY_POLL_MS)
    expect(calls.count).toBe(3)
  })

  it('ne sonde rien pour un compte qui n’y a pas droit', async () => {
    const calls = stubSummary(json([busy]))
    const { result } = renderHook(() => useAgentSummaryPoll(false))
    await tick(SUMMARY_POLL_MS * 2)
    expect(calls.count).toBe(0)
    expect(result.current).toEqual([])
  })

  it.each([403, 404])('reste muet et cesse de sonder après un %i', async status => {
    const calls = stubSummary(() => new Response('accès interdit', { status }))
    const errors = vi.spyOn(console, 'error').mockImplementation(() => {})
    const { result } = renderHook(() => useAgentSummaryPoll(true))
    await tick(0)
    await tick(SUMMARY_POLL_MS * 3)
    expect(calls.count).toBe(1)
    expect(result.current).toEqual([])
    expect(errors).not.toHaveBeenCalled()
    errors.mockRestore()
  })

  it('garde le dernier résumé quand le hub ne répond plus, et réessaie', async () => {
    let fail = false
    const calls = stubSummary(() => {
      if (fail) throw new TypeError('network')
      return json([busy])()
    })
    const { result } = renderHook(() => useAgentSummaryPoll(true))
    await tick(0)
    fail = true
    await tick(SUMMARY_POLL_MS)
    expect(calls.count).toBe(2)
    expect(result.current).toEqual([busy])
  })
})

function renderWithSummary(sessions: AgentSessionSummary[], ui: React.ReactNode, onOpen = vi.fn()) {
  const calls = stubSummary(json(sessions))
  const view = render(
    <AgentSummaryProvider enabled localHostId="hub" onOpen={onOpen}>
      {ui}
    </AgentSummaryProvider>,
  )
  return { calls, onOpen, ...view }
}

describe('pastille de la carte de session', () => {
  const card = (name: string, host = 'hub', onSelect = vi.fn()) => (
    <SessionCard session={session(host, name)} events={[]} sparklines={false} onSelect={onSelect} />
  )

  it('compte les sous-agents en cours et ouvre la page Agents sans ouvrir le terminal', async () => {
    const onSelect = vi.fn()
    const requested = vi.fn()
    window.addEventListener(AGENTS_SESSION_EVENT, requested)
    const { onOpen } = renderWithSummary([busy], card('chantier', 'hub', onSelect))
    await tick(0)
    const badge = screen.getByRole('button', { name: 'Agents de la session chantier : 5 sous-agents en cours' })
    expect(badge).toHaveTextContent('5 agents')
    fireEvent.click(badge)
    window.removeEventListener(AGENTS_SESSION_EVENT, requested)
    expect(onSelect).not.toHaveBeenCalled()
    expect(onOpen).toHaveBeenCalledTimes(1)
    expect(requested).toHaveBeenCalledTimes(1)
    expect(window.location.pathname + window.location.search).toBe('/agents?session=chantier')
  })

  it('reste dans la carte à libérer, à côté du bouton', async () => {
    const onRelease = vi.fn()
    renderWithSummary(
      [busy],
      <SessionCard
        session={session('hub', 'chantier')}
        events={[]}
        sparklines={false}
        onSelect={vi.fn()}
        onRelease={onRelease}
      />,
    )
    await tick(0)
    expect(screen.getByTestId('session-agents-badge')).toHaveTextContent('5 agents')
    fireEvent.click(screen.getByRole('button', { name: 'Libérer' }))
    expect(onRelease).toHaveBeenCalledTimes(1)
  })

  it('devient discrète quand il ne reste que des terminés récents', async () => {
    renderWithSummary([finished], card('calme'))
    await tick(0)
    const badge = screen.getByRole('button', { name: 'Agents de la session calme : 2 sous-agents terminés' })
    expect(badge).toHaveTextContent('2 terminés')
    expect(badge.className).toContain('text-muted-foreground')
  })

  it('accorde le singulier', async () => {
    renderWithSummary([{ ...busy, running: 1, agents: busy.agents.slice(0, 1) }], card('chantier'))
    await tick(0)
    expect(screen.getByTestId('session-agents-badge')).toHaveTextContent(/^1 agent$/)
  })

  it('n’apparaît ni sans sous-agent récent, ni pour la session du même nom sur un autre hôte', async () => {
    renderWithSummary(
      [busy, old],
      <>
        {card('ancienne')}
        {card('sans-agent')}
        {card('chantier', 'pair')}
      </>,
    )
    await tick(0)
    expect(screen.queryByTestId('session-agents-badge')).not.toBeInTheDocument()
  })

  it('sert de raccourci « Agents » dans la vue d’une session dès qu’elle a des sous-agents', async () => {
    const { onOpen } = renderWithSummary(
      [busy, old],
      <>
        <SessionAgentsBadge hostId="hub" session="ancienne" variant="link" />
        <SessionAgentsBadge hostId="hub" session="sans-agent" variant="link" />
      </>,
    )
    await tick(0)
    const links = screen.getAllByTestId('session-agents-badge')
    expect(links).toHaveLength(1)
    expect(links[0]).toHaveAccessibleName('Agents de la session ancienne : aucun sous-agent en cours')
    expect(links[0]).toHaveTextContent('Agents')
    fireEvent.click(links[0])
    expect(onOpen).toHaveBeenCalledTimes(1)
    expect(window.location.search).toBe('?session=ancienne')
  })
})

describe('sous-agents dans la barre latérale', () => {
  const hosts: Host[] = [
    { id: 'hub', name: 'Hub', online: true, local: true, sessions: [], last_seen: '' },
    { id: 'pair', name: 'Pair', online: true, sessions: [], last_seen: '' },
  ]
  const sessions = [session('hub', 'chantier'), session('hub', 'calme'), session('pair', 'chantier')]
  const workspace = buildWorkspaceViewModel(sessions, [], new Map(), hosts)
  const sidebar = (collapsed = false, onSessionSelect = vi.fn()) => (
    <Sidebar
      workspace={workspace}
      selectedSession={null}
      collapsed={collapsed}
      collapseMode="small"
      recentTargets={[]}
      onSessionSelect={onSessionSelect}
      onDetachSession={vi.fn()}
    />
  )

  it('liste trois sous-agents en cours avec leur action, puis « +N », sous la seule session concernée', async () => {
    const { calls } = renderWithSummary([busy, finished], sidebar())
    await tick(0)
    expect(calls.count).toBe(1)
    const lists = screen.getAllByTestId('sidebar-session-agents')
    expect(lists).toHaveLength(1)
    const list = within(lists[0])
    expect(list.getByText('Tests Go')).toBeInTheDocument()
    expect(list.getByText('Bash go test ./pkg/server/')).toBeInTheDocument()
    expect(list.getByText('Relecture de la doc')).toBeInTheDocument()
    expect(list.getByText('Longue compilation')).toBeInTheDocument()
    expect(list.getByText('silencieux')).toBeInTheDocument()
    expect(list.queryByText('Quatrième')).not.toBeInTheDocument()
    expect(lists[0]).toHaveTextContent('+2')
    expect(lists[0].closest('li')).toHaveTextContent('chantier')
    expect(lists[0]).toHaveAccessibleName(/^Sous-agents en cours de chantier/)
  })

  it('mène à la page Agents de la session sans ouvrir son terminal', async () => {
    const onSessionSelect = vi.fn()
    const { onOpen } = renderWithSummary([busy], sidebar(false, onSessionSelect))
    await tick(0)
    fireEvent.click(screen.getByTestId('sidebar-session-agents'))
    expect(onSessionSelect).not.toHaveBeenCalled()
    expect(onOpen).toHaveBeenCalledTimes(1)
    expect(window.location.pathname + window.location.search).toBe('/agents?session=chantier')
  })

  it('ne montre rien dans la barre repliée', async () => {
    renderWithSummary([busy], sidebar(true))
    await tick(0)
    expect(screen.queryByTestId('sidebar-session-agents')).not.toBeInTheDocument()
  })
})
