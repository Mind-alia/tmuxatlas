import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentsPage, formatDuration, initials } from './AgentsPage'
import type { AgentTeam } from './types'

const now = Date.now()
const iso = (msAgo: number) => new Date(now - msAgo).toISOString()

const team: AgentTeam = {
  session_id: 's1',
  name: 'Revue de la PR',
  cwd: '/home/u/src/repo',
  tmux_session: 'fable-pr1',
  status: 'busy',
  agents: [
    {
      id: 'a1',
      type: 'general-purpose',
      description: 'Tests Go',
      model: 'claude-opus-5-5',
      state: 'running',
      started_at: iso(300_000),
      updated_at: iso(2_000),
      steps: 12,
      action: 'Bash go test ./...',
      log: ['Read pkg/auth/accounts.go'],
    },
    {
      id: 'a2',
      type: 'Explore',
      description: 'Relecture docs',
      state: 'done',
      started_at: iso(900_000),
      updated_at: iso(600_000),
      steps: 4,
      action: 'Read docs/ops.md',
      log: [],
    },
  ],
}

function respond(status: number, body: unknown) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })),
  )
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('AgentsPage', () => {
  it('shows the team of a session and the details of the agent that is clicked', async () => {
    respond(200, { teams: [team, { ...team, session_id: 's2', name: 'Autre', tmux_session: 'micro', agents: [] }] })
    const onOpenSession = vi.fn()
    render(<AgentsPage onOpenSession={onOpenSession} />)

    const selected = await screen.findByRole('region', { name: 'Agent sélectionné' })
    expect(selected.textContent).toContain('Tests Go')
    expect(selected.textContent).toContain('Bash go test ./...')
    expect(selected.textContent).toContain('En cours depuis 5 min')

    fireEvent.click(screen.getByRole('button', { name: 'Relecture docs, Terminé' }))
    await waitFor(() => expect(selected.textContent).toContain('Terminé en 5 min'))
    expect(selected.textContent).toContain('Read docs/ops.md')

    fireEvent.click(screen.getByRole('button', { name: /Ouvrir le terminal/ }))
    expect(onOpenSession).toHaveBeenCalledWith('fable-pr1')

    fireEvent.click(screen.getByRole('button', { name: 'micro' }))
    expect(await screen.findByText(/Aucun sous-agent lancé par cette session/)).toBeTruthy()
  })

  it('says so when no Claude Code session is open', async () => {
    respond(200, { teams: [] })
    render(<AgentsPage />)
    expect(await screen.findByText(/Aucune session Claude Code/)).toBeTruthy()
  })

  it('explains a refusal instead of showing an empty sky', async () => {
    respond(403, {})
    render(<AgentsPage />)
    expect(await screen.findByText(/réservée aux administrateurs/)).toBeTruthy()
  })
})

describe('helpers', () => {
  it('builds initials and durations', () => {
    expect(initials('Revue sécurité')).toBe('RS')
    expect(initials('tests')).toBe('TE')
    expect(initials('')).toBe('?')
    expect(formatDuration(42_000)).toBe('42 s')
    expect(formatDuration(5 * 60_000)).toBe('5 min')
    expect(formatDuration(65 * 60_000)).toBe('1 h 05')
  })
})
