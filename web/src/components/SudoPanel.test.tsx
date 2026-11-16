import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SudoPanel } from './SudoPanel'
import type { SudoRequest } from '../lib/sudoApi'

vi.mock('../hooks/usePreferences', () => ({
  usePreferences: () => ({ prefs: { theme: 'atlas' } }),
}))

vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    fit = vi.fn()
    activate() {}
    dispose() {}
  },
}))

const terminals = vi.hoisted(() => ({ writes: [] as unknown[], data: [] as ((value: string) => void)[] }))

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    cols = 80
    rows = 12
    loadAddon() {}
    open() {}
    focus() {}
    dispose() {}
    write(data: unknown) {
      terminals.writes.push(data)
    }
    onData(listener: (value: string) => void) {
      terminals.data.push(listener)
      return { dispose() {} }
    }
    onResize() {
      return { dispose() {} }
    }
  },
}))

class FakeSocket extends EventTarget {
  static OPEN = 1
  static sockets: FakeSocket[] = []
  url: string
  binaryType = ''
  readyState = 0
  send = vi.fn()
  onmessage: ((event: MessageEvent) => void) | null = null
  onclose: (() => void) | null = null
  constructor(url: string) {
    super()
    this.url = url
    FakeSocket.sockets.push(this)
  }
  open() {
    this.readyState = 1
    this.dispatchEvent(new Event('open'))
  }
  close() {}
}

const base: SudoRequest = {
  id: 'sudo-1',
  status: 'pending',
  version: 1,
  command: 'apt-get install -y jq',
  original: 'apt-get install -y jq',
  reason: 'installer jq',
  origin: { host_id: 'host-agents', host_name: 'agents', session: 'claude', window: 0, pane: '%1' },
  revisions: [],
  created_at: '2026-11-16T12:00:00Z',
  updated_at: '2026-11-16T12:00:00Z',
  expires_at: '2026-11-16T12:15:00Z',
}

let current: SudoRequest
const calls: { url: string; body?: unknown }[] = []

function json(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } })
}

beforeEach(() => {
  current = { ...base }
  calls.length = 0
  FakeSocket.sockets = []
  terminals.writes = []
  terminals.data = []
  vi.stubGlobal('WebSocket', FakeSocket)
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      calls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : undefined })
      if (url === '/api/sudo/requests') return json([current])
      if (url === '/api/sudo/peers') return json([{ id: 'host-admin', name: 'admin', unix_user: 'anovisclaw', online: true }])
      if (url.endsWith('/approve')) {
        current = { ...current, status: 'approved' }
        return json({ grant: 'g-1', request_id: 'sudo-1', version: 1, expires_at: '' })
      }
      if (url.endsWith('/review')) {
        current = { ...current, status: 'delivered' }
        return json(current)
      }
      return json({ error: 'inconnu' }, 500)
    }),
  )
})

afterEach(() => vi.unstubAllGlobals())

describe('SudoPanel', () => {
  it('stays hidden when the server does not offer the sudo terminal', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('réservé aux administrateurs', { status: 403 })))
    render(<SudoPanel hostId="host-agents" sessionName="claude" enabled />)
    await act(async () => {
      await Promise.resolve()
    })
    expect(screen.queryByTestId('sudo-panel')).toBeNull()
  })

  it('approves the exact version, opens a dedicated terminal and reviews the result', async () => {
    render(<SudoPanel hostId="host-agents" sessionName="claude" enabled />)
    const approve = await screen.findByRole('button', { name: /Approuver v1/ })
    await waitFor(() => expect(approve).not.toBeDisabled())
    fireEvent.click(approve)
    await waitFor(() => expect(FakeSocket.sockets).toHaveLength(1))
    expect(calls.find(call => call.url.endsWith('/approve'))?.body).toEqual({ version: 1, peer: 'host-admin' })

    const socket = FakeSocket.sockets[0]
    expect(socket.url).not.toContain('g-1')
    act(() => socket.open())
    expect(JSON.parse(socket.send.mock.calls[0][0])).toMatchObject({ type: 'open', grant: 'g-1' })
    act(() => socket.onmessage?.(new MessageEvent('message', { data: JSON.stringify({ type: 'echo', on: false }) })))
    expect(screen.getByText(/Saisie masquée/)).toBeTruthy()
    act(() => terminals.data[0]('x'))
    expect(socket.send).toHaveBeenCalledTimes(2)
    expect(window.localStorage.length).toBe(0)

    current = {
      ...current,
      status: 'awaiting_review',
      capture: { version: 1, command: current.command, exit_code: 0, output: 'jq installé\n', output_truncated: false, redactions: 0, duration: 1 },
    }
    act(() => socket.onclose?.())
    fireEvent.click(await screen.findByRole('button', { name: 'Code de sortie seul' }))
    await waitFor(() => expect(calls.find(call => call.url.endsWith('/review'))?.body).toEqual({ decision: 'exit-code', note: '' }))
  })
})
