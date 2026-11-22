import { Eye, EyeOff, KeyRound, Lock, LockOpen, Pencil, Plus, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { Button, Dialog, EmptyState, IconButton, StatusPill } from '../ui'
import {
  SecretsApiError,
  applyApp,
  createApp,
  deleteApp,
  deleteVariable,
  exportDotenv,
  fetchApps,
  fetchAudit,
  fetchStatus,
  importDotenv,
  initVault,
  lockVault,
  revealVariable,
  setTarget,
  setVariable,
  unlockVault,
} from './api'
import type { ApplyResult, AuditEntry, SecretApp, VaultStatus } from './types'

/** A revealed value is masked again after this long. */
export const REVEAL_MS = 15_000
/** An exported dotenv text is cleared from the page after this long. */
export const EXPORT_MS = 30_000

const inputClass =
  'ui-focus-ring h-9 w-full min-w-0 rounded-md border border-border bg-input px-2.5 text-xs text-foreground placeholder:text-muted-foreground'
const areaClass =
  'ui-focus-ring w-full min-w-0 rounded-md border border-border bg-input px-2.5 py-1.5 font-mono text-xs text-foreground placeholder:text-muted-foreground'
const cardClass = 'rounded-lg border border-border bg-card p-3 sm:p-4'
const labelClass = 'mb-1 block text-2xs font-medium text-muted-foreground'

/** Keeps password managers and the browser away from secret fields. */
const secretField = {
  autoComplete: 'off',
  autoCorrect: 'off',
  autoCapitalize: 'off',
  spellCheck: false,
  'data-1p-ignore': '',
  'data-lpignore': 'true',
} as const

const actionLabels: Record<string, string> = {
  init: 'Coffre initialisé',
  unlock: 'Déverrouillage',
  unlock_failed: 'Déverrouillage refusé',
  unlock_blocked: 'Déverrouillage bloqué (trop d’essais)',
  lock: 'Verrouillage',
  app_create: 'Application créée',
  app_delete: 'Application supprimée',
  var_set: 'Valeur définie',
  var_delete: 'Variable supprimée',
  reveal: 'Valeur révélée',
  import: 'Import dotenv',
  export: 'Export dotenv',
  target_set: 'Cible définie',
  target_clear: 'Cible retirée',
  apply: 'Fichier appliqué',
  restart: 'Conteneur redémarré',
  restart_failed: 'Redémarrage en échec',
}

function message(error: unknown) {
  return error instanceof Error ? error.message : 'Erreur inconnue'
}

export function formatRemaining(seconds: number) {
  const safe = Math.max(0, Math.ceil(seconds))
  const minutes = Math.floor(safe / 60)
  const rest = String(safe % 60).padStart(2, '0')
  return minutes > 0 ? `${minutes} min ${rest} s` : `${safe % 60} s`
}

function formatTime(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString('fr-FR', { dateStyle: 'short', timeStyle: 'medium' })
}

function ErrorNote({ text, onDismiss }: { text: string; onDismiss?: () => void }) {
  return (
    <div
      role="alert"
      className="flex items-start gap-2 rounded-md px-2 py-1.5 text-xs"
      style={{ color: 'var(--destructive)', background: 'color-mix(in oklch, var(--destructive) 10%, transparent)' }}
    >
      <span className="min-w-0 flex-1 break-words">{text}</span>
      {onDismiss && (
        <button type="button" onClick={onDismiss} className="ui-focus-ring shrink-0 rounded px-1 text-2xs underline">
          Fermer
        </button>
      )}
    </div>
  )
}

function InitForm({ minLength, onDone }: { minLength: number; onDone: (status: VaultStatus) => void }) {
  const [passphrase, setPassphrase] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [understood, setUnderstood] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const tooShort = [...passphrase].length < minLength
  const mismatch = confirmation !== '' && confirmation !== passphrase

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (tooShort || passphrase !== confirmation || !understood) return
    setBusy(true)
    setError(null)
    try {
      const status = await initVault(passphrase)
      setPassphrase('')
      setConfirmation('')
      onDone(status)
    } catch (failure) {
      setError(message(failure))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={submit} className={`${cardClass} max-w-xl space-y-3`} autoComplete="off">
      <div>
        <h2 className="text-sm font-medium">Créer le coffre</h2>
        <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
          Choisis une phrase de passe. Elle chiffre la clé du coffre et n’est enregistrée nulle part : sans elle, les
          valeurs stockées ne peuvent plus être lues, par personne.
        </p>
      </div>
      <div>
        <label htmlFor="secrets-init-passphrase" className={labelClass}>
          Phrase de passe ({minLength} caractères au moins)
        </label>
        <input
          id="secrets-init-passphrase"
          type="password"
          autoComplete="new-password"
          className={inputClass}
          value={passphrase}
          onChange={event => setPassphrase(event.target.value)}
        />
      </div>
      <div>
        <label htmlFor="secrets-init-confirmation" className={labelClass}>
          Confirmer la phrase de passe
        </label>
        <input
          id="secrets-init-confirmation"
          type="password"
          autoComplete="new-password"
          className={inputClass}
          value={confirmation}
          onChange={event => setConfirmation(event.target.value)}
          aria-invalid={mismatch || undefined}
        />
        {mismatch && <p className="mt-1 text-2xs text-destructive">Les deux phrases de passe ne sont pas identiques.</p>}
      </div>
      <label className="flex items-start gap-2 text-xs leading-relaxed">
        <input
          type="checkbox"
          className="mt-0.5 accent-[var(--primary)]"
          checked={understood}
          onChange={event => setUnderstood(event.target.checked)}
        />
        <span>J’ai noté cette phrase de passe en lieu sûr : si je la perds, les secrets du coffre sont perdus.</span>
      </label>
      {error && <ErrorNote text={error} />}
      <Button
        type="submit"
        variant="primary"
        loading={busy}
        disabled={tooShort || passphrase !== confirmation || !understood}
        leadingIcon={<KeyRound size={14} aria-hidden />}
      >
        Créer le coffre
      </Button>
    </form>
  )
}

function LockBanner({
  status,
  remaining,
  onStatus,
}: {
  status: VaultStatus
  remaining: number
  onStatus: (status: VaultStatus) => void
}) {
  const [passphrase, setPassphrase] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const unlock = async (event: FormEvent) => {
    event.preventDefault()
    if (!passphrase) return
    setBusy(true)
    setError(null)
    try {
      const next = await unlockVault(passphrase)
      setPassphrase('')
      onStatus(next)
    } catch (failure) {
      setError(message(failure))
    } finally {
      setBusy(false)
    }
  }
  const lock = async () => {
    setBusy(true)
    setError(null)
    try {
      onStatus(await lockVault())
    } catch (failure) {
      setError(message(failure))
    } finally {
      setBusy(false)
    }
  }

  if (status.unlocked) {
    return (
      <section aria-label="État du coffre" className={`${cardClass} space-y-2`}>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
          <StatusPill status="waiting" mark={<LockOpen size={12} aria-hidden />}>
            Déverrouillé
          </StatusPill>
          <span className="min-w-0 flex-1 text-xs text-muted-foreground" role="timer" aria-live="off">
            Se reverrouille dans <span className="font-medium tabular-nums text-foreground">{formatRemaining(remaining)}</span>
          </span>
          <Button size="sm" onClick={lock} loading={busy} leadingIcon={<Lock size={14} aria-hidden />}>
            Verrouiller
          </Button>
        </div>
        {error && <ErrorNote text={error} />}
      </section>
    )
  }
  return (
    <section aria-label="État du coffre" className={`${cardClass} space-y-2`}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <StatusPill status="done" mark={<Lock size={12} aria-hidden />}>
          Verrouillé
        </StatusPill>
        <span className="min-w-0 flex-1 text-xs text-muted-foreground">
          {status.unlocked_elsewhere
            ? 'Déverrouillé dans une autre session, pas dans celle-ci : chaque session donne elle-même la phrase de passe. '
            : ''}
          Ajouter ou remplacer une valeur reste possible. Révéler, exporter et appliquer demandent la phrase de passe,
          pour {Math.round(status.ttl_seconds / 60)} minutes et pour cette session seulement.
        </span>
      </div>
      <form onSubmit={unlock} className="flex flex-col gap-2 sm:flex-row" autoComplete="off">
        <label htmlFor="secrets-unlock-passphrase" className="sr-only">
          Phrase de passe du coffre
        </label>
        <input
          id="secrets-unlock-passphrase"
          type="password"
          autoComplete="new-password"
          placeholder="Phrase de passe du coffre"
          className={`${inputClass} sm:max-w-xs`}
          value={passphrase}
          onChange={event => setPassphrase(event.target.value)}
        />
        <Button type="submit" variant="primary" loading={busy} disabled={!passphrase} leadingIcon={<LockOpen size={14} aria-hidden />}>
          Déverrouiller
        </Button>
      </form>
      {error && <ErrorNote text={error} />}
    </section>
  )
}

interface Confirmation {
  title: string
  body: ReactNode
  action: string
  destructive?: boolean
  run: () => Promise<void>
}

function ConfirmDialog({ request, onClose }: { request: Confirmation | null; onClose: () => void }) {
  const cancelRef = useRef<HTMLButtonElement>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    setBusy(false)
    setError(null)
  }, [request])
  const confirm = async () => {
    if (!request) return
    setBusy(true)
    setError(null)
    try {
      await request.run()
      onClose()
    } catch (failure) {
      setError(message(failure))
      setBusy(false)
    }
  }
  return (
    <Dialog
      open={Boolean(request)}
      onOpenChange={open => {
        if (!open && !busy) onClose()
      }}
      initialFocusRef={cancelRef}
      role="alertdialog"
      title={request?.title ?? ''}
      footer={
        <>
          <Button ref={cancelRef} variant="ghost" onClick={onClose} disabled={busy}>
            Annuler
          </Button>
          <Button variant={request?.destructive ? 'destructive' : 'primary'} onClick={confirm} loading={busy}>
            {request?.action}
          </Button>
        </>
      }
    >
      <div className="space-y-2 text-xs leading-relaxed">{request?.body}</div>
      {error && (
        <div className="mt-3">
          <ErrorNote text={error} />
        </div>
      )}
    </Dialog>
  )
}

