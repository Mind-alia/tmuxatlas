import { apiFetch } from '../../lib/api'

export type EntryType = 'dir' | 'file' | 'symlink' | 'other'

export interface FileEntry {
  name: string
  path: string
  type: EntryType
  size: number
  mode: string
  mtime: number
  hidden: boolean
  target?: string
  link_dir?: boolean
  broken?: boolean
}

export interface Listing {
  path: string
  parent?: string
  home: string
  entries: FileEntry[]
  truncated?: boolean
  writable: boolean
}

export interface FileContent {
  path: string
  name: string
  size: number
  mode: string
  mtime: number
  version: string
  binary: boolean
  too_large: boolean
  writable: boolean
  content: string
}

export interface SavedFile {
  path: string
  size: number
  mtime: number
  version: string
}

export class FilesError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
  ) {
    super(message)
  }
}

async function check<T>(response: Response): Promise<T> {
  if (response.ok) {
    if (response.status === 204) return undefined as T
    return (await response.json()) as T
  }
  let message = `Erreur ${response.status}`
  let code: string | undefined
  try {
    const text = await response.text()
    try {
      const body = JSON.parse(text) as { error?: string; code?: string }
      message = body.error || message
      code = body.code
    } catch {
      if (text.trim()) message = text.trim()
    }
  } catch {
    /* Le message par défaut suffit. */
  }
  if (response.status === 404 && message === '404 page not found') {
    message = 'L’explorateur de fichiers est désactivé sur ce serveur.'
    code = 'disabled'
  }
  throw new FilesError(message, response.status, code)
}

const q = (path: string) => encodeURIComponent(path)

export const filesApi = {
  list: (path: string, signal?: AbortSignal) =>
    apiFetch(`/api/files/list?path=${q(path)}`, { signal, cache: 'no-store' }).then(r => check<Listing>(r)),
  read: (path: string, signal?: AbortSignal) =>
    apiFetch(`/api/files/content?path=${q(path)}`, { signal, cache: 'no-store' }).then(r => check<FileContent>(r)),
  write: (path: string, content: string, version: string | null, force = false) => {
    const params = new URLSearchParams({ path })
    if (version) params.set('version', version)
    if (force) params.set('force', '1')
    return apiFetch(`/api/files/content?${params}`, {
      method: 'PUT',
      body: content,
      headers: { 'Content-Type': 'text/plain; charset=utf-8' },
    }).then(r => check<SavedFile>(r))
  },
  mkdir: (path: string) =>
    apiFetch('/api/files/mkdir', { method: 'POST', body: JSON.stringify({ path }) }).then(r => check<FileEntry>(r)),
  create: (path: string) =>
    apiFetch('/api/files/create', { method: 'POST', body: JSON.stringify({ path }) }).then(r => check<FileEntry>(r)),
  rename: (from: string, to: string) =>
    apiFetch('/api/files/rename', { method: 'POST', body: JSON.stringify({ from, to }) }).then(r =>
      check<FileEntry>(r),
    ),
  remove: (path: string, recursive: boolean) =>
    apiFetch('/api/files/delete', { method: 'POST', body: JSON.stringify({ path, recursive }) }).then(r =>
      check<void>(r),
    ),
}

export const rawURL = (path: string) => `/api/files/raw?path=${q(path)}`
export const downloadURL = (path: string, isDir = false) =>
  isDir ? `/api/files/archive?path=${q(path)}` : `/api/files/raw?download=1&path=${q(path)}`

/**
 * Uploads one file with progress. fetch cannot report upload progress, so this
 * uses XMLHttpRequest with the same CSRF header as apiFetch.
 */
export function uploadFile(
  path: string,
  file: Blob,
  options: { overwrite?: boolean; csrf?: string; onProgress?: (loaded: number) => void; signal?: AbortSignal },
): Promise<FileEntry> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    const params = new URLSearchParams({ path })
    if (options.overwrite) params.set('overwrite', '1')
    xhr.open('POST', `/api/files/upload?${params}`)
    xhr.setRequestHeader('Content-Type', 'application/octet-stream')
    if (options.csrf) xhr.setRequestHeader('X-TmuxAtlas-CSRF', options.csrf)
    xhr.upload.onprogress = event => options.onProgress?.(event.loaded)
    xhr.onload = () => {
      void check<FileEntry>(new Response(xhr.status === 204 ? null : xhr.responseText, { status: xhr.status }))
        .then(resolve, reject)
      if (xhr.status === 401) window.dispatchEvent(new Event('auth:unauthorized'))
    }
    xhr.onerror = () => reject(new FilesError('Connexion interrompue pendant l’envoi.', 0))
    xhr.onabort = () => reject(new FilesError('Envoi annulé.', 0, 'aborted'))
    options.signal?.addEventListener('abort', () => xhr.abort(), { once: true })
    xhr.send(file)
  })
}

