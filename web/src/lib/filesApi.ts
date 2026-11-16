import { apiFetch } from './api'

export interface FileHost {
  id: string
  name: string
  online: boolean
  local: boolean
}

export interface FileEntry {
  name: string
  type: 'file' | 'dir' | 'symlink' | 'other'
  size: number
  mtime: string
  mode: string
}

export interface FileListing {
  path: string
  entries: FileEntry[]
  truncated: boolean
}

export interface FileInfo {
  mode: 'off' | 'ro' | 'rw'
  root: string
}

export interface FileContent {
  path: string
  content: string
  size: number
  mtime: string
  version: string
  writable: boolean
}

export class FilesAPIError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message)
  }
}

const fallbackMessages: Record<number, string> = {
  403: 'Accès refusé.',
  404: 'Introuvable.',
  409: 'Conflit : le fichier a changé ou le nom existe déjà.',
  413: 'Fichier trop volumineux.',
  415: 'Fichier binaire : téléchargez-le.',
  429: 'Trop d’opérations en cours.',
  503: 'L’Agent de cet hôte est hors ligne.',
}

async function check(res: Response): Promise<Response> {
  if (res.ok) return res
  let code = ''
  let message = fallbackMessages[res.status] ?? 'Opération impossible.'
  try {
    const body = (await res.json()) as { code?: string; message?: string }
    code = body.code ?? ''
    if (body.message) message = body.message.charAt(0).toUpperCase() + body.message.slice(1) + '.'
  } catch {
    // not JSON: keep the fallback
  }
  throw new FilesAPIError(res.status, code, message)
}

function query(host: string, params: Record<string, string> = {}): string {
  return new URLSearchParams({ host_id: host, ...params }).toString()
}

export async function fetchFileHosts(): Promise<FileHost[]> {
  const res = await check(await apiFetch('/api/files/hosts', { cache: 'no-store' }))
  return ((await res.json()) as { hosts?: FileHost[] }).hosts ?? []
}

export async function fetchFileInfo(host: string): Promise<FileInfo> {
  const res = await check(await apiFetch(`/api/files/info?${query(host)}`, { cache: 'no-store' }))
  return (await res.json()) as FileInfo
}

export async function listFiles(host: string, path: string, signal?: AbortSignal): Promise<FileListing> {
  const res = await check(await apiFetch(`/api/files/list?${query(host, { path })}`, { cache: 'no-store', signal }))
  return (await res.json()) as FileListing
}

export async function readFile(host: string, path: string): Promise<FileContent> {
  const res = await check(await apiFetch(`/api/files/content?${query(host, { path })}`, { cache: 'no-store' }))
  return (await res.json()) as FileContent
}

export async function saveFile(host: string, path: string, content: string, version: string): Promise<FileContent> {
  const res = await check(
    await apiFetch(`/api/files/content?${query(host, { path, version })}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'text/plain; charset=utf-8' },
      body: content,
    }),
  )
  return (await res.json()) as FileContent
}

export async function uploadFile(host: string, path: string, file: Blob, overwrite = false): Promise<void> {
  const params: Record<string, string> = { path }
  if (overwrite) params.overwrite = '1'
  await check(
    await apiFetch(`/api/files/upload?${query(host, params)}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/octet-stream' },
      body: file,
    }),
  )
}

async function change(host: string, operation: 'mkdir' | 'rename' | 'delete', body: Record<string, string>) {
  await check(
    await apiFetch(`/api/files/${operation}?${query(host)}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  )
}

export const makeDirectory = (host: string, path: string) => change(host, 'mkdir', { path })
export const renameFile = (host: string, from: string, to: string) => change(host, 'rename', { from, to })
export const deleteFile = (host: string, path: string) => change(host, 'delete', { path })

export function downloadURL(host: string, path: string): string {
  return `/api/files/raw?${query(host, { path })}`
}

export function joinPath(dir: string, name: string): string {
  return dir === '/' ? `/${name}` : `${dir.replace(/\/+$/, '')}/${name}`
}

export function parentPath(path: string): string {
  const index = path.replace(/\/+$/, '').lastIndexOf('/')
  return index <= 0 ? '/' : path.slice(0, index)
}

export const MAX_UPLOAD_BYTES = 1024 * 1024 - 16 * 1024
