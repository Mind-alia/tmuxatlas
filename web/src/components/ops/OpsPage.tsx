import { useEffect, useState } from 'react'
import { Alerts } from '../monitoring/Monitoring'
import { toneColor } from '../monitoring/primitives'
import { formatRelative } from '../monitoring/format'
import { BackupsPanel } from './BackupsPanel'
import { DockerPage } from './docker/DockerPage'
import { Tabs } from './docker/parts'
import { NotificationsPanel } from './NotificationsPanel'
import type { OpsContainers, OpsResponse } from './types'
import { useOps } from './useOps'

type OpsTab = 'containers' | 'backups'

/** The collector feeds the phone alerts about containers: say when it stopped. */
function Freshness({ containers }: { containers: OpsContainers }) {
  if (!containers.available) {
    return (
      <p className="mb-3 text-2xs text-muted-foreground">
        Surveillance des conteneurs pour les alertes : aucun relevé ({containers.error ?? 'collecteur absent'}).
      </p>
    )
  }
  return (
    <p className="mb-3 text-2xs text-muted-foreground">
      Surveillance des conteneurs pour les alertes : relevé {formatRelative(containers.collected_at)}
      {containers.stale && (
        <span style={{ color: toneColor.critical }}> — périmé, le collecteur ne répond plus</span>
      )}
    </p>
  )
}

function NotConfigured() {
  return (
    <div className="mb-6 max-w-2xl rounded-lg border border-border bg-card p-4 text-xs leading-relaxed">
      <p className="mb-2 font-medium">Les alertes sur les conteneurs et les sauvegardes ne sont pas configurées.</p>
      <p className="text-muted-foreground">
        Elles lisent un relevé écrit par un collecteur, lancé par le compte qui possède Docker. Installe{' '}
        <code>contrib/ops/docker-snapshot.sh</code> et son timer, puis démarre le hub avec <code>TMUXATLAS_OPS_DIR</code>.
        Voir <code>docs/ops.md</code>. L’onglet Conteneurs, lui, interroge Docker directement.
      </p>
    </div>
  )
}

/** Alerts, backups and notifications, read from the ops directory (unchanged by the live view). */
export function OpsView({ data }: { data: OpsResponse }) {
  if (!data.configured) {
    return (
      <>
        <NotConfigured />
        {data.notifications && <NotificationsPanel notifications={data.notifications} />}
      </>
    )
  }
  return (
    <>
      <Alerts alerts={data.alerts ?? []} okText="Conteneurs et sauvegardes sont en ordre." />
      <Freshness containers={data.containers} />
      {data.backups && <BackupsPanel backups={data.backups} />}
      {data.notifications && <NotificationsPanel notifications={data.notifications} />}
    </>
  )
}

function readTab(): OpsTab {
  return new URLSearchParams(window.location.search).get('vue') === 'sauvegardes' ? 'backups' : 'containers'
}

function BackupsTab({ ops: { data, status, failed } }: { ops: ReturnType<typeof useOps> }) {
  return (
    <>
      {status === 'loading' && <p className="text-xs text-muted-foreground">Chargement…</p>}
      {status === 'forbidden' && (
        <p className="text-xs text-muted-foreground">Cette page est réservée aux administrateurs.</p>
      )}
      {status === 'error' && <p className="text-xs text-destructive">Impossible de joindre le hub.</p>}
      {failed && status === 'ready' && (
        <p className="mb-3 text-2xs text-warning">Dernière actualisation en échec, nouvel essai dans 15 s.</p>
      )}
      {status === 'ready' && data && <OpsView data={data} />}
    </>
  )
}

export function OpsPage({ onOpenSession }: { onOpenSession?: (sessionKey: string) => void }) {
  const [tab, setTab] = useState<OpsTab>(readTab)
  // Polled on both tabs: the backups tab shows it, the containers tab counts its alerts.
  const ops = useOps()
  const alertCount = ops.data?.alerts?.length ?? 0

  useEffect(() => {
    const onPop = () => setTab(readTab())
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])

  const changeTab = (next: OpsTab) => {
    setTab(next)
    const url = next === 'backups' ? `${window.location.pathname}?vue=sauvegardes` : window.location.pathname
    window.history.replaceState(window.history.state, '', url)
  }

  return (
    <main className="flex-1 overflow-y-auto p-4 text-sm sm:p-6">
      <h1 className="mb-3 text-2xl font-medium">Conteneurs</h1>
      <div className="mb-4">
        <Tabs
          label="Sections de la page Conteneurs"
          idPrefix="ops"
          value={tab}
          onChange={changeTab}
          items={[
            { id: 'containers', label: 'Conteneurs' },
            {
              id: 'backups',
              label: (
                <>
                  Sauvegardes et alertes
                  {alertCount > 0 && (
                    <span
                      className="rounded-full px-1.5 text-2xs font-semibold tabular-nums"
                      style={{ color: toneColor.critical, background: `color-mix(in oklch, ${toneColor.critical} 14%, transparent)` }}
                    >
                      <span className="sr-only">, alertes en cours : </span>
                      {alertCount}
                    </span>
                  )}
                </>
              ),
            },
          ]}
        />
      </div>
      <div role="tabpanel" id="ops-panel" aria-labelledby={`ops-tab-${tab}`}>
        {tab === 'containers' ? <DockerPage onOpenSession={onOpenSession} /> : <BackupsTab ops={ops} />}
      </div>
    </main>
  )
}
