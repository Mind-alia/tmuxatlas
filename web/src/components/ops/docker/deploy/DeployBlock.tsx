import { Ban, Check, ChevronDown, ChevronRight, CircleDashed, GitBranch, Loader2, Rocket, Undo2, X } from 'lucide-react'
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { Button, Dialog } from '../../../ui'
import { InlineError } from '../ContainersList'
import { cancelDeploy, fetchDeployRecord, fetchDeployState, startDeploy, startRollback } from './api'
import type { DeployCommit, DeployImage, DeployRecord, DeployState, DeployStep, DeployStepName } from './types'

/** Polling: fast while an operation runs, slow otherwise (each reading runs two git commands). */
const RUNNING_MS = 2_000
const IDLE_MS = 20_000

const stepLabel: Record<DeployStepName, string> = {
  check: 'Vérifier le dépôt git',
  fetch: 'Récupérer la branche distante (git fetch)',
  merge: 'Avancer la branche (git merge --ff-only)',
  images: 'Mémoriser les images en service',
  retag: 'Remettre les images précédentes sous leur nom',
  build: 'Construire (docker compose build)',
  up: 'Relancer les services (docker compose up -d)',
  verify: 'Vérifier les conteneurs',
  prune: 'Retirer les anciennes étiquettes de retour arrière',
}

const imageStatusLabel: Record<NonNullable<DeployImage['status']>, string> = {
  rebuilt: 'reconstruite, l’ancienne est gardée',
  unchanged: 'inchangée',
  replaced: 'remplacée par la précédente',
}

const deploySteps: DeployStepName[] = ['check', 'fetch', 'merge', 'images', 'build', 'up', 'verify', 'prune']

const resultLabel: Record<DeployRecord['result'], string> = {
  running: 'en cours',
  success: 'réussi',
  failed: 'échec',
  cancelled: 'annulé',
  interrupted: 'interrompu',
}

const resultColor: Record<DeployRecord['result'], string> = {
  running: 'var(--muted-foreground)',
  success: 'var(--success, var(--primary))',
  failed: 'var(--destructive)',
  cancelled: 'var(--warning)',
  interrupted: 'var(--warning)',
}

function plural(count: number, one: string, many: string) {
  return `${count} ${count > 1 ? many : one}`
}

function when(value?: string) {
  if (!value) return ''
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '' : date.toLocaleString('fr-FR', { dateStyle: 'short', timeStyle: 'short' })
}

function duration(ms: number) {
  if (ms < 1000) return '< 1 s'
  if (ms < 60_000) return `${Math.round(ms / 1000)} s`
  return `${Math.floor(ms / 60_000)} min ${Math.round((ms % 60_000) / 1000)} s`
}

function message(error: unknown) {
  return error instanceof Error ? error.message : 'Action impossible'
}

function CommitLabel({ commit }: { commit?: DeployCommit }) {
  if (!commit) return <span className="text-muted-foreground">commit inconnu</span>
  return (
    <span className="min-w-0 break-words" title={commit.sha}>
      <code className="font-mono text-foreground">{commit.short}</code> {commit.subject}
    </span>
  )
}

function StepIcon({ status }: { status: DeployStep['status'] }) {
  const size = 14
  if (status === 'running') return <Loader2 size={size} aria-hidden className="shrink-0 animate-spin text-primary" />
  if (status === 'done') return <Check size={size} aria-hidden className="shrink-0" style={{ color: resultColor.success }} />
  if (status === 'failed') return <X size={size} aria-hidden className="shrink-0 text-destructive" />
  if (status === 'skipped') return <Ban size={size} aria-hidden className="shrink-0 text-muted-foreground" />
  return <CircleDashed size={size} aria-hidden className="shrink-0 text-muted-foreground" />
}

const stepStatusLabel: Record<DeployStep['status'], string> = {
  pending: 'à venir',
  running: 'en cours',
  done: 'faite',
  failed: 'en échec',
  skipped: 'non tentée',
}

