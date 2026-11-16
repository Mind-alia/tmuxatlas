import { Alerts, NUM, StatusBadge, Table, toneColor } from '../monitoring/Monitoring'
import { formatDuration, formatRelative } from '../monitoring/format'
import { BackupsPanel } from './BackupsPanel'
import type { OpsContainer, OpsContainers, OpsResponse } from './types'
import { useOps } from './useOps'

const statusLabel: Record<string, string> = {
  healthy: 'en bonne santé',
  running: 'en marche',
  unhealthy: 'en mauvaise santé',
  starting: 'démarrage',
  restarting: 'redémarre en boucle',
  paused: 'en pause',
  exited: 'arrêté',
  dead: 'mort',
  created: 'créé',
  removing: 'suppression',
  unknown: 'inconnu',
}

function since(c: OpsContainer): string {
  if (c.state === 'running' && c.uptime_seconds !== undefined) return formatDuration(c.uptime_seconds)
  if (c.bad_since) return formatRelative(c.bad_since)
  return '—'
}

function ContainersTable({ items }: { items: OpsContainer[] }) {
  return (
    <Table
      label="Conteneurs"
      head={['État', 'Conteneur', 'Image', 'Redémarrages', 'Uptime / arrêt']}
      numeric={[3]}
      empty={items.length === 0}
    >
      {items.map(c => (
        <tr key={c.id}>
          <td>
            <StatusBadge tone={c.tone} label={statusLabel[c.status] ?? c.status} />
          </td>
          <td className="max-w-[260px]">
            <div className="font-medium text-foreground" title={c.short_id}>
              {c.name}
            </div>
            {(c.compose_project || c.exited_desc) && (
              <div
                className="truncate text-2xs"
                style={{ color: c.exited_desc ? toneColor.critical : undefined }}
              >
                {c.exited_desc ||
                  `${c.compose_project}${c.compose_service ? ` / ${c.compose_service}` : ''}`}
              </div>
            )}
          </td>
          <td className="max-w-[240px] truncate text-muted-foreground" title={c.image}>
            {c.image}
          </td>
          <td
            className={NUM}
            style={{ color: c.restarts_10m >= 3 ? toneColor.warning : undefined }}
            title={`${c.restarts_10m} sur 10 min, ${c.restart_count} au total pour Docker`}
          >
            {c.restarts_10m > 0 ? `${c.restarts_10m} / 10 min` : c.restart_count}
          </td>
          <td className="whitespace-nowrap text-muted-foreground">{since(c)}</td>
        </tr>
      ))}
    </Table>
  )
}

function Freshness({ containers }: { containers: OpsContainers }) {
  if (!containers.available) return null
  return (
    <p className="mb-3 text-2xs text-muted-foreground">
      Relevé {formatRelative(containers.collected_at)}
      {containers.stale && (
        <span style={{ color: toneColor.critical }}> — périmé, le collecteur ne répond plus</span>
      )}
    </p>
  )
}

function NotConfigured() {
  return (
    <div className="max-w-2xl rounded-lg border border-border bg-card p-4 text-xs leading-relaxed">
      <p className="mb-2 font-medium">Le suivi des conteneurs n’est pas configuré sur ce hub.</p>
      <p className="text-muted-foreground">
        Atlas ne parle jamais à Docker : un collecteur, lancé par le compte qui possède Docker, écrit l’état
        des conteneurs dans un dossier que le hub lit. Installe <code>contrib/ops/docker-snapshot.sh</code> et
        son timer, puis démarre le hub avec <code>TMUXATLAS_OPS_DIR</code>. Voir <code>docs/ops.md</code>.
      </p>
    </div>
  )
}

export function OpsView({ data }: { data: OpsResponse }) {
  if (!data.configured) return <NotConfigured />
  const items = data.containers.items ?? []
  return (
    <>
      <Alerts alerts={data.alerts ?? []} okText="Conteneurs et sauvegardes sont en ordre." />
      {data.backups && <BackupsPanel backups={data.backups} />}
      <section className="mb-6">
        <h2 className="mb-1 text-sm font-medium">Conteneurs</h2>
        <Freshness containers={data.containers} />
        {data.containers.available ? (
          <ContainersTable items={items} />
        ) : (
          <p className="text-xs text-muted-foreground">Aucune donnée : {data.containers.error}</p>
        )}
      </section>
    </>
  )
}

export function OpsPage() {
  const { data, status, failed } = useOps()
  return (
    <main className="flex-1 overflow-y-auto p-4 text-sm sm:p-6">
      <h1 className="mb-5 text-2xl font-medium">Conteneurs et sauvegardes</h1>
      {status === 'loading' && <p className="text-xs text-muted-foreground">Chargement…</p>}
      {status === 'forbidden' && (
        <p className="text-xs text-muted-foreground">Cette page est réservée aux administrateurs.</p>
      )}
      {status === 'error' && <p className="text-xs text-destructive">Impossible de joindre le hub.</p>}
      {failed && status === 'ready' && (
        <p className="mb-3 text-2xs text-warning">Dernière actualisation en échec, nouvel essai dans 15 s.</p>
      )}
      {status === 'ready' && data && <OpsView data={data} />}
    </main>
  )
}
