import { ChevronDown, ChevronRight, Layers, Search, X } from 'lucide-react'
import { useMemo, useRef, useState, type ReactNode } from 'react'
import { formatBytes, formatPercent, formatRelative } from '../../monitoring/format'
import { IconButton } from '../../ui'
import {
  cpuText,
  dotTone,
  filterContainers,
  groupContainers,
  imageLabel,
  lastStarted,
  memoryText,
  publishedPorts,
  stateLabel,
  summarizeGroup,
} from './model'
import { ContainerActions, HealthBadge, StatusDot, useNarrow, type DetailTab, type PendingKind } from './parts'
import type { DockerAction, DockerContainer, DockerList } from './types'

export interface ListCallbacks {
  pending: Record<string, PendingKind>
  errors: Record<string, string>
  terminal: boolean
  onAction: (c: DockerContainer, action: DockerAction) => void
  onOpen: (c: DockerContainer, tab?: DetailTab) => void
  onTerminal: (c: DockerContainer) => void
  onDismissError: (id: string) => void
}

/** The two tiles at the top of the view, as in Docker Desktop. */
export function SummaryTiles({ data }: { data: DockerList }) {
  const ncpu = data.host?.ncpu ?? 0
  const memTotal = data.host?.mem_total ?? 0
  const cpu = data.summary.cpu_percent
  const memory = data.summary.memory_usage
  // The first listing has memory at once; CPU needs a second sample, 3 s later.
  const waiting = data.summary.stats_pending && data.summary.running > 0
  return (
    <div className="mb-4 grid gap-3 sm:grid-cols-2">
      <Tile
        label="CPU des conteneurs"
        value={waiting && cpu === 0 ? '—' : formatPercent(cpu)}
        detail={ncpu ? `sur ${ncpu * 100} % (${ncpu} CPU disponible${ncpu > 1 ? 's' : ''})` : undefined}
        ratio={ncpu ? cpu / (ncpu * 100) : undefined}
      />
      <Tile
        label="Mémoire des conteneurs"
        value={waiting && memory === 0 ? '—' : formatBytes(memory)}
        detail={memTotal ? `sur ${formatBytes(memTotal)}` : undefined}
        ratio={memTotal ? memory / memTotal : undefined}
      />
    </div>
  )
}

function Tile({ label, value, detail, ratio }: { label: string; value: string; detail?: string; ratio?: number }) {
  const percent = ratio === undefined ? undefined : Math.min(100, Math.max(0, ratio * 100))
  const tone = percent !== undefined && percent >= 90 ? 'var(--destructive)' : percent !== undefined && percent >= 75 ? 'var(--warning)' : 'var(--mon-a)'
  return (
    <section aria-label={label} className="atlas-surface flex min-w-0 flex-col gap-2 rounded-lg p-3">
      <h2 className="text-xs text-muted-foreground">{label}</h2>
      <p className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
        <span className="text-2xl font-semibold leading-none tabular-nums text-foreground">{value}</span>
        {detail && <span className="text-2xs text-muted-foreground">{detail}</span>}
      </p>
      {percent !== undefined && (
        <div className="h-1 w-full overflow-hidden rounded-full bg-secondary" role="presentation">
          <div className="h-full rounded-full transition-[width] duration-300" style={{ width: `${percent}%`, background: tone }} />
        </div>
      )}
    </section>
  )
}

export function Toolbar({
  query,
  onQuery,
  onlyRunning,
  onOnlyRunning,
  counts,
}: {
  query: string
  onQuery: (value: string) => void
  onlyRunning: boolean
  onOnlyRunning: (value: boolean) => void
  counts: { shown: number; total: number; running: number }
}) {
  return (
    <div className="mb-3 flex flex-wrap items-center gap-x-4 gap-y-2">
      <div className="relative flex min-w-0 flex-1 basis-56 items-center sm:max-w-xs">
        <label className="contents">
          <span className="sr-only">Rechercher un conteneur</span>
          <Search size={14} aria-hidden className="pointer-events-none absolute left-2.5 text-muted-foreground" />
          <input
            type="search"
            value={query}
            onChange={event => onQuery(event.target.value)}
            placeholder="Rechercher"
            className="ui-focus-ring h-9 w-full min-w-0 rounded-md border border-border bg-input pl-8 pr-9 text-xs text-foreground placeholder:text-muted-foreground"
          />
        </label>
        {query && (
          <IconButton
            size="sm"
            aria-label="Effacer la recherche"
            className="absolute right-0 !min-h-9 !min-w-9"
            onClick={() => onQuery('')}
          >
            <X size={14} aria-hidden />
          </IconButton>
        )}
      </div>
      <label className="flex min-h-9 cursor-pointer select-none items-center gap-2 text-xs">
        <input
          type="checkbox"
          role="switch"
          aria-checked={onlyRunning}
          checked={onlyRunning}
          onChange={event => onOnlyRunning(event.target.checked)}
          className="peer sr-only"
        />
        <span
          aria-hidden
          className="relative inline-flex h-4 w-7 shrink-0 rounded-full bg-secondary transition-colors peer-checked:bg-primary peer-focus-visible:ring-2 peer-focus-visible:ring-ring after:absolute after:left-0.5 after:top-0.5 after:size-3 after:rounded-full after:bg-card after:transition-transform peer-checked:after:translate-x-3"
        />
        Seulement ceux en cours
      </label>
      <span className="text-2xs text-muted-foreground sm:ml-auto" aria-live="polite">
        {counts.shown === counts.total
          ? `${counts.total} conteneur${counts.total > 1 ? 's' : ''}, ${counts.running} en cours`
          : `${counts.shown} sur ${counts.total} conteneurs`}
      </span>
    </div>
  )
}

