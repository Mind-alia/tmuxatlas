import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { apiFetch } from '../../lib/api'
import { formatBytes, formatDuration, formatRelative } from '../monitoring/format'
import { StatusBadge, toneColor, type Tone } from '../monitoring/primitives'
import { Button, Dialog } from '../ui'

export type DbBackupOrigin = 'manual' | 'scheduled' | 'pre-restore'

export interface DbBackupRecord {
  id: string
  container: string
  engine: string
  origin: DbBackupOrigin
  state: 'ok' | 'failed' | 'missing'
  started_at: string
  duration_ms: number
  size: number
  sha256?: string
  tool?: string
  error?: string
}

export interface DbBackupSettings {
  mode: 'off' | 'daily' | 'weekly'
  time: string
  weekday: number
  keep: number
}

export interface DbBackupDatabase {
  container: string
  engine?: string
  image?: string
  present: boolean
  settings: DbBackupSettings
  next_run?: number
  running?: { kind: 'backup' | 'restore'; origin?: DbBackupOrigin; started_at: number; bytes: number }
  failure?: { at: string; detail: string }
  backups: DbBackupRecord[]
  more_backups?: number
}

export interface DbRestoreStatus {
  state: 'running' | 'ok' | 'failed'
  container: string
  backup: string
  actor?: string
  step: 'verify' | 'safety-backup' | 'restore' | 'done'
  started_at: number
  finished_at?: number
  safety_backup?: string
  exit_code?: number
  output?: string
  output_truncated?: boolean
  error?: string
}

export interface DbBackupsView {
  available: boolean
  error?: string
  restore_enabled: boolean
  dir: string
  free_bytes?: number
  min_free_bytes: number
  databases: DbBackupDatabase[]
  restore?: DbRestoreStatus
  now: number
}

const engineLabel: Record<string, string> = { postgres: 'PostgreSQL', mysql: 'MySQL', mariadb: 'MariaDB' }
const originLabel: Record<DbBackupOrigin, string> = {
  manual: 'manuelle',
  scheduled: 'planifiée',
  'pre-restore': 'avant restauration',
}
const weekdays = ['dimanche', 'lundi', 'mardi', 'mercredi', 'jeudi', 'vendredi', 'samedi']
const stepLabel: Record<DbRestoreStatus['step'], string> = {
  verify: 'vérification du fichier',
  'safety-backup': 'sauvegarde de sécurité de l’état actuel',
  restore: 'restauration en cours',
  done: 'terminée',
}

const inputClass =
  'ui-focus-ring h-9 w-full min-w-0 rounded-md border border-border bg-input px-2.5 text-xs text-foreground placeholder:text-muted-foreground'

const base = (container: string) => `/api/db-backups/${encodeURIComponent(container)}`
const fileURL = (container: string, id: string) => `${base(container)}/files/${encodeURIComponent(id)}`

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  let response: Response
  try {
    response = await apiFetch(path, { cache: 'no-store', ...init })
  } catch {
    throw new Error('Impossible de joindre le serveur Atlas.')
  }
  const text = await response.text()
  let body: unknown = null
  try {
    body = JSON.parse(text)
  } catch {
    /* not JSON */
  }
  if (!response.ok) {
    const message = (body as { error?: string } | null)?.error || text.trim() || `Erreur ${response.status}`
    throw new Error(message[0].toUpperCase() + message.slice(1))
  }
  return body as T
}

function scheduleText(settings: DbBackupSettings) {
  if (settings.mode === 'daily') return `tous les jours à ${settings.time}`
  if (settings.mode === 'weekly') return `chaque ${weekdays[settings.weekday] ?? '?'} à ${settings.time}`
  return 'désactivée'
}

function when(iso: string) {
  return new Date(iso).toLocaleString('fr-FR', { dateStyle: 'short', timeStyle: 'short' })
}

const seconds = (iso: string) => Math.floor(new Date(iso).getTime() / 1000)

function Item({ label, children, title }: { label: string; children: ReactNode; title?: string }) {
  return (
    <div className="min-w-0">
      <dt className="text-2xs text-muted-foreground">{label}</dt>
      <dd className="break-words text-xs" title={title}>
        {children}
      </dd>
    </div>
  )
}

