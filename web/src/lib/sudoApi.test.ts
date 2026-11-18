import { afterEach, describe, expect, it, vi } from 'vitest'
import { openSudoSocket, requestsForSession, type SudoRequest } from './sudoApi'

function request(id: string, status: SudoRequest['status'], host: string, session: string, created: string): SudoRequest {
  return {
    id,
    status,
    version: 1,
    command: 'id',
    original: 'id',
    origin: { host_id: host, session, window: 0 },
    revisions: [],
    created_at: created,
    updated_at: created,
    expires_at: created,
  }
}

describe('sudo API helpers', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('keeps one session and lists open requests first', () => {
    const list = [
      request('old-done', 'done', 'h', 's', '2026-11-16T10:00:00Z'),
      request('other', 'pending', 'h', 'other', '2026-11-16T11:00:00Z'),
      request('open', 'pending', 'h', 's', '2026-11-16T09:00:00Z'),
      request('other-host', 'pending', 'x', 's', '2026-11-16T12:00:00Z'),
    ]
    expect(requestsForSession(list, 'h', 's').map(r => r.id)).toEqual(['open', 'old-done'])
  })

  it('sends the grant in the first frame, never in the URL', () => {
    const sockets: any[] = []
    class FakeSocket extends EventTarget {
      url: string
      binaryType = ''
      send = vi.fn()
      constructor(url: string) {
        super()
        this.url = url
        sockets.push(this)
      }
    }
    vi.stubGlobal('WebSocket', FakeSocket)
    openSudoSocket('grant-token', 100, 30)
    const socket = sockets[0]
    expect(socket.url).not.toContain('grant-token')
    expect(socket.url).toMatch(/\/ws\/sudo$/)
    socket.dispatchEvent(new Event('open'))
    expect(JSON.parse(socket.send.mock.calls[0][0])).toEqual({ type: 'open', grant: 'grant-token', cols: 100, rows: 30 })
  })
})