/** Filters, groups by Compose project and renders the table, or cards when narrow. */
export function ContainersList({ data, callbacks }: { data: DockerList; callbacks: ListCallbacks }) {
  const [query, setQuery] = useState('')
  const [onlyRunning, setOnlyRunning] = useState(false)
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => new Set())
  const rootRef = useRef<HTMLDivElement>(null)
  const narrow = useNarrow(rootRef, 820)
  const all = data.containers ?? []
  const shown = useMemo(() => filterContainers(all, query, onlyRunning), [all, query, onlyRunning])
  const rows = useMemo(() => groupContainers(shown), [shown])
  const toggle = (project: string) =>
    setCollapsed(previous => {
      const next = new Set(previous)
      if (next.has(project)) next.delete(project)
      else next.add(project)
      return next
    })
  // A search opens every group so matches are never hidden.
  const isOpen = (project: string) => query.trim() !== '' || !collapsed.has(project)

  return (
    <div ref={rootRef}>
      <Toolbar
        query={query}
        onQuery={setQuery}
        onlyRunning={onlyRunning}
        onOnlyRunning={setOnlyRunning}
        counts={{ shown: shown.length, total: all.length, running: data.summary.running }}
      />
      {rows.length === 0 ? (
        <div className="rounded-lg border border-border bg-card px-3 py-6 text-center text-xs text-muted-foreground">
          {all.length === 0 ? 'Aucun conteneur sur ce serveur.' : 'Aucun conteneur ne correspond.'}
        </div>
      ) : narrow ? (
        <CardList rows={rows} isOpen={isOpen} toggle={toggle} callbacks={callbacks} />
      ) : (
        <ContainersTable rows={rows} isOpen={isOpen} toggle={toggle} callbacks={callbacks} />
      )}
    </div>
  )
}

type RowsProps = {
  rows: ReturnType<typeof groupContainers>
  isOpen: (project: string) => boolean
  toggle: (project: string) => void
  callbacks: ListCallbacks
}

const head = ['', 'Nom', 'Image', 'Ports', 'CPU', 'Mémoire', 'Dernier démarrage', 'Actions']

