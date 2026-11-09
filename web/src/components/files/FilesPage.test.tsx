import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { forwardRef } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { breadcrumbs, dirname, fileKind, formatSize, joinPath, resolvePath } from './filesApi'
import { sortEntries } from './FileList'
import { detectEOL, fromEditorText, toEditorText } from './tabs'

// The page is tested with a plain textarea: the editor has its own tests.
vi.mock('./CodeEditor', () => ({
  CodeEditor: forwardRef(function MockEditor(
    props: { value: string; onChange: (value: string) => void; ariaLabel?: string; readOnly?: boolean },
    _ref,
  ) {
    return (
      <textarea
        aria-label={props.ariaLabel}
        value={props.value}
        readOnly={props.readOnly}
        onChange={event => props.onChange(event.target.value)}
      />
    )
  }),
}))

const entry = (name: string, type: 'dir' | 'file', extra: Record<string, unknown> = {}) => ({
  name,
  path: `/home/me/${name}`,
  type,
  size: 12,
  mode: type === 'dir' ? 'drwxr-xr-x' : '-rw-r--r--',
  mtime: Date.parse('2026-11-01T10:00:00Z'),
  hidden: name.startsWith('.'),
  ...extra,
})

const listing = {
  path: '/home/me',
  parent: '/home',
  home: '/home/me',
  writable: true,
  entries: [entry('projects', 'dir'), entry('.bashrc', 'file'), entry('notes.txt', 'file'), entry('logo.svg', 'file')],
}

type Call = { url: string; init?: RequestInit }
let calls: Call[] = []
let version = 'v1'

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

beforeEach(() => {
  calls = []
  version = 'v1'
  window.localStorage.clear()
  window.history.replaceState(null, '', '/files')
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      calls.push({ url, init })
      if (url.startsWith('/api/files/list')) return json(listing)
      if (url.startsWith('/api/files/content') && (!init?.method || init.method === 'GET')) {
        return json({
          path: '/home/me/notes.txt', name: 'notes.txt', size: 12, mode: '-rw-r--r--', mtime: 1,
          version, binary: false, too_large: false, writable: true, content: 'hello\r\nworld',
        })
      }
      if (url.startsWith('/api/files/content') && init?.method === 'PUT') {
        if (!url.includes(`version=${version}`)) return json({ error: 'changed', code: 'conflict' }, 409)
        version = 'v2'
        return json({ path: '/home/me/notes.txt', size: 13, mtime: 2, version })
      }
      return json({ error: 'unexpected' }, 500)
    }),
  )
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

async function renderPage() {
  const { FilesPage } = await import('./FilesPage')
  render(<FilesPage hosts={[{ id: 'h', name: 'vps-1', local: true, online: true, sessions: [], last_seen: '' }]} />)
  await screen.findByRole('option', { name: /notes\.txt/ })
}

