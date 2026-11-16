import { useState } from 'react'
import { apiFetch } from '../../lib/api'
import { StatusBadge } from '../monitoring/Monitoring'
import { formatRelative } from '../monitoring/format'
import type { OpsAlertState, OpsNotifications } from './types'

const phaseLabel: Record<OpsAlertState['phase'], string> = {
  pending: 'en observation',
  firing: 'notifiée',
  resolving: 'en rétablissement',
}

const kindLabel: Record<string, string> = {
  firing: 'Panne',
  reminder: 'Rappel',
  resolved: 'Rétabli',
  group: 'Groupe',
  test: 'Test',
}

const channelLabel: Record<string, string> = { webpush: 'Notifications du navigateur (PWA)', ntfy: 'ntfy' }

function unix(iso?: string) {
  const ms = iso ? Date.parse(iso) : NaN
  return Number.isNaN(ms) ? undefined : Math.floor(ms / 1000)
}

export function NotificationsPanel({ notifications }: { notifications: OpsNotifications }) {
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(null)
  const [sending, setSending] = useState(false)
  const channels = notifications.channels ?? []
  const alerts = notifications.alerts ?? []
  const recent = notifications.recent ?? []

  const sendTest = async () => {
    setSending(true)
    setResult(null)
    try {
      const res = await apiFetch('/api/ops/notifications/test', { method: 'POST' })
      if (!res.ok) {
        setResult({ ok: false, text: (await res.text()).trim() || `Erreur ${res.status}` })
        return
      }
      const body = (await res.json()) as { channels: string[] | null; errors: Record<string, string> | null }
      const errors = Object.entries(body.errors ?? {})
      setResult(
        errors.length === 0
          ? { ok: true, text: 'Notification de test envoyée. Vérifie ton téléphone.' }
          : { ok: false, text: errors.map(([channel, error]) => `${channel} : ${error}`).join(' · ') },
      )
    } catch {
      setResult({ ok: false, text: 'Impossible de joindre le hub.' })
    } finally {
      setSending(false)
    }
  }

  return (
    <section className="mb-6" aria-label="Notifications">
      <h2 className="mb-2 text-sm font-medium">Notifications sur le téléphone</h2>
      <div className="rounded-lg border border-border bg-card p-3 text-xs">
        <div className="mb-3 flex flex-wrap items-center gap-2">
          {channels.length === 0 ? (
            <StatusBadge tone="warning" label="aucun canal configuré" />
          ) : (
            channels.map(channel => <StatusBadge key={channel} tone="ok" label={channelLabel[channel] ?? channel} />)
          )}
          <button
            type="button"
            onClick={sendTest}
            disabled={sending || channels.length === 0}
            className="ui-focus-ring ml-auto min-h-11 rounded border border-border px-3 hover:bg-secondary disabled:opacity-50 md:min-h-8"
          >
            {sending ? 'Envoi…' : 'Envoyer une notification de test'}
          </button>
        </div>
        {result && (
          <p role="status" className={`mb-3 ${result.ok ? 'text-muted-foreground' : 'text-destructive'}`}>
            {result.text}
          </p>
        )}
        <p className="mb-3 text-2xs text-muted-foreground">
          Panne d’un conteneur (après 60 s), site muet (3 sondes de suite), sauvegarde en échec, bloquée ou en
          retard. Rappel toutes les 6 h, rétablissement après 2 vérifications, 10 notifications au plus par
          10 min. Les notifications du navigateur s’activent dans Réglages.
        </p>
        {alerts.length > 0 && (
          <ul className="mb-3 flex flex-col gap-1" aria-label="Alertes suivies">
            {alerts.map(alert => (
              <li key={alert.key} className="flex flex-wrap items-center gap-2">
                <StatusBadge
                  tone={alert.phase === 'firing' ? (alert.severity === 'critical' ? 'critical' : 'warning') : 'neutral'}
                  label={phaseLabel[alert.phase] ?? alert.phase}
                />
                <span>{alert.title}</span>
                <span className="text-muted-foreground">{formatRelative(unix(alert.since))}</span>
              </li>
            ))}
          </ul>
        )}
        {recent.length > 0 && (
          <details>
            <summary className="cursor-pointer text-muted-foreground">Dernières notifications ({recent.length})</summary>
            <ul className="mt-2 flex flex-col gap-1">
              {recent.map((n, index) => (
                <li key={`${n.key}-${n.at}-${index}`}>
                  <span className="text-muted-foreground">{formatRelative(unix(n.at))} · </span>
                  <span className="font-medium">{kindLabel[n.kind] ?? n.kind}</span> — {n.title}
                </li>
              ))}
            </ul>
          </details>
        )}
      </div>
    </section>
  )
}