/** The steps of one operation; the running and failed ones show the end of their output. */
function Steps({ record }: { record: DeployRecord }) {
  const [shown, setShown] = useState<Record<string, boolean>>({})
  return (
    <ol aria-label="Étapes" className="flex flex-col gap-1">
      {(record.steps ?? []).map(step => {
        const auto = step.status === 'running' || step.status === 'failed'
        const open = shown[step.name] ?? auto
        const hasOutput = Boolean(step.output)
        return (
          <li key={step.name} data-step={step.name} data-status={step.status} className="min-w-0">
            <button
              type="button"
              disabled={!hasOutput}
              aria-expanded={hasOutput ? open : undefined}
              onClick={() => setShown(current => ({ ...current, [step.name]: !open }))}
              className="ui-focus-ring flex min-h-8 w-full min-w-0 items-center gap-2 rounded text-left"
            >
              <StepIcon status={step.status} />
              <span className="min-w-0 flex-1 break-words text-foreground">{stepLabel[step.name] ?? step.name}</span>
              <span className="shrink-0 whitespace-nowrap text-2xs text-muted-foreground">
                {stepStatusLabel[step.status]}
                {step.status === 'done' || step.status === 'failed' ? ` · ${duration(step.duration_ms)}` : ''}
              </span>
            </button>
            {step.error && <p className="break-words pl-6 text-2xs text-destructive">{step.error}</p>}
            {open && hasOutput && (
              <pre
                aria-label={`Sortie de l’étape ${stepLabel[step.name] ?? step.name}`}
                tabIndex={0}
                className="ui-focus-ring ml-6 mt-1 max-h-56 overflow-auto whitespace-pre-wrap break-all rounded-md border border-border bg-background p-2 font-mono text-2xs text-muted-foreground"
              >
                {step.truncated ? '[… début tronqué]\n' : ''}
                {step.output}
              </pre>
            )}
          </li>
        )
      })}
    </ol>
  )
}

function ResultBadge({ record }: { record: DeployRecord }) {
  return (
    <span data-result={record.result} className="whitespace-nowrap font-medium" style={{ color: resultColor[record.result] }}>
      {resultLabel[record.result]}
    </span>
  )
}

