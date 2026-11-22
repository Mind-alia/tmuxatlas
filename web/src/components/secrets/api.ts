import { apiFetch } from '../../lib/api'
import type { ApplyResult, AuditEntry, ImportResult, SecretApp, VaultStatus } from './types'

export class SecretsApiError extends Error {
  status: number
  /** Machine-readable reason: locked, wrong_passphrase, throttled, invalid… */
  code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'SecretsApiError'
    this.status = status
    this.code = code
  }
}

function capitalize(text: string) {
  return text ? text[0].toUpperCase() + text.slice(1) : text
}

async function failure(response: Response): Promise<SecretsApiError> {
  let message = ''
  let code = ''
  try {
    const text = await response.text()
    try {
      const body = JSON.parse(text) as { error?: string; code?: string }
      message = body.error ?? ''
      code = body.code ?? ''
    } catch {
      message = text
    }
  } catch {
    /* corps illisible */
  }
  if (response.status === 401) message = 'Session expirée : reconnecte-toi.'
  if (response.status === 403 && !code) message = 'Cette page est réservée aux administrateurs.'
  if (response.status === 413) message = 'Texte trop long.'
  return new SecretsApiError(response.status, code, capitalize(message.trim()) || `Erreur ${response.status}`)
}

async function request<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  let response: Response
  try {
    response = await apiFetch(path, {
      method,
      cache: 'no-store',
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    })
  } catch {
    throw new SecretsApiError(0, '', 'Impossible de joindre le serveur Atlas.')
  }
  if (!response.ok) throw await failure(response)
  return (await response.json()) as T
}

const appPath = (app: string) => `/api/secrets/apps/${encodeURIComponent(app)}`
const varPath = (app: string, name: string) => `${appPath(app)}/vars/${encodeURIComponent(name)}`

export const fetchStatus = () => request<VaultStatus>('/api/secrets/status')
export const initVault = (passphrase: string) => request<VaultStatus>('/api/secrets/init', 'POST', { passphrase })
export const unlockVault = (passphrase: string) => request<VaultStatus>('/api/secrets/unlock', 'POST', { passphrase })
export const lockVault = () => request<VaultStatus>('/api/secrets/lock', 'POST')
export const fetchApps = async () => (await request<{ apps: SecretApp[] }>('/api/secrets/apps')).apps
export const createApp = (name: string) => request<SecretApp>('/api/secrets/apps', 'POST', { name })
export const deleteApp = (app: string) => request<unknown>(appPath(app), 'DELETE')
export const setTarget = (app: string, envFile: string, container: string) =>
  request<SecretApp>(`${appPath(app)}/target`, 'PUT', { env_file: envFile, container })
export const setVariable = (app: string, name: string, value: string) =>
  request<SecretApp>(varPath(app, name), 'PUT', { value })
export const deleteVariable = (app: string, name: string) => request<SecretApp>(varPath(app, name), 'DELETE')
export const revealVariable = async (app: string, name: string) =>
  (await request<{ value: string }>(`${varPath(app, name)}/reveal`, 'POST')).value
export const importDotenv = (app: string, text: string) =>
  request<ImportResult>(`${appPath(app)}/import`, 'POST', { text })
export const exportDotenv = async (app: string) =>
  (await request<{ dotenv: string }>(`${appPath(app)}/export`, 'POST')).dotenv
export const applyApp = (app: string) => request<ApplyResult>(`${appPath(app)}/apply`, 'POST')
export const fetchAudit = async (limit = 50) =>
  (await request<{ entries: AuditEntry[] }>(`/api/secrets/audit?limit=${limit}`)).entries
