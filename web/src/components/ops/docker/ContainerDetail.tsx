import { ArrowLeft, Copy, Play, RefreshCw, RotateCw, Square, SquareTerminal, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { TimeSeriesChart, type ChartSeries } from '../../monitoring/TimeSeriesChart'
import { formatBytes, formatNumber, formatPercent, formatRate, formatRelative } from '../../monitoring/format'
import { Button } from '../../ui'
import { fetchInspect } from './api'
import { InlineError } from './ContainersList'
import { LogsPanel } from './LogsPanel'
import {
  canDelete,
  canStop,
  cpuText,
  dotTone,
  exposedPorts,
  imageLabel,
  publishedPorts,
  startedAt,
  stateLabel,
} from './model'
import { HealthBadge, pendingLabel, StatusDot, Tabs, type DetailTab, type PendingKind } from './parts'
import type { DockerAction, DockerContainer, DockerStats } from './types'

const tabs: { id: DetailTab; label: string }[] = [
  { id: 'logs', label: 'Journaux' },
  { id: 'inspect', label: 'Inspecter' },
  { id: 'stats', label: 'Statistiques' },
  { id: 'terminal', label: 'Terminal' },
]

export function ContainerDetail({
  container: c,
  tab,
  onTab,
  history,
  pending,
  error,
  terminal,
  onBack,
  onAction,
  onTerminal,
  onDismissError,
}: {
  container: DockerContainer
  tab: DetailTab
  onTab: (tab: DetailTab) => void
  history: readonly DockerStats[]
  pending?: PendingKind
  error?: string
  terminal: boolean
  onBack: () => void
  onAction: (c: DockerContainer, action: DockerAction) => void
  onTerminal: (c: DockerContainer) => void
  onDismissError: () => void
}) {
  const busy = pending !== undefined
  const running = canStop(c)
  const ports = publishedPorts(c.ports)
  const started = startedAt(c)
  return (
    <div className="flex min-w-0 flex-col gap-4">
      <div>
        <Button size="sm" variant="ghost" leadingIcon={<ArrowLeft size={14} aria-hidden />} onClick={onBack} className="-ml-2">
          Conteneurs
        </Button>
      </div>
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="flex min-w-0 items-center gap-2 text-lg font-semibold">
            <StatusDot tone={dotTone(c)} label={stateLabel(c)} pulse={c.state === 'restarting'} />
            <span className="truncate">{c.name}</span>
          </h2>
          <p className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
            <span className="truncate" title={c.image}>
              {imageLabel(c.image)}
            </span>
            <span className="font-mono text-2xs">{c.short_id}</span>
            <span>
              {stateLabel(c)}
              {c.state === 'running' && started ? ` depuis ${formatRelative(started).replace(/^il y a /, '')}` : ''}
            </span>
            <HealthBadge health={c.health} />
            {c.restart_count > 0 && (
              <span title="Redémarrages faits par la politique de redémarrage de Docker">
                relancé {c.restart_count} fois par Docker
              </span>
            )}
            {c.compose_project && (
              <span>
                Compose : {c.compose_project}
                {c.compose_service ? ` / ${c.compose_service}` : ''}
              </span>
            )}
          </p>
          {(ports.length > 0 || exposedPorts(c.ports).length > 0) && (
            <p className="mt-1 font-mono text-2xs text-muted-foreground">
              {ports.length > 0 && <>Ports publiés : {ports.join(', ')}</>}
              {ports.length > 0 && exposedPorts(c.ports).length > 0 && ' · '}
              {exposedPorts(c.ports).length > 0 && <>exposés : {exposedPorts(c.ports).join(', ')}</>}
            </p>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {pending !== undefined && (
            <span role="status" className="text-2xs text-muted-foreground">
              {pendingLabel[pending]}
            </span>
          )}
          {running ? (
            <Button size="sm" leadingIcon={<Square size={14} aria-hidden />} disabled={busy} onClick={() => onAction(c, 'stop')}>
              Arrêter
            </Button>
          ) : (
            <Button size="sm" variant="primary" leadingIcon={<Play size={14} aria-hidden />} disabled={busy} onClick={() => onAction(c, 'start')}>
              Démarrer
            </Button>
          )}
          <Button size="sm" leadingIcon={<RotateCw size={14} aria-hidden />} disabled={busy} onClick={() => onAction(c, 'restart')}>
            Redémarrer
          </Button>
          <Button
            size="sm"
            variant="ghost"
            leadingIcon={<Trash2 size={14} aria-hidden />}
            disabled={busy || !canDelete(c)}
            title={canDelete(c) ? undefined : 'Arrête le conteneur avant de le supprimer'}
            onClick={() => onAction(c, 'delete')}
          >
            Supprimer
          </Button>
        </div>
      </header>
      {error && <InlineError message={error} onDismiss={onDismissError} />}
      <Tabs items={tabs} value={tab} onChange={onTab} label="Vue du conteneur" idPrefix="docker-detail" />
      <div role="tabpanel" id="docker-detail-panel" aria-labelledby={`docker-detail-tab-${tab}`} className="min-w-0">
        {tab === 'logs' && <LogsPanel containerId={c.id} />}
        {tab === 'inspect' && <InspectPanel containerId={c.id} />}
        {tab === 'stats' && <StatsPanel container={c} history={history} />}
        {tab === 'terminal' && <TerminalPanel container={c} terminal={terminal} pending={pending} onTerminal={onTerminal} />}
      </div>
    </div>
  )
}

function InspectPanel({ containerId }: { containerId: string }) {
  const [doc, setDoc] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)
  const [version, setVersion] = useState(0)
  useEffect(() => {
    const controller = new AbortController()
    setError(null)
    fetchInspect(containerId, controller.signal)
      .then(value => setDoc(JSON.stringify(value, null, 2)))
      .catch(err => {
        if (!controller.signal.aborted) setError(err instanceof Error ? err.message : 'Inspection impossible')
      })
    return () => controller.abort()
  }, [containerId, version])
  const copy = useCallback(async () => {
    if (!doc) return
    try {
      await navigator.clipboard.writeText(doc)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      setError('Copie impossible : le navigateur refuse l’accès au presse-papiers.')
    }
  }, [doc])
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <p className="min-w-0 flex-1 text-2xs text-muted-foreground">
          Sortie de <code>docker inspect</code>. Les valeurs des variables d’environnement et les secrets reconnus
          sont masqués (••••).
        </p>
        <Button size="sm" variant="ghost" leadingIcon={<RefreshCw size={14} aria-hidden />} onClick={() => setVersion(v => v + 1)}>
          Actualiser
        </Button>
        <Button size="sm" variant="ghost" leadingIcon={<Copy size={14} aria-hidden />} disabled={!doc} onClick={() => void copy()}>
          {copied ? 'Copié' : 'Copier'}
        </Button>
      </div>
      {error && <InlineError message={error} />}
      <pre
        tabIndex={0}
        aria-label="Inspection du conteneur"
        className="ui-focus-ring h-[min(60vh,36rem)] overflow-auto rounded-lg border border-border bg-muted p-3 font-mono text-[11px] leading-[1.45] text-foreground"
      >
        {doc ?? (error ? '' : 'Chargement…')}
      </pre>
    </div>
  )
}

/** Bytes per second between consecutive samples of a cumulative counter. */
export function rates(history: readonly DockerStats[], pick: (s: DockerStats) => number): number[] {
  const out: number[] = []
  for (let i = 1; i < history.length; i++) {
    const seconds = (history[i].at - history[i - 1].at) / 1000
    const delta = pick(history[i]) - pick(history[i - 1])
    out.push(seconds > 0 && delta >= 0 ? delta / seconds : 0)
  }
  return out
}

function Kpi({ label, value, detail }: { label: string; value: string; detail?: string }) {
  return (
    <div className="atlas-surface flex min-w-0 flex-col gap-1 rounded-lg p-3">
      <span className="truncate text-2xs text-muted-foreground">{label}</span>
      <span className="truncate text-lg font-semibold tabular-nums leading-tight">{value}</span>
      {detail && <span className="truncate text-2xs text-muted-foreground">{detail}</span>}
    </div>
  )
}

function StatsPanel({ container: c, history }: { container: DockerContainer; history: readonly DockerStats[] }) {
  const latest = c.stats
  const charts = useMemo(() => {
    const times = history.map(s => s.at)
    const rateTimes = times.slice(1)
    const cpu: ChartSeries[] = [{ key: 'cpu', label: 'CPU', color: 'var(--mon-a)', values: history.map(s => s.cpu_percent ?? 0) }]
    const memory: ChartSeries[] = [{ key: 'mem', label: 'Utilisée', color: 'var(--mon-a)', values: history.map(s => s.memory_usage) }]
    const network: ChartSeries[] = [
      { key: 'rx', label: 'Reçu', color: 'var(--mon-a)', values: rates(history, s => s.net_rx) },
      { key: 'tx', label: 'Envoyé', color: 'var(--mon-b)', values: rates(history, s => s.net_tx) },
    ]
    const disk: ChartSeries[] = [
      { key: 'read', label: 'Lecture', color: 'var(--mon-a)', values: rates(history, s => s.block_read) },
      { key: 'write', label: 'Écriture', color: 'var(--mon-b)', values: rates(history, s => s.block_write) },
    ]
    return { times, rateTimes, cpu, memory, network, disk }
  }, [history])

  if (c.state !== 'running') {
    return <p className="text-xs text-muted-foreground">Le conteneur n’est pas en marche : il n’a pas de statistiques.</p>
  }
  const limit = latest?.memory_limit ?? 0
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-5">
        <Kpi label="CPU" value={cpuText(latest)} detail="100 % = un processeur" />
        <Kpi
          label="Mémoire"
          value={latest ? formatBytes(latest.memory_usage) : '—'}
          detail={limit ? `sur ${formatBytes(limit)} (${formatPercent(((latest?.memory_usage ?? 0) / limit) * 100)})` : undefined}
        />
        <Kpi label="Réseau" value={latest ? `↓ ${formatBytes(latest.net_rx)}` : '—'} detail={latest ? `↑ ${formatBytes(latest.net_tx)} depuis le démarrage` : undefined} />
        <Kpi label="Disque" value={latest ? `${formatBytes(latest.block_read)} lus` : '—'} detail={latest ? `${formatBytes(latest.block_write)} écrits` : undefined} />
        <Kpi label="Processus" value={latest ? formatNumber(latest.pids) : '—'} />
      </div>
      <p className="text-2xs text-muted-foreground">
        Échantillon toutes les 3 s tant que la page est ouverte ; les courbes couvrent au plus les 5 dernières minutes.
      </p>
      <div className="grid gap-3 lg:grid-cols-2">
        <TimeSeriesChart title="CPU" times={charts.times} series={charts.cpu} format={formatPercent} minMax={5} interval={3} />
        <TimeSeriesChart
          title="Mémoire"
          times={charts.times}
          series={charts.memory}
          format={formatBytes}
          scale="bytes"
          minMax={16 << 20}
          reference={limit ? { value: limit, label: `limite ${formatBytes(limit)}` } : undefined}
          interval={3}
        />
        <TimeSeriesChart title="Réseau" times={charts.rateTimes} series={charts.network} format={formatRate} scale="bytes" minMax={1024} interval={3} />
        <TimeSeriesChart title="Disque" times={charts.rateTimes} series={charts.disk} format={formatRate} scale="bytes" minMax={1024} interval={3} />
      </div>
    </div>
  )
}