// ---------------------------------------------------------------------------
// Paths

export function joinPath(dir: string, name: string) {
  return dir === '/' ? `/${name}` : `${dir.replace(/\/+$/, '')}/${name}`
}

export function dirname(path: string) {
  const index = path.replace(/\/+$/, '').lastIndexOf('/')
  return index <= 0 ? '/' : path.slice(0, index)
}

export function basename(path: string) {
  const trimmed = path.replace(/\/+$/, '')
  return trimmed.slice(trimmed.lastIndexOf('/') + 1) || '/'
}

/** Resolves a relative reference (from a Markdown link) against a folder. */
export function resolvePath(dir: string, ref: string) {
  const parts = (ref.startsWith('/') ? ref : `${dir}/${ref}`).split('/')
  const out: string[] = []
  for (const part of parts) {
    if (!part || part === '.') continue
    if (part === '..') out.pop()
    else out.push(part)
  }
  return `/${out.join('/')}`
}

export function breadcrumbs(path: string): { name: string; path: string }[] {
  const crumbs = [{ name: '/', path: '/' }]
  let current = ''
  for (const part of path.split('/').filter(Boolean)) {
    current += `/${part}`
    crumbs.push({ name: part, path: current })
  }
  return crumbs
}

// ---------------------------------------------------------------------------
// File kinds

export type FileKind = 'image' | 'video' | 'audio' | 'markdown' | 'text' | 'pdf' | 'archive' | 'binary'

const imageExt = new Set(['png', 'jpg', 'jpeg', 'jfif', 'gif', 'webp', 'avif', 'bmp', 'ico', 'svg', 'tif', 'tiff'])
const videoExt = new Set(['mp4', 'webm', 'mov', 'ogv'])
const audioExt = new Set(['mp3', 'wav', 'ogg', 'oga', 'flac', 'm4a', 'opus'])
const archiveExt = new Set(['zip', 'tar', 'gz', 'tgz', 'bz2', 'xz', 'zst', '7z', 'rar'])
const markdownExt = new Set(['md', 'markdown', 'mdx', 'mkd'])

export function extension(name: string) {
  const index = name.lastIndexOf('.')
  return index > 0 ? name.slice(index + 1).toLowerCase() : ''
}

export function fileKind(name: string): FileKind {
  const ext = extension(name)
  if (imageExt.has(ext)) return 'image'
  if (videoExt.has(ext)) return 'video'
  if (audioExt.has(ext)) return 'audio'
  if (markdownExt.has(ext)) return 'markdown'
  if (ext === 'pdf') return 'pdf'
  if (archiveExt.has(ext)) return 'archive'
  return 'text'
}

export function isDirectory(entry: Pick<FileEntry, 'type' | 'link_dir'>) {
  return entry.type === 'dir' || Boolean(entry.link_dir)
}

// ---------------------------------------------------------------------------
// Formatting

export function formatSize(bytes: number) {
  if (bytes < 1024) return `${bytes} o`
  const units = ['Kio', 'Mio', 'Gio', 'Tio']
  let value = bytes / 1024
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value >= 100 ? Math.round(value) : value.toFixed(1).replace('.', ',')} ${units[unit]}`
}

export function formatDate(ms: number, now = Date.now()) {
  const date = new Date(ms)
  const sameDay = new Date(now).toDateString() === date.toDateString()
  if (sameDay) return date.toLocaleTimeString('fr-FR', { hour: '2-digit', minute: '2-digit' })
  const sameYear = new Date(now).getFullYear() === date.getFullYear()
  return date.toLocaleDateString('fr-FR', {
    day: 'numeric',
    month: 'short',
    ...(sameYear ? {} : { year: 'numeric' }),
  })
}
