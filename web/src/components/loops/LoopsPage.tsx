import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Bot,
  CheckCircle2,
  Clock,
  FolderGit2,
  GitBranch,
  Infinity as InfinityIcon,
  MessageSquare,
  Pause,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  RotateCcw,
  Square,
  Terminal as TerminalIcon,
  Trash2,
  TriangleAlert,
} from 'lucide-react'
import type { Session } from '../../hooks/useSessions'
import { sessionKey } from '../../hooks/useSessions'
import { getBrandStorage, setBrandStorage } from '../../lib/brandStorage'
import { Button, Dialog, EmptyState } from '../ui'
import { ProjectSelector } from './ProjectSelector'
import { LoopPlan } from './LoopPlan'
import { SubscriptionPool } from './SubscriptionPool'
import {
  cliMeta,
  cliOrder,
  deriveAvailableSubscriptions,
  formatClockTime,
  formatCountdown,
  formatDuration,
  formatRelative,
  statusMeta,
  toWorkerInputs,
} from './loopHelpers'
import {
  createLoop,
  deleteLoop,
  fetchLoopLogs,
  reviewLoop,
  queueInstruction,
  fetchLoops,
  fetchLoopsMeta,
  openCliChat,
  pauseLoop,
  startLoop,
  stopLoop,
  updateLoop,
  workerAction,
  type CLIAvailability,
  type LoopCLI,
  type LoopItem,
  type LoopLogResponse,
  type LoopProfile,
  type LoopWorker,
  type LoopsMeta,
  type WorkerActionType,
} from './loopsApi'

interface LoopsPageProps {
  currentUsername?: string | null
  sessions: Session[]
  onOpenSession: (key: string) => void
  onRefreshSessions?: () => Promise<void> | void
}

function uniquePeople(profiles: LoopProfile[] | undefined): string[] {
  if (!profiles) return []
  const seen = new Set<string>()
  const out: string[] = []
  for (const profile of profiles) {
    const person = profile.person?.trim()
    if (person && !seen.has(person.toLowerCase())) {
      seen.add(person.toLowerCase())
      out.push(person)
    }
  }
  return out
}

function defaultPersonFor(profiles: LoopProfile[] | undefined, saved?: string): string {
  const people = uniquePeople(profiles)
  if (saved && people.some(p => p.toLowerCase() === saved.toLowerCase())) {
    return people.find(p => p.toLowerCase() === saved.toLowerCase()) || ''
  }
  return people[0] || ''
}

