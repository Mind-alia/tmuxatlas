import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { LoopsPage } from './LoopsPage'

type Call = { url: string; init?: RequestInit }
let calls: Call[] = []

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

const sampleMeta = {
  host_id: 'local-host',
  projects: [
    { name: 'tmuxatlas', path: '/root/Project/tmuxatlas', is_git: true, branch: 'perso' },
    { name: 'Aris/StatQuest', path: '/root/Project/Aris/StatQuest', is_git: true, branch: 'main' },
  ],
  accounts: {
    claude: [
      { id: 'c1', provider: 'claude', name: 'ArisClaude', person: 'Aris' },
      { id: 'c2', provider: 'claude', name: 'Aris2Claude', person: 'Aris2' },
    ],
    codex: [{ id: 'x1', provider: 'codex', name: 'ArisCodex', person: 'Aris' }],
    agy: [{ id: 'a1', provider: 'agy', name: 'ArisAgy', person: 'Aris' }],
  },
  clis: [
    { cli: 'claude', label: 'Claude Code', available: true },
    { cli: 'codex', label: 'Codex CLI', available: true },
    { cli: 'agy', label: 'Antigravity (agy)', available: true },
  ],
  presets: [
    {
      id: 'quality-bugs',
      label: 'Qualité & Bugs',
      description: 'Corriger les bugs.',
      prompt: 'Inspecte et corrige un bug réel.',
    },
  ],
}

let mockLoops: unknown[] = []