/** One past operation; its steps and output are fetched when it is opened. */
function HistoryRow({
  stack,
  record,
  open,
  onToggle,
  action,
}: {
  stack: string
  record: DeployRecord
  open: boolean
  onToggle: () => void
  action?: ReactNode
}) {
  const [detail, setDetail] = useState<DeployRecord | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  useEffect(() => {
    if (!open) return
    const controller = new AbortController()
    fetchDeployRecord(stack, record.id, controller.signal)
      .then(next => {
        setDetail(next)
        setFailure(null)
      })
      .catch(error => {
        if (!controller.signal.aborted) setFailure(message(error))
      })
    return () => controller.abort()
  }, [open, stack, record.id, record.result])
  const moved = record.commit_before && record.commit_after && record.commit_before.sha !== record.commit_after.sha
  const images = (record.images ?? []).filter(image => image.status)
  return (
    <li data-record={record.id} className="flex min-w-0 flex-col gap-1 py-2">
      <button
        type="button"
        aria-expanded={open}
        onClick={onToggle}
        className="ui-focus-ring flex min-h-9 w-full min-w-0 items-start gap-1.5 rounded text-left"
      >
        <span className="pt-0.5">{open ? <ChevronDown size={14} aria-hidden /> : <ChevronRight size={14} aria-hidden />}</span>
        <span className="min-w-0 flex-1">
          <span className="flex flex-wrap items-baseline gap-x-2">
            <span className="font-medium text-foreground">{record.kind === 'rollback' ? 'Retour arrière' : 'Déploiement'}</span>
            <ResultBadge record={record} />
            <span className="text-2xs text-muted-foreground">
              {when(record.started_at)} · {record.actor}
            </span>
          </span>
          <span className="block break-words text-2xs text-muted-foreground">
            {record.kind === 'rollback' ? (
              'Images précédentes remises en service ; code inchangé'
            ) : moved ? (
              <>
                <code className="font-mono">{record.commit_before?.short}</code> →{' '}
                <code className="font-mono">{record.commit_after?.short}</code> {record.commit_after?.subject}
              </>
            ) : record.commit_before ? (
              <>
                <code className="font-mono">{record.commit_before.short}</code> (aucun nouveau commit)
              </>
            ) : (
              'Arrêté avant de lire le dépôt'
            )}
          </span>
        </span>
      </button>
      {record.error && <p className="break-words pl-5 text-2xs text-destructive">{record.error}</p>}
      {action && <div className="pl-5">{action}</div>}
      {open && (
        <div className="pl-5">
          {failure && <InlineError message={failure} />}
          {!detail && !failure && <p className="text-2xs text-muted-foreground">Chargement…</p>}
          {detail && <Steps record={detail} />}
          {images.length > 0 && (
            <ul aria-label="Images des services" className="mt-1 flex flex-col gap-0.5 text-2xs text-muted-foreground">
              {images.map(image => (
                <li key={image.container} data-image-status={image.status} className="break-all">
                  <span className="font-mono text-foreground">{image.service || image.container}</span> : {image.image} —{' '}
                  {imageStatusLabel[image.status!]}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </li>
  )
}

type Pending = { kind: 'deploy' } | { kind: 'rollback'; record: DeployRecord }

/** The "Déploiement" block of an application card. */
export default function DeployBlock({ stack, onChanged }: { stack: string; onChanged: () => void }) {
  const [state, setState] = useState<DeployState | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [confirm, setConfirm] = useState<Pending | null>(null)
  const [sending, setSending] = useState(false)
  const [historyOpen, setHistoryOpen] = useState(false)
  const [openRecord, setOpenRecord] = useState<string | null>(null)
  const cancelRef = useRef<HTMLButtonElement>(null)
  const wasRunning = useRef(false)
  const onChangedRef = useRef(onChanged)
  onChangedRef.current = onChanged

  const running = Boolean(state?.current)
  const load = useCallback(
    async (signal?: AbortSignal) => {
      try {
        const next = await fetchDeployState(stack, signal)
        if (signal?.aborted) return
        setState(next)
        setFailure(null)
        if (wasRunning.current && !next.current) {
          // The operation just ended: show its record and refresh the containers.
          setHistoryOpen(true)
          setOpenRecord(next.history?.[0]?.id ?? null)
          onChangedRef.current()
        }
        wasRunning.current = Boolean(next.current)
      } catch (error) {
        if (!signal?.aborted) setFailure(message(error))
      }
    },
    [stack],
  )

  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    const timer = setInterval(
      () => {
        if (document.visibilityState !== 'hidden') void load(controller.signal)
      },
      running ? RUNNING_MS : IDLE_MS,
    )
    return () => {
      clearInterval(timer)
      controller.abort()
    }
  }, [load, running])

  const act = async (run: () => Promise<unknown>) => {
    setSending(true)
    setActionError(null)
    try {
      await run()
    } catch (error) {
      setActionError(message(error))
    }
    setSending(false)
    await load()
  }

  if (!state) {
    return failure ? (
      <InlineError message={`Impossible de lire l’état du déploiement : ${failure}`} />
    ) : (
      <p className="text-2xs text-muted-foreground">Lecture de l’état git…</p>
    )
  }

  const { git } = state
  const history = state.history ?? []
  const services = state.services ?? []
  const dirty = git.dirty ?? []
  const canDeploy = state.enabled && !state.blocked && git.repo && !git.error && !running && !sending
  const rollbackRecord = history.find(record => record.id === state.rollback_id)
  const past = history.filter(record => record.id !== state.current?.id)

  return (
    <div data-deploy={stack} className="flex min-w-0 flex-col gap-2 rounded-md border border-border/60 p-2">
      {failure && (
        <p role="status" className="text-2xs text-warning">
          Dernière actualisation en échec ({failure}).
        </p>
      )}
      {git.repo ? (
        <div className="flex min-w-0 flex-col gap-0.5">
          <p className="flex min-w-0 flex-wrap items-center gap-x-1.5">
            <GitBranch size={14} aria-hidden className="shrink-0 text-muted-foreground" />
            <span className="font-medium text-foreground">{git.detached ? 'HEAD détachée' : git.branch}</span>
            {git.upstream && <span className="break-all text-2xs text-muted-foreground">suit {git.upstream}</span>}
          </p>
          <p className="min-w-0 text-muted-foreground">
            <CommitLabel commit={git.commit} />
            {git.commit && <span className="whitespace-nowrap text-2xs"> · {when(git.commit.date)}</span>}
          </p>
          <p data-tracking className="text-2xs text-muted-foreground">
            {!git.upstream
              ? 'Cette branche ne suit aucune branche distante : rien à déployer.'
              : !git.tracking
                ? 'Écart avec la branche distante inconnu.'
                : git.behind === 0 && git.ahead === 0
                  ? 'À jour avec la branche distante, d’après le dernier fetch.'
                  : [
                      git.behind > 0 ? plural(git.behind, 'commit de retard', 'commits de retard') : '',
                      git.ahead > 0 ? plural(git.ahead, 'commit local non poussé', 'commits locaux non poussés') : '',
                    ]
                      .filter(Boolean)
                      .join(', ') + ', d’après le dernier fetch.'}
          </p>
          {git.dirty_count > 0 && (
            <p className="break-words text-2xs text-warning">
              {plural(git.dirty_count, 'fichier suivi modifié', 'fichiers suivis modifiés')} sans commit, le déploiement sera
              refusé : {dirty.join(', ')}
              {git.dirty_count > dirty.length ? '…' : ''}
            </p>
          )}
          {git.error && <p className="break-words text-2xs text-warning">{git.error}</p>}
        </div>
      ) : (
        <p className="break-words text-2xs text-muted-foreground">
          Déploiement depuis git indisponible : {git.error || 'ce dossier n’est pas dans un dépôt git'}.
        </p>
      )}

      {state.blocked && <p className="break-words text-2xs text-warning">Déploiement impossible d’ici : {state.blocked}.</p>}
      {!state.enabled && (
        <p data-disabled className="break-words text-2xs leading-relaxed text-muted-foreground">
          Lecture seule : {state.disabled_reason || 'déploiement désactivé'}. Pour l’activer, ajoute{' '}
          <code className="font-mono">TMUXATLAS_DEPLOY=on</code> au fichier <code className="font-mono">~/.config/tmuxatlas/.env</code>{' '}
          du serveur puis redémarre Atlas.
        </p>
      )}

      <div className="flex flex-wrap items-center gap-1.5">
        <Button
          size="sm"
          variant="primary"
          leadingIcon={<Rocket size={14} aria-hidden />}
          disabled={!canDeploy}
          onClick={() => setConfirm({ kind: 'deploy' })}
          aria-label={`Déployer l’application ${stack}`}
        >
          Déployer
        </Button>
        {running && (
          <Button
            size="sm"
            disabled={sending || state.current?.cancel_requested}
            onClick={() => void act(() => cancelDeploy(stack))}
            aria-label={`Annuler l’opération en cours sur ${stack}`}
          >
            {state.current?.cancel_requested ? 'Annulation…' : 'Annuler'}
          </Button>
        )}
      </div>
      {actionError && <InlineError message={actionError} onDismiss={() => setActionError(null)} />}

      {state.current && (
        <section aria-label="Opération en cours" className="flex min-w-0 flex-col gap-1">
          <p role="status" className="text-2xs text-muted-foreground">
            {state.current.kind === 'rollback' ? 'Retour arrière' : 'Déploiement'} en cours, lancé par {state.current.actor} à{' '}
            {when(state.current.started_at)}. Tu peux fermer cette page : il continue sur le serveur.
          </p>
          <Steps record={state.current} />
        </section>
      )}

      {past.length > 0 && (
        <>
          <button
            type="button"
            aria-expanded={historyOpen}
            onClick={() => setHistoryOpen(open => !open)}
            className="ui-focus-ring -mx-1 flex min-h-9 items-center gap-1.5 self-start rounded px-1 text-2xs text-muted-foreground hover:text-foreground"
          >
            {historyOpen ? <ChevronDown size={14} aria-hidden /> : <ChevronRight size={14} aria-hidden />}
            Historique ({past.length}) · dernier : {resultLabel[past[0].result]}
          </button>
          {historyOpen && (
            <ul aria-label="Historique des déploiements" className="flex flex-col divide-y divide-border/60 border-t border-border/60">
              {past.map(record => (
                <HistoryRow
                  key={record.id}
                  stack={stack}
                  record={record}
                  open={openRecord === record.id}
                  onToggle={() => setOpenRecord(current => (current === record.id ? null : record.id))}
                  action={
                    record.id === state.rollback_id && state.enabled && !running ? (
                      <Button
                        size="sm"
                        leadingIcon={<Undo2 size={14} aria-hidden />}
                        disabled={sending}
                        onClick={() => setConfirm({ kind: 'rollback', record })}
                      >
                        Revenir à la version d’avant
                      </Button>
                    ) : record.id === state.rollback_blocked_id ? (
                      <div className="flex flex-col gap-1">
                        <Button size="sm" leadingIcon={<Undo2 size={14} aria-hidden />} disabled aria-describedby={`${record.id}-blocked`}>
                          Revenir à la version d’avant
                        </Button>
                        <p id={`${record.id}-blocked`} className="break-words text-2xs text-muted-foreground">
                          Retour arrière indisponible : {state.rollback_blocked}.
                        </p>
                      </div>
                    ) : undefined
                  }
                />
              ))}
            </ul>
          )}
        </>
      )}

      <Dialog
        open={Boolean(confirm)}
        onOpenChange={open => {
          if (!open) setConfirm(null)
        }}
        initialFocusRef={cancelRef}
        role="alertdialog"
        title={confirm?.kind === 'rollback' ? `Revenir à la version d’avant pour « ${stack} » ?` : `Déployer « ${stack} » ?`}
        description={
          confirm?.kind === 'rollback'
            ? 'Atlas remet en service les images que ce déploiement a reconstruites, telles qu’elles étaient avant. Les images inchangées, le code sur le disque et la base de données ne sont pas touchés.'
            : 'Atlas met le code à jour depuis git, reconstruit les images et relance les services. L’application est coupée le temps que ses conteneurs soient recréés.'
        }
        footer={
          <>
            <Button ref={cancelRef} variant="ghost" onClick={() => setConfirm(null)}>
              Annuler
            </Button>
            <Button
              variant={confirm?.kind === 'rollback' ? 'destructive' : 'primary'}
              onClick={() => {
                const request = confirm
                setConfirm(null)
                if (!request) return
                void act(() => (request.kind === 'rollback' ? startRollback(stack, request.record.id) : startDeploy(stack)))
              }}
            >
              {confirm?.kind === 'rollback' ? 'Revenir en arrière' : 'Déployer'}
            </Button>
          </>
        }
      >
        {confirm?.kind === 'rollback' ? (
          <div className="flex flex-col gap-2 text-xs">
            <ul aria-label="Images remises en service" className="max-h-40 list-disc overflow-y-auto pl-5">
              {(rollbackRecord?.images ?? confirm.record.images ?? []).filter(image => image.status === 'rebuilt' && image.rollback_tag).map(image => (
                <li key={image.container} className="break-all">
                  <span className="font-mono">{image.service || image.container}</span> : {image.image}{' '}
                  <span className="text-muted-foreground">({image.image_id.replace('sha256:', '').slice(0, 12)})</span>
                </li>
              ))}
            </ul>
            <p className="text-muted-foreground">
              Limites : les migrations de base de données déjà appliquées ne sont pas annulées, et le code sur le disque
              reste au commit{' '}
              <code className="font-mono">{confirm.record.commit_after?.short ?? 'actuel'}</code>, en avance sur les images
              relancées. Un prochain « Déployer » reconstruira ce commit.
            </p>
          </div>
        ) : (
          <div className="flex flex-col gap-2 text-xs">
            <p className="text-muted-foreground">
              {git.tracking && git.behind > 0
                ? `${plural(git.behind, 'commit', 'commits')} à récupérer sur ${git.upstream}, d’après le dernier fetch.`
                : 'Aucun nouveau commit connu : les images seront reconstruites depuis le commit actuel, sauf si le fetch en trouve.'}
            </p>
            <ol aria-label="Étapes du déploiement" className="list-decimal pl-5">
              {deploySteps.map(name => (
                <li key={name}>{stepLabel[name]}</li>
              ))}
            </ol>
            <p className="break-words">
              {plural(services.length, 'service concerné', 'services concernés')} :{' '}
              <span className="font-mono">{services.join(', ') || '—'}</span>
            </p>
            <p className="text-muted-foreground">
              Arrêt à la première erreur. Jamais de reset, de changement de branche ni de push. En cas de problème après
              la relance, l’historique propose de revenir aux images précédentes.
            </p>
          </div>
        )}
      </Dialog>
    </div>
  )
}
