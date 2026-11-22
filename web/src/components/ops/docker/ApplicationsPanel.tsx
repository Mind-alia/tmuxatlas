import { ChevronDown, ChevronRight, Layers, Play, RotateCw, ScrollText, Square } from 'lucide-react'
import { lazy, Suspense, useRef, useState } from 'react'
import { Button, Dialog } from '../../ui'
import { fetchStacks, runStackAction } from './api'
import { InlineError } from './ContainersList'
import {
  imageLabel,
  publishedPorts,
  routeLabel,
  routePort,
  routeSourceLabel,
  stackStateLabel,
  stackTargets,
  stackTone,
  stateLabel,
  dotTone,
} from './model'
import { HealthBadge, StatusDot, useNarrow, type DetailTab } from './parts'
import { Empty, ListingState, Table, useListing } from './ResourcesPanels'
import type {
  DockerContainer,
  DockerRoute,
  DockerStack,
  DockerStackAction,
  DockerStackActionResponse,
  DockerStackResult,
} from './types'

/** Deployment from git: loaded when an application's block is first opened. */
const DeployBlock = lazy(() => import('./deploy/DeployBlock'))

type View = 'stacks' | 'routes'

interface StackRequest {
  stack: DockerStack
  action: DockerStackAction
}

/** What the last action on a stack gave: the server's report, or why it was refused. */
type Outcome = { report: DockerStackActionResponse } | { error: string }

const actionText: Record<DockerStackAction, { title: string; body: string; button: string; pending: string; done: string }> = {
  start: {
    title: 'Démarrer',
    body: 'Atlas démarre les conteneurs arrêtés de l’application, un par un, les dépendances d’abord. Rien n’est créé, reconstruit ni téléchargé.',
    button: 'Démarrer',
    pending: 'Démarrage…',
    done: 'Application démarrée',
  },
  stop: {
    title: 'Arrêter',
    body: 'L’application sera coupée. Atlas arrête ses conteneurs un par un, les dépendances en dernier ; rien n’est supprimé.',
    button: 'Arrêter',
    pending: 'Arrêt…',
    done: 'Application arrêtée',
  },
  restart: {
    title: 'Redémarrer',
    body: 'L’application sera coupée le temps que ses conteneurs en marche redémarrent, un par un, les dépendances d’abord. Les conteneurs arrêtés ne sont pas relancés.',
    button: 'Redémarrer',
    pending: 'Redémarrage…',
    done: 'Application redémarrée',
  },
}

const resultText: Record<DockerStackResult['status'], string> = {
  done: 'fait',
  unchanged: 'déjà dans cet état',
  failed: 'échec',
  skipped: 'non tenté',
}

function plural(count: number, one: string, many: string) {
  return `${count} ${count > 1 ? many : one}`
}

