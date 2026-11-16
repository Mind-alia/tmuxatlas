import { apiFetch } from './api'
import { RuntimeAPIError, type RuntimeAPIErrorBody } from './runtimeApi'

export type AccountRoleName = 'admin' | 'guest'

export interface AccountUser {
  id: string
  username: string
  role: AccountRoleName
  disabled: boolean
  created_at: string
  password_changed_at?: string
  /** Host IDs a guest may use. Administrators see every host. */
  hosts?: string[]
  unix_user?: string
  /** No password yet: the account waits for its invitation. */
  pending?: boolean
  invitation_expires_at?: string
}

export interface InvitationLink {
  token: string
  /** Relative link, `/invite#<token>`: the token stays in the URL fragment. */
  path: string
  expires_at: string
}

export interface CreatedAccount extends AccountUser {
  invitation?: InvitationLink
}

export interface AdminHost {
  id: string
  name: string
  online: boolean
  local: boolean
  grantable: boolean
  guest?: string
}

export interface AdminSession {
  id: string
  name: string
  host: string
  host_name?: string
  host_online?: boolean
  owner?: string | null
  created?: string
  attached?: boolean
  last_activity?: string
}

const HEX_ACCOUNT_ID_RE = /^[0-9a-f]{32}$/i

export function isAccountHexId(value: string | null | undefined): boolean {
  if (!value) return false
  return HEX_ACCOUNT_ID_RE.test(value.trim())
}

export function resolveOwnerName(
  owner: string | null | undefined,
  options: {
    usersById?: ReadonlyMap<string, string> | Record<string, string>
    currentUsername?: string | null
    currentRole?: AccountRoleName | null
  } = {},
): string | null {
  const raw = owner?.trim()
  if (!raw) return null

  const { usersById, currentUsername, currentRole } = options
  if (usersById) {
    const mapped =
      'get' in usersById && typeof usersById.get === 'function'
        ? usersById.get(raw)
        : (usersById as Record<string, string>)[raw]
    if (mapped) return mapped
  }

  if (isAccountHexId(raw)) {
    return null
  }

  return raw
}

async function parseErrorMessage(res: Response, fallback: string): Promise<string> {
  try {
    const text = (await res.text()).trim()
    if (!text) return fallback
    try {
      const parsed = JSON.parse(text) as { code?: string; error?: string; message?: string }
      if (parsed.code) return new RuntimeAPIError(res.status, parsed).message
      if (parsed.error) return translateAccountServerError(parsed.error, fallback)
      if (parsed.message) return translateAccountServerError(parsed.message, fallback)
    } catch {
      return translateAccountServerError(text, fallback)
    }
  } catch {
    // ignore
  }
  return fallback
}

export function translateAccountServerError(raw: string, fallback: string): string {
  const text = raw.trim()
  if (/nom d'utilisateur déjà pris/i.test(text) || /already exists/i.test(text)) {
    return 'Ce nom d’utilisateur est déjà utilisé.'
  }
  if (/nom d'utilisateur invalide/i.test(text)) {
    return 'Nom d’utilisateur invalide (1 à 32 caractères : lettres minuscules, chiffres, point, tiret ou tiret bas).'
  }
  if (/mot de passe/i.test(text) && /10.*200/i.test(text)) {
    return 'Le mot de passe doit contenir entre 10 et 200 caractères.'
  }
  if (/identifiants invalides/i.test(text)) {
    return 'L’ancien mot de passe est incorrect.'
  }
  if (/dernier administrateur/i.test(text)) {
    return 'Impossible de désactiver ou supprimer le dernier administrateur actif.'
  }
  if (/déjà attribué à un autre invité/i.test(text)) {
    return 'Cet hôte est déjà attribué à un autre invité : un compte Linux par personne.'
  }
  if (/ne peut pas être attribué/i.test(text)) {
    return 'Cet hôte est celui du hub (compte de production) : il ne peut pas être attribué à un invité.'
  }
  if (/opération interdite sur cet administrateur/i.test(text)) {
    return 'Impossible : c’est votre propre compte ou le dernier administrateur actif.'
  }
  if (/introuvable/i.test(text)) {
    return 'Ce compte est introuvable.'
  }
  if (/trop de tentatives/i.test(text)) {
    return 'Trop de tentatives. Réessayez dans quelques instants.'
  }
  return text || fallback
}

export interface AccountDirectoryEntry {
  id: string
  username: string
}

export async function fetchAccountDirectory(): Promise<AccountDirectoryEntry[]> {
  const res = await apiFetch('/api/accounts/directory')
  if (!res.ok) {
    throw new Error(await parseErrorMessage(res, 'Impossible de charger les comptes.'))
  }
  return (await res.json()) as AccountDirectoryEntry[]
}

export async function fetchAdminUsers(): Promise<AccountUser[]> {
  const res = await apiFetch('/api/admin/users')
  if (!res.ok) {
    throw new Error(await parseErrorMessage(res, 'Impossible de charger les comptes.'))
  }
  const data = (await res.json()) as { users?: AccountUser[] } | AccountUser[]
  if (Array.isArray(data)) return data
  return data?.users ?? []
}

/** Creates an account. Without a password, the response carries a one-time invitation link. */
export async function createAdminUser(
  username: string,
  password: string,
  options: { role?: AccountRoleName; unixUser?: string } = {},
): Promise<CreatedAccount> {
  const res = await apiFetch('/api/admin/users', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      username: username.trim(),
      password,
      role: options.role ?? 'guest',
      unix_user: options.unixUser?.trim() ?? '',
    }),
  })
  if (!res.ok) {
    throw new Error(await parseErrorMessage(res, 'Impossible de créer le compte.'))
  }
  return (await res.json()) as CreatedAccount
}

