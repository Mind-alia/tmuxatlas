import { Check, Clock, Pause, Play, Plus, RotateCcw, TriangleAlert, X } from 'lucide-react'
import { Button } from '../ui'
import type { CLIAvailability, LoopCLI, LoopWorker, WorkerActionType } from './loopsApi'
import { cliMeta, cliOrder, formatClockTime, formatCountdown } from './loopHelpers'

interface SubscriptionPoolBaseProps {
  availableSubscriptions: LoopWorker[]
  availabilityByCLI: Partial<Record<LoopCLI, CLIAvailability>>
  nowMs?: number
}

interface DraftSubscriptionPoolProps extends SubscriptionPoolBaseProps {
  mode: 'draft'
  workers: LoopWorker[]
  onChangeWorkers: (next: LoopWorker[]) => void
}

interface LiveSubscriptionPoolProps extends SubscriptionPoolBaseProps {
  mode: 'live'
  workers: LoopWorker[]
  activeWorkerId?: string
  busyAction?: string | null
  onWorkerAction: (payload: {
    worker_id?: string
    action: WorkerActionType
    cli?: LoopCLI
    person?: string
    label?: string
  }) => void
}

type SubscriptionPoolProps = DraftSubscriptionPoolProps | LiveSubscriptionPoolProps