/** The Applications tab: one card per Compose project, and the routes that lead to the containers. */
export function ApplicationsPanel({
  onOpenContainer,
  onContainersChanged,
}: {
  onOpenContainer: (id: string, tab?: DetailTab) => void
  onContainersChanged: () => void
}) {
  const { data, failure, forbidden, refresh } = useListing(fetchStacks)
  const [view, setView] = useState<View>('stacks')
  const [closed, setClosed] = useState<Record<string, boolean>>({})
  const [confirm, setConfirm] = useState<StackRequest | null>(null)
  const [busy, setBusy] = useState<Record<string, DockerStackAction>>({})
  const [outcomes, setOutcomes] = useState<Record<string, Outcome>>({})

  const perform = async ({ stack, action }: StackRequest) => {
    setBusy(current => ({ ...current, [stack.name]: action }))
    setOutcomes(current => {
      const next = { ...current }
      delete next[stack.name]
      return next
    })
    let outcome: Outcome
    try {
      outcome = { report: await runStackAction(stack.name, action) }
    } catch (error) {
      outcome = { error: error instanceof Error ? error.message : 'Action impossible' }
    }
    setOutcomes(current => ({ ...current, [stack.name]: outcome }))
    setBusy(current => {
      const next = { ...current }
      delete next[stack.name]
      return next
    })
    void refresh()
    onContainersChanged()
  }

  if (!data) return <ListingState data={data} failure={failure} forbidden={forbidden} what="applications" />
  if (forbidden) return <ListingState data={null} failure={null} forbidden what="applications" />
  if (!data.available) {
    return <InlineError message={`Impossible de lire les applications : ${data.error || 'Docker ne répond pas'}`} />
  }

  const stacks = data.stacks ?? []
  const standalone = data.standalone ?? []
  const routes = data.routes ?? []
  const views: { id: View; label: string }[] = [
    { id: 'stacks', label: `Applications (${stacks.length})` },
    { id: 'routes', label: `Routes (${routes.length})` },
  ]

  return (
    <div className="flex flex-col gap-3">
      {failure && (
        <p className="text-2xs text-warning" role="status">
          Dernière actualisation en échec ({failure}).
        </p>
      )}
      <div role="group" aria-label="Vue" className="flex gap-1 self-start rounded-lg border border-border bg-card p-0.5 text-xs">
        {views.map(item => (
          <button
            key={item.id}
            type="button"
            aria-pressed={view === item.id}
            onClick={() => setView(item.id)}
            className="ui-focus-ring h-8 rounded-md px-3 text-muted-foreground hover:text-foreground aria-pressed:bg-secondary aria-pressed:font-medium aria-pressed:text-foreground"
          >
            {item.label}
          </button>
        ))}
      </div>

      {view === 'routes' ? (
        <RoutesView routes={routes} onOpenContainer={onOpenContainer} />
      ) : (
        <>
          {stacks.length === 0 && (
            <Empty>
              Aucune application Compose sur ce serveur : aucun conteneur ne porte l’étiquette{' '}
              <code>com.docker.compose.project</code>.
            </Empty>
          )}
          {stacks.map(stack => (
            <StackCard
              key={stack.name}
              stack={stack}
              open={!closed[stack.name]}
              onToggle={() => setClosed(current => ({ ...current, [stack.name]: !current[stack.name] }))}
              busy={busy[stack.name]}
              outcome={outcomes[stack.name]}
              onDismiss={() =>
                setOutcomes(current => {
                  const next = { ...current }
                  delete next[stack.name]
                  return next
                })
              }
              onAction={action => setConfirm({ stack, action })}
              onChanged={() => {
                void refresh()
                onContainersChanged()
              }}
              onOpenContainer={onOpenContainer}
            />
          ))}
          {standalone.length > 0 && (
            <section aria-label="Sans application" className="atlas-surface flex min-w-0 flex-col gap-2 rounded-lg p-3 text-xs">
              <header className="flex min-w-0 items-baseline gap-2">
                <h3 className="font-medium text-foreground">Sans application</h3>
                <span className="text-2xs text-muted-foreground">
                  {plural(standalone.length, 'conteneur lancé', 'conteneurs lancés')} hors de Compose
                </span>
              </header>
              <ServiceList containers={standalone} onOpenContainer={onOpenContainer} />
            </section>
          )}
        </>
      )}

      <StackConfirmDialog
        request={confirm}
        onCancel={() => setConfirm(null)}
        onConfirm={request => {
          setConfirm(null)
          void perform(request)
        }}
      />
    </div>
  )
}