interface AppPanelProps {
  app: SecretApp
  unlocked: boolean
  revealed: Record<string, string>
  onReveal: (name: string) => Promise<void>
  onHide: (name: string) => void
  onChanged: () => Promise<void>
  onLocked: () => void
}

function AppPanel({ app, unlocked, revealed, onReveal, onHide, onChanged, onLocked }: AppPanelProps) {
  const [name, setName] = useState('')
  const [value, setValue] = useState('')
  const [editing, setEditing] = useState<string | null>(null)
  const [editValue, setEditValue] = useState('')
  const [dotenv, setDotenv] = useState('')
  const [envFile, setEnvFile] = useState(app.target?.env_file ?? '')
  const [container, setContainer] = useState(app.target?.container ?? '')
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [note, setNote] = useState<string | null>(null)
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null)
  const [exported, setExported] = useState<string | null>(null)
  const [applied, setApplied] = useState<ApplyResult | null>(null)

  useEffect(() => {
    setEnvFile(app.target?.env_file ?? '')
    setContainer(app.target?.container ?? '')
  }, [app.name, app.target?.env_file, app.target?.container])

  // The exported text does not stay on the page, and goes with the lock.
  useEffect(() => {
    if (exported === null) return
    const timer = window.setTimeout(() => setExported(null), EXPORT_MS)
    return () => window.clearTimeout(timer)
  }, [exported])
  useEffect(() => {
    if (!unlocked) setExported(null)
  }, [unlocked])

  /** Runs one action; a vault found locked refreshes the banner. */
  const run = async (key: string, action: () => Promise<void>) => {
    setBusy(key)
    setError(null)
    setNote(null)
    try {
      await action()
    } catch (failure) {
      if (failure instanceof SecretsApiError && failure.code === 'locked') onLocked()
      setError(message(failure))
    } finally {
      setBusy(null)
    }
  }

  const addVariable = (event: FormEvent) => {
    event.preventDefault()
    const trimmed = name.trim()
    if (!trimmed) return
    void run('add', async () => {
      await setVariable(app.name, trimmed, value)
      setName('')
      setValue('')
      onHide(trimmed)
      await onChanged()
    })
  }
  const saveEdit = (event: FormEvent) => {
    event.preventDefault()
    if (!editing) return
    const target = editing
    void run(`edit:${target}`, async () => {
      await setVariable(app.name, target, editValue)
      setEditing(null)
      setEditValue('')
      onHide(target)
      await onChanged()
    })
  }
  const runImport = (event: FormEvent) => {
    event.preventDefault()
    if (!dotenv.trim()) return
    void run('import', async () => {
      const result = await importDotenv(app.name, dotenv)
      setDotenv('')
      setNote(
        result.imported.length > 1
          ? `${result.imported.length} variables importées et chiffrées.`
          : '1 variable importée et chiffrée.',
      )
      await onChanged()
    })
  }
  const saveTarget = (event: FormEvent) => {
    event.preventDefault()
    void run('target', async () => {
      await setTarget(app.name, envFile.trim(), container.trim())
      setNote(envFile.trim() ? 'Cible enregistrée.' : 'Cible retirée.')
      await onChanged()
    })
  }
  const runExport = () =>
    void run('export', async () => {
      setExported(await exportDotenv(app.name))
      await onChanged()
    })

  const count = app.variables.length
  const removeApp = (body: ReactNode) =>
    setConfirmation({
      title: `Supprimer « ${app.name} » ?`,
      action: 'Supprimer définitivement',
      destructive: true,
      body,
      run: async () => {
        await deleteApp(app.name)
        await onChanged()
      },
    })

  if (app.unreadable) {
    return (
      <section aria-label={`Application ${app.name}`} className={`${cardClass} space-y-3`}>
        <h2 className="break-all font-mono text-sm font-medium">{app.name}</h2>
        <ErrorNote text="Le fichier de cette application est illisible ou a été modifié à la main. Ses valeurs ne peuvent plus être lues ni appliquées ; elle peut seulement être supprimée, puis recréée." />
        <Button
          size="sm"
          variant="destructive"
          leadingIcon={<Trash2 size={14} aria-hidden />}
          onClick={() => removeApp(<p>Le fichier illisible de cette application est retiré du coffre.</p>)}
        >
          Supprimer l’application
        </Button>
        <ConfirmDialog request={confirmation} onClose={() => setConfirmation(null)} />
      </section>
    )
  }

  const target = app.target
  const targetDirty = envFile.trim() !== (target?.env_file ?? '') || container.trim() !== (target?.container ?? '')
  const lockedHint = unlocked ? undefined : 'Déverrouille le coffre d’abord'

  const askApply = () => {
    if (!target) return
    setApplied(null)
    setConfirmation({
      title: `Appliquer « ${app.name} » ?`,
      action: target.container ? 'Écrire et redémarrer' : 'Écrire le fichier',
      body: (
        <>
          <p>
            Le fichier <code className="break-all font-mono">{target.env_file}</code> sera <strong>remplacé</strong> par
            les {count} variable{count > 1 ? 's' : ''} de cette application, <strong>en clair</strong>, lisible seulement
            par le compte du hub (0600).
          </p>
          <p>
            S’il existe déjà et qu’il change, son contenu actuel est gardé à côté dans{' '}
            <code className="break-all font-mono">{target.env_file}.bak</code> (la sauvegarde précédente passe en{' '}
            <code className="font-mono">.bak.1</code>).
          </p>
          <p>
            Le fichier écrit est un <strong>nouveau fichier</strong>, lisible par le seul compte du hub : un conteneur
            qui monte ce fichier lui-même (et non son dossier) garde l’ancien jusqu’à sa recréation, et un processus qui
            tourne sous un autre utilisateur ne pourra plus le lire.
          </p>
          {target.container ? (
            <p>
              Ensuite le conteneur <code className="break-all font-mono">{target.container}</code> sera{' '}
              <strong>redémarré</strong> : le service est coupé pendant le redémarrage. Un conteneur créé par Compose
              avec <code className="font-mono">env_file</code> ne relit ce fichier qu’à sa recréation (
              <code className="font-mono">docker compose up -d</code>), pas à un simple redémarrage.
            </p>
          ) : (
            <p>Aucun conteneur ne sera redémarré.</p>
          )}
        </>
      ),
      run: async () => {
        try {
          setApplied(await applyApp(app.name))
        } catch (failure) {
          if (failure instanceof SecretsApiError && failure.code === 'locked') onLocked()
          throw failure
        }
        await onChanged()
      },
    })
  }

  return (
    <section aria-label={`Application ${app.name}`} className={`${cardClass} space-y-4`}>
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="min-w-0 flex-1 break-all font-mono text-sm font-medium">{app.name}</h2>
        <Button
          size="sm"
          variant="ghost"
          leadingIcon={<Trash2 size={14} aria-hidden />}
          onClick={() =>
            removeApp(
              <p>
                L’application et ses {count} valeur{count > 1 ? 's' : ''} chiffrée{count > 1 ? 's' : ''} sont retirées du
                coffre. Les fichiers déjà appliqués ne sont pas touchés.
              </p>,
            )
          }
        >
          Supprimer l’application
        </Button>
      </div>

      {error && <ErrorNote text={error} onDismiss={() => setError(null)} />}
      {note && (
        <p role="status" className="text-xs text-success">
          {note}
        </p>
      )}

      <div>
        <h3 className="mb-2 text-xs font-medium">Variables</h3>
        {count === 0 ? (
          <p className="text-xs text-muted-foreground">Aucune variable. Ajoute-en une ou colle un fichier .env plus bas.</p>
        ) : (
          <ul className="divide-y divide-border rounded-md border border-border">
            {app.variables.map(variable => {
              const shown = revealed[variable.name]
              const isShown = shown !== undefined
              return (
                <li key={variable.name} className="space-y-2 px-2.5 py-2">
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                    <span className="min-w-0 flex-1 basis-40 break-all font-mono text-xs font-medium">{variable.name}</span>
                    <span className="flex shrink-0 items-center gap-1">
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={!isShown && !unlocked}
                        title={isShown ? undefined : lockedHint}
                        loading={busy === `reveal:${variable.name}`}
                        aria-label={`${isShown ? 'Masquer' : 'Révéler'} ${variable.name}`}
                        leadingIcon={isShown ? <EyeOff size={14} aria-hidden /> : <Eye size={14} aria-hidden />}
                        onClick={() =>
                          isShown
                            ? onHide(variable.name)
                            : void run(`reveal:${variable.name}`, () => onReveal(variable.name))
                        }
                      >
                        {isShown ? 'Masquer' : 'Révéler'}
                      </Button>
                      <IconButton
                        size="sm"
                        aria-label={`Modifier ${variable.name}`}
                        onClick={() => {
                          setEditing(editing === variable.name ? null : variable.name)
                          setEditValue('')
                        }}
                      >
                        <Pencil size={14} aria-hidden />
                      </IconButton>
                      <IconButton
                        size="sm"
                        aria-label={`Supprimer ${variable.name}`}
                        onClick={() =>
                          setConfirmation({
                            title: `Supprimer ${variable.name} ?`,
                            action: 'Supprimer',
                            destructive: true,
                            body: <p>La valeur chiffrée est retirée du coffre. Elle ne pourra pas être récupérée.</p>,
                            run: async () => {
                              await deleteVariable(app.name, variable.name)
                              onHide(variable.name)
                              await onChanged()
                            },
                          })
                        }
                      >
                        <Trash2 size={14} aria-hidden />
                      </IconButton>
                    </span>
                  </div>
                  {isShown ? (
                    <pre
                      data-testid={`value-${variable.name}`}
                      className="max-h-40 overflow-auto whitespace-pre-wrap break-all rounded bg-muted px-2 py-1 font-mono text-xs"
                    >
                      {shown === '' ? <span className="text-muted-foreground">(valeur vide)</span> : shown}
                    </pre>
                  ) : (
                    <p className="font-mono text-xs text-muted-foreground" aria-label="Valeur masquée">
                      ••••••••••••
                    </p>
                  )}
                  {editing === variable.name && (
                    <form onSubmit={saveEdit} className="flex flex-col gap-2 sm:flex-row" autoComplete="off">
                      <label htmlFor={`secrets-edit-${variable.name}`} className="sr-only">
                        Nouvelle valeur de {variable.name}
                      </label>
                      <input
                        id={`secrets-edit-${variable.name}`}
                        type="password"
                        {...secretField}
                        placeholder="Nouvelle valeur"
                        className={inputClass}
                        value={editValue}
                        onChange={event => setEditValue(event.target.value)}
                      />
                      <Button type="submit" size="sm" variant="primary" loading={busy === `edit:${variable.name}`}>
                        Remplacer
                      </Button>
                      <Button size="sm" variant="ghost" onClick={() => setEditing(null)}>
                        Annuler
                      </Button>
                    </form>
                  )}
                </li>
              )
            })}
          </ul>
        )}
      </div>

      <form onSubmit={addVariable} className="space-y-2" autoComplete="off">
        <h3 className="text-xs font-medium">Ajouter ou remplacer une variable</h3>
        <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1.5fr)_auto]">
          <div>
            <label htmlFor="secrets-new-name" className={labelClass}>
              Nom
            </label>
            <input
              id="secrets-new-name"
              {...secretField}
              placeholder="API_TOKEN"
              className={`${inputClass} font-mono`}
              value={name}
              onChange={event => setName(event.target.value)}
            />
          </div>
          <div>
            <label htmlFor="secrets-new-value" className={labelClass}>
              Valeur
            </label>
            <input
              id="secrets-new-value"
              type="password"
              {...secretField}
              className={inputClass}
              value={value}
              onChange={event => setValue(event.target.value)}
            />
          </div>
          <Button type="submit" className="self-end" loading={busy === 'add'} disabled={!name.trim()} leadingIcon={<Plus size={14} aria-hidden />}>
            Chiffrer et enregistrer
          </Button>
        </div>
      </form>

      <form onSubmit={runImport} className="space-y-2" autoComplete="off">
        <label htmlFor="secrets-import" className="block text-xs font-medium">
          Importer un fichier .env (coller le texte)
        </label>
        <textarea
          id="secrets-import"
          rows={4}
          {...secretField}
          placeholder={'# NOM=valeur, une variable par ligne\nAPI_TOKEN=…'}
          className={areaClass}
          value={dotenv}
          onChange={event => setDotenv(event.target.value)}
        />
        <p className="text-2xs text-muted-foreground">
          Le texte est analysé et chiffré sur le serveur, puis effacé de cette page. Une variable déjà présente est
          remplacée.
        </p>
        <Button type="submit" loading={busy === 'import'} disabled={!dotenv.trim()}>
          Importer
        </Button>
      </form>

      <form onSubmit={saveTarget} className="space-y-2" autoComplete="off">
        <h3 className="text-xs font-medium">Cible</h3>
        <div className="grid gap-2 sm:grid-cols-2">
          <div>
            <label htmlFor="secrets-target-file" className={labelClass}>
              Fichier .env à écrire (chemin absolu, dans le dossier personnel)
            </label>
            <input
              id="secrets-target-file"
              autoComplete="off"
              spellCheck={false}
              placeholder="/home/…/mon-app/.env"
              className={`${inputClass} font-mono`}
              value={envFile}
              onChange={event => setEnvFile(event.target.value)}
            />
          </div>
          <div>
            <label htmlFor="secrets-target-container" className={labelClass}>
              Conteneur à redémarrer (facultatif)
            </label>
            <input
              id="secrets-target-container"
              autoComplete="off"
              spellCheck={false}
              placeholder="mon-app-web-1"
              className={`${inputClass} font-mono`}
              value={container}
              onChange={event => setContainer(event.target.value)}
            />
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button type="submit" loading={busy === 'target'} disabled={!targetDirty}>
            Enregistrer la cible
          </Button>
          <Button
            variant="primary"
            disabled={!unlocked || !target || targetDirty || count === 0}
            title={
              lockedHint ??
              (!target ? 'Enregistre une cible d’abord' : count === 0 ? 'Aucune variable à appliquer' : undefined)
            }
            onClick={askApply}
          >
            Appliquer…
          </Button>
          <Button disabled={!unlocked || count === 0} title={lockedHint} loading={busy === 'export'} onClick={runExport}>
            Exporter en .env
          </Button>
        </div>
      </form>

      {applied && (
        <div role="status" className="rounded-md border border-border px-2.5 py-2 text-xs leading-relaxed">
          {applied.unchanged ? (
            <p>
              <code className="break-all font-mono">{applied.env_file}</code> contenait déjà exactement ces{' '}
              {applied.variables} variable{applied.variables > 1 ? 's' : ''} : rien n’a changé.
            </p>
          ) : (
            <p>
              {applied.variables} variable{applied.variables > 1 ? 's' : ''} écrite{applied.variables > 1 ? 's' : ''} dans{' '}
              <code className="break-all font-mono">{applied.env_file}</code>
              {applied.backup ? ' (ancien fichier gardé en .bak).' : '.'}
            </p>
          )}
          {applied.container && applied.restarted && <p>Conteneur {applied.container} redémarré.</p>}
          {applied.container && !applied.restarted && (
            <p className="text-destructive">
              Le fichier est écrit, mais le conteneur {applied.container} n’a pas été redémarré : {applied.restart_error}
            </p>
          )}
        </div>
      )}

      {exported !== null && (
        <div className="space-y-1">
          <label htmlFor="secrets-export" className="block text-xs font-medium">
            Export en clair (effacé de la page dans {EXPORT_MS / 1000} s)
          </label>
          <textarea id="secrets-export" readOnly rows={Math.min(10, count + 1)} {...secretField} className={areaClass} value={exported} />
          <Button size="sm" variant="ghost" onClick={() => setExported(null)}>
            Effacer maintenant
          </Button>
        </div>
      )}

      <ConfirmDialog request={confirmation} onClose={() => setConfirmation(null)} />
    </section>
  )
}

