import { useEffect, useState } from 'react'
import { Copy, Link2, Server } from 'lucide-react'
import {
  createAccountPairingCode,
  createInvitation,
  fetchAdminHosts,
  invitationURL,
  updateAdminUser,
  type AccountRoleName,
  type AccountUser,
  type AdminHost,
  type InvitationLink,
} from '../lib/accountsApi'
import { isValidUnixUser, linuxSetupCommands } from '../lib/linuxSetup'
import { Button, Dialog } from './ui'

interface AccountAccessDialogProps {
  user: AccountUser
  isSelf: boolean
  usersById: ReadonlyMap<string, string>
  onClose: () => void
  onChanged: () => void
}

async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}

/**
 * Role, host grants, invitation link and Linux account setup of one account.
 * Every rule is enforced by the server; this dialog only edits the values.
 */
export function AccountAccessDialog({ user, isSelf, usersById, onClose, onChanged }: AccountAccessDialogProps) {
  const [role, setRole] = useState<AccountRoleName>(user.role)
  const [unixUser, setUnixUser] = useState(user.unix_user ?? '')
  const [hosts, setHosts] = useState<string[]>(user.hosts ?? [])
  const [available, setAvailable] = useState<AdminHost[]>([])
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [invitation, setInvitation] = useState<InvitationLink | null>(null)
  const [pairingCode, setPairingCode] = useState<string | null>(null)
  const [copied, setCopied] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    fetchAdminHosts()
      .then(list => {
        if (!cancelled) setAvailable(list)
      })
      .catch(err => {
        if (!cancelled) setError(err instanceof Error ? err.message : 'Impossible de charger les hôtes.')
      })
    return () => {
      cancelled = true
    }
  }, [])

  const unixValid = unixUser === '' || isValidUnixUser(unixUser)
  const knownIds = new Set(available.map(host => host.id))
  const listedHosts: AdminHost[] = [
    ...available,
    // A granted host that is not connected right now stays visible, so it can be removed.
    ...hosts
      .filter(id => !knownIds.has(id))
      .map(id => ({ id, name: id, online: false, local: false, grantable: true })),
  ]

  const save = async () => {
    setError(null)
    if (!unixValid) {
      setError('Nom de compte Linux invalide (minuscules, chiffres, - et _, 32 caractères au plus).')
      return
    }
    setSaving(true)
    try {
      const patch: Parameters<typeof updateAdminUser>[1] = { hosts, unix_user: unixUser }
      if (role !== user.role) patch.role = role
      await updateAdminUser(user.id, patch)
      onChanged()
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Impossible d’enregistrer les accès.')
    } finally {
      setSaving(false)
    }
  }

  const newInvitation = async () => {
    setError(null)
    try {
      setInvitation(await createInvitation(user.id))
      onChanged()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Impossible de créer le lien.')
    }
  }

  const newPairingCode = async () => {
    setError(null)
    try {
      setPairingCode((await createAccountPairingCode(user.id)).code)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Impossible de créer le code.')
    }
  }

  const copy = async (key: string, text: string) => {
    if (await copyText(text)) setCopied(key)
  }

  const commands = unixUser && unixValid
    ? linuxSetupCommands({ unixUser, hubURL: window.location.origin, pairingCode: pairingCode ?? undefined })
    : ''

  return (
    <Dialog
      open
      onOpenChange={open => {
        if (!open) onClose()
      }}
      title={`Accès de ${user.username}`}
      description="Le serveur vérifie chaque droit : un invité ne voit que les hôtes cochés ici."
      footer={
        <>
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuler
          </Button>
          <Button type="button" variant="primary" loading={saving} onClick={() => void save()}>
            Enregistrer
          </Button>
        </>
      }
    >
      <div className="space-y-5 text-xs">
        {error && (
          <p role="alert" className="rounded border border-destructive/40 bg-destructive/10 px-3 py-2 text-destructive">
            {error}
          </p>
        )}

        <fieldset className="space-y-2">
          <legend className="font-medium text-foreground">Rôle</legend>
          {(['guest', 'admin'] as const).map(value => (
            <label key={value} className="flex items-start gap-2">
              <input
                type="radio"
                name="account-role"
                value={value}
                checked={role === value}
                disabled={isSelf}
                onChange={() => setRole(value)}
                className="mt-0.5"
              />
              <span>
                <span className="text-foreground">{value === 'admin' ? 'Administrateur' : 'Invité'}</span>
                <span className="block text-muted-foreground">
                  {value === 'admin'
                    ? 'Voit et pilote tous les hôtes, y compris celui du hub (production), et gère les comptes.'
                    : 'Ne voit que ses hôtes, sans monitoring, micro ni administration.'}
                </span>
              </span>
            </label>
          ))}
          {isSelf && <p className="text-muted-foreground">Vous ne pouvez pas changer votre propre rôle.</p>}
        </fieldset>

        <div className="space-y-1">
          <label htmlFor="account-unix-user" className="block font-medium text-foreground">
            Compte Linux de la personne
          </label>
          <input
            id="account-unix-user"
            type="text"
            value={unixUser}
            onChange={event => setUnixUser(event.target.value.trim())}
            placeholder="ex. inv-alice"
            spellCheck={false}
            autoComplete="off"
            aria-invalid={!unixValid}
            className="ui-focus-ring min-h-11 w-full rounded bg-input px-3 font-mono text-sm text-foreground placeholder:text-muted-foreground"
          />
          <p className="text-muted-foreground">Un compte par personne : un invité ne partage jamais celui d’un autre, ni la production.</p>
        </div>

        <fieldset className="space-y-2">
          <legend className="font-medium text-foreground">Hôtes attribués</legend>
          {role === 'admin' && <p className="text-muted-foreground">Un administrateur voit tous les hôtes.</p>}
          {listedHosts.length === 0 && <p className="text-muted-foreground">Aucun hôte connu.</p>}
          {listedHosts.map(host => {
            const takenBy = host.guest && host.guest !== user.id ? usersById.get(host.guest) ?? 'un autre invité' : null
            const disabled = !host.grantable || Boolean(takenBy)
            return (
              <label key={host.id} className="flex items-start gap-2">
                <input
                  type="checkbox"
                  checked={hosts.includes(host.id)}
                  disabled={disabled && !hosts.includes(host.id)}
                  onChange={event =>
                    setHosts(current =>
                      event.target.checked ? [...current, host.id] : current.filter(id => id !== host.id),
                    )
                  }
                  className="mt-0.5"
                />
                <span>
                  <span className="inline-flex items-center gap-1 text-foreground">
                    <Server size={12} aria-hidden="true" />
                    {host.name}
                  </span>
                  <span className="block font-mono text-2xs text-muted-foreground">
                    {host.id}
                    {host.online ? '' : ' · hors ligne'}
                    {!host.grantable ? ' · hôte du hub, jamais attribué à un invité' : ''}
                    {takenBy ? ` · attribué à ${takenBy}` : ''}
                  </span>
                </span>
              </label>
            )
          })}
        </fieldset>

        <section className="space-y-2">
          <h3 className="font-medium text-foreground">Lien d’invitation</h3>
          <p className="text-muted-foreground">
            À usage unique, valable 24 h : la personne y choisit son mot de passe. Un nouveau lien remplace l’ancien et
            sert aussi à réinitialiser un mot de passe oublié.
          </p>
          <Button
            type="button"
            variant="secondary"
            size="sm"
            leadingIcon={<Link2 size={14} aria-hidden="true" />}
            onClick={() => void newInvitation()}
          >
            Créer un lien
          </Button>
          {invitation && (
            <div className="flex items-center gap-2">
              <code data-testid="invitation-url" className="min-w-0 flex-1 break-all rounded bg-muted px-2 py-1">
                {invitationURL(invitation)}
              </code>
              <Button
                type="button"
                variant="secondary"
                size="sm"
                leadingIcon={<Copy size={14} aria-hidden="true" />}
                onClick={() => void copy('invitation', invitationURL(invitation))}
              >
                {copied === 'invitation' ? 'Copié' : 'Copier'}
              </Button>
            </div>
          )}
        </section>

        <section className="space-y-2">
          <h3 className="font-medium text-foreground">Préparer son compte Linux</h3>
          <p className="text-muted-foreground">
            À lancer par un humain, en root : TmuxAtlas n’exécute rien. L’hôte appairé avec ce code est attribué
            automatiquement à {user.username}.
          </p>
          <Button type="button" variant="secondary" size="sm" onClick={() => void newPairingCode()}>
            Créer un code d’appairage (5 min)
          </Button>
          {commands ? (
            <div className="space-y-2">
              <pre data-testid="linux-setup-commands" className="overflow-x-auto rounded bg-muted p-2 font-mono text-2xs">
                {commands}
              </pre>
              <Button
                type="button"
                variant="secondary"
                size="sm"
                leadingIcon={<Copy size={14} aria-hidden="true" />}
                onClick={() => void copy('commands', commands)}
              >
                {copied === 'commands' ? 'Copié' : 'Copier les commandes'}
              </Button>
            </div>
          ) : (
            <p className="text-muted-foreground">Indiquez le compte Linux pour afficher les commandes.</p>
          )}
        </section>
      </div>
    </Dialog>
  )
}