function StackCard({
  stack,
  open,
  onToggle,
  busy,
  outcome,
  onDismiss,
  onAction,
  onChanged,
  onOpenContainer,
}: {
  stack: DockerStack
  open: boolean
  onToggle: () => void
  busy?: DockerStackAction
  outcome?: Outcome
  onDismiss: () => void
  onAction: (action: DockerStackAction) => void
  onChanged: () => void
  onOpenContainer: (id: string, tab?: DetailTab) => void
}) {
  const services = stack.services ?? []
  const label = stackStateLabel[stack.state]
  const files = stack.config_files ?? []
  const can = (action: DockerStackAction) => !busy && stackTargets(stack, action).length > 0
  const guarded = Boolean(stack.protected)
  return (
    <article
      aria-label={`Application ${stack.name}`}
      data-stack={stack.name}
      className="atlas-surface flex min-w-0 flex-col gap-2 rounded-lg p-3 text-xs"
    >
      <header className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        <StatusDot tone={stackTone(stack)} label={label} decorative />
        <Layers size={14} aria-hidden className="shrink-0 text-muted-foreground" />
        <h3 className="min-w-0 truncate font-medium text-foreground">{stack.name}</h3>
        <span className="ml-auto whitespace-nowrap text-2xs text-muted-foreground">
          {label} · {stack.running}/{stack.total} {stack.total > 1 ? 'services démarrés' : 'service démarré'}
        </span>
      </header>
      {stack.working_dir && (
        <p className="truncate font-mono text-2xs text-muted-foreground" title={files.length ? `Fichiers Compose : ${files.join(', ')}` : undefined}>
          {stack.working_dir}
        </p>
      )}
      {stack.protected && (
        <p className="text-2xs text-warning">
          Arrêt et redémarrage désactivés : {stack.protected}.
        </p>
      )}
      <div className="flex flex-wrap items-center gap-1.5">
        <Button size="sm" leadingIcon={<Play size={14} aria-hidden />} disabled={!can('start')} onClick={() => onAction('start')} aria-label={`Démarrer l’application ${stack.name}`}>
          Démarrer
        </Button>
        <Button size="sm" leadingIcon={<Square size={14} aria-hidden />} disabled={guarded || !can('stop')} onClick={() => onAction('stop')} aria-label={`Arrêter l’application ${stack.name}`}>
          Arrêter
        </Button>
        <Button size="sm" leadingIcon={<RotateCw size={14} aria-hidden />} disabled={guarded || !can('restart')} onClick={() => onAction('restart')} aria-label={`Redémarrer l’application ${stack.name}`}>
          Redémarrer
        </Button>
        {busy && (
          <span role="status" className="text-2xs text-muted-foreground">
            {actionText[busy].pending}
          </span>
        )}
      </div>
      {outcome && <OutcomeReport outcome={outcome} onDismiss={onDismiss} />}
      {stack.working_dir && <DeploySection stack={stack.name} onChanged={onChanged} />}
      <button
        type="button"
        aria-expanded={open}
        onClick={onToggle}
        className="ui-focus-ring -mx-1 flex min-h-9 items-center gap-1.5 self-start rounded px-1 text-2xs text-muted-foreground hover:text-foreground"
      >
        {open ? <ChevronDown size={14} aria-hidden /> : <ChevronRight size={14} aria-hidden />}
        {plural(services.length, 'conteneur', 'conteneurs')}
      </button>
      {open && <ServiceList containers={services} onOpenContainer={onOpenContainer} />}
    </article>
  )
}

const deployOpenKey = (stack: string) => `atlas-deploy-open:${stack}`

/** The "Déploiement" block of a card. It stays open across reloads, so a running deployment is found again. */
function DeploySection({ stack, onChanged }: { stack: string; onChanged: () => void }) {
  const [open, setOpen] = useState(() => {
    try {
      return window.localStorage.getItem(deployOpenKey(stack)) === '1'
    } catch {
      return false
    }
  })
  const toggle = () => {
    const next = !open
    setOpen(next)
    try {
      if (next) window.localStorage.setItem(deployOpenKey(stack), '1')
      else window.localStorage.removeItem(deployOpenKey(stack))
    } catch {
      /* stockage indisponible : le bloc se refermera au rechargement */
    }
  }
  return (
    <>
      <button
        type="button"
        aria-expanded={open}
        onClick={toggle}
        className="ui-focus-ring -mx-1 flex min-h-9 items-center gap-1.5 self-start rounded px-1 text-2xs text-muted-foreground hover:text-foreground"
      >
        {open ? <ChevronDown size={14} aria-hidden /> : <ChevronRight size={14} aria-hidden />}
        Déploiement
      </button>
      {open && (
        <Suspense fallback={<p className="text-2xs text-muted-foreground">Chargement…</p>}>
          <DeployBlock stack={stack} onChanged={onChanged} />
        </Suspense>
      )}
    </>
  )
}