function ContainersTable({ rows, isOpen, toggle, callbacks }: RowsProps) {
  return (
    <div role="region" aria-label="Conteneurs" className="rounded-lg border border-border bg-card">
      <table className="w-full border-collapse text-left text-xs font-normal">
        <caption className="sr-only">Conteneurs Docker, groupés par projet Compose</caption>
        <thead>
          <tr className="border-b border-border text-2xs text-muted-foreground">
            {head.map((label, i) => (
              <th
                key={i}
                scope="col"
                className={`whitespace-nowrap px-2 py-2 font-medium first:pl-3 last:pr-2 ${i >= 4 && i <= 5 ? 'text-right' : ''} ${i === 7 ? 'text-right' : ''}`}
              >
                {label || <span className="sr-only">État</span>}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map(row =>
            row.kind === 'group' ? (
              <GroupRows key={`group-${row.project}`} project={row.project} containers={row.containers} open={isOpen(row.project)} onToggle={() => toggle(row.project)} callbacks={callbacks} />
            ) : (
              <ContainerRow key={row.container.id} container={row.container} callbacks={callbacks} />
            ),
          )}
        </tbody>
      </table>
    </div>
  )
}

function GroupRows({
  project,
  containers,
  open,
  onToggle,
  callbacks,
}: {
  project: string
  containers: DockerContainer[]
  open: boolean
  onToggle: () => void
  callbacks: ListCallbacks
}) {
  const summary = summarizeGroup(containers)
  return (
    <>
      <tr className="border-b border-border/60 bg-secondary/20 transition-colors hover:bg-secondary/40" data-group={project}>
        <td className="py-1.5 pl-3 pr-1">
          <button
            type="button"
            aria-expanded={open}
            aria-label={`${open ? 'Replier' : 'Déplier'} le projet ${project}`}
            onClick={onToggle}
            className="ui-focus-ring -ml-1 inline-flex size-7 items-center justify-center rounded text-muted-foreground hover:bg-secondary hover:text-foreground"
          >
            {open ? <ChevronDown size={14} aria-hidden /> : <ChevronRight size={14} aria-hidden />}
          </button>
        </td>
        <td className="max-w-[18rem] px-2 py-1.5" colSpan={3}>
          <div className="flex min-w-0 items-center gap-2">
            <StatusDot tone={summary.tone} label={summary.label} decorative />
            <Layers size={14} aria-hidden className="shrink-0 text-muted-foreground" />
            <button type="button" onClick={onToggle} tabIndex={-1} className="ui-focus-ring truncate rounded font-medium text-foreground">
              {project}
            </button>
            <span className="whitespace-nowrap text-2xs text-muted-foreground">{summary.label}</span>
          </div>
        </td>
        <td className="whitespace-nowrap px-2 py-1.5 text-right tabular-nums">{summary.cpu === null ? '—' : formatPercent(summary.cpu)}</td>
        <td className="whitespace-nowrap px-2 py-1.5 text-right tabular-nums">{summary.memory ? formatBytes(summary.memory) : '—'}</td>
        <td className="whitespace-nowrap px-2 py-1.5 text-muted-foreground">{summary.lastStart ? formatRelative(summary.lastStart) : '—'}</td>
        <td className="px-2 py-1.5" />
      </tr>
      {open && containers.map(c => <ContainerRow key={c.id} container={c} nested callbacks={callbacks} />)}
    </>
  )
}

function ContainerRow({ container: c, nested = false, callbacks }: { container: DockerContainer; nested?: boolean; callbacks: ListCallbacks }) {
  const ports = publishedPorts(c.ports)
  const error = callbacks.errors[c.id]
  const tone = dotTone(c)
  return (
    <>
      <tr
        className="cursor-pointer border-b border-border/60 transition-colors hover:bg-secondary/40"
        onClick={() => callbacks.onOpen(c)}
        data-container={c.name}
      >
        <td className={`py-1.5 pr-1 ${nested ? 'pl-8' : 'pl-3'}`}>
          <StatusDot tone={tone} label={stateLabel(c)} pulse={c.state === 'restarting'} />
        </td>
        <td className="max-w-[18rem] px-2 py-1.5">
          <div className="flex min-w-0 items-center gap-2">
            <button
              type="button"
              className="ui-focus-ring min-w-0 truncate rounded text-left font-medium text-foreground hover:underline"
              onClick={event => {
                event.stopPropagation()
                callbacks.onOpen(c)
              }}
              title={c.name}
            >
              {c.name}
            </button>
            <HealthBadge health={c.health} />
          </div>
          <div className="truncate font-mono text-2xs text-muted-foreground">
            {c.short_id}
            {tone === 'error' || c.state !== 'running' ? <span className="font-sans"> · {stateLabel(c)}</span> : null}
          </div>
        </td>
        <td className="max-w-[14rem] truncate px-2 py-1.5 text-muted-foreground" title={c.image}>
          {imageLabel(c.image)}
        </td>
        <td className="px-2 py-1.5 font-mono text-2xs text-muted-foreground">
          {ports.length === 0 ? '—' : <PortList ports={ports} />}
        </td>
        <td className="whitespace-nowrap px-2 py-1.5 text-right tabular-nums">{c.state === 'running' ? cpuText(c.stats) : '—'}</td>
        <td className="whitespace-nowrap px-2 py-1.5 text-right tabular-nums" title={c.stats?.memory_limit ? `limite ${formatBytes(c.stats.memory_limit)}` : undefined}>
          {c.state === 'running' ? memoryText(c.stats) : '—'}
        </td>
        <td className="whitespace-nowrap px-2 py-1.5 text-muted-foreground" title={c.started_at}>
          {lastStarted(c)}
        </td>
        <td className="py-1 pl-1 pr-2">
          <ContainerActions
            container={c}
            pending={callbacks.pending[c.id]}
            terminal={callbacks.terminal}
            onAction={callbacks.onAction}
            onOpen={callbacks.onOpen}
            onTerminal={callbacks.onTerminal}
          />
        </td>
      </tr>
      {error && (
        <tr className="border-b border-border/60">
          <td colSpan={8} className={`py-1.5 pr-2 ${nested ? 'pl-8' : 'pl-3'}`}>
            <InlineError message={error} onDismiss={() => callbacks.onDismissError(c.id)} />
          </td>
        </tr>
      )}
    </>
  )
}

function PortList({ ports }: { ports: string[] }) {
  const visible = ports.slice(0, 2)
  return (
    <span className="flex flex-wrap gap-x-2 gap-y-0.5" title={ports.join(', ')}>
      {visible.map(port => (
        <span key={port} className="whitespace-nowrap">
          {port}
        </span>
      ))}
      {ports.length > visible.length && <span className="font-sans">+{ports.length - visible.length}</span>}
    </span>
  )
}

export function InlineError({ message, onDismiss }: { message: string; onDismiss?: () => void }) {
  return (
    <div role="alert" className="flex items-start gap-2 rounded-md px-2 py-1.5 text-xs" style={{ color: 'var(--destructive)', background: 'color-mix(in oklch, var(--destructive) 10%, transparent)' }}>
      <span className="min-w-0 flex-1 break-words">{message}</span>
      {onDismiss && (
        <button type="button" onClick={onDismiss} className="ui-focus-ring shrink-0 rounded px-1 text-2xs underline">
          Fermer
        </button>
      )}
    </div>
  )
}

function CardList({ rows, isOpen, toggle, callbacks }: RowsProps) {
  return (
    <div role="region" aria-label="Conteneurs" className="flex flex-col gap-3">
      {rows.map(row =>
        row.kind === 'group' ? (
          <section key={`group-${row.project}`} aria-label={`Projet ${row.project}`} className="flex flex-col gap-2">
            <GroupHeader project={row.project} containers={row.containers} open={isOpen(row.project)} onToggle={() => toggle(row.project)} />
            {isOpen(row.project) && row.containers.map(c => <ContainerCard key={c.id} container={c} callbacks={callbacks} />)}
          </section>
        ) : (
          <ContainerCard key={row.container.id} container={row.container} callbacks={callbacks} />
        ),
      )}
    </div>
  )
}

function GroupHeader({ project, containers, open, onToggle }: { project: string; containers: DockerContainer[]; open: boolean; onToggle: () => void }) {
  const summary = summarizeGroup(containers)
  return (
    <button
      type="button"
      aria-expanded={open}
      aria-label={`Projet ${project}, ${summary.label}`}
      onClick={onToggle}
      className="ui-focus-ring flex min-h-11 w-full min-w-0 items-center gap-2 rounded-lg bg-secondary/40 px-3 text-left text-xs"
    >
      {open ? <ChevronDown size={14} aria-hidden /> : <ChevronRight size={14} aria-hidden />}
      <StatusDot tone={summary.tone} label={summary.label} decorative />
      <Layers size={14} aria-hidden className="shrink-0 text-muted-foreground" />
      <span className="min-w-0 truncate font-medium">{project}</span>
      <span className="ml-auto whitespace-nowrap text-2xs text-muted-foreground">{summary.label}</span>
    </button>
  )
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-2xs text-muted-foreground">{label}</dt>
      <dd className="truncate tabular-nums">{children}</dd>
    </div>
  )
}

function ContainerCard({ container: c, callbacks }: { container: DockerContainer; callbacks: ListCallbacks }) {
  const ports = publishedPorts(c.ports)
  const error = callbacks.errors[c.id]
  return (
    <article aria-label={c.name} data-container={c.name} className="atlas-surface flex min-w-0 flex-col gap-2 rounded-lg p-3 text-xs">
      <div className="flex min-w-0 items-start gap-2">
        <span className="pt-1">
          <StatusDot tone={dotTone(c)} label={stateLabel(c)} pulse={c.state === 'restarting'} />
        </span>
        <button
          type="button"
          onClick={() => callbacks.onOpen(c)}
          className="ui-focus-ring min-w-0 flex-1 rounded text-left"
        >
          <span className="block truncate font-medium text-foreground">{c.name}</span>
          <span className="block truncate font-mono text-2xs text-muted-foreground">
            {c.short_id} · <span className="font-sans">{stateLabel(c)}</span>
          </span>
        </button>
        <HealthBadge health={c.health} />
      </div>
      <p className="truncate text-muted-foreground" title={c.image}>
        {imageLabel(c.image)}
      </p>
      <dl className="grid grid-cols-3 gap-2">
        <Fact label="CPU">{c.state === 'running' ? cpuText(c.stats) : '—'}</Fact>
        <Fact label="Mémoire">{c.state === 'running' ? memoryText(c.stats) : '—'}</Fact>
        <Fact label="Démarré">{lastStarted(c)}</Fact>
      </dl>
      {ports.length > 0 && <p className="truncate font-mono text-2xs text-muted-foreground">Ports : {ports.join(', ')}</p>}
      {error && <InlineError message={error} onDismiss={() => callbacks.onDismissError(c.id)} />}
      <div className="-mb-1 -mr-1 flex justify-end border-t border-border/60 pt-1">
        <ContainerActions
          container={c}
          pending={callbacks.pending[c.id]}
          terminal={callbacks.terminal}
          onAction={callbacks.onAction}
          onOpen={callbacks.onOpen}
          onTerminal={callbacks.onTerminal}
        />
      </div>
    </article>
  )
}