function SettingsForm({
  database,
  onSaved,
  onClose,
}: {
  database: DbBackupDatabase
  onSaved: () => void
  onClose: () => void
}) {
  const [settings, setSettings] = useState(database.settings)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const save = async () => {
    setSaving(true)
    setError('')
    try {
      await request(`${base(database.container)}/settings`, { method: 'PUT', body: JSON.stringify(settings) })
      onSaved()
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Enregistrement impossible.')
    } finally {
      setSaving(false)
    }
  }
  const keepValid = Number.isInteger(settings.keep) && settings.keep >= 1 && settings.keep <= 365
  return (
    <form
      className="mt-3 grid grid-cols-1 gap-3 rounded-md border border-border p-3 sm:grid-cols-4"
      aria-label={`Planification de ${database.container}`}
      onSubmit={event => {
        event.preventDefault()
        void save()
      }}
    >
      <label className="flex flex-col gap-1 text-xs">
        <span className="font-medium">Planification</span>
        <select
          className={inputClass}
          value={settings.mode}
          onChange={event => setSettings({ ...settings, mode: event.target.value as DbBackupSettings['mode'] })}
        >
          <option value="off">Désactivée</option>
          <option value="daily">Tous les jours</option>
          <option value="weekly">Toutes les semaines</option>
        </select>
      </label>
      {settings.mode === 'weekly' && (
        <label className="flex flex-col gap-1 text-xs">
          <span className="font-medium">Jour</span>
          <select
            className={inputClass}
            value={settings.weekday}
            onChange={event => setSettings({ ...settings, weekday: Number(event.target.value) })}
          >
            {[1, 2, 3, 4, 5, 6, 0].map(day => (
              <option key={day} value={day}>
                {weekdays[day]}
              </option>
            ))}
          </select>
        </label>
      )}
      {settings.mode !== 'off' && (
        <label className="flex flex-col gap-1 text-xs">
          <span className="font-medium">Heure du serveur</span>
          <input
            type="time"
            required
            className={inputClass}
            value={settings.time}
            onChange={event => setSettings({ ...settings, time: event.target.value })}
          />
        </label>
      )}
      <label className="flex flex-col gap-1 text-xs">
        <span className="font-medium">Sauvegardes gardées</span>
        <input
          type="number"
          min={1}
          max={365}
          required
          className={inputClass}
          value={Number.isNaN(settings.keep) ? '' : settings.keep}
          onChange={event => setSettings({ ...settings, keep: event.target.valueAsNumber })}
        />
      </label>
      <p className="text-2xs text-muted-foreground sm:col-span-4">
        Les plus anciennes ne sont supprimées qu’après une sauvegarde réussie.
      </p>
      {error && (
        <p role="alert" className="text-xs text-destructive sm:col-span-4">
          {error}
        </p>
      )}
      <div className="flex flex-wrap gap-2 sm:col-span-4">
        <Button type="submit" variant="primary" size="sm" loading={saving} disabled={!keepValid || !/^\d\d:\d\d$/.test(settings.time)}>
          Enregistrer
        </Button>
        <Button variant="ghost" size="sm" onClick={onClose}>
          Annuler
        </Button>
      </div>
    </form>
  )
}

function BackupRow({
  record,
  database,
  restoreEnabled,
  busy,
  onDelete,
  onRestore,
}: {
  record: DbBackupRecord
  database: DbBackupDatabase
  restoreEnabled: boolean
  busy: boolean
  onDelete: () => void
  onRestore: () => void
}) {
  const ok = record.state === 'ok'
  const tone: Tone = ok ? 'ok' : 'critical'
  const date = when(record.started_at)
  return (
    <li className="flex flex-col gap-2 border-t border-border py-2 first:border-t-0 sm:flex-row sm:items-center">
      <div className="min-w-0 flex-1">
        <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
          <span className="font-medium tabular-nums">{date}</span>
          <StatusBadge tone={tone} label={ok ? 'complète' : record.state === 'missing' ? 'fichier absent' : 'ratée'} />
          <span className="text-muted-foreground">{originLabel[record.origin] ?? record.origin}</span>
        </p>
        {ok ? (
          <p className="text-2xs text-muted-foreground" title={record.sha256 ? `sha256 ${record.sha256}` : undefined}>
            {formatBytes(record.size)} · {formatDuration(record.duration_ms / 1000)}
            {record.tool ? ` · ${record.tool}` : ''}
          </p>
        ) : (
          record.error && (
            <p className="break-words text-2xs" style={{ color: toneColor.critical }}>
              {record.error}
            </p>
          )
        )}
      </div>
      <div className="flex flex-wrap gap-1.5">
        {ok && (
          <a
            className="ui-button ui-focus-ring"
            data-size="sm"
            data-variant="secondary"
            href={`${fileURL(record.container, record.id)}/download`}
            download
            aria-label={`Télécharger la sauvegarde du ${date}`}
          >
            Télécharger
          </a>
        )}
        {ok && (
          <Button
            size="sm"
            disabled={!restoreEnabled || !database.present || busy}
            title={
              !restoreEnabled
                ? 'Restauration désactivée sur ce serveur (TMUXATLAS_DB_RESTORE=on)'
                : !database.present
                  ? 'Le conteneur n’est pas en cours d’exécution'
                  : undefined
            }
            aria-label={`Restaurer la sauvegarde du ${date}`}
            onClick={onRestore}
          >
            Restaurer
          </Button>
        )}
        <Button size="sm" variant="ghost" disabled={busy} aria-label={`Supprimer la sauvegarde du ${date}`} onClick={onDelete}>
          Supprimer
        </Button>
      </div>
    </li>
  )
}