function ServiceList({ containers, onOpenContainer }: { containers: DockerContainer[]; onOpenContainer: (id: string, tab?: DetailTab) => void }) {
  return (
    <ul className="flex flex-col divide-y divide-border/60 border-t border-border/60">
      {containers.map(c => {
        const ports = publishedPorts(c.ports)
        return (
          <li key={c.id} data-container={c.name} className="flex min-w-0 items-start gap-2 py-2">
            <span className="pt-1">
              <StatusDot tone={dotTone(c)} label={stateLabel(c)} pulse={c.state === 'restarting'} decorative />
            </span>
            <div className="min-w-0 flex-1">
              <p className="flex min-w-0 flex-wrap items-center gap-x-2">
                <span className="truncate font-medium text-foreground">{c.compose_service || c.name}</span>
                <span className="whitespace-nowrap text-2xs text-muted-foreground">{stateLabel(c)}</span>
                <HealthBadge health={c.health} />
              </p>
              <p className="truncate text-2xs text-muted-foreground" title={c.image}>
                {c.compose_service ? <span className="font-mono">{c.name} · </span> : null}
                {imageLabel(c.image)}
              </p>
              {ports.length > 0 && <p className="truncate font-mono text-2xs text-muted-foreground">Ports : {ports.join(', ')}</p>}
            </div>
            <Button
              size="sm"
              variant="ghost"
              leadingIcon={<ScrollText size={14} aria-hidden />}
              aria-label={`Journaux et détails de ${c.name}`}
              onClick={() => onOpenContainer(c.id, 'logs')}
            >
              Journaux
            </Button>
          </li>
        )
      })}
    </ul>
  )
}

function OutcomeReport({ outcome, onDismiss }: { outcome: Outcome; onDismiss: () => void }) {
  if ('error' in outcome) return <InlineError message={outcome.error} onDismiss={onDismiss} />
  const { report } = outcome
  const results = report.results ?? []
  if (report.ok) {
    const changed = results.filter(r => r.status === 'done').length
    return (
      <p role="status" className="flex items-start gap-2 text-2xs text-muted-foreground">
        <span className="min-w-0 flex-1">
          {actionText[report.action].done} : {changed === 0 ? 'rien à faire' : plural(changed, 'conteneur concerné', 'conteneurs concernés')}.
        </span>
        <button type="button" onClick={onDismiss} className="ui-focus-ring shrink-0 rounded px-1 underline">
          Fermer
        </button>
      </p>
    )
  }
  return (
    <div
      role="alert"
      className="flex flex-col gap-1 rounded-md px-2 py-1.5 text-xs"
      style={{ color: 'var(--destructive)', background: 'color-mix(in oklch, var(--destructive) 10%, transparent)' }}
    >
      <div className="flex items-start gap-2">
        <p className="min-w-0 flex-1 font-medium">
          Action interrompue : un conteneur a échoué, les suivants n’ont pas été touchés.
        </p>
        <button type="button" onClick={onDismiss} className="ui-focus-ring shrink-0 rounded px-1 text-2xs underline">
          Fermer
        </button>
      </div>
      <ul className="flex flex-col gap-0.5 text-foreground">
        {results.map(result => (
          <li key={result.id} data-result={result.status} className="min-w-0 break-words">
            <span className="font-mono">{result.name}</span> : {resultText[result.status]}
            {result.error ? ` — ${result.error}` : ''}
          </li>
        ))}
      </ul>
    </div>
  )
}

/** Every stack action is confirmed, with the containers it will touch, in order. */
function StackConfirmDialog({
  request,
  onCancel,
  onConfirm,
}: {
  request: StackRequest | null
  onCancel: () => void
  onConfirm: (request: StackRequest) => void
}) {
  // The safe choice has the focus: Enter alone never stops an application.
  const cancelRef = useRef<HTMLButtonElement>(null)
  const text = request ? actionText[request.action] : null
  const targets = request ? stackTargets(request.stack, request.action) : []
  return (
    <Dialog
      open={Boolean(request)}
      onOpenChange={open => {
        if (!open) onCancel()
      }}
      initialFocusRef={cancelRef}
      role="alertdialog"
      title={request && text ? `${text.title} l’application « ${request.stack.name} » ?` : ''}
      description={text?.body}
      footer={
        request &&
        text && (
          <>
            <Button ref={cancelRef} variant="ghost" onClick={onCancel}>
              Annuler
            </Button>
            <Button variant={request.action === 'stop' ? 'destructive' : 'primary'} onClick={() => onConfirm(request)}>
              {text.button}
            </Button>
          </>
        )
      }
    >
      <p className="mb-1 text-xs text-muted-foreground">
        {plural(targets.length, 'conteneur touché', 'conteneurs touchés')}, dans cet ordre :
      </p>
      <ol aria-label="Conteneurs touchés" className="max-h-48 list-decimal overflow-y-auto pl-5 text-xs">
        {targets.map(c => (
          <li key={c.id} className="break-words">
            <span className="font-mono">{c.name}</span>
            {c.compose_service ? <span className="text-muted-foreground"> ({c.compose_service})</span> : null}
          </li>
        ))}
      </ol>
    </Dialog>
  )
}

