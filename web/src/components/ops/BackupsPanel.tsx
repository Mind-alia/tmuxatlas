import type { ReactNode } from 'react'
import { StatusBadge, toneColor } from '../monitoring/primitives'
import { formatBytes, formatRelative } from '../monitoring/format'
import type { OpsBackups, OpsBackupState } from './types'

const stateLabel: Record<OpsBackupState, string> = {
  ok: 'à jour',
  running: 'en cours',
  failed: 'en échec',
  'r2-failed': 'copie R2 en échec',
  late: 'en retard',
  stuck: 'bloquée',
  unknown: 'inconnue',
}

function absolute(unixSeconds?: number) {
  return unixSeconds ? new Date(unixSeconds * 1000).toLocaleString('fr-FR') : undefined
}

function Item({ label, children, title }: { label: string; children: ReactNode; title?: string }) {
  return (
    <div className="min-w-0">
      <dt className="text-2xs text-muted-foreground">{label}</dt>
      <dd className="truncate text-xs" title={title}>
        {children}
      </dd>
    </div>
  )
}

export function BackupsPanel({ backups }: { backups: OpsBackups }) {
  const run = backups.current?.result === 'running' ? backups.last_success : (backups.current ?? backups.last_success)
  const r2 =
    backups.state === 'r2-failed'
      ? { tone: 'critical' as const, label: 'échec' }
      : !run
        ? { tone: 'neutral' as const, label: '—' }
        : backups.r2_ok && backups.r2_verified
          ? { tone: 'ok' as const, label: 'copiée et vérifiée' }
          : backups.r2_ok
            ? { tone: 'warning' as const, label: 'copiée, non vérifiée' }
            : { tone: 'critical' as const, label: 'absente' }
  return (
    <section className="mb-6" aria-label="Sauvegardes">
      <h2 className="mb-2 flex items-center gap-2 text-sm font-medium">
        Sauvegardes
        <StatusBadge tone={backups.tone} label={stateLabel[backups.state] ?? backups.state} />
      </h2>
      <div className="rounded-lg border border-border bg-card p-3">
        {backups.reason && (
          <p
            className="mb-3 text-xs"
            style={{ color: backups.tone === 'ok' || backups.tone === 'neutral' ? undefined : toneColor[backups.tone] }}
          >
            {backups.reason}
          </p>
        )}
        <dl className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <Item label="Dernière sauvegarde réussie" title={absolute(backups.last_success_at)}>
            {backups.last_success_at ? formatRelative(backups.last_success_at) : 'jamais'}
          </Item>
          <Item label="Copie sur Cloudflare R2">
            <StatusBadge tone={r2.tone} label={r2.label} />
          </Item>
          <Item label="Prochaine attendue avant" title={absolute(backups.next_expected_by)}>
            {backups.next_expected_by ? formatRelative(backups.next_expected_by) : '—'}
          </Item>
          <Item label="Fichier" title={run?.local.sha256 ? `sha256 ${run.local.sha256}` : undefined}>
            {run?.local.file ? `${run.local.file} · ${formatBytes(run.local.size)}` : '—'}
          </Item>
          <Item label="Rétention locale">
            {backups.local_retention_days} jours{run?.local.count ? ` · ${run.local.count} copies` : ''}
          </Item>
          <Item label="Rétention R2">
            {backups.r2_retention_days} jours{run?.remote.count ? ` · ${run.remote.count} copies` : ''}
          </Item>
          {backups.current?.job && <Item label="Tâche">{backups.current.job}</Item>}
          {backups.error && (
            <Item label="Lecture du statut">
              <span style={{ color: toneColor.critical }}>{backups.error}</span>
            </Item>
          )}
        </dl>
      </div>
    </section>
  )
}