function AuditTrail({ entries }: { entries: AuditEntry[] }) {
  return (
    <section aria-label="Journal d’audit" className={cardClass}>
      <h2 className="mb-1 text-sm font-medium">Journal d’audit</h2>
      <p className="mb-2 text-2xs text-muted-foreground">
        Les {entries.length > 1 ? `${entries.length} dernières actions` : 'dernières actions'} sur le coffre. Il ne contient
        jamais de valeur.
      </p>
      {entries.length === 0 ? (
        <p className="text-xs text-muted-foreground">Aucune action enregistrée.</p>
      ) : (
        <ul className="max-h-80 divide-y divide-border overflow-y-auto text-xs">
          {entries.map((entry, index) => (
            <li key={`${entry.time}-${index}`} className="flex flex-wrap gap-x-3 gap-y-0.5 py-1.5">
              <span className="shrink-0 tabular-nums text-muted-foreground">{formatTime(entry.time)}</span>
              <span className="shrink-0 font-medium">{entry.actor}</span>
              <span className={entry.action.endsWith('_failed') ? 'text-destructive' : undefined}>
                {actionLabels[entry.action] ?? entry.action}
              </span>
              {(entry.app || entry.name) && (
                <span className="min-w-0 break-all font-mono">{[entry.app, entry.name].filter(Boolean).join(' / ')}</span>
              )}
              {entry.detail && <span className="min-w-0 break-all text-muted-foreground">{entry.detail}</span>}
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

export function SecretsPage() {
  const [status, setStatus] = useState<VaultStatus | null>(null)
  const [apps, setApps] = useState<SecretApp[]>([])
  const [audit, setAudit] = useState<AuditEntry[]>([])
  const [selected, setSelected] = useState<string | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [forbidden, setForbidden] = useState(false)
  const [remaining, setRemaining] = useState(0)
  const [revealed, setRevealed] = useState<Record<string, string>>({})
  const [newApp, setNewApp] = useState('')
  const [creating, setCreating] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)
  const deadlineRef = useRef(0)
  const timersRef = useRef(new Map<string, number>())

  const hideAll = useCallback(() => {
    for (const timer of timersRef.current.values()) window.clearTimeout(timer)
    timersRef.current.clear()
    setRevealed({})
  }, [])
  const hide = useCallback((key: string) => {
    const timer = timersRef.current.get(key)
    if (timer !== undefined) window.clearTimeout(timer)
    timersRef.current.delete(key)
    setRevealed(current => {
      if (!(key in current)) return current
      const next = { ...current }
      delete next[key]
      return next
    })
  }, [])

  const acceptStatus = useCallback(
    (next: VaultStatus) => {
      setStatus(next)
      if (next.unlocked && next.expires_at) {
        // The countdown follows the server clock, not the browser's.
        const left = Date.parse(next.expires_at) - Date.parse(next.now)
        deadlineRef.current = Date.now() + left
        setRemaining(left / 1000)
      } else {
        deadlineRef.current = 0
        setRemaining(0)
        hideAll()
      }
    },
    [hideAll],
  )

  const loadContent = useCallback(async () => {
    const [nextApps, nextAudit] = await Promise.all([fetchApps(), fetchAudit()])
    setApps(nextApps)
    setAudit(nextAudit)
  }, [])

  const refresh = useCallback(async () => {
    try {
      const next = await fetchStatus()
      acceptStatus(next)
      if (next.initialized && !next.problem) await loadContent()
      setLoadError(null)
    } catch (failure) {
      if (failure instanceof SecretsApiError && failure.status === 403) setForbidden(true)
      else setLoadError(message(failure))
    }
  }, [acceptStatus, loadContent])

  useEffect(() => {
    void refresh()
  }, [refresh])

  // Nothing revealed outlives the page.
  useEffect(() => {
    const timers = timersRef.current
    return () => {
      for (const timer of timers.values()) window.clearTimeout(timer)
      timers.clear()
    }
  }, [])

  const unlocked = Boolean(status?.unlocked)
  const unlockedRef = useRef(false)
  unlockedRef.current = unlocked
  useEffect(() => {
    if (!unlocked) return
    const timer = window.setInterval(() => {
      const left = (deadlineRef.current - Date.now()) / 1000
      setRemaining(left)
      if (left <= 0) {
        window.clearInterval(timer)
        setStatus(current => (current ? { ...current, unlocked: false, expires_at: undefined } : current))
        hideAll()
        void refresh()
      }
    }, 1000)
    return () => window.clearInterval(timer)
  }, [unlocked, hideAll, refresh])

  useEffect(() => {
    if (apps.length === 0) {
      if (selected !== null) setSelected(null)
    } else if (!selected || !apps.some(app => app.name === selected)) {
      setSelected(apps[0].name)
    }
  }, [apps, selected])

  const current = apps.find(app => app.name === selected) ?? null

  const reveal = useCallback(
    async (app: string, name: string) => {
      const value = await revealVariable(app, name)
      // An answer that comes back after the vault was locked is dropped.
      if (!unlockedRef.current) return
      const key = `${app}/${name}`
      const previous = timersRef.current.get(key)
      if (previous !== undefined) window.clearTimeout(previous)
      timersRef.current.set(
        key,
        window.setTimeout(() => hide(key), REVEAL_MS),
      )
      setRevealed(shown => ({ ...shown, [key]: value }))
      void fetchAudit().then(setAudit, () => {})
    },
    [hide],
  )

  const submitNewApp = async (event: FormEvent) => {
    event.preventDefault()
    const name = newApp.trim()
    if (!name) return
    setCreating(true)
    setCreateError(null)
    try {
      await createApp(name)
      setNewApp('')
      await loadContent()
      setSelected(name)
    } catch (failure) {
      setCreateError(message(failure))
    } finally {
      setCreating(false)
    }
  }

  const revealedForCurrent: Record<string, string> = {}
  if (current) {
    const prefix = `${current.name}/`
    for (const [key, value] of Object.entries(revealed)) {
      if (key.startsWith(prefix)) revealedForCurrent[key.slice(prefix.length)] = value
    }
  }

  return (
    <main className="flex-1 overflow-y-auto p-4 text-sm sm:p-6">
      <h1 className="mb-1 text-2xl font-medium">Secrets</h1>
      <p className="mb-4 max-w-3xl text-xs leading-relaxed text-muted-foreground">
        Les variables d’environnement des applications, chiffrées une à une. Le serveur ne peut pas les lire tant que le
        coffre est verrouillé.
      </p>

      {forbidden ? (
        <p className="text-xs text-muted-foreground">Cette page est réservée aux administrateurs.</p>
      ) : !status ? (
        loadError ? (
          <ErrorNote text={`Impossible de lire le coffre : ${loadError}`} />
        ) : (
          <p className="text-xs text-muted-foreground">Chargement…</p>
        )
      ) : status.problem ? (
        <div className="max-w-3xl space-y-2">
          <ErrorNote text={`${status.problem[0].toUpperCase()}${status.problem.slice(1)}.`} />
          <p className="text-xs leading-relaxed text-muted-foreground">
            Rien n’est chiffré ni déchiffré tant que le fichier du coffre n’est pas revenu à l’état connu au démarrage
            du hub. Voir docs/secrets.md.
          </p>
        </div>
      ) : !status.initialized ? (
        <InitForm
          minLength={status.min_passphrase_length}
          onDone={next => {
            acceptStatus(next)
            void refresh()
          }}
        />
      ) : (
        <div className="max-w-4xl space-y-4">
          {loadError && <ErrorNote text={loadError} onDismiss={() => setLoadError(null)} />}
          <LockBanner
            status={status}
            remaining={remaining}
            onStatus={next => {
              acceptStatus(next)
              void loadContent().catch(() => {})
            }}
          />

          <section aria-label="Applications" className={`${cardClass} space-y-3`}>
            <h2 className="text-sm font-medium">Applications</h2>
            {apps.length > 0 && (
              <div className="flex flex-wrap gap-1.5">
                {apps.map(app => (
                  <button
                    key={app.name}
                    type="button"
                    aria-pressed={app.name === selected}
                    onClick={() => setSelected(app.name)}
                    className="ui-focus-ring max-w-full break-all rounded-md border border-border px-2.5 py-1.5 text-left font-mono text-xs aria-pressed:border-primary aria-pressed:bg-secondary aria-pressed:text-secondary-foreground"
                  >
                    {app.name}
                    <span className="ml-1.5 font-sans text-2xs text-muted-foreground">{app.variables.length}</span>
                  </button>
                ))}
              </div>
            )}
            <form onSubmit={submitNewApp} className="flex flex-col gap-2 sm:flex-row" autoComplete="off">
              <label htmlFor="secrets-new-app" className="sr-only">
                Nom de la nouvelle application
              </label>
              <input
                id="secrets-new-app"
                autoComplete="off"
                autoCapitalize="off"
                spellCheck={false}
                placeholder="nouvelle-application"
                className={`${inputClass} font-mono sm:max-w-xs`}
                value={newApp}
                onChange={event => setNewApp(event.target.value)}
              />
              <Button type="submit" loading={creating} disabled={!newApp.trim()} leadingIcon={<Plus size={14} aria-hidden />}>
                Créer l’application
              </Button>
            </form>
            {createError && <ErrorNote text={createError} onDismiss={() => setCreateError(null)} />}
          </section>

          {current ? (
            <AppPanel
              key={current.name}
              app={current}
              unlocked={unlocked}
              revealed={revealedForCurrent}
              onReveal={name => reveal(current.name, name)}
              onHide={name => hide(`${current.name}/${name}`)}
              onChanged={loadContent}
              onLocked={() => void refresh()}
            />
          ) : (
            <EmptyState
              icon={<KeyRound size={20} />}
              title="Aucune application"
              description="Crée une application, puis ajoute ses variables ou colle son fichier .env."
            />
          )}

          <AuditTrail entries={audit} />
        </div>
      )}
    </main>
  )
}
