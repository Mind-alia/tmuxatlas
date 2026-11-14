import { useCallback, useEffect, useRef, useState } from 'react'
import { ChevronDown, Maximize2, Plus, X } from 'lucide-react'
import type { useMicStream } from '../hooks/useMicStream'
import { getBrandStorage, setBrandStorage } from '../lib/brandStorage'
import { activePaneOf, talkAgentOf } from '../lib/talkAgent'
import { useHostLabel } from '../lib/hostLabel'
import { isAttentionStatus, type WorkspaceViewModel } from '../workspace/model'
import { IconButton, Menu, MenuItem, StatusIcon } from './ui'
import { Terminal } from './Terminal'

const legacyStorageKey = 'wall-targets:v1'
interface WallChoices {
  targets: string[]
  omitted: string[]
}
const sessionKeys = (value: unknown): string[] =>
  Array.isArray(value) ? [...new Set(value.filter((key): key is string => typeof key === 'string'))] : []
type WallSession = WorkspaceViewModel['sessions'][number]
const noLocalHosts: ReadonlySet<string> = new Set()

/**
 * Start with every session owned by the account, preserving manual choices.
 * New sessions can then be added with one click, with no limit on tile count.
 */
export function WallView({
  workspace,
  owner,
  localHosts = noLocalHosts,
  onOpenSession,
  sharedMic,
  onNewSession,
  pendingSession,
  onSessionAdded,
}: {
  workspace: WorkspaceViewModel
  /** The signed-in account; without one, every session counts as its own. */
  owner?: string | null
  /** Hosts served by this page's machine, where the virtual microphone lives. */
  localHosts?: ReadonlySet<string>
  sharedMic?: ReturnType<typeof useMicStream>
  onNewSession?: () => void
  pendingSession?: string | null
  onSessionAdded?: () => void
  onOpenSession: (key: string) => void
}) {
  const hostLabel = useHostLabel()
  const storageKey = owner ? `wall-targets:v2:${owner}` : 'wall-targets:v2'
  const needsDefaults = useRef(false)
  const isOwn = (session: WallSession) => !owner || session.ownerName === owner
  const ownSessions = workspace.sessions.filter(isOwn).sort(
    (a, b) =>
      Number(isAttentionStatus(b.status)) - Number(isAttentionStatus(a.status)) ||
      (Date.parse(b.lastActivity ?? '') || 0) - (Date.parse(a.lastActivity ?? '') || 0),
  )
  const addOwnSessions = (choices: WallChoices): WallChoices => ({
    ...choices,
    targets: [...new Set([
      ...choices.targets.filter(Boolean),
      ...ownSessions.filter(session => !choices.omitted.includes(session.key)).map(session => session.key),
    ])],
  })
  const [choices, setChoices] = useState<WallChoices>(() => {
    let saved: WallChoices = { targets: [], omitted: [] }
    try {
      const stored = getBrandStorage(storageKey)
      if (stored !== null) {
        const parsed: unknown = JSON.parse(stored)
        if (parsed && typeof parsed === 'object' && 'targets' in parsed && 'omitted' in parsed) {
          saved = { targets: sessionKeys(parsed.targets), omitted: sessionKeys(parsed.omitted).filter(Boolean) }
        }
      } else {
        // Keep existing tile choices when upgrading the Wall.
        saved.targets = sessionKeys(JSON.parse(getBrandStorage(legacyStorageKey) ?? '[]'))
      }
    } catch {
      /* Stockage indisponible. */
    }
    needsDefaults.current = ownSessions.length === 0
    return addOwnSessions(saved)
  })
  const { targets } = choices
  const [active, setActive] = useState<string | null>(null)
  useEffect(() => {
    if (!needsDefaults.current || ownSessions.length === 0) return
    needsDefaults.current = false
    setChoices(addOwnSessions)
  }, [workspace.sessions, owner])
  useEffect(() => {
    // An empty initial projection does not mean saved sessions have disappeared.
    if (needsDefaults.current && workspace.sessions.length === 0) return
    setChoices(previous => {
      const next = previous.targets.filter(key => !key || workspace.byTarget.has(key))
      return next.length === previous.targets.length ? previous : { ...previous, targets: next }
    })
  }, [workspace.byTarget])
  useEffect(() => {
    if (needsDefaults.current) return
    try {
      setBrandStorage(storageKey, JSON.stringify(choices))
    } catch {
      /* Stockage indisponible. */
    }
  }, [choices, storageKey])
  useEffect(() => {
    if (!pendingSession || !workspace.byTarget.has(pendingSession)) return
    needsDefaults.current = false
    setChoices(previous => ({
      targets: [...new Set([...previous.targets.filter(Boolean), pendingSession])],
      omitted: previous.omitted.filter(key => key !== pendingSession),
    }))
    onSessionAdded?.()
  }, [pendingSession, workspace.byTarget, onSessionAdded])
  // The tile holding Claude's talk key: the others wait, so that one voice reaches one Claude.
  const [talking, setTalking] = useState<string | null>(null)
  const [talkSlots, setTalkSlots] = useState<Record<string, HTMLElement | null>>({})
  const slotRefs = useRef(new Map<string, (node: HTMLElement | null) => void>())
  const talkSlotRef = useCallback((key: string) => {
    let ref = slotRefs.current.get(key)
    if (!ref) {
      ref = node => setTalkSlots(previous => (previous[key] === node ? previous : { ...previous, [key]: node }))
      slotRefs.current.set(key, ref)
    }
    return ref
  }, [])
  const holdRefs = useRef(new Map<string, (on: boolean) => void>())
  const talkHold = useCallback((key: string) => {
    let hold = holdRefs.current.get(key)
    if (!hold) {
      hold = on => setTalking(previous => (on ? key : previous === key ? null : previous))
      holdRefs.current.set(key, hold)
    }
    return hold
  }, [])
  const available = workspace.sessions.filter(session => !targets.includes(session.key))
  const availableOwn = available.filter(isOwn)
  const availableOthers = available.filter(session => !isOwn(session))
  const multipleHosts = new Set(workspace.sessions.map(session => session.hostId)).size > 1
  const name = (session: WallSession) =>
    multipleHosts
      ? `${hostLabel(session.hostId, session.hostName === session.hostId ? 'Hôte' : session.hostName || 'Hôte')} / ${session.name}`
      : session.name
  const label = (session: WallSession) =>
    isOwn(session) ? name(session) : `${name(session)} · ${session.ownerName || 'sans propriétaire'}`
  const option = (session: WallSession) => (
    <option key={session.key} value={session.key}>
      {label(session)}
    </option>
  )
  const choose = (update: (previous: string[]) => string[]) => {
    needsDefaults.current = false
    setChoices(previous => {
      const next = update(previous.targets)
      // Remember removed/replaced tiles so reopening respects the account's choices.
      const omitted = new Set(previous.omitted)
      previous.targets.filter(key => key && !next.includes(key)).forEach(key => omitted.add(key))
      next.forEach(key => omitted.delete(key))
      return { targets: next, omitted: [...omitted] }
    })
  }
  const select = (index: number, key: string) =>
    choose(previous => {
      const next = [...previous]
      if (index === next.length) next.push(key)
      else next[index] = key
      return next
    })
  return (
    <section aria-label="Mur de sessions" className="relative flex min-h-0 flex-1 flex-col gap-3 p-3">
      {(available.length > 0 || onNewSession) && (
        <Menu
          label="Ajouter une session au mur"
          className="wall-add-menu absolute bottom-6 right-6 z-30"
          trigger={<Plus size={22} aria-hidden />}
        >
          <div className="px-3 pb-2 pt-1 text-xs font-semibold text-muted-foreground">Ajouter au mur</div>
          <div className="max-h-[min(50dvh,24rem)] overflow-y-auto">
            {[...availableOwn, ...availableOthers].map(session => (
              <MenuItem key={session.key} onSelect={() => select(targets.length, session.key)}>
                <StatusIcon status={session.status} />
                <span className="min-w-0 truncate">{label(session)}</span>
              </MenuItem>
            ))}
          </div>
          {onNewSession && (
            <MenuItem className="mt-1 border-t border-border text-primary" onSelect={onNewSession}>
              <Plus size={16} aria-hidden />Créer une session
            </MenuItem>
          )}
        </Menu>
      )}
      {targets.length > 2 && (
        <p className="text-xs text-muted-foreground md:hidden">
          Au-delà de deux tuiles, la lecture est moins confortable sur téléphone.
        </p>
      )}
      {targets.length === 0 && available.length === 0 ? (
        <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">
          Aucune session à afficher pour le moment.
        </div>
      ) : (
        <div
          className={`grid min-h-0 flex-1 auto-rows-[minmax(18rem,1fr)] grid-cols-1 gap-3 overflow-auto md:grid-cols-2 ${targets.length === 1 ? 'md:grid-cols-1' : targets.length === 3 ? 'md:grid-cols-3' : ''}`}
        >
          {(targets.length ? targets : ['']).map((key, index) => {
            const session = workspace.byTarget.get(key)
            return (
              <div
                key={key || `empty-${index}`}
                data-tone={
                  session?.status === 'error' ? 'error' : session?.status === 'waiting' ? 'waiting' : undefined
                }
                className={`atlas-surface flex min-h-72 min-w-0 flex-col overflow-hidden rounded-lg ${active === key && key ? 'ring-2 ring-primary' : ''}`}
                onFocusCapture={() => setActive(key)}
                onMouseDown={() => setActive(key)}
              >
                <header className="flex h-11 shrink-0 items-center gap-2 px-3">
                  <div className="relative flex min-w-0 flex-1 items-center gap-2">
                    {session && <StatusIcon status={session.status} />}
                    <select
                      aria-label={session ? 'Changer de session' : 'Choisir une session'}
                      value={key}
                      className="ui-focus-ring min-h-11 min-w-0 flex-1 appearance-none truncate bg-transparent pr-5 text-sm text-foreground"
                      onChange={event => select(index, event.target.value)}
                    >
                      <option value="" disabled>
                        Choisir une session
                      </option>
                      {session && <option value={key}>{label(session)}</option>}
                      {availableOthers.length > 0 && owner ? (
                        <>
                          {availableOwn.length > 0 && (
                            <optgroup label="Mes sessions">{availableOwn.map(option)}</optgroup>
                          )}
                          <optgroup label="Autres propriétaires">{availableOthers.map(option)}</optgroup>
                        </>
                      ) : (
                        available.map(option)
                      )}
                    </select>
                    <ChevronDown size={16} aria-hidden="true" className="pointer-events-none absolute right-0" />
                  </div>
                  {session && <span ref={talkSlotRef(key)} className="flex shrink-0 items-center text-xs" />}
                  {session && (
                    <IconButton aria-label="Agrandir" size="sm" onClick={() => onOpenSession(key)}>
                      <Maximize2 size={16} />
                    </IconButton>
                  )}
                  <IconButton
                    aria-label="Retirer"
                    size="sm"
                    onClick={() => choose(previous => previous.filter((_, i) => i !== index))}
                  >
                    <X size={16} />
                  </IconButton>
                </header>
                {session && (
                  <Terminal
                    embedded
                    sessionName={session.name}
                    hostId={session.hostId}
                    hostName={session.hostName}
                    sessionStatus={session.status}
                    hostLocal={localHosts.has(session.hostId)}
                    voiceAllowed
                    claudeActive={talkAgentOf(activePaneOf(session.source)) === 'claude'}
                    agyActive={talkAgentOf(activePaneOf(session.source)) === 'agy'}
                    codexActive={talkAgentOf(activePaneOf(session.source)) === 'codex'}
                    talkSlot={talkSlots[key]}
                    sharedMic={sharedMic}
                    talkLocked={talking !== null && talking !== key}
                    onTalkHold={talkHold(key)}
                  />
                )}
              </div>
            )
          })}
        </div>
      )}
    </section>
  )
}