export function LoopsPage({ sessions, onOpenSession, onRefreshSessions, currentUsername }: LoopsPageProps) {
  const [meta, setMeta] = useState<LoopsMeta | null>(null)
  const [loops, setLoops] = useState<LoopItem[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [actionBusy, setActionBusy] = useState<string | null>(null)
  const [nowMs, setNowMs] = useState(() => Date.now())

  // Shared active project across Loops & Quick Chats
  const [selectedProject, setSelectedProject] = useState(() => getBrandStorage('loops-chat-project') || '')
  const [filterByProject, setFilterByProject] = useState(false)

  // Quick chat controls
  const [chatPrompt, setChatPrompt] = useState('')
  const [chatModel, setChatModel] = useState('')
  const [chatAdvanced, setChatAdvanced] = useState(false)
  const [chatPersons, setChatPersons] = useState<Record<LoopCLI, string>>(() => ({
    claude: getBrandStorage('loops-person-claude') || '',
    codex: getBrandStorage('loops-person-codex') || '',
    agy: getBrandStorage('loops-person-agy') || '',
  }))

  // Selected loop + log inspector
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [selectedIter, setSelectedIter] = useState<number>(0)
  const [logData, setLogData] = useState<LoopLogResponse | null>(null)

  // Create / Edit loop modal
  const [modalOpen, setModalOpen] = useState(false)
  const [editingLoop, setEditingLoop] = useState<LoopItem | null>(null)
  const [verifyCommand, setVerifyCommand] = useState('')
  const [timeLimit, setTimeLimit] = useState(120)
  const [lastVisit] = useState(() => getBrandStorage(`loops-last-visit:${currentUsername || 'local'}`) || '')
  useEffect(() => { setBrandStorage(`loops-last-visit:${currentUsername || 'local'}`,  new Date().toISOString()) }, [])
  const [guideStep, setGuideStep] = useState(0)
  const [criteria, setCriteria] = useState('')
  const [scope, setScope] = useState('')
  const [instruction, setInstruction] = useState('')
  const [detailTab, setDetailTab] = useState('Résumé')
  const [formName, setFormName] = useState('')
  const [formProject, setFormProject] = useState('')
  const [formWorkers, setFormWorkers] = useState<LoopWorker[]>([])
  const [formModel, setFormModel] = useState('')
  const [formPrompt, setFormPrompt] = useState('')
  const [formContinuous, setFormContinuous] = useState(true)
  const [formMaxIter, setFormMaxIter] = useState(10)
  const [formDelay, setFormDelay] = useState(5)
  const [formAutoApprove, setFormAutoApprove] = useState(true)
  const [formUseWorktree, setFormUseWorktree] = useState(false)
  const [formStopWhenDone, setFormStopWhenDone] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  const [savingLoop, setSavingLoop] = useState(false)

  const promptInputRef = useRef<HTMLTextAreaElement>(null)

  const loadAll = useCallback(async (signal?: AbortSignal) => {
    try {
      const [metaRes, loopsRes] = await Promise.all([fetchLoopsMeta(signal), fetchLoops(signal)])
      if (signal?.aborted) return
      setMeta(metaRes)
      setLoops(loopsRes)
      setError(null)
      setSelectedProject(current => {
        if (current) return current
        const first = metaRes.projects[0]?.path || ''
        if (first) setBrandStorage('loops-chat-project', first)
        return first
      })
      setChatPersons(prev => ({
        claude: defaultPersonFor(metaRes.accounts.claude, prev.claude),
        codex: defaultPersonFor(metaRes.accounts.codex, prev.codex),
        agy: defaultPersonFor(metaRes.accounts.agy, prev.agy),
      }))
      setSelectedId(prev => {
        if (prev && loopsRes.some(l => l.id === prev)) return prev
        return loopsRes[0]?.id ?? null
      })
    } catch (err) {
      if (signal?.aborted) return
      setError(err instanceof Error ? err.message : 'Impossible de charger les boucles.')
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    void loadAll(controller.signal)
    return () => controller.abort()
  }, [loadAll])

  const hasActiveLoop = loops.some(l => l.status === 'running' || l.status === 'waiting_reset')
  const hasWaitingTimer = loops.some(
    l => l.status === 'waiting_reset' || l.workers?.some(w => w.status === 'rate_limited'),
  )

  // Poll loop list while any loop is running or waiting for reset
  useEffect(() => {
    const interval = hasActiveLoop ? 2000 : 10000
    const timer = window.setInterval(() => {
      void fetchLoops()
        .then(next => setLoops(next))
        .catch(() => {})
    }, interval)
    return () => window.clearInterval(timer)
  }, [hasActiveLoop])

  // Live 1-second tick for 5-hour reset countdowns
  useEffect(() => {
    if (!hasWaitingTimer) return
    setNowMs(Date.now())
    const timer = window.setInterval(() => setNowMs(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [hasWaitingTimer])

  const availableSubscriptions = useMemo(() => deriveAvailableSubscriptions(meta), [meta])

  const availabilityByCLI = useMemo(() => {
    const map: Partial<Record<LoopCLI, CLIAvailability>> = {}
    for (const item of meta?.clis ?? []) map[item.cli] = item
    return map
  }, [meta])

  const displayedLoops = useMemo(() => {
    const rank = (l: LoopItem) => l.outcome === 'blocked' || l.status === 'error' ? 0 : l.outcome === 'review' ? 1 : l.status === 'running' ? 2 : 3
    const visible = filterByProject && selectedProject ? loops.filter(l => l.project_dir === selectedProject) : [...loops]
    return visible.sort((a, b) => rank(a) - rank(b))
  }, [filterByProject, loops, selectedProject])

  const selectedLoop = useMemo(
    () => displayedLoops.find(l => l.id === selectedId) ?? displayedLoops[0] ?? null,
    [displayedLoops, loops, selectedId],
  )

  useEffect(() => { setSelectedIter(0); setLogData(null) }, [selectedLoop?.id])

  // Sync selected iteration to latest when switching loops or when new iterations appear
  useEffect(() => {
    if (!selectedLoop) {
      setSelectedIter(0)
      setLogData(null)
      return
    }
    const latest = selectedLoop.iterations[selectedLoop.iterations.length - 1]?.number ?? 0
    setSelectedIter(prev => {
      if (prev > 0 && selectedLoop.iterations.some(i => i.number === prev)) return prev
      return latest
    })
  }, [selectedLoop?.id, selectedLoop?.iterations.length])

  // Load logs for the selected loop + iteration
  useEffect(() => {
    if (!selectedLoop) return
    const controller = new AbortController()
    const loadLog = () => {
      void fetchLoopLogs(selectedLoop.id, selectedIter || undefined, controller.signal)
        .then(data => {
          if (!controller.signal.aborted) setLogData(data)
        })
        .catch(() => {})
    }
    loadLog()
    if (selectedLoop.status === 'running') {
      const timer = window.setInterval(loadLog, 2000)
      return () => {
        controller.abort()
        window.clearInterval(timer)
      }
    }
    return () => controller.abort()
  }, [selectedLoop?.id, selectedLoop?.status, selectedIter])

  // Detect active tmux sessions per CLI on the local host for quick jump
  const activeSessionsByCLI = useMemo(() => {
    const map: Record<LoopCLI, Session[]> = { claude: [], codex: [], agy: [] }
    const localHost = meta?.host_id
    for (const session of sessions) {
      if (localHost && session.host !== localHost) continue
      const commands = session.windows.flatMap(w => w.panes.map(p => p.current_command.toLowerCase()))
      const nameLower = session.name.toLowerCase()
      for (const cli of cliOrder) {
        if (commands.includes(cli) || nameLower.endsWith(`-${cli}`) || nameLower.includes(`-${cli}-`)) {
          map[cli].push(session)
        }
      }
    }
    return map
  }, [meta?.host_id, sessions])

  const handleSelectProject = (path: string) => {
    setSelectedProject(path)
    if (path.trim()) setBrandStorage('loops-chat-project', path.trim())
  }

  const handlePersonChange = (cli: LoopCLI, person: string) => {
    setChatPersons(prev => ({ ...prev, [cli]: person }))
    setBrandStorage(`loops-person-${cli}`, person)
  }

  const handleOpenChat = async (cli: LoopCLI, cwdOverride?: string, personOverride?: string) => {
    const cwd = (cwdOverride || selectedProject).trim()
    if (!cwd) {
      setError('Choisissez un dossier projet avant d’ouvrir un chat.')
      return
    }
    const person = personOverride !== undefined ? personOverride : chatPersons[cli]
    setActionBusy(`chat-${cli}`)
    setError(null)
    try {
      const res = await openCliChat({
        cli,
        cwd,
        ...(person && { person }),
        ...(chatModel.trim() && !cwdOverride && { model: chatModel.trim() }),
        ...(cwdOverride && selectedLoop ? { prompt: `Discussion de la boucle ${selectedLoop.name}\nObjectif : ${selectedLoop.prompt}\nCritères : ${selectedLoop.criteria || ''}\nMémoire : ${selectedLoop.memory || selectedLoop.last_summary || ''}\nCette discussion ne modifie pas les consignes du moteur.` } : chatPrompt.trim() ? { prompt: chatPrompt.trim() } : {}),
      })
      if (!cwdOverride) {
        setBrandStorage('loops-chat-project', cwd)
      }
      await onRefreshSessions?.()
      onOpenSession(res.key)
    } catch (err) {
      setError(err instanceof Error ? err.message : `Impossible d’ouvrir le chat ${cli}.`)
    } finally {
      setActionBusy(null)
    }
  }

  const openCreateModal = (projectOverride?: string) => {
    const proj = projectOverride || selectedProject || meta?.projects[0]?.path || ''
    // Pre-enable every available subscription so the user can launch all of them in 1 click
    const initialWorkers = availableSubscriptions.map(sub => {
      const avail = availabilityByCLI[sub.cli]
      const cliOk = avail ? avail.available : true
      return {
        ...sub,
        enabled: cliOk,
        paused: false,
        status: cliOk ? ('ready' as const) : ('disabled' as const),
      }
    })
    setVerifyCommand('')
    setTimeLimit(120)
    setCriteria('')
    setScope('')
    setGuideStep(0)
    setEditingLoop(null)
    setFormName('')
    setFormProject(proj)
    setFormWorkers(initialWorkers)
    setFormModel('')
    setFormPrompt(meta?.presets[0]?.prompt || '')
    setFormContinuous(false)
    setFormMaxIter(10)
    setFormDelay(5)
    setFormAutoApprove(true)
    setFormUseWorktree(true)
    setFormStopWhenDone(true)
    setFormError(null)
    setModalOpen(true)
  }

  const openEditModal = (loop: LoopItem) => {
    const baseWorkers =
      loop.workers && loop.workers.length > 0
        ? loop.workers
        : [
            {
              id: loop.person ? `${loop.cli}:${loop.person}` : loop.cli,
              cli: loop.cli,
              person: loop.person,
              enabled: true,
              paused: false,
              status: 'ready' as const,
              run_count: 0,
            },
          ]
    setVerifyCommand(loop.verify_command || '')
    setTimeLimit(loop.time_limit_minutes || 0)
    setCriteria(loop.criteria || '')
    setScope(loop.scope || '')
    setGuideStep(0)
    setEditingLoop(loop)
    setFormName(loop.name)
    setFormProject(loop.project_dir)
    setFormWorkers(baseWorkers)
    setFormModel(loop.model || '')
    setFormPrompt(loop.prompt)
    setFormContinuous(loop.max_iterations === 0)
    setFormMaxIter(loop.max_iterations > 0 ? loop.max_iterations : 10)
    setFormDelay(loop.delay_seconds)
    setFormAutoApprove(loop.auto_approve)
    setFormUseWorktree(loop.use_worktree)
    setFormStopWhenDone(loop.stop_when_done)
    setFormError(null)
    setModalOpen(true)
  }

  const handleSaveLoop = async (startNow: boolean) => {
    if (!formProject.trim()) {
      setFormError('Sélectionnez un projet.')
      return
    }
    const enabledWorkers = formWorkers.filter(w => w.enabled)
    if (enabledWorkers.length === 0) {
      setFormError('Activez au moins un abonnement pour travailler sur ce projet.')
      return
    }
    if (!formPrompt.trim()) {
      setFormError('Décrivez l’objectif de la boucle.')
      return
    }
    const primary = enabledWorkers[0]
    setSavingLoop(true)
    setFormError(null)
    try {
      let saved: LoopItem
      if (editingLoop) {
        saved = await updateLoop({
          id: editingLoop.id,
          name: formName.trim(),
          cli: primary.cli,
          person: primary.person,
          model: formModel.trim(),
          workers: toWorkerInputs(formWorkers),
          prompt: formPrompt.trim(),
          mode: formContinuous ? 'maintenance' : 'mission', scope, criteria, verify_command: verifyCommand, time_limit_minutes: timeLimit,
          max_iterations: formContinuous ? 0 : formMaxIter,
          continuous: formContinuous,
          delay_seconds: formDelay,
          auto_approve: formAutoApprove,
          stop_when_done: formStopWhenDone,
        })
        if (startNow) {
          saved = await startLoop(saved.id)
        }
      } else {
        saved = await createLoop({
          name: formName.trim(),
          project_dir: formProject.trim(),
          cli: primary.cli,
          person: primary.person,
          model: formModel.trim(),
          workers: toWorkerInputs(formWorkers),
          prompt: formPrompt.trim(),
          mode: formContinuous ? 'maintenance' : 'mission', scope, criteria, verify_command: verifyCommand, time_limit_minutes: timeLimit,
          max_iterations: formContinuous ? 0 : formMaxIter,
          continuous: formContinuous,
          delay_seconds: formDelay,
          auto_approve: formAutoApprove,
          use_worktree: formUseWorktree,
          stop_when_done: formStopWhenDone,
          start_now: startNow,
        })
        handleSelectProject(formProject.trim())
      }
      setLoops(prev => {
        const filtered = prev.filter(item => item.id !== saved.id)
        return [saved, ...filtered]
      })
      setSelectedId(saved.id)
      setModalOpen(false)
    } catch (err) {
      setFormError(err instanceof Error ? err.message : 'Erreur lors de l’enregistrement.')
    } finally {
      setSavingLoop(false)
    }
  }

  const runLoopAction = async (id: string, action: 'start' | 'pause' | 'stop' | 'delete') => {
    setActionBusy(`${action}-${id}`)
    setError(null)
    try {
      if (action === 'delete') {
        await deleteLoop(id)
        setLoops(prev => {
          const next = prev.filter(item => item.id !== id)
          if (selectedId === id) setSelectedId(next[0]?.id ?? null)
          return next
        })
        return
      }
      const updated =
        action === 'start' ? await startLoop(id) : action === 'pause' ? await pauseLoop(id) : await stopLoop(id)
      setLoops(prev => prev.map(item => (item.id === updated.id ? updated : item)))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Action impossible.')
    } finally {
      setActionBusy(null)
    }
  }

  const runWorkerAction = async (
    loopId: string,
    payload: {
      worker_id?: string
      action: WorkerActionType
      cli?: LoopCLI
      person?: string
      label?: string
    },
  ) => {
    const busyKey = payload.worker_id ? `${payload.action}-${payload.worker_id}` : payload.action
    setActionBusy(busyKey)
    setError(null)
    try {
      const updated = await workerAction({ loop_id: loopId, ...payload })
      setLoops(prev => prev.map(item => (item.id === updated.id ? updated : item)))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Impossible de modifier cet abonnement.')
    } finally {
      setActionBusy(null)
    }
  }

  const selectedProjectInfo = meta?.projects.find(p => p.path === formProject)
  const selectedLoopWorkers: LoopWorker[] = useMemo(() => {
    if (!selectedLoop) return []
    if (selectedLoop.workers && selectedLoop.workers.length > 0) return selectedLoop.workers
    return [
      {
        id: selectedLoop.person ? `${selectedLoop.cli}:${selectedLoop.person}` : selectedLoop.cli,
        cli: selectedLoop.cli,
        person: selectedLoop.person,
        enabled: true,
        paused: false,
        status: selectedLoop.status === 'running' ? 'running' : 'ready',
        run_count: selectedLoop.current_iteration,
      },
    ]
  }, [selectedLoop])

  const selectedLoopIsActive =
    selectedLoop?.status === 'running' || selectedLoop?.status === 'waiting_reset'

  return (
    <main className="flex-1 overflow-y-auto p-4 text-sm sm:p-6">
      <div className="mx-auto flex max-w-7xl flex-col gap-5">
        {/* Header */}
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h1 className="text-lg font-semibold text-foreground">Boucles</h1>
            <p className="mt-0.5 text-xs text-muted-foreground">
              Des agents qui avancent sur tes objectifs.
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Button
              variant="secondary"
              size="sm"
              onClick={() => void loadAll()}
              leadingIcon={<RefreshCw size={14} />}
              disabled={loading}
            >
              Actualiser
            </Button>
            <Button variant="primary" size="sm" onClick={() => openCreateModal()} leadingIcon={<Plus size={14} />}>
              Nouvelle boucle
            </Button>
          </div>
        </div>

        {error && (
          <div
            role="alert"
            className="flex items-center gap-2 rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive"
          >
            <TriangleAlert size={15} className="shrink-0" />
            <span className="flex-1">{error}</span>
            <button type="button" className="underline" onClick={() => setError(null)}>
              Fermer
            </button>
          </div>
        )}

        <section className="rounded-xl border border-border bg-card p-4">
          <h2 className="font-medium">{lastVisit ? 'Depuis ta dernière visite' : 'Activité récente'}</h2>
          {loops.filter(l => !lastVisit || l.updated_at > lastVisit).slice(0, 8).map(l => <button key={l.id} className="mt-2 block text-left text-xs" onClick={() => { handleSelectProject(l.project_dir); setSelectedId(l.id) }}>{l.name} · {l.last_summary || statusMeta[l.status]?.label}{l.outcome === 'review' ? ' · À examiner' : ''}</button>)}
          {lastVisit && !loops.some(l => l.updated_at > lastVisit) && <p className="mt-2 text-xs text-muted-foreground">Aucune nouvelle activité.</p>}
        </section>
        {/* Section 1: Shared Intuitive Project Selector */}
        <section className="rounded-xl border border-border bg-card p-4">
          <div className="mb-2.5 flex flex-wrap items-center justify-between gap-2">
            <div className="flex items-center gap-2">
              <FolderGit2 size={16} className="text-primary" />
              <h2 className="font-medium text-foreground">Projet actif</h2>
            </div>
            <div className="flex items-center gap-2">
              <label className="flex cursor-pointer items-center gap-1.5 text-xs text-muted-foreground">
                <input
                  type="checkbox"
                  checked={filterByProject}
                  onChange={e => setFilterByProject(e.target.checked)}
                />
                <span>Filtrer les boucles sur ce projet</span>
              </label>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => openCreateModal(selectedProject)}
                leadingIcon={<Plus size={13} />}
              >
                Boucle sur ce projet
              </Button>
            </div>
          </div>

          <ProjectSelector
            projects={meta?.projects ?? []}
            value={selectedProject}
            onChange={handleSelectProject}
            loops={loops}
            compact
            inputId="loops-active-project"
          />
        </section>

        {/* Section 2: Continuous Improvement Loops */}
        <section className="grid grid-cols-1 gap-4 lg:grid-cols-[330px_1fr]">
          {/* Loops List */}
          <div className="flex flex-col gap-2 rounded-xl border border-border bg-card p-3">
            <div className="flex items-center justify-between px-1 pb-1">
              <div className="flex items-center gap-2">
                <Bot size={16} className="text-primary" />
                <h2 className="font-medium text-foreground">Boucles ({displayedLoops.length})</h2>
              </div>
              <button
                type="button"
                onClick={() => openCreateModal()}
                className="text-xs font-medium text-primary hover:underline"
              >
                + Créer
              </button>
            </div>

            {displayedLoops.length === 0 ? (
              <EmptyState
                icon={<RefreshCw size={22} />}
                title={filterByProject ? 'Aucune boucle sur ce projet' : 'Aucune boucle'}
                description="Créez une boucle continue et activez les abonnements de votre choix."
                action={
                  <Button
                    variant="primary"
                    size="sm"
                    onClick={() => openCreateModal(selectedProject)}
                    leadingIcon={<Plus size={14} />}
                  >
                    Nouvelle boucle
                  </Button>
                }
              />
            ) : (
              <div className="flex flex-col gap-2">
                {displayedLoops.map(loop => {
                  const status = statusMeta[loop.status] || statusMeta.idle
                  const selected = loop.id === selectedLoop?.id
                  const workers = loop.workers ?? []
                  const activeWorker = workers.find(w => w.id === loop.active_worker_id)
                  const enabledCount = workers.filter(w => w.enabled).length
                  const pausedCount = workers.filter(w => w.enabled && w.paused).length
                  const limitedCount = workers.filter(w => w.enabled && !w.paused && w.status === 'rate_limited').length
                  return (
                    <div
                      key={loop.id}
                      onClick={() => setSelectedId(loop.id)}
                      role="button"
                      tabIndex={0}
                      onKeyDown={e => {
                        if (e.key === 'Enter' || e.key === ' ') {
                          e.preventDefault()
                          setSelectedId(loop.id)
                        }
                      }}
                      className={`cursor-pointer rounded-lg border p-3 transition-colors ${
                        selected
                          ? 'border-primary/60 bg-primary/5'
                          : 'border-border bg-background/60 hover:border-border/80 hover:bg-secondary/40'
                      }`}
                    >
                      <div className="flex items-start justify-between gap-2">
                        <div className="min-w-0">
                          <div className="truncate font-medium text-foreground">{loop.name}</div>
                          <div className="mt-0.5 truncate font-mono text-2xs text-muted-foreground">
                            {loop.project_dir}
                          </div>
                        </div>
                        <span className={`shrink-0 rounded-full border px-2 py-0.5 text-2xs font-medium ${status.className}`}>
                          {status.label}
                        </span>
                      </div>

                      <div className="mt-2 flex flex-wrap items-center justify-between gap-1.5 text-2xs text-muted-foreground">
                        <div className="flex flex-wrap items-center gap-1.5">
                          {activeWorker ? (
                            <span className={`rounded border px-1.5 py-0.5 font-semibold ${cliMeta[activeWorker.cli].badge}`}>
                              {activeWorker.cli} · {activeWorker.person || 'défaut'}
                            </span>
                          ) : (
                            <span className={`rounded border px-1.5 py-0.5 font-semibold ${cliMeta[loop.cli].badge}`}>
                              {enabledCount > 1 ? `${enabledCount} abonnements` : loop.person ? `${loop.cli} · ${loop.person}` : loop.cli}
                            </span>
                          )}
                          {pausedCount > 0 && <span className="text-warning">{pausedCount} pause</span>}
                          {limitedCount > 0 && <span className="text-warning">{limitedCount} limité</span>}
                        </div>
                        <span className="tabular-nums">
                          {loop.max_iterations === 0
                            ? `Passe ${loop.current_iteration} · ∞`
                            : `Passe ${loop.current_iteration}/${loop.max_iterations}`}
                        </span>
                      </div>
                    </div>
                  )
                })}
              </div>
            )}
          </div>

          {/* Selected Loop Cockpit */}
          <div className="rounded-xl border border-border bg-card p-4">
            {!selectedLoop ? (
              <EmptyState
                icon={<FolderGit2 size={22} />}
                title="Sélectionnez ou créez une boucle"
                description="Activez tous vos abonnements disponibles sur un projet pour travailler en boucle continue."
                action={
                  <Button
                    variant="primary"
                    size="sm"
                    onClick={() => openCreateModal(selectedProject)}
                    leadingIcon={<Plus size={14} />}
                  >
                    Créer une boucle continue
                  </Button>
                }
              />
            ) : (
              <div className="flex flex-col gap-4">
                {/* Loop Header */}
                <div className="flex flex-wrap items-start justify-between gap-3 border-b border-border pb-3">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <h3 className="text-base font-semibold text-foreground">{selectedLoop.name}</h3>
                      <span
                        className={`rounded-full border px-2 py-0.5 text-2xs font-medium ${
                          (statusMeta[selectedLoop.status] || statusMeta.idle).className
                        }`}
                      >
                        {(statusMeta[selectedLoop.status] || statusMeta.idle).label}
                      </span>
                      {selectedLoop.max_iterations === 0 && (
                        <span className="inline-flex items-center gap-1 rounded-full border border-primary/30 bg-primary/10 px-2 py-0.5 text-2xs font-medium text-primary">
                          <InfinityIcon size={11} />
                          Continu
                        </span>
                      )}
                      {selectedLoop.pause_requested && selectedLoopIsActive && (
                        <span className="text-2xs text-warning">Pause demandée après cette passe…</span>
                      )}
                    </div>
                    <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
                      <span className="font-mono">{selectedLoop.work_dir || selectedLoop.project_dir}</span>
                      {selectedLoop.branch && (
                        <span className="inline-flex items-center gap-1 text-primary">
                          <GitBranch size={12} />
                          {selectedLoop.branch}
                        </span>
                      )}
                      <span className="tabular-nums">
                        {selectedLoop.max_iterations === 0
                          ? `Itérations : ${selectedLoop.current_iteration} (continu)`
                          : `Itération ${selectedLoop.current_iteration}/${selectedLoop.max_iterations}`}
                      </span>
                    </div>
                  </div>

                  <div className="flex flex-wrap items-center gap-2">
                    {selectedLoopIsActive ? (
                      <>
                        {selectedLoop.status === 'waiting_reset' && (
                          <Button
                            variant="primary"
                            size="sm"
                            loading={actionBusy === 'wake'}
                            onClick={() => void runWorkerAction(selectedLoop.id, { action: 'wake' })}
                            leadingIcon={<RotateCcw size={14} />}
                          >
                            Vérifier maintenant
                          </Button>
                        )}
                        <Button
                          variant="secondary"
                          size="sm"
                          disabled={selectedLoop.pause_requested || actionBusy === `pause-${selectedLoop.id}`}
                          onClick={() => void runLoopAction(selectedLoop.id, 'pause')}
                          leadingIcon={<Pause size={14} />}
                        >
                          Pause
                        </Button>
                        <Button
                          variant="destructive"
                          size="sm"
                          loading={actionBusy === `stop-${selectedLoop.id}`}
                          onClick={() => void runLoopAction(selectedLoop.id, 'stop')}
                          leadingIcon={<Square size={14} />}
                        >
                          Arrêter
                        </Button>
                      </>
                    ) : (
                      <>
                        <Button
                          variant="primary"
                          size="sm"
                          loading={actionBusy === `start-${selectedLoop.id}`}
                          onClick={() => void runLoopAction(selectedLoop.id, 'start')}
                          leadingIcon={<Play size={14} />}
                        >
                          {selectedLoop.status === 'paused' ? 'Reprendre' : 'Démarrer'}
                        </Button>
                        <Button
                          variant="secondary"
                          size="sm"
                          onClick={() => openEditModal(selectedLoop)}
                          leadingIcon={<Pencil size={14} />}
                        >
                          Modifier
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          loading={actionBusy === `delete-${selectedLoop.id}`}
                          onClick={() => void runLoopAction(selectedLoop.id, 'delete')}
                          leadingIcon={<Trash2 size={14} />}
                        >
                          Supprimer
                        </Button>
                      </>
                    )}
                  </div>
                </div>

                {/* Waiting for 5h reset banner */}
                {selectedLoop.status === 'waiting_reset' && (
                  <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-warning/45 bg-warning/10 px-3.5 py-2.5 text-xs">
                    <div className="flex items-start gap-2.5">
                      <Clock size={16} className="mt-0.5 shrink-0 text-warning" />
                      <div>
                        <div className="font-medium text-foreground">
                          Comptes temporairement indisponibles · prochaine tentative{' '}
                          {selectedLoop.next_reset_at
                            ? `à ${formatClockTime(selectedLoop.next_reset_at)} (dans ${formatCountdown(selectedLoop.next_reset_at, nowMs)})`
                            : 'après le délai de réessai'}
                        </div>
                        <div className="mt-0.5 text-2xs text-muted-foreground">
                          Activez ou réactivez un autre abonnement ci-dessous pour reprendre le travail immédiatement.
                        </div>
                      </div>
                    </div>
                    <Button
                      variant="secondary"
                      size="sm"
                      loading={actionBusy === 'wake'}
                      onClick={() => void runWorkerAction(selectedLoop.id, { action: 'wake' })}
                      leadingIcon={<RotateCcw size={13} />}
                    >
                      Réessayer
                    </Button>
                  </div>
                )}

                {/* Auto-paused banner */}
                {selectedLoop.status === 'paused' && selectedLoop.auto_paused && (
                  <div className="flex items-center gap-2 rounded-lg border border-warning/40 bg-warning/10 px-3 py-2 text-xs text-foreground">
                    <Pause size={14} className="shrink-0 text-warning" />
                    <span>
                      Boucle mise en pause car tous ses abonnements sont sur pause. Réactivez un abonnement ci-dessous pour reprendre automatiquement.
                    </span>
                  </div>
                )}

                {/* Live Interactive Subscription Pool */}
                <details><summary className="cursor-pointer text-xs">Comptes autorisés et disponibilité</summary><SubscriptionPool
                  mode="live"
                  availableSubscriptions={availableSubscriptions}
                  availabilityByCLI={availabilityByCLI}
                  workers={selectedLoopWorkers}
                  activeWorkerId={selectedLoop.active_worker_id}
                  busyAction={actionBusy}
                  nowMs={nowMs}
                  onWorkerAction={payload => void runWorkerAction(selectedLoop.id, payload)}
                /></details>

                {/* Objective & quick chat buttons inside this loop's work_dir */}
                <div className="rounded-lg border border-border bg-background/60 p-3">
                  <div className="text-2xs font-medium uppercase tracking-wide text-muted-foreground">Objectif</div>
                  <p className="mt-1 whitespace-pre-wrap text-xs text-foreground">{selectedLoop.prompt}</p>
                  <div className="mt-3 flex flex-wrap items-center gap-2 border-t border-border/70 pt-2.5">
                    <span className="text-2xs text-muted-foreground">Discuter de cette boucle :</span>
                    {cliOrder.map(cli => (
                      <Button
                        key={cli}
                        variant="secondary"
                        size="sm"
                        loading={actionBusy === `chat-${cli}`}
                        onClick={() =>
                          void handleOpenChat(
                            cli,
                            selectedLoop.work_dir || selectedLoop.project_dir,
                            cli === selectedLoop.cli && selectedLoop.person ? selectedLoop.person : chatPersons[cli],
                          )
                        }
                        leadingIcon={<TerminalIcon size={12} />}
                      >
                        {cliMeta[cli].title}
                      </Button>
                    ))}
                  </div>
                </div>

                {selectedLoop.last_error && (
                  <div className="rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
                    {selectedLoop.last_error}
                  </div>
                )}

                {selectedLoop.last_summary && (
                  <div className="flex items-start gap-2 rounded-lg border border-border bg-secondary/40 px-3 py-2 text-xs text-foreground">
                    <CheckCircle2 size={14} className="mt-0.5 shrink-0 text-success" />
                    <span>{selectedLoop.last_summary}</span>
                  </div>
                )}

                <div className="flex gap-2" role="tablist" aria-label="Détails de la boucle">
                  {['Résumé', 'Modifications', 'Historique', 'Logs'].map(tab => <button key={tab} role="tab" aria-selected={detailTab === tab} className={`rounded border px-3 py-2 ${detailTab === tab ? 'border-primary text-primary' : 'border-border'}`} onClick={() => setDetailTab(tab)}>{tab}</button>)}
                </div>
                {detailTab === 'Résumé' && <div className="space-y-3">
                  <p className="font-medium">{selectedLoop.outcome === 'achieved' ? 'Objectif validé par l’utilisateur' : selectedLoop.outcome === 'blocked' ? 'Bloquée — vérification ou décision nécessaire' : selectedLoop.outcome === 'limit' ? `Limite de ${selectedLoop.limit_reason === 'time' ? 'temps' : 'passes'} atteinte — bilan à examiner` : selectedLoop.outcome === 'review' ? 'Fin déclarée par l’agent — résultat à examiner' : selectedLoop.outcome === 'waiting_work' ? 'En attente de nouveau travail' : 'Progression de la boucle'}</p>
                  {!selectedLoopIsActive && selectedLoop.iterations.length > 0 && selectedLoop.outcome !== 'achieved' && <Button size="sm" variant="secondary" onClick={() => { void reviewLoop(selectedLoop.id).then(updated => setLoops(prev => prev.map(l => l.id === updated.id ? updated : l))).catch(err => setError(String(err))) }}>Valider le résultat après revue</Button>}
                  <p className="text-xs text-muted-foreground">{selectedLoop.phase === 'verifying' ? 'En vérification : exécution du contrôle configuré' : selectedLoop.phase === 'waiting_account' ? 'En attente d’un compte partagé disponible' : selectedLoop.status === 'running' ? 'Maintenant : passe en cours' : ''}</p>
                  <LoopPlan loop={selectedLoop} onSaved={updated => setLoops(prev => prev.map(l => l.id === updated.id ? updated : l))} onError={setError} />
                  {selectedLoop.criteria && <p>Critères : {selectedLoop.criteria}</p>}
                  {selectedLoop.iterations.slice(-1).map(iter => <div key={iter.number}>                    {iter.verification && <div className="rounded border border-border p-2"><strong>Contrôle exécuté par Atlas : {iter.verification.passed ? 'réussi' : 'échoué'}</strong><p className="font-mono">{iter.verification.command}</p><pre className="max-h-40 overflow-auto whitespace-pre-wrap text-xs">{iter.verification.output}</pre></div>}</div>)}
                  {selectedLoop.iterations.slice(-1).map(iter => iter.result ? <div key={iter.number} className="space-y-2">

                    <p><strong>Accompli :</strong> {iter.result.change}</p>
                    <p><strong>Vérifications déclarées par l’agent :</strong> {iter.result.checks || 'Non renseignées'}</p>
                    <p><strong>Limites :</strong> {iter.result.limitations || 'Non renseignées'}</p>
                    <p><strong>Suite :</strong> {iter.result.next || 'Non renseignée'}</p>
                    {iter.result.decision && <p className="text-warning">Décision demandée : {iter.result.decision}</p>}
                  </div> : <p key={iter.number}>Bilan structuré indisponible. Consultez les logs pour examiner cette passe.</p>)}
                  <label className="block">Consigne pour la prochaine passe<textarea className="mt-2 w-full rounded border border-border bg-background p-2" maxLength={8000} value={instruction} onChange={e => setInstruction(e.target.value)} /></label>
                  <Button variant="secondary" size="sm" disabled={!instruction.trim()} onClick={() => { void queueInstruction(selectedLoop.id, instruction).then(updated => { setLoops(prev => prev.map(l => l.id === updated.id ? updated : l)); setInstruction('') }).catch(err => setError(String(err))) }}>Transmettre pour la prochaine passe</Button>
                  {selectedLoop.pending_instruction && <p>Consigne en attente : {selectedLoop.pending_instruction}</p>}
                  <details><summary>Mémoire de la boucle</summary><pre className="whitespace-pre-wrap text-xs">{selectedLoop.memory || 'Aucune mémoire enregistrée.'}</pre></details>
                </div>}
                {detailTab === 'Historique' && <ol className="space-y-2">{selectedLoop.iterations.map(iter => <li key={iter.number} className="rounded border border-border p-3"><strong>Passe {iter.number}</strong> · {iter.step_id || 'Diagnostic'}<p>{iter.result?.change || iter.error || iter.status}</p><p>Décision : {iter.decision || 'Historique antérieur'}</p>{iter.verification && <p>Contrôle Atlas : {iter.verification.passed ? 'réussi' : 'échoué'}</p>}</li>)}</ol>}
                {/* Iteration tabs */}
                <div hidden={detailTab === 'Résumé' || detailTab === 'Historique'}>
                  <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span className="text-xs font-medium text-muted-foreground">Itérations :</span>
                      {selectedLoop.iterations.length === 0 ? (
                        <span className="text-xs text-muted-foreground">Aucune passe exécutée pour le moment.</span>
                      ) : (
                        selectedLoop.iterations.map(iter => {
                          const who = iter.cli ? `${iter.cli}${iter.person ? `·${iter.person}` : ''}` : ''
                          return (
                            <button
                              key={iter.number}
                              type="button"
                              onClick={() => setSelectedIter(iter.number)}
                              className={`rounded-md border px-2 py-1 text-2xs tabular-nums transition-colors ${
                                selectedIter === iter.number
                                  ? 'border-primary/50 bg-primary/15 font-semibold text-primary'
                                  : iter.rate_limited
                                    ? 'border-warning/40 bg-warning/10 text-warning'
                                    : 'border-border bg-background text-muted-foreground hover:text-foreground'
                              }`}
                            >
                              #{iter.number}
                              {who ? ` (${who})` : ''} ·{' '}
                              {iter.status === 'running'
                                ? 'en cours'
                                : iter.status === 'rate_limited'
                                  ? 'indisponible'
                                  : iter.status === 'completed'
                                    ? `${formatDuration(iter.duration_ms)}${iter.changed_files ? ` · ${iter.changed_files}f` : ''}`
                                    : iter.status}
                            </button>
                          )
                        })
                      )}
                    </div>
                    {selectedLoop.updated_at && (
                      <span className="text-2xs text-muted-foreground">
                        Mis à jour {formatRelative(selectedLoop.updated_at)}
                      </span>
                    )}
                  </div>

                  {/* Live / Iteration Log Output */}
                  <pre hidden={detailTab === 'Modifications'} className="max-h-96 min-h-[160px] overflow-auto rounded-lg border border-border bg-[#0c0f14] p-3 font-mono text-xs leading-relaxed text-[#e2e8f0] whitespace-pre-wrap">
                    {logData?.content ||
                      (selectedLoop.status === 'running'
                        ? selectedLoop.live_output || 'Démarrage de l’itération en cours…'
                        : 'Lancez la boucle pour afficher les journaux d’exécution.')}
                  </pre>

                  {(logData?.git_status || logData?.git_diff) && (
                    <div hidden={detailTab === 'Logs'} className="mt-3 grid gap-3 sm:grid-cols-2">
                      {logData.git_status && (
                        <div className="rounded-lg border border-border bg-background/60 p-2.5">
                          <div className="mb-1 text-2xs font-medium text-muted-foreground">Git status</div>
                          <pre className="max-h-40 overflow-auto font-mono text-2xs text-foreground whitespace-pre-wrap">
                            {logData.git_status}
                          </pre>
                        </div>
                      )}
                      {logData.git_diff && (
                        <div className="rounded-lg border border-border bg-background/60 p-2.5">
                          <div className="mb-1 text-2xs font-medium text-muted-foreground">Git diff / commits</div>
                          <pre className="max-h-40 overflow-auto font-mono text-2xs text-foreground whitespace-pre-wrap">
                            {logData.git_diff}
                          </pre>
                          <p className="mt-2 text-xs text-muted-foreground">Examinez ces changements et les vérifications avant de valider le résultat. Branche : {selectedLoop.branch || 'dossier courant'} · {selectedLoop.work_dir}. L’intégration s’effectue depuis cette branche ou ce dossier.</p>
                        </div>
                      )}
                    </div>
                  )}
                </div>
              </div>
            )}
          </div>
        </section>

        {/* Section 3: Quick Chats (Claude / Codex / AGY) */}
        <section className="rounded-xl border border-border bg-card p-4">
          <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
            <div className="flex items-center gap-2">
              <MessageSquare size={16} className="text-primary" />
              <h2 className="font-medium text-foreground">Ouvrir un chat tmux</h2>
              {selectedProject && (
                <span className="truncate font-mono text-2xs text-muted-foreground">dans {selectedProject}</span>
              )}
            </div>
            <button
              type="button"
              onClick={() => setChatAdvanced(v => !v)}
              className="text-xs text-muted-foreground hover:text-foreground"
            >
              {chatAdvanced ? 'Masquer les options' : 'Prompt initial / modèle'}
            </button>
          </div>

          {chatAdvanced && (
            <div className="mb-4 grid gap-2 rounded-lg border border-border bg-background/60 p-3 sm:grid-cols-3">
              <div className="sm:col-span-2">
                <label className="mb-1 block text-xs text-muted-foreground" htmlFor="loops-chat-prompt">
                  Prompt initial (optionnel)
                </label>
                <input
                  id="loops-chat-prompt"
                  type="text"
                  value={chatPrompt}
                  onChange={e => setChatPrompt(e.target.value)}
                  placeholder="Ex : Analyse les derniers commits et propose les prochaines améliorations"
                  className="w-full rounded-md border border-border bg-background px-2.5 py-1.5 text-xs text-foreground"
                />
              </div>
              <div>
                <label className="mb-1 block text-xs text-muted-foreground" htmlFor="loops-chat-model">
                  Modèle (optionnel)
                </label>
                <input
                  id="loops-chat-model"
                  type="text"
                  value={chatModel}
                  onChange={e => setChatModel(e.target.value)}
                  placeholder="Défaut du CLI"
                  className="w-full rounded-md border border-border bg-background px-2.5 py-1.5 font-mono text-xs text-foreground"
                />
              </div>
            </div>
          )}

          {/* 3 CLI cards: Claude, Codex, AGY */}
          <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
            {cliOrder.map(cli => {
              const info = cliMeta[cli]
              const avail = availabilityByCLI[cli]
              const people = uniquePeople(meta?.accounts[cli])
              const openSessions = activeSessionsByCLI[cli]
              const busy = actionBusy === `chat-${cli}`
              return (
                <div
                  key={cli}
                  className="flex flex-col justify-between gap-3 rounded-lg border border-border bg-background/70 p-3"
                >
                  <div className="flex items-start justify-between gap-2">
                    <div>
                      <div className="flex items-center gap-2">
                        <span className={`rounded border px-1.5 py-0.5 text-2xs font-semibold ${info.badge}`}>
                          {cli.toUpperCase()}
                        </span>
                        <span className="font-medium text-foreground">{info.title}</span>
                      </div>
                      <p className="mt-0.5 text-2xs text-muted-foreground">{info.subtitle}</p>
                    </div>
                    <span className={`text-2xs ${avail?.available ? 'text-success' : 'text-muted-foreground'}`}>
                      {avail?.available ? 'Prêt' : 'Absent'}
                    </span>
                  </div>

                  <div>
                    <label className="mb-1 block text-2xs text-muted-foreground" htmlFor={`chat-person-${cli}`}>
                      Compte {cli}
                    </label>
                    {people.length > 0 ? (
                      <select
                        id={`chat-person-${cli}`}
                        value={chatPersons[cli]}
                        onChange={e => handlePersonChange(cli, e.target.value)}
                        className="w-full rounded-md border border-border bg-card px-2 py-1.5 text-xs text-foreground"
                      >
                        {people.map(person => (
                          <option key={person} value={person}>
                            {person}
                          </option>
                        ))}
                        <option value="">Demander au terminal</option>
                      </select>
                    ) : (
                      <div className="rounded-md border border-dashed border-border px-2 py-1.5 text-2xs text-muted-foreground">
                        Aucun compte nommé ({cli})
                      </div>
                    )}
                  </div>

                  <div className="flex flex-col gap-2">
                    <Button
                      variant="primary"
                      size="sm"
                      loading={busy}
                      disabled={!selectedProject.trim() || (avail ? !avail.available : false)}
                      onClick={() => void handleOpenChat(cli)}
                      leadingIcon={<TerminalIcon size={14} />}
                      className="w-full justify-center"
                    >
                      Ouvrir {info.title}
                    </Button>

                    {openSessions.length > 0 && (
                      <div className="flex flex-wrap items-center gap-1">
                        <span className="text-2xs text-muted-foreground">Ouverts :</span>
                        {openSessions.slice(0, 3).map(sess => {
                          const key = sessionKey(sess)
                          return (
                            <button
                              key={key}
                              type="button"
                              onClick={() => onOpenSession(key)}
                              className="max-w-[140px] truncate rounded border border-border bg-card px-1.5 py-0.5 text-2xs text-foreground hover:border-primary/50 hover:text-primary"
                              title={`Aller à ${sess.name}`}
                            >
                              {sess.name}
                            </button>
                          )
                        })}
                      </div>
                    )}
                  </div>
                </div>
              )
            })}
          </div>
        </section>
      </div>

      {/* Create / Edit Loop Dialog */}
      <Dialog
        open={modalOpen}
        onOpenChange={setModalOpen}
        title={editingLoop ? 'Modifier la boucle' : 'Nouvelle boucle'}
        description="Choisissez le projet, les abonnements autorisés et l’objectif à poursuivre."

        className="max-w-3xl"
        footer={
          <>
            <Button variant="secondary" onClick={() => setModalOpen(false)} disabled={savingLoop}>
              Annuler
            </Button>
            {guideStep > 0 && <Button variant="secondary" onClick={() => setGuideStep(guideStep - 1)}>Précédent</Button>}
            {guideStep < 3 && <Button variant="primary" onClick={() => { if (guideStep === 0 && !formProject.trim()) { setFormError('Sélectionnez un projet.'); return }; if (guideStep === 1 && !formPrompt.trim()) { setFormError('Décrivez l’objectif.'); return }; setFormError(null); setGuideStep(guideStep + 1) }}>Suivant</Button>}
            <Button className={guideStep !== 3 ? 'hidden' : ''} variant="secondary" onClick={() => void handleSaveLoop(false)} loading={savingLoop}>
              Enregistrer
            </Button>
            <Button className={guideStep !== 3 ? 'hidden' : ''} variant="primary" onClick={() => void handleSaveLoop(true)} loading={savingLoop}>
              {editingLoop ? 'Enregistrer & Démarrer' : 'Créer & Démarrer'}
            </Button>
          </>
        }
      >
        <div className="flex flex-col gap-3.5 text-xs">
          {formError && (
            <div className="rounded border border-destructive/40 bg-destructive/10 px-2.5 py-1.5 text-destructive">
              {formError}
            </div>
          )}

          <fieldset hidden={guideStep !== 0} className="space-y-3"><legend className="mb-2 font-medium">1. Projet</legend>          {!editingLoop && (
            <div>
              <div className="mb-1.5 font-medium text-foreground">Projet</div>
              <ProjectSelector
                projects={meta?.projects ?? []}
                value={formProject}
                onChange={setFormProject}
                loops={loops}
                inputId="loop-modal-project"
              />
            </div>
          )}


{editingLoop && <p>{formProject}</p>}</fieldset>
<fieldset hidden={guideStep !== 1} className="space-y-3"><legend className="mb-2 font-medium">2. Objectif et critères</legend>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div>
              <label className="mb-1 block font-medium text-foreground" htmlFor="loop-name">
                Nom (optionnel)
              </label>
              <input
                id="loop-name"
                type="text"
                value={formName}
                onChange={e => setFormName(e.target.value)}
                placeholder="Ex : Amélioration continue"
                className="w-full rounded-md border border-border bg-background px-2.5 py-1.5 text-foreground"
              />
            </div>
            <div>
              <label className="mb-1 block font-medium text-foreground" htmlFor="loop-model">
                Modèle (optionnel)
              </label>
              <input
                id="loop-model"
                type="text"
                value={formModel}
                onChange={e => setFormModel(e.target.value)}
                placeholder="Défaut du CLI"
                className="w-full rounded-md border border-border bg-background px-2.5 py-1.5 font-mono text-foreground"
              />
            </div>
          </div>

          <label className="block">Périmètre<input className="mt-1 w-full rounded border border-border bg-background p-2" value={scope} onChange={e => setScope(e.target.value)} /></label>
          <label className="block">Critères de réussite<textarea className="mt-1 w-full rounded border border-border bg-background p-2" value={criteria} onChange={e => setCriteria(e.target.value)} placeholder="Comment vérifier que l’objectif est atteint ?" /></label>
          {/* Presets */}
          {(meta?.presets.length ?? 0) > 0 && (
            <div>
              <div className="mb-1 font-medium text-foreground">Modèles d’objectif</div>
              <div className="flex flex-wrap gap-1.5">
                {meta?.presets.map(preset => (
                  <button
                    key={preset.id}
                    type="button"
                    onClick={() => {
                      setFormPrompt(preset.prompt)
                      if (!formName.trim()) setFormName(preset.label)
                    }}
                    title={preset.description}
                    className="rounded-md border border-border bg-secondary/60 px-2 py-1 text-2xs text-foreground hover:border-primary/50 hover:text-primary"
                  >
                    {preset.label}
                  </button>
                ))}
              </div>
            </div>
          )}

          <div>
            <label className="mb-1 block font-medium text-foreground" htmlFor="loop-prompt">
              Objectif / Instructions de boucle
            </label>
            <textarea
              id="loop-prompt"
              ref={promptInputRef}
              rows={4}
              value={formPrompt}
              onChange={e => setFormPrompt(e.target.value)}
              placeholder="Décrivez ce que chaque itération doit chercher et améliorer…"
              className="w-full rounded-md border border-border bg-background px-2.5 py-1.5 text-foreground"
            />
          </div>

</fieldset><fieldset hidden={guideStep !== 2} className="space-y-3"><legend className="mb-2 font-medium">3. Autonomie et limites</legend>          <label className="block">Commande de vérification (optionnelle)<input className="mt-1 w-full rounded border border-border bg-background p-2 font-mono" value={verifyCommand} onChange={e => setVerifyCommand(e.target.value)} placeholder="Ex. npm test" /></label>
          <p className="text-muted-foreground">Exécutée dans le dossier de la boucle après chaque passe, avec un délai maximal de cinq minutes.</p>
          <label className="block">Limite de temps en minutes (0 = aucune)<input type="number" min={0} max={10080} className="ml-2 rounded border border-border bg-background p-2" value={timeLimit} onChange={e => setTimeLimit(Math.max(0, Math.min(10080, Number(e.target.value) || 0)))} /></label>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div>
              <div className="mb-1 font-medium text-foreground">Durée de la boucle</div>
              <div className="flex items-center gap-2">
                <button
                  type="button"
                  onClick={() => setFormContinuous(true)}
                  className={`inline-flex items-center gap-1 rounded-md border px-2.5 py-1.5 text-xs ${
                    formContinuous
                      ? 'border-primary/50 bg-primary/15 font-medium text-primary'
                      : 'border-border bg-background text-muted-foreground'
                  }`}
                >
                  <InfinityIcon size={13} />
                  Entretien continu
                </button>
                <button
                  type="button"
                  onClick={() => setFormContinuous(false)}
                  className={`rounded-md border px-2.5 py-1.5 text-xs ${
                    !formContinuous
                      ? 'border-primary/50 bg-primary/15 font-medium text-primary'
                      : 'border-border bg-background text-muted-foreground'
                  }`}
                >
                  Mission
                </button>
                {!formContinuous && (
                  <input
                    id="loop-max-iter"
                    type="number"
                    min={1}
                    max={1000}
                    value={formMaxIter}
                    onChange={e => setFormMaxIter(Math.max(1, Math.min(1000, Number(e.target.value) || 1)))}
                    className="w-20 rounded-md border border-border bg-background px-2 py-1.5 tabular-nums text-foreground"
                  />
                )}
              </div>
            </div>
            <div>
              <label className="mb-1 block font-medium text-foreground" htmlFor="loop-delay">
                Pause entre passes (s)
              </label>
              <input
                id="loop-delay"
                type="number"
                min={0}
                max={3600}
                value={formDelay}
                onChange={e => setFormDelay(Math.max(0, Math.min(3600, Number(e.target.value) || 0)))}
                className="w-full rounded-md border border-border bg-background px-2.5 py-1.5 tabular-nums text-foreground"
              />
            </div>
          </div>

          <div className="flex flex-col gap-2 rounded-lg border border-border bg-background/60 p-2.5">
            <label className="flex items-center gap-2 text-foreground">
              <input
                type="checkbox"
                checked={formAutoApprove}
                onChange={e => setFormAutoApprove(e.target.checked)}
              />
              <span>Exécution autonome sans confirmation manuelle</span>
            </label>
            <label className="flex items-center gap-2 text-foreground">
              <input
                type="checkbox"
                checked={formStopWhenDone}
                onChange={e => setFormStopWhenDone(e.target.checked)}
              />
              <span>Arrêter la boucle dès que l’agent signale LOOP_DONE</span>
            </label>
            {!editingLoop && (selectedProjectInfo?.is_git ?? true) && (
              <label className="flex items-center gap-2 text-foreground">
                <input
                  type="checkbox"
                  checked={formUseWorktree}
                  onChange={e => setFormUseWorktree(e.target.checked)}
                />
                <span>Isoler les modifications dans un worktree Git dédié</span>
              </label>
            )}
          </div>
</fieldset><fieldset hidden={guideStep !== 3} className="space-y-3"><legend className="mb-2 font-medium">4. Agents autorisés</legend>          <SubscriptionPool
            mode="draft"
            availableSubscriptions={availableSubscriptions}
            availabilityByCLI={availabilityByCLI}
            workers={formWorkers}
            onChangeWorkers={setFormWorkers}
          /></fieldset>        </div>
      </Dialog>
    </main>
  )
}