describe('FilesPage', () => {
  it('lists folders first and hides dotfiles until asked', async () => {
    await renderPage()
    const names = screen.getAllByRole('option').map(option => option.getAttribute('data-path'))
    expect(names).toEqual(['/home/me/projects', '/home/me/logo.svg', '/home/me/notes.txt'])
    await userEvent.click(screen.getByRole('button', { name: 'Afficher les fichiers cachés' }))
    expect(screen.getByRole('option', { name: /\.bashrc/ })).toBeInTheDocument()
    expect(screen.getByText('vps-1')).toBeInTheDocument()
  })

  it('opens a text file, edits it and saves with its version and line endings', async () => {
    await renderPage()
    await userEvent.click(screen.getByRole('option', { name: /notes\.txt/ }))
    const editor = await screen.findByLabelText('Contenu de notes.txt')
    expect(editor).toHaveValue('hello\nworld')
    expect(screen.getByText('CRLF')).toBeInTheDocument()

    fireEvent.change(editor, { target: { value: 'hello\nthere' } })
    expect(screen.getByRole('button', { name: /Fermer notes\.txt \(modifié\)/ })).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Enregistrer' }))

    await waitFor(() => expect(calls.some(call => call.init?.method === 'PUT')).toBe(true))
    const put = calls.find(call => call.init?.method === 'PUT')!
    expect(put.url).toContain('version=v1')
    expect(put.init?.body).toBe('hello\r\nthere')
    await waitFor(() => expect(screen.queryByRole('button', { name: /\(modifié\)/ })).not.toBeInTheDocument())
  })

  it('asks before overwriting a file changed elsewhere', async () => {
    await renderPage()
    await userEvent.click(screen.getByRole('option', { name: /notes\.txt/ }))
    const editor = await screen.findByLabelText('Contenu de notes.txt')
    version = 'changed-elsewhere'
    fireEvent.change(editor, { target: { value: 'mine' } })
    await act(async () => {
      fireEvent.keyDown(window, { key: 's', ctrlKey: true })
    })
    expect(await screen.findByText('Le fichier a changé sur le serveur')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Écraser' }))
    await waitFor(() => expect(calls.some(call => call.url.includes('force=1'))).toBe(true))
  })

  it('previews images without downloading their text', async () => {
    await renderPage()
    await userEvent.click(screen.getByRole('option', { name: /logo\.svg/ }))
    // The image stays hidden until it loads, which jsdom never does.
    const image = await waitFor(() => {
      const element = document.querySelector('img[alt="logo.svg"]')
      expect(element).not.toBeNull()
      return element!
    })
    expect(image.getAttribute('src')).toContain('/api/files/raw?path=%2Fhome%2Fme%2Flogo.svg')
    expect(calls.some(call => call.url.startsWith('/api/files/content'))).toBe(false)
  })

  it('navigates into folders and keeps the folder in the URL', async () => {
    await renderPage()
    await userEvent.click(screen.getByRole('option', { name: /projects/ }))
    await waitFor(() => expect(calls.some(call => call.url.includes(encodeURIComponent('/home/me/projects')))).toBe(true))
  })
})

describe('file helpers', () => {
  it('joins, splits and resolves paths', () => {
    expect(joinPath('/', 'etc')).toBe('/etc')
    expect(joinPath('/home/me/', 'a')).toBe('/home/me/a')
    expect(dirname('/home/me/a.txt')).toBe('/home/me')
    expect(dirname('/a')).toBe('/')
    expect(resolvePath('/docs/guide', '../img/./a.png')).toBe('/docs/img/a.png')
    expect(resolvePath('/docs', '/etc/hosts')).toBe('/etc/hosts')
    expect(breadcrumbs('/home/me').map(crumb => crumb.path)).toEqual(['/', '/home', '/home/me'])
  })

  it('classifies files and formats sizes', () => {
    expect(fileKind('photo.JPG')).toBe('image')
    expect(fileKind('README.md')).toBe('markdown')
    expect(fileKind('main.go')).toBe('text')
    expect(formatSize(512)).toBe('512 o')
    expect(formatSize(1536)).toBe('1,5 Kio')
  })

  it('keeps line endings through the editor', () => {
    expect(detectEOL('a\r\nb\r\n')).toBe('\r\n')
    expect(detectEOL('a\nb')).toBe('\n')
    expect(fromEditorText(toEditorText('a\r\nb'), '\r\n')).toBe('a\r\nb')
  })

  it('sorts folders first, then by the chosen key', () => {
    const entries = [entry('b.txt', 'file', { size: 1 }), entry('z', 'dir'), entry('a.txt', 'file', { size: 9 })] as never[]
    expect(sortEntries(entries, { key: 'name', descending: false }).map((e: { name: string }) => e.name)).toEqual(['z', 'a.txt', 'b.txt'])
    expect(sortEntries(entries, { key: 'size', descending: true }).map((e: { name: string }) => e.name)).toEqual(['z', 'a.txt', 'b.txt'])
  })
})