function RouteContainer({ route, onOpenContainer }: { route: DockerRoute; onOpenContainer: (id: string, tab?: DetailTab) => void }) {
  return (
    <>
      <button
        type="button"
        onClick={() => onOpenContainer(route.container_id, 'logs')}
        className="ui-focus-ring max-w-full truncate rounded text-left text-primary underline"
      >
        {route.container}
      </button>
      {route.state !== 'running' && <span className="ml-1 whitespace-nowrap text-2xs text-warning">(à l’arrêt)</span>}
      {route.compose_project && <span className="block truncate text-2xs text-muted-foreground">{route.compose_project}</span>}
    </>
  )
}

function RoutesView({ routes, onOpenContainer }: { routes: DockerRoute[]; onOpenContainer: (id: string, tab?: DetailTab) => void }) {
  const ref = useRef<HTMLDivElement>(null)
  const narrow = useNarrow(ref, 560)
  const onlyPorts = routes.length > 0 && routes.every(route => route.source === 'port')
  const key = (route: DockerRoute, index: number) => `${route.source}-${route.container_id}-${index}`
  return (
    <div ref={ref} className="flex min-w-0 flex-col gap-2">
      {routes.length === 0 ? (
        <Empty>
          Aucune route détectable : aucun conteneur ne porte d’étiquette Traefik ou Caddy, et aucun ne publie de port.
        </Empty>
      ) : narrow ? (
        <ul aria-label="Routes" className="flex flex-col gap-2">
          {routes.map((route, index) => (
            <li key={key(route, index)} className="atlas-surface flex min-w-0 flex-col gap-1 rounded-lg p-3 text-xs">
              <p className="break-all font-mono text-foreground" title={route.rule}>
                {routeLabel(route)}
              </p>
              <p className="min-w-0 text-muted-foreground">
                → <RouteContainer route={route} onOpenContainer={onOpenContainer} />
              </p>
              <p className="text-2xs text-muted-foreground">
                Port {routePort(route)} · {routeSourceLabel[route.source]}
              </p>
            </li>
          ))}
        </ul>
      ) : (
        <Table label="Routes" head={['Domaine ou règle', 'Conteneur', 'Port du conteneur', 'Source']}>
          {routes.map((route, index) => (
            <tr key={key(route, index)} className="border-b border-border/60 last:border-b-0">
              <td className="max-w-[22rem] break-all px-3 py-2 font-mono" title={route.rule}>
                {routeLabel(route)}
              </td>
              <td className="max-w-[16rem] px-3 py-2">
                <RouteContainer route={route} onOpenContainer={onOpenContainer} />
              </td>
              <td className="whitespace-nowrap px-3 py-2 tabular-nums" title={route.port ? undefined : 'Les étiquettes ne précisent pas le port'}>
                {routePort(route)}
              </td>
              <td className="whitespace-nowrap px-3 py-2 text-muted-foreground">{routeSourceLabel[route.source]}</td>
            </tr>
          ))}
        </Table>
      )}
      <p className="text-2xs leading-relaxed text-muted-foreground">
        {onlyPorts
          ? 'Aucune étiquette Traefik ou Caddy sur les conteneurs : seuls les ports publiés sur le serveur sont listés. '
          : ''}
        Lecture seule, déduite des étiquettes des conteneurs et de leurs ports publiés. Un proxy configuré par fichier
        (nginx, Caddyfile, configuration Traefik) n’apparaît pas ici.
      </p>
    </div>
  )
}