export async function updateAdminUser(
  id: string,
  patch: {
    password?: string
    disabled?: boolean
    role?: AccountRoleName
    hosts?: string[]
    unix_user?: string
  },
): Promise<AccountUser> {
  const res = await apiFetch(`/api/admin/users/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(patch),
  })
  if (!res.ok) {
    throw new Error(await parseErrorMessage(res, 'Impossible de mettre à jour le compte.'))
  }
  return (await res.json()) as AccountUser
}

export async function deleteAdminUser(id: string): Promise<void> {
  const res = await apiFetch(`/api/admin/users/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })
  if (!res.ok) {
    throw new Error(await parseErrorMessage(res, 'Impossible de supprimer le compte.'))
  }
}

export async function fetchAdminSessions(): Promise<AdminSession[]> {
  const res = await apiFetch('/api/admin/sessions')
  if (!res.ok) {
    throw new Error(await parseErrorMessage(res, 'Impossible de charger les sessions.'))
  }
  const data = (await res.json()) as AdminSession[] | null
  return data ?? []
}

export async function claimSession(hostId: string, session: string, owner?: string): Promise<void> {
  const payload: Record<string, unknown> = { host_id: hostId, session }
  if (owner !== undefined) {
    payload.owner = owner
  }
  const res = await apiFetch('/api/session/claim', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  })
  if (!res.ok) {
    let errorBody: RuntimeAPIErrorBody = {}
    try {
      errorBody = (await res.json()) as RuntimeAPIErrorBody
    } catch {
      // ignore non-json
    }
    if (res.status === 409 && !errorBody.code) {
      errorBody.code = 'already-owned'
    }
    throw new RuntimeAPIError(res.status, errorBody)
  }
}

export async function releaseSession(hostId: string, session: string): Promise<void> {
  const res = await apiFetch('/api/session/release', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ host_id: hostId, session }),
  })
  if (!res.ok) {
    let errorBody: RuntimeAPIErrorBody = {}
    try {
      errorBody = (await res.json()) as RuntimeAPIErrorBody
    } catch {
      // ignore non-json
    }
    throw new RuntimeAPIError(res.status, errorBody)
  }
}

export async function changeOwnPassword(oldPassword: string, newPassword: string): Promise<void> {
  const res = await apiFetch('/api/auth/change-password', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ old_password: oldPassword, new_password: newPassword }),
  })
  if (!res.ok) {
    if (res.status === 401) {
      throw new Error('L’ancien mot de passe est incorrect.')
    }
    if (res.status === 429) {
      throw new Error('Trop de tentatives. Réessayez dans quelques instants.')
    }
    throw new Error(await parseErrorMessage(res, 'Impossible de modifier le mot de passe.'))
  }
}

export async function fetchAdminHosts(): Promise<AdminHost[]> {
  const res = await apiFetch('/api/admin/hosts')
  if (!res.ok) {
    throw new Error(await parseErrorMessage(res, 'Impossible de charger les hôtes.'))
  }
  const data = (await res.json()) as { hosts?: AdminHost[] }
  return data.hosts ?? []
}

export async function createInvitation(id: string, hours?: number): Promise<InvitationLink> {
  const res = await apiFetch(`/api/admin/users/${encodeURIComponent(id)}/invitation`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(hours ? { hours } : {}),
  })
  if (!res.ok) {
    throw new Error(await parseErrorMessage(res, 'Impossible de créer le lien d’invitation.'))
  }
  return (await res.json()) as InvitationLink
}

export async function cancelInvitation(id: string): Promise<void> {
  const res = await apiFetch(`/api/admin/users/${encodeURIComponent(id)}/invitation`, { method: 'DELETE' })
  if (!res.ok) {
    throw new Error(await parseErrorMessage(res, 'Impossible d’annuler l’invitation.'))
  }
}

export async function createAccountPairingCode(id: string): Promise<{ code: string; expires_at: string }> {
  const res = await apiFetch(`/api/admin/users/${encodeURIComponent(id)}/pairing`, { method: 'POST' })
  if (!res.ok) {
    throw new Error(await parseErrorMessage(res, 'Impossible de créer le code d’appairage.'))
  }
  return (await res.json()) as { code: string; expires_at: string }
}

/** Absolute invitation URL. The token is in the fragment and never reaches the server's logs. */
export function invitationURL(link: InvitationLink, origin = window.location.origin): string {
  return `${origin}${link.path}`
}

export async function inspectInvitation(token: string): Promise<{ username: string; expires_at: string; has_password: boolean }> {
  const res = await apiFetch('/api/auth/invitation/inspect', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token }),
  })
  if (!res.ok) {
    if (res.status === 429) throw new Error('Trop de tentatives. Réessayez dans quelques instants.')
    throw new Error('Ce lien d’invitation est invalide, déjà utilisé ou expiré.')
  }
  return (await res.json()) as { username: string; expires_at: string; has_password: boolean }
}

export async function acceptInvitation(token: string, password: string): Promise<void> {
  const res = await apiFetch('/api/auth/invitation/accept', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token, password }),
  })
  if (!res.ok) {
    if (res.status === 404) throw new Error('Ce lien d’invitation est invalide, déjà utilisé ou expiré.')
    throw new Error(await parseErrorMessage(res, 'Impossible d’enregistrer le mot de passe.'))
  }
}