export function SubscriptionPool(props: SubscriptionPoolProps) {
  const { availableSubscriptions, availabilityByCLI, nowMs = Date.now() } = props

  // Merge available subscriptions with any workers already stored on the loop.
  const mergedWorkers = (() => {
    const map = new Map<string, LoopWorker>()
    for (const sub of availableSubscriptions) {
      map.set(sub.id, {
        ...sub,
        enabled: props.mode === 'draft' ? false : false,
        paused: false,
        status: 'disabled',
        run_count: 0,
      })
    }
    for (const w of props.workers) {
      map.set(w.id, w)
    }
    return [...map.values()]
  })()

  const grouped = cliOrder.map(cli => ({
    cli,
    items: mergedWorkers.filter(w => w.cli === cli),
    avail: availabilityByCLI[cli],
  }))

  const enabledWorkers = mergedWorkers.filter(w => w.enabled)
  const activeCount = enabledWorkers.filter(w => !w.paused && (w.status === 'ready' || w.status === 'running')).length
  const pausedCount = enabledWorkers.filter(w => w.paused || w.status === 'paused').length
  const limitedCount = enabledWorkers.filter(w => !w.paused && w.status === 'rate_limited').length

  if (props.mode === 'draft') {
    const toggleDraft = (target: LoopWorker) => {
      const exists = props.workers.find(w => w.id === target.id)
      if (exists) {
        props.onChangeWorkers(
          props.workers.map(w =>
            w.id === target.id
              ? { ...w, enabled: !w.enabled, paused: false, status: !w.enabled ? 'ready' : 'disabled' }
              : w,
          ),
        )
      } else {
        props.onChangeWorkers([
          ...props.workers,
          { ...target, enabled: true, paused: false, status: 'ready' },
        ])
      }
    }

    const enableAllDraft = () => {
      props.onChangeWorkers(
        mergedWorkers.map(w => ({ ...w, enabled: true, paused: false, status: 'ready' })),
      )
    }

    const disableAllDraft = () => {
      props.onChangeWorkers(
        mergedWorkers.map(w => ({ ...w, enabled: false, paused: false, status: 'disabled' })),
      )
    }

    return (
      <div className="rounded-lg border border-border bg-background/50 p-3">
        <div className="mb-2.5 flex flex-wrap items-center justify-between gap-2">
          <div>
            <div className="text-xs font-semibold text-foreground">
              Abonnements autorisés ({enabledWorkers.length}/{mergedWorkers.length})
            </div>
            <div className="text-2xs text-muted-foreground">
              Un abonnement travaille à la fois. En cas d’indisponibilité, la boucle passe automatiquement au suivant.
            </div>
          </div>
          <div className="flex items-center gap-1.5">
            <button
              type="button"
              onClick={enableAllDraft}
              className="rounded border border-primary/40 bg-primary/10 px-2 py-1 text-2xs font-medium text-primary hover:bg-primary/20"
            >
              Tout activer ({mergedWorkers.length})
            </button>
            <button
              type="button"
              onClick={disableAllDraft}
              className="rounded border border-border bg-card px-2 py-1 text-2xs text-muted-foreground hover:text-foreground"
            >
              Aucun
            </button>
          </div>
        </div>

        <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-3">
          {grouped.map(({ cli, items, avail }) => {
            const meta = cliMeta[cli]
            const cliMissing = avail ? !avail.available : false
            return (
              <div key={cli} className="flex flex-col gap-1.5 rounded-md border border-border bg-card/70 p-2.5">
                <div className="flex items-center justify-between gap-1">
                  <span className={`rounded border px-1.5 py-0.5 text-2xs font-semibold ${meta.badge}`}>
                    {meta.title}
                  </span>
                  {cliMissing && <span className="text-2xs text-muted-foreground">Absent</span>}
                </div>
                {items.length === 0 ? (
                  <div className="py-2 text-2xs text-muted-foreground">Aucun abonnement détecté</div>
                ) : (
                  <div className="flex flex-col gap-1.5">
                    {items.map(worker => (
                      <button
                        key={worker.id}
                        type="button"
                        disabled={cliMissing}
                        onClick={() => toggleDraft(worker)}
                        aria-pressed={worker.enabled}
                        className={`flex items-center justify-between gap-2 rounded-md border px-2.5 py-1.5 text-left text-xs transition-colors ${
                          worker.enabled
                            ? 'border-primary/50 bg-primary/10 text-foreground'
                            : 'border-border bg-background/60 text-muted-foreground hover:border-border/80 hover:text-foreground'
                        } ${cliMissing ? 'cursor-not-allowed opacity-50' : ''}`}
                      >
                        <div className="min-w-0">
                          <div className="truncate font-medium">{worker.person || worker.label || cli}</div>
                          {worker.label && worker.person && worker.label !== worker.person && (
                            <div className="truncate text-2xs text-muted-foreground">{worker.label}</div>
                          )}
                        </div>
                        <span
                          className={`flex h-4 w-4 shrink-0 items-center justify-center rounded border ${
                            worker.enabled
                              ? 'border-primary bg-primary text-primary-foreground'
                              : 'border-border bg-card'
                          }`}
                        >
                          {worker.enabled && <Check size={11} />}
                        </span>
                      </button>
                    ))}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      </div>
    )
  }

  // Live cockpit mode
  return (
    <div className="rounded-lg border border-border bg-background/50 p-3">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-xs font-semibold text-foreground">Abonnements sur ce projet</span>
          <span className="rounded-full border border-border bg-card px-2 py-0.5 text-2xs text-muted-foreground">
            {activeCount} prêt{activeCount > 1 ? 's' : ''}
          </span>
          {pausedCount > 0 && (
            <span className="rounded-full border border-warning/40 bg-warning/10 px-2 py-0.5 text-2xs text-warning">
              {pausedCount} en pause
            </span>
          )}
          {limitedCount > 0 && (
            <span className="rounded-full border border-warning/40 bg-warning/15 px-2 py-0.5 text-2xs text-warning">
              {limitedCount} limité{limitedCount > 1 ? 's' : ''}
            </span>
          )}
        </div>

        <div className="flex flex-wrap items-center gap-1.5">
          <Button
            variant="secondary"
            size="sm"
            loading={props.busyAction === 'enable_all'}
            onClick={() => props.onWorkerAction({ action: 'enable_all' })}
          >
            Tout activer
          </Button>
          {activeCount > 0 && (
            <Button
              variant="secondary"
              size="sm"
              loading={props.busyAction === 'pause_all'}
              onClick={() => props.onWorkerAction({ action: 'pause_all' })}
              leadingIcon={<Pause size={12} />}
            >
              Tout en pause
            </Button>
          )}
          {pausedCount > 0 && (
            <Button
              variant="secondary"
              size="sm"
              loading={props.busyAction === 'resume_all'}
              onClick={() => props.onWorkerAction({ action: 'resume_all' })}
              leadingIcon={<Play size={12} />}
            >
              Tout réactiver
            </Button>
          )}
        </div>
      </div>

      <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
        {grouped.map(({ cli, items, avail }) => {
          const meta = cliMeta[cli]
          const cliMissing = avail ? !avail.available : false
          return (
            <div key={cli} className="flex flex-col gap-2 rounded-lg border border-border bg-card/80 p-2.5">
              <div className="flex items-center justify-between border-b border-border/60 pb-1.5">
                <div className="flex items-center gap-1.5">
                  <span className={`rounded border px-1.5 py-0.5 text-2xs font-semibold ${meta.badge}`}>
                    {cli.toUpperCase()}
                  </span>
                  <span className="text-xs font-medium text-foreground">{meta.title}</span>
                </div>
                <span className="text-2xs text-muted-foreground">
                  {cliMissing ? 'CLI absent' : `${items.filter(i => i.enabled).length}/${items.length} actifs`}
                </span>
              </div>

              {items.length === 0 ? (
                <div className="py-2 text-2xs text-muted-foreground">Aucun abonnement configuré</div>
              ) : (
                <div className="flex flex-col gap-2">
                  {items.map(worker => {
                    const isWorking = props.activeWorkerId === worker.id || worker.status === 'running'
                    const isPaused = worker.enabled && (worker.paused || worker.status === 'paused')
                    const isRateLimited = worker.enabled && !isPaused && worker.status === 'rate_limited'
                    const isError = worker.enabled && !isPaused && worker.status === 'error'
                    const countdown = isRateLimited ? formatCountdown(worker.reset_at, nowMs) : ''
                    const resetClock = isRateLimited ? formatClockTime(worker.reset_at) : ''

                    let cardClass = 'border-border bg-background/50'
                    if (!worker.enabled) {
                      cardClass = 'border-dashed border-border/70 bg-background/30 opacity-75'
                    } else if (isWorking) {
                      cardClass = 'border-primary/60 bg-primary/10'
                    } else if (isPaused) {
                      cardClass = 'border-warning/35 bg-warning/5'
                    } else if (isRateLimited) {
                      cardClass = 'border-warning/45 bg-warning/10'
                    } else if (isError) {
                      cardClass = 'border-destructive/45 bg-destructive/10'
                    }

                    return (
                      <div key={worker.id} className={`flex flex-col gap-1.5 rounded-md border p-2 transition-colors ${cardClass}`}>
                        <div className="flex items-start justify-between gap-2">
                          <div className="min-w-0">
                            <div className="flex items-center gap-1.5">
                              {isWorking && (
                                <span
                                  className="inline-block h-2 w-2 shrink-0 animate-pulse rounded-full bg-primary"
                                  title="Au travail actuellement"
                                />
                              )}
                              <span className="truncate text-xs font-semibold text-foreground">
                                {worker.person || worker.label || cli}
                              </span>
                            </div>
                            <div className="mt-0.5 flex flex-wrap items-center gap-1.5 text-2xs text-muted-foreground">
                              {worker.label && worker.label !== worker.person && (
                                <span className="truncate">{worker.label}</span>
                              )}
                              {worker.run_count > 0 && (
                                <span className="tabular-nums">· {worker.run_count} passe{worker.run_count > 1 ? 's' : ''}</span>
                              )}
                            </div>
                          </div>

                          {/* Status pill */}
                          {!worker.enabled ? (
                            <span className="shrink-0 rounded border border-border px-1.5 py-0.5 text-2xs text-muted-foreground">
                              Inactif
                            </span>
                          ) : isWorking ? (
                            <span className="shrink-0 rounded-full border border-primary/40 bg-primary/15 px-1.5 py-0.5 text-2xs font-medium text-primary">
                              {worker.pause_requested ? 'Pause après passe…' : 'Au travail'}
                            </span>
                          ) : isPaused ? (
                            <span className="shrink-0 rounded-full border border-warning/40 bg-warning/15 px-1.5 py-0.5 text-2xs font-medium text-warning">
                              En pause
                            </span>
                          ) : isRateLimited ? (
                            <span className="inline-flex shrink-0 items-center gap-1 rounded-full border border-warning/45 bg-warning/15 px-1.5 py-0.5 text-2xs font-medium text-warning">
                              <Clock size={10} />
                              {resetClock ? `${resetClock}` : 'Indisponible'}
                              {countdown ? ` (${countdown})` : ''}
                            </span>
                          ) : isError ? (
                            <span
                              className="inline-flex shrink-0 items-center gap-1 rounded-full border border-destructive/40 bg-destructive/15 px-1.5 py-0.5 text-2xs font-medium text-destructive"
                              title={worker.last_error}
                            >
                              <TriangleAlert size={10} />
                              Erreur
                            </span>
                          ) : (
                            <span className="shrink-0 rounded-full border border-success/40 bg-success/10 px-1.5 py-0.5 text-2xs font-medium text-success">
                              Prêt
                            </span>
                          )}
                        </div>

                        {worker.last_error && (isError || isRateLimited) && (
                          <div className="truncate text-2xs text-muted-foreground" title={worker.last_error}>
                            {worker.last_error}
                          </div>
                        )}

                        {/* 1-click per-subscription controls */}
                        <div className="flex flex-wrap items-center gap-1.5 pt-0.5">
                          {!worker.enabled ? (
                            <button
                              type="button"
                              disabled={cliMissing || props.busyAction === `enable-${worker.id}`}
                              onClick={() =>
                                props.onWorkerAction({
                                  worker_id: worker.id,
                                  action: 'enable',
                                  cli: worker.cli,
                                  person: worker.person,
                                  label: worker.label,
                                })
                              }
                              className="inline-flex items-center gap-1 rounded border border-primary/40 bg-primary/10 px-2 py-0.5 text-2xs font-medium text-primary hover:bg-primary/20 disabled:opacity-50"
                            >
                              <Plus size={11} />
                              Activer
                            </button>
                          ) : (
                            <>
                              {isPaused ? (
                                <button
                                  type="button"
                                  disabled={props.busyAction === `resume-${worker.id}`}
                                  onClick={() =>
                                    props.onWorkerAction({ worker_id: worker.id, action: 'resume' })
                                  }
                                  className="inline-flex items-center gap-1 rounded border border-success/40 bg-success/10 px-2 py-0.5 text-2xs font-medium text-success hover:bg-success/20 disabled:opacity-50"
                                >
                                  <Play size={11} />
                                  Réactiver
                                </button>
                              ) : (
                                <button
                                  type="button"
                                  disabled={worker.pause_requested || props.busyAction === `pause-${worker.id}`}
                                  onClick={() =>
                                    props.onWorkerAction({ worker_id: worker.id, action: 'pause' })
                                  }
                                  className="inline-flex items-center gap-1 rounded border border-warning/40 bg-warning/10 px-2 py-0.5 text-2xs font-medium text-warning hover:bg-warning/20 disabled:opacity-50"
                                >
                                  <Pause size={11} />
                                  {worker.pause_requested ? 'Pause…' : 'Pause'}
                                </button>
                              )}

                              {(isRateLimited || isError) && (
                                <button
                                  type="button"
                                  disabled={props.busyAction === `retry-${worker.id}`}
                                  onClick={() =>
                                    props.onWorkerAction({ worker_id: worker.id, action: 'retry' })
                                  }
                                  className="inline-flex items-center gap-1 rounded border border-border bg-card px-2 py-0.5 text-2xs text-foreground hover:border-primary/40 hover:text-primary disabled:opacity-50"
                                  title="Réinitialiser l'état de cet abonnement et le remettre en file"
                                >
                                  <RotateCcw size={10} />
                                  Tester
                                </button>
                              )}

                              <button
                                type="button"
                                disabled={props.busyAction === `disable-${worker.id}`}
                                onClick={() =>
                                  props.onWorkerAction({ worker_id: worker.id, action: 'disable' })
                                }
                                className="ml-auto inline-flex items-center gap-0.5 rounded px-1.5 py-0.5 text-2xs text-muted-foreground hover:bg-secondary hover:text-foreground disabled:opacity-50"
                                title="Retirer cet abonnement de la boucle"
                              >
                                <X size={11} />
                                Retirer
                              </button>
                            </>
                          )}
                        </div>
                      </div>
                    )
                  })}
                </div>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}