function TerminalPanel({
  container: c,
  terminal,
  pending,
  onTerminal,
}: {
  container: DockerContainer
  terminal: boolean
  pending?: PendingKind
  onTerminal: (c: DockerContainer) => void
}) {
  const reason = !terminal
    ? 'Le terminal n’est pas disponible sur ce serveur (pas de tmux local ou commande docker introuvable).'
    : c.state !== 'running'
      ? 'Démarre le conteneur pour ouvrir un terminal.'
      : null
  return (
    <div className="flex max-w-2xl flex-col items-start gap-3 text-xs">
      <p className="leading-relaxed text-muted-foreground">
        Ouvre un shell dans le conteneur (<code>bash</code> s’il existe, sinon <code>sh</code>) dans une session tmux
        nommée <code>docker-…</code>, comme une session Atlas ordinaire. Quitter le shell (<code>exit</code>) ferme la
        session ; un nouveau clic la rouvre.
      </p>
      <Button
        variant="primary"
        leadingIcon={<SquareTerminal size={16} aria-hidden />}
        loading={pending === 'terminal'}
        loadingLabel="Ouverture…"
        disabled={Boolean(reason) || pending !== undefined}
        onClick={() => onTerminal(c)}
      >
        Ouvrir un terminal
      </Button>
      {reason && <p className="text-muted-foreground">{reason}</p>}
    </div>
  )
}