beforeEach(() => {
  calls = []
  mockLoops = []
  window.localStorage.clear()
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString()
      calls.push({ url, init })
      if (url === '/api/loops/meta') return json(sampleMeta)
      if (url === '/api/loops') return json({ loops: mockLoops })
      if (url.startsWith('/api/loops/logs')) {
        return json({ loop_id: 'l1', iteration: 1, content: 'itérations ok' })
      }
      if (url === '/api/loops/chat') {
        const body = JSON.parse(String(init?.body || '{}'))
        return json({
          host_id: 'local-host',
          session: `tmuxatlas-${body.cli}`,
          key: `local-host/tmuxatlas-${body.cli}`,
          cwd: body.cwd,
          cli: body.cli,
          person: body.person,
        })
      }
      if (url === '/api/loops/create') {
        const body = JSON.parse(String(init?.body || '{}'))
        return json(
          {
            id: 'loop-1',
            name: body.name || 'tmuxatlas · continu',
            project_dir: body.project_dir,
            work_dir: body.project_dir,
            cli: body.cli || 'claude',
            person: body.person,
            prompt: body.prompt,
            max_iterations: body.max_iterations ?? 0,
            delay_seconds: body.delay_seconds,
            auto_approve: body.auto_approve,
            use_worktree: body.use_worktree,
            stop_when_done: body.stop_when_done,
            status: body.start_now ? 'running' : 'idle',
            current_iteration: 0,
            workers: (body.workers || []).map((w: Record<string, unknown>) => ({
              id: w.person ? `${w.cli}:${w.person}` : w.cli,
              ...w,
              status: w.enabled ? 'ready' : 'disabled',
              run_count: 0,
            })),
            created_at: '2026-11-16T10:00:00Z',
            updated_at: '2026-11-16T10:00:00Z',
            iterations: [],
          },
          201,
        )
      }
      if (url === '/api/loops/worker') {
        const body = JSON.parse(String(init?.body || '{}'))
        const base = (mockLoops[0] || {}) as Record<string, unknown>
        const workers = ((base.workers as Array<Record<string, unknown>>) || []).map(w => {
          if (body.action === 'pause' && w.id === body.worker_id) {
            return { ...w, paused: true, status: 'paused' }
          }
          if (body.action === 'resume' && w.id === body.worker_id) {
            return { ...w, paused: false, status: 'ready' }
          }
          if (body.action === 'enable_all') {
            return { ...w, enabled: true, paused: false, status: 'ready' }
          }
          return w
        })
        return json({ ...base, workers })
      }
      return json({ error: 'unexpected ' + url }, 404)
    }),
  )
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('LoopsPage', () => {
  it('isolates accounts per CLI and opens claude, codex, and agy chats', async () => {
    const user = userEvent.setup()
    const onOpenSession = vi.fn()
    render(<LoopsPage sessions={[]} onOpenSession={onOpenSession} />)

    const claudeSelect = (await screen.findByLabelText('Compte claude')) as HTMLSelectElement
    const codexSelect = screen.getByLabelText('Compte codex') as HTMLSelectElement
    const agySelect = screen.getByLabelText('Compte agy') as HTMLSelectElement

    // Aris2 appears only in claude, never in codex or agy.
    expect([...claudeSelect.options].map(o => o.value)).toContain('Aris2')
    expect([...codexSelect.options].map(o => o.value)).not.toContain('Aris2')
    expect([...agySelect.options].map(o => o.value)).not.toContain('Aris2')

    await user.selectOptions(claudeSelect, 'Aris2')
    await user.click(screen.getByRole('button', { name: /Ouvrir Claude Code/i }))
    await waitFor(() => expect(onOpenSession).toHaveBeenCalledWith('local-host/tmuxatlas-claude'))

    await user.click(screen.getByRole('button', { name: /Ouvrir Codex CLI/i }))
    await waitFor(() => expect(onOpenSession).toHaveBeenCalledWith('local-host/tmuxatlas-codex'))

    await user.click(screen.getByRole('button', { name: /Ouvrir AGY/i }))
    await waitFor(() => expect(onOpenSession).toHaveBeenCalledWith('local-host/tmuxatlas-agy'))

    const chatPayloads = calls
      .filter(c => c.url === '/api/loops/chat')
      .map(c => JSON.parse(String(c.init?.body)))
    expect(chatPayloads).toEqual([
      { cli: 'claude', cwd: '/root/Project/tmuxatlas', person: 'Aris2' },
      { cli: 'codex', cwd: '/root/Project/tmuxatlas', person: 'Aris' },
      { cli: 'agy', cwd: '/root/Project/tmuxatlas', person: 'Aris' },
    ])
  })

  it('creates and starts an improvement loop from the modal', async () => {
    const user = userEvent.setup()
    render(<LoopsPage sessions={[]} onOpenSession={vi.fn()} />)

    await screen.findByLabelText('Compte claude')
    await user.click(screen.getAllByRole('button', { name: /Nouvelle boucle/i })[0])

    for (let step = 0; step < 3; step++) await user.click(screen.getByRole('button', { name: 'Suivant' }))
    const createBtn = await screen.findByRole('button', { name: /Créer & Démarrer/i })
    await user.click(createBtn)

    await waitFor(() => {
      const createCall = calls.find(c => c.url === '/api/loops/create')
      expect(createCall).toBeTruthy()
      const payload = JSON.parse(String(createCall?.init?.body))
      expect(payload.project_dir).toBe('/root/Project/tmuxatlas')
      expect(payload.cli).toBe('claude')
      expect(payload.continuous).toBe(false)
      expect(payload.max_iterations).toBe(10)
      expect(payload.mode).toBe('mission')
      expect(payload.use_worktree).toBe(true)
      expect(payload.workers).toHaveLength(4)
      expect(payload.workers.every((w: { enabled: boolean }) => w.enabled)).toBe(true)
      expect(payload.start_now).toBe(true)
    })
    expect(await screen.findByText('Boucles (1)')).toBeInTheDocument()
  })

  it('switches project via ProjectSelector and pauses/resumes individual subscriptions live', async () => {
    const user = userEvent.setup()
    const resetAt = new Date(Date.now() + 3600_000).toISOString()
    mockLoops = [
      {
        id: 'loop-live',
        name: 'StatQuest · continu',
        project_dir: '/root/Project/Aris/StatQuest',
        work_dir: '/root/Project/Aris/StatQuest',
        cli: 'claude',
        person: 'Aris2',
        prompt: 'Amélioration continue',
        max_iterations: 0,
        delay_seconds: 5,
        auto_approve: true,
        use_worktree: false,
        stop_when_done: false,
        status: 'waiting_reset',
        active_worker_id: '',
        next_reset_at: resetAt,
        current_iteration: 3,
        workers: [
          {
            id: 'claude:Aris',
            cli: 'claude',
            person: 'Aris',
            label: 'ArisClaude',
            enabled: true,
            paused: false,
            status: 'rate_limited',
            reset_at: resetAt,
            run_count: 2,
          },
          {
            id: 'claude:Aris2',
            cli: 'claude',
            person: 'Aris2',
            label: 'Aris2Claude',
            enabled: true,
            paused: false,
            status: 'ready',
            run_count: 1,
          },
        ],
        created_at: '2026-11-16T10:00:00Z',
        updated_at: '2026-11-16T10:00:00Z',
        iterations: [],
      },
    ]

    render(<LoopsPage sessions={[]} onOpenSession={vi.fn()} />)

    expect(await screen.findByText(/Comptes temporairement indisponibles/i)).toBeInTheDocument()

    await user.click(screen.getByText('Comptes autorisés et disponibilité'))
    // Pause Aris2 individually
    const pauseButtons = screen.getAllByRole('button', { name: /^Pause$/i })
    // Click the per-worker Pause button for Aris2 (after global Pause and Aris)
    await user.click(pauseButtons[pauseButtons.length - 1])

    await waitFor(() => {
      const workerCall = calls.find(c => c.url === '/api/loops/worker')
      expect(workerCall).toBeTruthy()
      expect(JSON.parse(String(workerCall?.init?.body))).toEqual({
        loop_id: 'loop-live',
        worker_id: 'claude:Aris2',
        action: 'pause',
      })
    })

    // Reactivate Aris2
    const resumeBtn = await screen.findByRole('button', { name: /^Réactiver$/i })
    await user.click(resumeBtn)
    await waitFor(() => {
      const workerCalls = calls.filter(c => c.url === '/api/loops/worker').map(c => JSON.parse(String(c.init?.body)))
      expect(workerCalls).toContainEqual({
        loop_id: 'loop-live',
        worker_id: 'claude:Aris2',
        action: 'resume',
      })
    })

    // Switch active project via searchable ProjectSelector
    await user.click(screen.getByRole('button', { name: /Changer \(2\)/i }))
    const search = screen.getByPlaceholderText(/Filtrer un projet/i)
    await user.type(search, 'StatQuest')
    await user.click(screen.getByRole('button', { name: /Aris\/StatQuest\s+main/i }))
    expect(screen.getByText(/dans \/root\/Project\/Aris\/StatQuest/i)).toBeInTheDocument()
  })
})