function DatabaseCard({
  database,
  view,
  onChanged,
  onDelete,
  onRestore,
}: {
  database: DbBackupDatabase
  view: DbBackupsView
  onChanged: () => void
  onDelete: (record: DbBackupRecord) => void
  onRestore: (record: DbBackupRecord) => void
}) {
  const [editing, setEditing] = useState(false)
  const [starting, setStarting] = useState(false)
  const [error, setError] = useState('')
  const last = database.backups[0]
  const lastOK = database.backups.find(record => record.state === 'ok')
  const running = database.running
  const badge: { tone: Tone; label: string } = running
    ? { tone: 'neutral', label: running.kind === 'restore' ? 'restauration en cours' : 'sauvegarde en cours' }
    : database.failure
      ? { tone: 'critical', label: 'sauvegarde planifiée en échec' }
      : !database.present
        ? { tone: 'neutral', label: 'conteneur absent' }
        : !last
          ? { tone: 'warning', label: 'jamais sauvegardée' }
          : last.state === 'ok'
            ? { tone: 'ok', label: 'sauvegardée' }
            : { tone: 'critical', label: 'dernière sauvegarde ratée' }

  const backupNow = async () => {
    setStarting(true)
    setError('')
    try {
      await request(`${base(database.container)}/backup`, { method: 'POST' })
      onChanged()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Sauvegarde impossible.')
    } finally {
      setStarting(false)
    }
  }

  return (
    <li className="rounded-lg border border-border bg-card p-3" aria-label={`Base ${database.container}`}>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <h3 className="min-w-0 break-all font-mono text-sm font-medium">{database.container}</h3>
        {database.engine && (
          <span className="rounded border border-border px-1.5 text-2xs text-muted-foreground">
            {engineLabel[database.engine] ?? database.engine}
          </span>
        )}
        <StatusBadge tone={badge.tone} label={badge.label} />
      </div>
      {database.image && <p className="mt-0.5 break-all text-2xs text-muted-foreground">{database.image}</p>}
      {running && (
        <p role="status" className="mt-2 text-xs">
          {running.kind === 'restore' ? 'Restauration' : `Sauvegarde ${originLabel[running.origin ?? 'manual']}`} en cours depuis{' '}
          {formatDuration(Math.max(0, view.now - running.started_at))}
          {running.kind === 'backup' && running.bytes > 0 ? ` · ${formatBytes(running.bytes)} lus` : ''}
        </p>
      )}
      {database.failure && (
        <p className="mt-2 break-words text-xs" style={{ color: toneColor.critical }}>
          Échec du {when(database.failure.at)} : {database.failure.detail}
        </p>
      )}
      <dl className="mt-3 grid grid-cols-2 gap-3 sm:grid-cols-4">
        <Item label="Dernière sauvegarde réussie" title={lastOK ? when(lastOK.started_at) : undefined}>
          {lastOK ? `${formatRelative(seconds(lastOK.started_at))} · ${formatBytes(lastOK.size)}` : 'jamais'}
        </Item>
        <Item label="Planification">{scheduleText(database.settings)}</Item>
        <Item label="Prochaine exécution" title={database.next_run ? new Date(database.next_run * 1000).toLocaleString('fr-FR') : undefined}>
          {database.next_run ? formatRelative(database.next_run) : '—'}
        </Item>
        <Item label="Rétention">{database.settings.keep} dernières</Item>
      </dl>
      <div className="mt-3 flex flex-wrap gap-2">
        <Button
          variant="primary"
          size="sm"
          loading={starting}
          disabled={!database.present || Boolean(running)}
          title={database.present ? undefined : 'Le conteneur n’est pas en cours d’exécution'}
          onClick={() => void backupNow()}
        >
          Sauvegarder maintenant
        </Button>
        <Button size="sm" aria-expanded={editing} onClick={() => setEditing(open => !open)}>
          Planification et rétention
        </Button>
      </div>
      {error && (
        <p role="alert" className="mt-2 text-xs text-destructive">
          {error}
        </p>
      )}
      {editing && <SettingsForm database={database} onSaved={onChanged} onClose={() => setEditing(false)} />}
      <details className="mt-3" open={database.backups.length > 0 && database.backups.length <= 3}>
        <summary className="cursor-pointer text-xs font-medium">Sauvegardes ({database.backups.length + (database.more_backups ?? 0)})</summary>
        {database.backups.length === 0 ? (
          <p className="mt-2 text-xs text-muted-foreground">Aucune sauvegarde pour l’instant.</p>
        ) : (
          <ul className="mt-1" aria-label={`Sauvegardes de ${database.container}`}>
            {database.backups.map(record => (
              <BackupRow
                key={record.id}
                record={record}
                database={database}
                restoreEnabled={view.restore_enabled}
                busy={Boolean(running) || view.restore?.state === 'running'}
                onDelete={() => onDelete(record)}
                onRestore={() => onRestore(record)}
              />
            ))}
          </ul>
        )}
        {database.more_backups ? (
          <p className="mt-1 text-2xs text-muted-foreground">{database.more_backups} plus anciennes ne sont pas affichées.</p>
        ) : null}
      </details>
    </li>
  )
}

