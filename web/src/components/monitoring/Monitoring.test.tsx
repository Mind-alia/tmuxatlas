import { cleanup, render, screen, fireEvent } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { CLIAccountsOverview } from './CLIAccountsPanel'
import { Monitoring } from './Monitoring'
import { chartMax } from './TimeSeriesChart'
import type { MonitorResponse } from './types'

const now = Date.now()
const response: MonitorResponse = {
  supported: true,
  interval: 5,
  collected_at: now,
  host: { hostname: 'vps', os: 'Ubuntu', kernel: '6.8', arch: 'amd64', cpu_model: 'x', cpus: 8, boot_time: 0, reboot_required: false, updates: 0, security_updates: 0 },
  now: {
    cpu: { user: 10, system: 5, iowait: 1, steal: 0, idle: 84 },
    cores: [10, 20],
    load: [0.5, 0.4, 0.3],
    memory: { total: 8 * 2 ** 30, used: 2 * 2 ** 30, available: 6 * 2 ** 30, cached: 2 ** 30, swap_total: 0, swap_used: 0 },
    uptime_seconds: 3600,
    procs: 200,
    procs_running: 1,
    threads: 400,
    tcp: { established: 10, time_wait: 0, listen: 5 },
    psi: { cpu10: 0, cpu60: 0, mem10: 0, mem60: 0, io10: 0, io60: 0 },
  },
  series: [
    { t: now - 5000, cpu: 12, iowait: 1, steal: 0, load1: 0.5, mem: 25, swap: 0, net_rx: 100, net_tx: 50, disk_r: 0, disk_w: 10, disk_util: 1, psi_cpu: 0, psi_mem: 0, psi_io: 0, tcp: 10 },
    { t: now, cpu: 16, iowait: 1, steal: 0, load1: 0.5, mem: 25, swap: 0, net_rx: 200, net_tx: 50, disk_r: 0, disk_w: 10, disk_util: 1, psi_cpu: 0, psi_mem: 0, psi_io: 0, tcp: 10 },
  ],
  filesystems: [{ mount: '/', device: '/dev/sda1', fstype: 'ext4', total: 100, used: 20, available: 80, percent: 20, inodes_percent: 5 }],
  interfaces: [],
  disks: [],
  processes: [],
  listeners: [],
  services: [
    { unit: 'api.service', scope: 'system', description: 'API', active: 'failed', sub: 'failed', result: 'exit-code', custom: true, restarts: 0 },
    { unit: 'cron.service', scope: 'system', description: 'Cron', active: 'active', sub: 'running', custom: false, restarts: 0 },
  ],
  timers: [],
  endpoints: [],
  logs: { errors_last_hour: 0, ssh_failures_24h: 0, recent_errors: [], checked_at: 0 },
  alerts: [{
    severity: 'critical',
    title: 'Service en échec : api.service',
    detail: 'code de sortie non nul',
    facts: [{ label: 'Cause', value: 'code de sortie non nul' }],
    lists: [{ title: 'Derniers logs du service', items: ['boom: port already in use'] }],
    commands: [{ label: 'Redémarrer', command: 'sudo systemctl restart api.service' }],
    advice: 'Lis les derniers logs.',
  }],
}

describe('Monitoring', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('renders alerts, KPIs and filters services to the operator units', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(response), { status: 200 })))
    render(<Monitoring />)
    expect(await screen.findByText('Service en échec : api.service')).toBeInTheDocument()
    expect(screen.getByText('Critique')).toBeInTheDocument()
    expect(screen.getByRole('img', { name: /CPU\. Utilisation : actuel 16 %/ })).toBeInTheDocument()
    expect(screen.getByText('api')).toBeInTheDocument()
    expect(screen.queryByText('cron')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('radio', { name: 'Toutes' }))
    expect(screen.getByText('cron')).toBeInTheDocument()
  })

  it('expands an alert to show facts, logs, advice and copyable commands', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(response), { status: 200 })))
    const writeText = vi.fn(async () => {})
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    render(<Monitoring />)
    const toggle = await screen.findByRole('button', { name: /Service en échec : api\.service/ })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText('boom: port already in use')).toBeInTheDocument()
    expect(screen.getByText('Lis les derniers logs.')).toBeInTheDocument()
    expect(screen.getByText('sudo systemctl restart api.service')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Copier la commande' }))
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith('sudo systemctl restart api.service'))
    expect(await screen.findByRole('button', { name: 'Commande copiée' })).toBeInTheDocument()
  })

  it('hides itself when the role has no monitoring', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 404 })))
    const { container } = render(<Monitoring />)
    await vi.waitFor(() => expect(container).toBeEmptyDOMElement())
  })
})