function RestoreBanner({ restore }: { restore: DbRestoreStatus }) {
  const tone: Tone = restore.state === 'failed' ? 'critical' : restore.state === 'ok' ? (restore.output ? 'warning' : 'ok') : 'neutral'
  const title =
    restore.state === 'running'
      ? `Restauration de « ${restore.container} » : ${stepLabel[restore.step]}…`
      : restore.state === 'ok'
        ? `Restauration de « ${restore.container} » terminée${restore.output ? ', avec des messages du client' : ''}`
        : `Restauration de « ${restore.container} » en échec`
  return (
    <div
      role="status"
      aria-label="État de la restauration"
      className="mb-3 rounded-lg border p-3 text-xs"
      style={{ borderColor: tone === 'neutral' ? 'var(--border)' : toneColor[tone] }}
    >
      <p className="font-medium">{title}</p>
      <p className="mt-1 text-muted-foreground">
        Sauvegarde du {restore.backup}
        {restore.actor ? `, demandée par ${restore.actor}` : ''}
        {restore.finished_at ? `, ${formatRelative(restore.finished_at)}` : ''}
        {restore.exit_code !== undefined ? ` · code de sortie ${restore.exit_code}` : ''}
      </p>
      {restore.error && (
        <p className="mt-1 break-words" style={{ color: toneColor.critical }}>
          {restore.error}
        </p>
      )}
      {restore.safety_backup && (
        <p className="mt-1 text-muted-foreground">
          État d’avant la restauration gardé dans la sauvegarde {restore.safety_backup} (« avant restauration »).
        </p>
      )}
      {restore.output && (
        <pre className="mt-2 max-h-48 overflow-auto whitespace-pre-wrap break-words rounded bg-muted p-2 font-mono text-2xs">
          {restore.output}
          {restore.output_truncated ? '\n[sortie tronquée]' : ''}
        </pre>
      )}
    </div>
  )
}

type Pending = { kind: 'delete' | 'restore'; record: DbBackupRecord } | null

/** Database backups of the containers: GET /api/db-backups, admin only. */
export default function DbBackupsPanel() {
  const [view, setView] = useState<DbBackupsView | null>(null)
  const [loadError, setLoadError] = useState('')
  const [pending, setPending] = useState<Pending>(null)
  const [typed, setTyped] = useState('')
  const [acting, setActing] = useState(false)
  const [actionError, setActionError] = useState('')
  const cancelRef = useRef<HTMLButtonElement>(null)

  const load = useCallback(async () => {
    try {
      setView(await request<DbBackupsView>('/api/db-backups'))
      setLoadError('')
    } catch (err) {
      setLoadError(err instanceof Error ? err.message : 'Chargement impossible.')
    }
  }, [])

  // Every 3 s while something runs, every 15 s otherwise; not while hidden.
  const active = Boolean(view && (view.restore?.state === 'running' || view.databases.some(database => database.running)))
  useEffect(() => {
    void load()
    const timer = setInterval(() => {
      if (!document.hidden) void load()
    }, active ? 3000 : 15000)
    return () => clearInterval(timer)
  }, [load, active])

  const close = () => {
    setPending(null)
    setTyped('')
    setActionError('')
  }
  const confirm = async () => {
    if (!pending) return
    const { kind, record } = pending
    setActing(true)
    setActionError('')
    try {
      if (kind === 'delete') await request(fileURL(record.container, record.id), { method: 'DELETE' })
      else await request(`${fileURL(record.container, record.id)}/restore`, { method: 'POST', body: JSON.stringify({ confirm: typed }) })
      close()
      await load()
    } catch (err) {
      setActionError(err instanceof Error ? err.message : 'Action impossible.')
    } finally {
      setActing(false)
    }
  }

  const record = pending?.record
  const restoring = pending?.kind === 'restore'
  return (
    <section className="mb-6" aria-label="Sauvegardes des bases de données">
      <h2 className="mb-2 text-sm font-medium">Bases de données des conteneurs</h2>
      {!view && !loadError && <p className="text-xs text-muted-foreground">Chargement…</p>}
      {loadError && (
        <p role="alert" className="mb-2 text-xs text-destructive">
          {loadError}
        </p>
      )}
      {view && (
        <>
          <p className="mb-3 text-2xs text-muted-foreground">
            Fichiers dans <code className="break-all">{view.dir}</code>
            {view.free_bytes !== undefined ? ` · ${formatBytes(view.free_bytes)} libres` : ''} (minimum{' '}
            {formatBytes(view.min_free_bytes)}). Ces copies sont sur le même disque que les bases : elles ne protègent pas d’une panne
            de ce disque.
          </p>
          {!view.available && (
            <p className="mb-3 text-xs" style={{ color: toneColor.warning }}>
              Docker n’est pas interrogeable : {view.error ?? 'raison inconnue'}. Les sauvegardes déjà faites restent listées.
            </p>
          )}
          {!view.restore_enabled && (
            <p className="mb-3 text-2xs text-muted-foreground">
              Restauration désactivée sur ce serveur. Pour l’autoriser, démarre le hub avec <code>TMUXATLAS_DB_RESTORE=on</code>.
            </p>
          )}
          {view.restore && <RestoreBanner restore={view.restore} />}
          {view.databases.length === 0 ? (
            <p className="rounded-lg border border-border bg-card p-3 text-xs text-muted-foreground">
              Aucune base PostgreSQL, MySQL ou MariaDB détectée parmi les conteneurs en cours.
            </p>
          ) : (
            <ul className="flex flex-col gap-3">
              {view.databases.map(database => (
                <DatabaseCard
                  key={database.container}
                  database={database}
                  view={view}
                  onChanged={() => void load()}
                  onDelete={target => setPending({ kind: 'delete', record: target })}
                  onRestore={target => setPending({ kind: 'restore', record: target })}
                />
              ))}
            </ul>
          )}
        </>
      )}
      <Dialog
        open={Boolean(pending)}
        onOpenChange={open => {
          if (!open && !acting) close()
        }}
        initialFocusRef={cancelRef}
        role="alertdialog"
        title={
          record ? (restoring ? `Restaurer « ${record.container} » ?` : `Supprimer la sauvegarde du ${when(record.started_at)} ?`) : ''
        }
        description={
          restoring
            ? 'La base du conteneur sera remplacée par le contenu de cette sauvegarde. Tout ce qui a été écrit depuis sera perdu.'
            : 'Le fichier est supprimé définitivement du serveur.'
        }
        footer={
          <>
            <Button ref={cancelRef} variant="ghost" onClick={close} disabled={acting}>
              Annuler
            </Button>
            <Button
              variant="destructive"
              loading={acting}
              disabled={restoring && typed !== record?.container}
              onClick={() => void confirm()}
            >
              {restoring ? 'Restaurer cette sauvegarde' : 'Supprimer définitivement'}
            </Button>
          </>
        }
      >
        {record && restoring && (
          <div className="flex flex-col gap-2 text-xs">
            <p>
              Sauvegarde du {when(record.started_at)} ({formatBytes(record.size)}). Une sauvegarde de sécurité de l’état actuel est faite
              juste avant ; si elle échoue, rien n’est restauré.
            </p>
            <p className="text-muted-foreground">
              Arrête d’abord les applications qui utilisent cette base : des connexions ouvertes font échouer une partie de la
              restauration.
            </p>
            <label className="flex flex-col gap-1">
              <span className="font-medium">
                Pour confirmer, tape le nom du conteneur : <code>{record.container}</code>
              </span>
              <input
                className={inputClass}
                value={typed}
                onChange={event => setTyped(event.target.value)}
                autoComplete="off"
                autoCapitalize="off"
                spellCheck={false}
              />
            </label>
          </div>
        )}
        {actionError && (
          <p role="alert" className="mt-2 text-xs text-destructive">
            {actionError}
          </p>
        )}
      </Dialog>
    </section>
  )
}