describe('chartMax', () => {
  it('fits the axis to the data within its bounds', () => {
    expect(chartMax([[4, 5]], { ceiling: 100, minMax: 10 })).toBe(10)
    expect(chartMax([[30]], { ceiling: 100, minMax: 10 })).toBe(40)
    expect(chartMax([[99]], { ceiling: 100, minMax: 10 })).toBe(100)
    expect(chartMax([[0.14]], { minMax: 1 })).toBe(1)
    expect(chartMax([[27]], { minMax: 4 })).toBe(30)
  })

  it('rounds byte scales on 1024 so ticks read as round numbers', () => {
    expect(chartMax([[200 * 1024]], { scale: 'bytes' })).toBe(300 * 1024)
    expect(chartMax([[0]], { scale: 'bytes' })).toBe(1)
  })
})

describe('Monitoring states', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('always shows information alerts such as refused SSH attempts', async () => {
    const alerts = [
      { severity: 'warning', title: 'Disque presque plein' },
      { severity: 'info', title: '1815 tentatives SSH refusées sur 24 h', detail: '174 IP distinctes' },
    ]
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ ...response, alerts }), { status: 200 })))
    render(<Monitoring />)
    expect(await screen.findByText('Disque presque plein')).toBeInTheDocument()
    expect(screen.getByText('1815 tentatives SSH refusées sur 24 h')).toBeInTheDocument()
    expect(screen.getByText('174 IP distinctes')).toBeInTheDocument()
  })

  it('moves between detail tabs with the arrow keys', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(response), { status: 200 })))
    render(<Monitoring />)
    const services = await screen.findByRole('tab', { name: /Services/ })
    fireEvent.keyDown(services, { key: 'ArrowRight' })
    const timers = screen.getByRole('tab', { name: /Tâches planifiées/ })
    expect(timers).toHaveAttribute('aria-selected', 'true')
    expect(timers).toHaveFocus()
    fireEvent.keyDown(timers, { key: 'End' })
    expect(screen.getByRole('tab', { name: /Journal/ })).toHaveFocus()
  })

  it('announces frozen data as soon as a poll fails', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      const fetchMock = vi.fn(async () => new Response(JSON.stringify(response), { status: 200 }))
      vi.stubGlobal('fetch', fetchMock)
      render(<Monitoring />)
      expect(await screen.findByText('en direct')).toBeInTheDocument()
      fetchMock.mockRejectedValue(new Error('offline'))
      await vi.advanceTimersByTimeAsync(5000)
      expect(await screen.findByText(/données figées/)).toBeInTheDocument()
    } finally {
      vi.useRealTimers()
    }
  })

  it('displays CLI account token usage, quotas, and filters in the Comptes CLI tab', async () => {
    const nowSec = Math.floor(now / 1000)
    const cli_accounts = [
      {
        id: 'claude-1',
        cli: 'claude',
        name: 'Aris2 Claude',
        person: 'Aris2',
        account: 'aris2@example.com',
        plan: 'Pro',
        connected: true,
        quota_source: 'provider_cache',
        quota_at: nowSec - 120,
        last_activity: nowSec - 60,
        last_model: 'claude-opus-5-5',
        windows: [
          { label: '5 h', used_percent: 100, window_minutes: 300, resets_at: nowSec + 900 },
          { label: '7 j', used_percent: 40, window_minutes: 10080, resets_at: nowSec + 172800 },
        ],
        extra: { enabled: true, used: 4.64, limit: 40, used_percent: 11.6, currency: 'USD' },
        tokens_5h: { input: 200000, cache_read: 29500000, output: 50000, total: 29750000, requests: 219 },
        tokens_24h: { input: 500000, cache_read: 87000000, output: 150000, total: 87650000, requests: 600 },
        tokens_7d: { input: 2000000, cache_read: 850000000, output: 500000, total: 852500000, requests: 3200 },
      },
      {
        id: 'codex-1',
        cli: 'codex',
        name: 'Aris Codex',
        person: 'Aris',
        plan: 'Plus',
        connected: true,
        quota_source: 'session_events',
        quota_at: nowSec - 1200,
        last_activity: nowSec - 1200,
        last_model: 'gpt-6.1-sol',
        context_window: 475000,
        windows: [
          { label: '5 h', used_percent: 27, window_minutes: 300, resets_at: nowSec + 7200 },
          { label: '7 j', used_percent: 12, window_minutes: 10080, resets_at: nowSec - 600, expired: true },
        ],
        tokens_5h: { input: 400000, cache_read: 17000000, output: 100000, total: 17500000, requests: 80 },
        tokens_24h: { input: 500000, cache_read: 18000000, output: 120000, total: 18620000, requests: 90 },
        tokens_7d: { input: 3000000, cache_read: 128000000, output: 400000, total: 131400000, requests: 610 },
      },
      {
        id: 'agy-1',
        cli: 'agy',
        name: 'Aris Agy',
        person: 'Aris',
        account: 'aris@example.com',
        connected: true,
        quota_source: 'local_logs_only',
        last_activity: nowSec - 30,
        last_model: 'gemini-4-hydrogen-high',
        tokens_5h: { input: 150000, cache_read: 38000000, output: 50000, total: 38200000, requests: 244 },
        tokens_24h: { input: 150000, cache_read: 38000000, output: 50000, total: 38200000, requests: 244 },
        tokens_7d: { input: 400000, cache_read: 91000000, output: 100000, total: 91500000, requests: 520 },
      },
    ]
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ ...response, cli_accounts }), { status: 200 })))
    render(<Monitoring />)

    await screen.findByRole('tab', { name: /Services/ })
    expect(screen.queryByRole('tab', { name: /Comptes CLI/ })).not.toBeInTheDocument()

    render(<CLIAccountsOverview accounts={cli_accounts} />)
    expect(screen.getAllByText('Aris2').length).toBeGreaterThanOrEqual(1)
    expect(screen.getAllByText('Saturé').length).toBeGreaterThanOrEqual(2)
    expect(screen.getAllByText('100 %').length).toBeGreaterThanOrEqual(2)
    expect(screen.getAllByText('40 %').length).toBeGreaterThanOrEqual(2)
    // Before clicking Codex or Agy, their 5h and 7d limits are already visible on their compact rows
    expect(screen.getByText('27 %')).toBeInTheDocument()
    expect(screen.getByText('Réinitialisé')).toBeInTheDocument()
    expect(screen.getByText(/5 h : 38,2 M · 7 j : 91,5 M/)).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /Codex.*Aris/ }))
    expect(screen.getByText('Aris Codex')).toBeInTheDocument()
    expect(screen.getAllByText('27 %').length).toBeGreaterThanOrEqual(2)
    expect(screen.getAllByText('Réinitialisé').length).toBeGreaterThanOrEqual(1)
    fireEvent.click(screen.getByRole('button', { name: /Agy.*Aris/ }))
    expect(screen.getByText(/Quota fournisseur non exposé localement/)).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Codex' }))
    expect(screen.getByText('Aris Codex')).toBeInTheDocument()
    expect(screen.queryByText('Aris2')).not.toBeInTheDocument()
  })

  it('lets the operator inspect each CLI account, live tmux sessions, and recent activity in the overview', () => {
    const nowSec = Math.floor(now / 1000)
    const onOpenSessionName = vi.fn()
    const accounts = [
      {
        id: 'claude-1',
        cli: 'claude',
        name: 'Aris2 Claude',
        person: 'Aris2',
        plan: 'Pro',
        connected: true,
        quota_source: 'provider_cache',
        last_activity: nowSec - 60,
        last_model: 'claude-opus-5-5',
        windows: [{ label: '5 h', used_percent: 100, window_minutes: 300, resets_at: nowSec + 900 }],
        tokens_5h: { input: 200000, cache_read: 29500000, output: 50000, total: 29750000, requests: 219 },
        tokens_24h: { input: 500000, cache_read: 87000000, output: 150000, total: 87650000, requests: 600 },
        tokens_7d: { input: 2000000, cache_read: 850000000, output: 500000, total: 852500000, requests: 3200 },
        live_sessions: [{ session: 'fable_swift', command: 'claude', path: '/root/Project/VieCore' }],
        recent_sessions: [
          {
            id: 'sess-1',
            project: '/root/Project/VieCore',
            branch: 'main',
            model: 'claude-opus-5-5',
            tokens: 15000000,
            requests: 120,
            last_activity: nowSec - 60,
            active: true,
          },
        ],
      },
      {
        id: 'codex-1',
        cli: 'codex',
        name: 'Aris Codex',
        person: 'Aris',
        plan: 'Plus',
        connected: true,
        quota_source: 'session_events',
        last_activity: nowSec - 180,
        last_model: 'gpt-6.1-sol',
        windows: [{ label: '5 h', used_percent: 27, window_minutes: 300, resets_at: nowSec + 7200 }],
        tokens_5h: { input: 400000, cache_read: 17000000, output: 100000, total: 17500000, requests: 80 },
        tokens_24h: { input: 500000, cache_read: 18000000, output: 120000, total: 18620000, requests: 90 },
        tokens_7d: { input: 3000000, cache_read: 128000000, output: 400000, total: 131400000, requests: 610 },
        live_sessions: [{ session: 'business_plan', command: 'codex', path: '/root/Project/VieCore' }],
        recent_sessions: [
          {
            id: 'sess-2',
            project: '/root/Project/OpenPaname',
            model: 'gpt-6.1-sol',
            tokens: 8200000,
            requests: 42,
            last_activity: nowSec - 180,
          },
        ],
      },
    ]
    render(<CLIAccountsOverview accounts={accounts} onOpenSessionName={onOpenSessionName} />)
    expect(screen.getByRole('heading', { name: 'Comptes CLI' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /fable_swift/ })).toBeInTheDocument()
    expect(screen.getByText(/branche main/)).toBeInTheDocument()

    // Switch to Aris Codex and inspect its activity
    fireEvent.click(screen.getByRole('button', { name: /Codex.*Aris/ }))
    const bpBtn = screen.getByRole('button', { name: /business_plan/ })
    expect(bpBtn).toBeInTheDocument()
    expect(screen.getByText('OpenPaname')).toBeInTheDocument()
    fireEvent.click(bpBtn)
    expect(onOpenSessionName).toHaveBeenCalledWith('business_plan')
  })

  it('marks stale expired quotas with recent activity as À actualiser and shows disabled extra reason', () => {
    const nowSec = Math.floor(now / 1000)
    const accounts = [
      {
        id: 'claude-stale',
        cli: 'claude',
        name: 'Novanns Claude',
        person: 'Novanns',
        plan: 'Pro',
        connected: true,
        quota_source: 'provider_cache',
        quota_at: nowSec - 72000,
        last_activity: nowSec - 120,
        last_model: 'claude-opus-5-5',
        windows: [
          { label: '5 h', used_percent: 8, window_minutes: 300, resets_at: nowSec - 36000, expired: true },
        ],
        extra: {
          enabled: false,
          used: 5.43,
          limit: 40,
          used_percent: 13.6,
          currency: 'USD',
          disabled_reason: 'out_of_credits',
        },
        tokens_5h: { input: 100000, cache_read: 24500000, output: 50000, total: 24650000, requests: 138 },
        tokens_24h: { input: 200000, cache_read: 46800000, output: 100000, total: 47100000, requests: 258 },
        tokens_7d: { input: 400000, cache_read: 60000000, output: 300000, total: 60700000, requests: 457 },
      },
    ]
    render(<CLIAccountsOverview accounts={accounts} />)
    expect(screen.queryByText('Disponible')).not.toBeInTheDocument()
    expect(screen.getAllByText('À actualiser').length).toBeGreaterThanOrEqual(2)
    expect(screen.getByText('Inconnu · à actualiser')).toBeInTheDocument()
    expect(screen.getByText('Extra · crédits épuisés')).toBeInTheDocument()
  })

  it('always displays all 5h and 7d quota windows for multi-group accounts without clicking them', () => {
    const nowSec = Math.floor(now / 1000)
    const accounts = [
      {
        id: 'claude-live',
        cli: 'claude',
        name: 'Aris Claude',
        person: 'Aris',
        plan: 'Pro',
        connected: true,
        quota_source: 'provider_cache',
        last_activity: nowSec - 60,
        windows: [
          { label: '5 h', used_percent: 12, window_minutes: 300, resets_at: nowSec + 3600 },
          { label: '7 j', used_percent: 25, window_minutes: 10080, resets_at: nowSec + 86400 },
        ],
        tokens_5h: { input: 1000, cache_read: 10000, output: 500, total: 11500, requests: 5 },
        tokens_24h: { input: 2000, cache_read: 20000, output: 1000, total: 23000, requests: 10 },
        tokens_7d: { input: 5000, cache_read: 50000, output: 2500, total: 57500, requests: 25 },
        live_sessions: [{ session: 'active_session', command: 'claude', path: '/root/Project/tmuxatlas' }],
      },
      {
        id: 'agy-multi',
        cli: 'agy',
        name: 'Novanns Agy',
        person: 'Novanns',
        plan: 'Pro',
        connected: true,
        quota_source: 'agy_usage_cli',
        last_activity: nowSec - 300,
        windows: [
          { label: 'Gemini 5 h', used_percent: 38.6, window_minutes: 300, resets_at: nowSec + 7200 },
          { label: 'Gemini 7 j', used_percent: 11, window_minutes: 10080, resets_at: nowSec + 345600 },
          { label: 'Claude/GPT 5 h', used_percent: 100, window_minutes: 300, resets_at: nowSec + 7200 },
          { label: 'Claude/GPT 7 j', used_percent: 50, window_minutes: 10080, resets_at: nowSec + 345600 },
        ],
        tokens_5h: { input: 10000, cache_read: 100000, output: 2000, total: 112000, requests: 12 },
        tokens_24h: { input: 20000, cache_read: 200000, output: 4000, total: 224000, requests: 24 },
        tokens_7d: { input: 40000, cache_read: 400000, output: 8000, total: 448000, requests: 48 },
      },
    ]

    render(<CLIAccountsOverview accounts={accounts} />)
    // claude-live is auto-selected because it has a live session; agy-multi is NOT clicked
    const agyButton = screen.getByRole('button', { name: /Agy.*Novanns/ })
    expect(agyButton).toHaveAttribute('aria-pressed', 'false')
    expect(screen.getByText('Gemini 5 h')).toBeInTheDocument()
    expect(screen.getByText('Gemini 7 j')).toBeInTheDocument()
    expect(screen.getByText('Claude/GPT 5 h')).toBeInTheDocument()
    expect(screen.getByText('Claude/GPT 7 j')).toBeInTheDocument()
    expect(screen.getByText('39 %')).toBeInTheDocument()
    expect(screen.getByText('11 %')).toBeInTheDocument()
    expect(screen.getByText('100 %')).toBeInTheDocument()
    expect(screen.getByText('50 %')).toBeInTheDocument()
  })

  it('triggers onRefresh when clicking Actualiser and disables the button while refreshing', () => {
    const nowSec = Math.floor(now / 1000)
    const onRefresh = vi.fn()
    const accounts = [
      {
        id: 'claude-1',
        cli: 'claude',
        name: 'Aris Claude',
        person: 'Aris',
        plan: 'Pro',
        connected: true,
        quota_source: 'provider_cache',
        last_activity: nowSec - 60,
        windows: [
          { label: '5 h', used_percent: 12, window_minutes: 300, resets_at: nowSec + 3600 },
          { label: '7 j', used_percent: 25, window_minutes: 10080, resets_at: nowSec + 86400 },
        ],
        tokens_5h: { input: 1000, cache_read: 10000, output: 500, total: 11500, requests: 5 },
        tokens_24h: { input: 2000, cache_read: 20000, output: 1000, total: 23000, requests: 10 },
        tokens_7d: { input: 5000, cache_read: 50000, output: 2500, total: 57500, requests: 25 },
      },
    ]

    const { rerender } = render(<CLIAccountsOverview accounts={accounts} onRefresh={onRefresh} />)
    const refreshBtn = screen.getByRole('button', { name: 'Actualiser' })
    expect(refreshBtn).toBeEnabled()
    fireEvent.click(refreshBtn)
    expect(onRefresh).toHaveBeenCalledTimes(1)

    rerender(<CLIAccountsOverview accounts={accounts} onRefresh={onRefresh} refreshing />)
    const refreshingBtn = screen.getByRole('button', { name: 'Actualisation…' })
    expect(refreshingBtn).toBeDisabled()
  })

  it('filters CLI accounts by TmuxAtlas owner', () => {
    const tokens = { input: 0, cache_read: 0, output: 0, total: 0, requests: 0 }
    const base = { cli: 'claude', connected: true, quota_source: 'provider_cache', tokens_5h: tokens, tokens_24h: tokens, tokens_7d: tokens }
    const accounts = [
      { ...base, id: 'a1', name: 'Aris Claude', person: 'Aris', owner: 'u-aris', owner_name: 'aris' },
      { ...base, id: 'a2', name: 'Aris2 Claude', person: 'Aris2', owner: 'u-aris', owner_name: 'aris' },
      { ...base, id: 'n1', name: 'Novanns Claude', person: 'Novanns', owner: 'u-nova', owner_name: 'novanns' },
      { ...base, id: 'c1', name: 'Camille Codex', cli: 'codex', person: 'Camille' },
    ]
    render(<CLIAccountsOverview accounts={accounts} />)
    const select = screen.getByLabelText('Filtrer par utilisateur TmuxAtlas')
    expect([...select.querySelectorAll('option')].map(o => o.textContent)).toEqual([
      'Tout le monde',
      'aris',
      'novanns',
      'Non assignés',
    ])
    expect(screen.getByText(/^4 comptes/)).toBeInTheDocument()

    fireEvent.change(select, { target: { value: 'u-aris' } })
    expect(screen.getByText(/^2 comptes/)).toBeInTheDocument()
    expect(screen.queryByText('Novanns Claude')).not.toBeInTheDocument()

    fireEvent.change(select, { target: { value: '__none__' } })
    expect(screen.getByText(/^1 comptes/)).toBeInTheDocument()
  })

  it('hides the owner filter when no CLI account is assigned', () => {
    const tokens = { input: 0, cache_read: 0, output: 0, total: 0, requests: 0 }
    render(
      <CLIAccountsOverview
        accounts={[{ id: 'a1', cli: 'claude', name: 'Aris Claude', person: 'Aris', connected: true, quota_source: 'provider_cache', tokens_5h: tokens, tokens_24h: tokens, tokens_7d: tokens }]}
      />,
    )
    expect(screen.queryByLabelText('Filtrer par utilisateur TmuxAtlas')).not.toBeInTheDocument()
  })
})
