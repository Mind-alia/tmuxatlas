import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { ArrowDown, ArrowLeft, ArrowRight, ArrowUp, CornerDownLeft, Keyboard, KeyboardOff, MessageSquareText, Mic } from 'lucide-react'
import { useMediaQuery, COMPACT_LAYOUT_QUERY } from '../lib/useMediaQuery'
import { useHostLabel } from '../lib/hostLabel'
import type { Window as SessionWindow } from '../hooks/useSessions'
import type { WorkspaceStatus } from '../workspace/model'
import { WindowTabs } from './WindowTabs'
import { isEditingOutsideTerminal } from '../lib/focusGuard'
import { useTerminal, type PendingTerminalPaste } from '../hooks/useTerminal'
import { useTerminalDrafts, type TerminalDraftStore } from '../hooks/useTerminalDrafts'
import { MobileTerminalInput, terminalKeys, type ModifierName, type ModifierState } from '../lib/mobileTerminalInput'
import { terminalTargetKey } from '../lib/terminalInput'
import type { ConnectionState } from '../state/types'
import { deriveTerminalWorkspaceConnection, type TerminalWorkspaceConnectionState } from '../state/terminalConnection'
import { useMicStream, type MicState } from '../hooks/useMicStream'
import { readAgyVoiceStatus } from '../lib/agyVoiceStatus'
import type { TalkAgent } from '../lib/talkAgent'
import { RelayTalkButton, TalkButton, type DictationRelay } from './TalkButton'
import { TerminalCockpit } from './TerminalCockpit'
import { TerminalContextMenu, type TerminalMenuPosition } from './TerminalContextMenu'
import { TerminalPasteConfirmation } from './TerminalPasteConfirmation'
import { TerminalSearchBar } from './TerminalSearchBar'
import { MobileInputComposer } from './MobileInputComposer'
import { hideSoftKeyboard, useSoftKeyboardOpen } from '../lib/useSoftKeyboard'

export interface TerminalCommandActions {
  reconnect: () => void
  focus: () => void
  openSearch: () => void
  copy: () => Promise<void>
  paste: () => Promise<void>
  scrollToBottom: () => void
}

export interface TerminalProps {
  embedded?: boolean
  windows?: SessionWindow[]
  windowAttention?: Record<number, 'error' | 'waiting'>
  onSelectWindow?: (index: number) => void
  onSelectPane?: (paneId: string) => void
  onBackToSessions?: () => void
  onHome?: () => void
  onToggleFiles?: () => void
  filesOpen?: boolean
  onPreviousSession?: () => void
  onNextSession?: () => void
  attentionCount?: number
  onOpenAlerts?: () => void
  sessionStatus?: WorkspaceStatus
  sessionName: string
  ownerName?: string | null
  hostId: string
  hostName?: string
  windowLabel?: string
  paneLabel?: string
  toolStatus?: string
  hubConnection?: ConnectionState
  agentOnline?: boolean
  sessionAvailable?: boolean
  connectionState?: TerminalWorkspaceConnectionState
  fullscreen?: boolean
  zenMode?: boolean
  /** The session runs on the machine serving this page, where the virtual microphone lives. */
  hostLocal?: boolean
  /** Whether voice/microphone features are allowed for the current user role. */
  voiceAllowed?: boolean
  /** Claude Code runs in the pane this Terminal has active. */
  claudeActive?: boolean
  /** agy runs in the pane this Terminal has active; Talk toggles its F5 dictation. */
  agyActive?: boolean
  /** Codex runs in the pane this Terminal has active; a hidden Claude transcribes for it. */
  codexActive?: boolean
  /** Where the talk button renders, in the app header or a Wall tile. */
  talkSlot?: HTMLElement | null
  /**
   * The app microphone, shared across views and Wall tiles. An embedded Terminal opens no
   * microphone of its own, and shows Talk only with this one.
   */
  sharedMic?: { available: boolean; state: MicState }
  /** Another tile is dictating: the virtual microphone serves one Claude at a time. */
  talkLocked?: boolean
  /** Reports when this Terminal starts and stops holding Claude's talk key. */
  onTalkHold?: (on: boolean) => void
  onToggleFullscreen?: () => void
  onToggleZen?: () => void
  onRetry?: () => void
  onCommandActionsChange?: (actions: TerminalCommandActions | null) => void
  terminalDrafts?: TerminalDraftStore
}

type MobileKey =
  | { label: string; content: ReactNode; value: string; wide?: boolean; modifier?: undefined }
  | { label: string; content: string; modifier: ModifierName; value?: undefined; wide?: undefined }

// Ordered by how often an agent session needs them: what scrolls out of view is rarer.
const mobileKeys: MobileKey[] = [
  { label: 'Échap', content: 'Esc', value: terminalKeys.escape, wide: true },
  { label: 'Ctrl', content: 'Ctrl', modifier: 'ctrl' },
  { label: 'Flèche haut', content: <ArrowUp size={16} aria-hidden />, value: terminalKeys.up },
  { label: 'Flèche bas', content: <ArrowDown size={16} aria-hidden />, value: terminalKeys.down },
  { label: 'Entrée', content: <CornerDownLeft size={16} aria-hidden />, value: terminalKeys.enter },
  { label: 'Tabulation', content: 'Tab', value: terminalKeys.tab, wide: true },
  { label: 'Flèche gauche', content: <ArrowLeft size={16} aria-hidden />, value: terminalKeys.left },
  { label: 'Flèche droite', content: <ArrowRight size={16} aria-hidden />, value: terminalKeys.right },
  { label: 'Maj+Tabulation', content: '⇧Tab', value: terminalKeys.shiftTab, wide: true },
  { label: 'Alt', content: 'Alt', modifier: 'alt' },
]

// F5 as a terminal sends it: agy's /voice key.
const AGY_DICTATION_KEY = '\x1b[15~'

const terminalStateMessages: Record<TerminalWorkspaceConnectionState, string> = {
  connecting: 'Connexion au terminal…',
  rehydrating: 'Synchronisation du Hub…',
  connected: '',
  reconnecting: 'Terminal déconnecté. Reconnexion…',
  'hub-offline': 'La connexion au Hub est indisponible.',
  'agent-offline': 'L’agent est hors ligne.',
  'session-ended': 'Cette session n’est plus disponible.',
  'auth-required': 'Votre connexion au Hub a expiré.',
  'reload-required': 'Atlas a été mis à jour. Actualisez la page pour continuer.',
}

export function Terminal({
  embedded = false,
  windows = [],
  windowAttention,
  onSelectWindow,
  onSelectPane,
  onBackToSessions,
  onHome,
  onToggleFiles,
  filesOpen,
  onPreviousSession,
  onNextSession,
  attentionCount,
  onOpenAlerts,
  sessionStatus,
  sessionName,
  ownerName,
  hostId,
  hostName,
  windowLabel,
  paneLabel,
  toolStatus,
  hubConnection,
  agentOnline,
  sessionAvailable,
  connectionState,
  fullscreen,
  zenMode,
  hostLocal,
  voiceAllowed = true,
  claudeActive,
  agyActive,
  codexActive,
  talkSlot,
  sharedMic,
  talkLocked = false,
  onTalkHold,
  onToggleFullscreen,
  onToggleZen,
  onRetry,
  onCommandActionsChange,
  terminalDrafts,
}: TerminalProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<MobileTerminalInput | null>(null)
  if (!inputRef.current) inputRef.current = new MobileTerminalInput()
  const input = inputRef.current
  const targetKey = terminalTargetKey(hostId, sessionName)
  const resolveHostLabel = useHostLabel()
  const hostLabel = resolveHostLabel(hostId, hostName === hostId ? 'Hôte' : hostName || 'Hôte') || 'Hôte'
  const targetLabel = `${hostLabel}/${sessionName}`
  const compact = useMediaQuery(COMPACT_LAYOUT_QUERY)
  const keyboardOpen = useSoftKeyboardOpen()
  const localTerminalDrafts = useTerminalDrafts()
  const { getDraft, setDraft } = terminalDrafts ?? localTerminalDrafts
  const [modifiers, setModifiers] = useState<ModifierState>(input.snapshot())
  const [composerOpen, setComposerOpen] = useState(false)
  const [toolbarError, setToolbarError] = useState('')
  const [toolbarFeedback, setToolbarFeedback] = useState('')
  const [searchOpen, setSearchOpen] = useState(false)
  const [contextMenu, setContextMenu] = useState<TerminalMenuPosition | null>(null)
  const [pendingPaste, setPendingPaste] = useState<PendingTerminalPaste | null>(null)
  const [pasteError, setPasteError] = useState('')
  const {
    connect,
    disconnect,
    reconnect,
    fit,
    focus,
    ptyState,
    termConnected,
    hasSelection,
    isAtBottom,
    hasNewOutput,
    scrollToBottom,
    adjustFontSize,
    sendInput,
    sendRawInput,
    setVoiceHold,
    claimSize,
    dictate,
    dictationText,
    readScreen,
    sendCommand,
    copySelection,
    pasteClipboard,
    imagePasteStatus,
    commitClipboardPaste,
    selectAll,
    searchState,
    ensureSearchAddon,
    findNext,
    findPrevious,
    clearSearch,
  } = useTerminal(sessionName, hostId, input)
  const mic = useMicStream(Boolean(hostLocal && !embedded && voiceAllowed && !sharedMic))
  const toggleMic = useCallback(() => {
    mic.toggle()
    focus()
  }, [focus, mic])
  const talkAgent: TalkAgent | null = claudeActive ? 'claude' : agyActive ? 'agy' : codexActive ? 'codex' : null
  // The agent a hold started with, so that its release reaches the same agent.
  const heldAgent = useRef<TalkAgent | null>(null)
  const holdSpace = useCallback(
    (on: boolean) => {
      const agent = on ? talkAgent : heldAgent.current
      heldAgent.current = on ? talkAgent : null
      if (agent === 'codex') {
        // The server holds the hidden Claude's space bar for Codex.
      } else if (agent === 'agy') {
        // F5 starts agy's dictation and, while it records, inserts the transcript.
        // Reading its footer first keeps a stray F5 from starting or stopping one.
        const status = readAgyVoiceStatus(readScreen())
        if (on ? status === 'idle' : status === 'recording') {
          try {
            sendRawInput(new TextEncoder().encode(AGY_DICTATION_KEY))
          } catch {
            /* the Terminal reports the disconnect */
          }
        }
      } else {
        setVoiceHold(on)
      }
      onTalkHold?.(on)
      focus()
    },
    [focus, onTalkHold, readScreen, sendRawInput, setVoiceHold, talkAgent],
  )
  const talkMic = sharedMic ?? (embedded ? undefined : mic)
  const showTalk = Boolean(talkMic?.available && hostLocal && voiceAllowed && (embedded || !compact))

  const dictationRelay = useMemo<DictationRelay>(
    () => ({
      start: () => dictate('start'),
      stop: submit => dictate('stop', submit),
      cancel: () => void dictate('cancel').catch(() => {}),
    }),
    [dictate],
  )
  // Codex has no dictation: the server transcribes with a hidden Claude.
  const renderTalk = (
    disabled: boolean,
    label?: string,
    livePlacement: 'above-left' | 'above-right' | 'below-right' = 'above-left',
  ) =>
    talkAgent === 'codex' ? (
      <RelayTalkButton
        key={targetKey}
        agentName="Codex"
        disabled={disabled}
        relay={dictationRelay}
        onHold={onTalkHold}
        targetLabel={label}
        liveText={dictationText}
        livePlacement={livePlacement}
      />
    ) : (
      <TalkButton
        key={targetKey}
        agent={talkAgent ?? 'claude'}
        disabled={disabled}
        onHold={holdSpace}
        readScreen={readScreen}
        onSubmit={submitPrompt}
        targetLabel={label}
      />
    )

  const submitPrompt = useCallback(() => {
    try {
      sendRawInput(new Uint8Array([13]))
    } catch {
      /* the Terminal reports the disconnect */
    }
    focus()
  }, [focus, sendRawInput])

  const workspaceConnection =
    connectionState ??
    deriveTerminalWorkspaceConnection({
      hub: hubConnection ?? 'ready',
      agentOnline: agentOnline ?? true,
      sessionAvailable: sessionAvailable ?? true,
      pty: ptyState,
    }).state
  const canPaste = workspaceConnection === 'connected' && Boolean(navigator.clipboard?.readText)

  useEffect(() => input.subscribe(setModifiers), [input])
  useEffect(() => {
    input.reset()
    setToolbarError('')
    setToolbarFeedback('')
    setComposerOpen(false)
    setPendingPaste(null)
    setPasteError('')
    setContextMenu(null)
    setSearchOpen(false)
    clearSearch()
  }, [clearSearch, hostId, input, sessionName])

  const cycleModifier = (modifier: ModifierName) => {
    input.cycle(modifier)
    // A modifier applies to the next typed letter, so it brings the keyboard up.
    focus()
  }

  const reportError = useCallback((error: unknown, fallback: string) => {
    setToolbarFeedback('')
    setToolbarError(error instanceof Error ? error.message : fallback)
  }, [])

  const copy = useCallback(async () => {
    setToolbarError('')
    try {
      await copySelection()
      setToolbarFeedback('Sélection copiée.')
    } catch (error) {
      reportError(error, 'Impossible de copier la sélection.')
      throw error
    }
  }, [copySelection, reportError])

  const paste = useCallback(async () => {
    setToolbarError('')
    setToolbarFeedback('')
    try {
      const confirmation = await pasteClipboard()
      if (confirmation) {
        setPendingPaste(confirmation)
        setPasteError('')
      } else {
        setToolbarFeedback('Texte collé dans le terminal.')
      }
    } catch (error) {
      reportError(error, 'Impossible de coller le presse-papiers.')
      throw error
    }
  }, [pasteClipboard, reportError])

  const confirmPaste = useCallback(() => {
    if (!pendingPaste) return
    setPasteError('')
    try {
      commitClipboardPaste(pendingPaste)
      setPendingPaste(null)
      setToolbarFeedback('Lignes collées dans le terminal.')
      setToolbarError('')
    } catch (error) {
      setPasteError(error instanceof Error ? error.message : 'Impossible d’envoyer le collage.')
    }
  }, [commitClipboardPaste, pendingPaste])

  const openSearch = useCallback(() => {
    setContextMenu(null)
    setSearchOpen(true)
  }, [])

  const closeSearch = useCallback(() => {
    clearSearch()
    setSearchOpen(false)
    window.requestAnimationFrame(() => focus())
  }, [clearSearch, focus])

  const sendComposerCommand = useCallback(
    (value: string) => {
      sendCommand(value)
      setToolbarError('')
    },
    [sendCommand],
  )

  useEffect(() => {
    if (!onCommandActionsChange) return
    const actions: TerminalCommandActions = {
      reconnect,
      focus,
      openSearch,
      copy: async () => {
        await copy()
      },
      paste: async () => {
        await paste()
      },
      scrollToBottom,
    }
    onCommandActionsChange(actions)
    return () => onCommandActionsChange(null)
  }, [copy, focus, onCommandActionsChange, openSearch, paste, reconnect, scrollToBottom, targetKey])

  useEffect(() => {
    const container = containerRef.current
    if (!container) return
    connect(container)
    const autoFocus = () => {
      if (!isEditingOutsideTerminal()) focus()
    }
    const focusFrame = embedded ? 0 : window.requestAnimationFrame(autoFocus)
    const focusTimer = embedded ? 0 : window.setTimeout(autoFocus, 100)
    return () => {
      window.cancelAnimationFrame(focusFrame)
      window.clearTimeout(focusTimer)
      disconnect()
    }
  }, [connect, disconnect, focus, embedded])

  useEffect(() => {
    if (embedded || !termConnected || document.hidden) return
    const timer = window.setTimeout(() => {
      fit()
      if (isEditingOutsideTerminal()) return
      focus()
      containerRef.current?.querySelector<HTMLTextAreaElement>('textarea.xterm-helper-textarea')?.focus()
    }, 100)
    return () => window.clearTimeout(timer)
  }, [fit, focus, termConnected, embedded])

  useEffect(() => {
    if (embedded) return
    let timer: number | null = null
    const refocus = () => {
      if (document.hidden || !containerRef.current) return
      if (timer !== null) window.clearTimeout(timer)
      timer = window.setTimeout(() => {
        timer = null
        fit()
        if (isEditingOutsideTerminal()) return
        focus()
        containerRef.current?.querySelector<HTMLTextAreaElement>('textarea.xterm-helper-textarea')?.focus()
      }, 200)
    }
    document.addEventListener('visibilitychange', refocus)
    window.addEventListener('focus', refocus)
    return () => {
      document.removeEventListener('visibilitychange', refocus)
      window.removeEventListener('focus', refocus)
      if (timer !== null) window.clearTimeout(timer ?? undefined)
    }
  }, [fit, focus, embedded])

  useEffect(() => {
    const container = containerRef.current
    if (!container) return
    // tmux sizes the windows for the client that typed last: after a phone typed,
    // a computer would keep its narrow width. Turning to this tab, or touching its
    // terminal, hands the windows back to this screen.
    let last = 0
    const claim = () => {
      const now = Date.now()
      if (document.hidden || now - last < 500) return
      last = now
      claimSize()
    }
    const onVisibility = () => {
      if (!document.hidden) claim()
    }
    container.addEventListener('pointerdown', claim, { passive: true })
    container.addEventListener('focusin', claim)
    // A Wall tile claims only when touched: the page coming back would make every tile claim at once.
    if (!embedded) {
      window.addEventListener('focus', claim)
      document.addEventListener('visibilitychange', onVisibility)
    }
    return () => {
      container.removeEventListener('pointerdown', claim)
      container.removeEventListener('focusin', claim)
      window.removeEventListener('focus', claim)
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [claimSize, embedded])

  useEffect(() => {
    const container = containerRef.current
    if (!container) return
    // Fit once the size stops changing. A phone keyboard slides in over many
    // frames: resizing tmux at each one makes the agent redraw for every
    // intermediate size while the browser screen still holds the previous one.
    let timer: number | null = null
    const scheduleFit = () => {
      if (timer !== null) window.clearTimeout(timer)
      timer = window.setTimeout(() => {
        timer = null
        fit()
      }, 120)
    }
    const observer = new ResizeObserver(scheduleFit)
    observer.observe(container)
    // Font loading and renderer changes can resize the cells without changing
    // the tile. Refit once those new screen dimensions have been applied.
    const screen = container.querySelector('.xterm-screen')
    if (screen) observer.observe(screen)
    window.visualViewport?.addEventListener('resize', scheduleFit)
    window.visualViewport?.addEventListener('scroll', scheduleFit)
    return () => {
      observer.disconnect()
      if (timer !== null) window.clearTimeout(timer)
      window.visualViewport?.removeEventListener('resize', scheduleFit)
      window.visualViewport?.removeEventListener('scroll', scheduleFit)
    }
  }, [connect, fit])

  useEffect(() => {
    const container = containerRef.current
    if (!container) return
    let lastY = 0
    let lastTime = 0
    let accumulated = 0
    let velocity = 0
    let inertiaId: number | null = null
    let lastClientX = 0
    let lastClientY = 0
    const lineHeight = 20

    const dispatchScroll = (lines: number, clientX: number, clientY: number) => {
      const target = container.querySelector('.xterm-screen')
      if (!target) return
      for (let index = 0; index < Math.abs(lines); index++) {
        target.dispatchEvent(
          new WheelEvent('wheel', {
            deltaY: lines > 0 ? lineHeight : -lineHeight,
            clientX,
            clientY,
            bubbles: true,
            cancelable: true,
          }),
        )
      }
    }

    const processAccumulated = (clientX: number, clientY: number) => {
      const speed = Math.abs(velocity)
      const multiplier = speed > 12 ? 8 : speed > 7 ? 5 : speed > 4 ? 3 : speed > 2 ? 2 : 1
      const threshold = lineHeight / multiplier
      while (Math.abs(accumulated) >= threshold) {
        const direction = accumulated > 0 ? 1 : -1
        dispatchScroll(direction, clientX, clientY)
        accumulated -= direction * threshold
      }
    }

    const stopInertia = () => {
      if (inertiaId === null) return
      window.cancelAnimationFrame(inertiaId)
      inertiaId = null
    }
    const inertiaLoop = () => {
      if (Math.abs(velocity) < 0.3) {
        velocity = 0
        accumulated = 0
        inertiaId = null
        return
      }
      velocity *= 0.92
      accumulated += velocity * 16
      processAccumulated(lastClientX, lastClientY)
      inertiaId = window.requestAnimationFrame(inertiaLoop)
    }
    const onTouchStart = (event: TouchEvent) => {
      if (event.touches.length !== 1) return
      stopInertia()
      lastY = event.touches[0].clientY
      lastTime = performance.now()
      accumulated = 0
      velocity = 0
    }
    const onTouchMove = (event: TouchEvent) => {
      if (event.touches.length !== 1) return
      event.preventDefault()
      const now = performance.now()
      const currentY = event.touches[0].clientY
      const deltaY = lastY - currentY
      const elapsed = now - lastTime
      lastClientX = event.touches[0].clientX
      lastClientY = event.touches[0].clientY
      if (elapsed > 0) velocity = velocity * 0.3 + (deltaY / elapsed) * 0.7
      accumulated += deltaY
      lastY = currentY
      lastTime = now
      processAccumulated(lastClientX, lastClientY)
    }
    const onTouchEnd = () => {
      if (Math.abs(velocity) > 0.5) inertiaId = window.requestAnimationFrame(inertiaLoop)
    }

    container.addEventListener('touchstart', onTouchStart, { passive: true })
    container.addEventListener('touchmove', onTouchMove, { passive: false })
    container.addEventListener('touchend', onTouchEnd, { passive: true })
    container.addEventListener('touchcancel', onTouchEnd, { passive: true })
    return () => {
      stopInertia()
      container.removeEventListener('touchstart', onTouchStart)
      container.removeEventListener('touchmove', onTouchMove)
      container.removeEventListener('touchend', onTouchEnd)
      container.removeEventListener('touchcancel', onTouchEnd)
    }
  }, [sessionName])

  useEffect(() => {
    const timer = window.setTimeout(() => {
      fit()
      if (!embedded && !isEditingOutsideTerminal()) focus()
    }, 100)
    return () => window.clearTimeout(timer)
  }, [fit, focus, fullscreen, zenMode, embedded])

  const showMenuAtButton = (button: HTMLButtonElement) => {
    const rect = button.getBoundingClientRect()
    setContextMenu({ x: rect.right - 160, y: rect.bottom + 4 })
  }

  return (
    <div className="relative flex min-h-0 flex-1 flex-col overflow-hidden">
      {!embedded && !zenMode && (
        <TerminalCockpit
          hostLabel={hostLabel}
          sessionStatus={sessionStatus}
          onBackToSessions={onBackToSessions}
          onHome={onHome}
          onToggleFiles={onToggleFiles}
          filesOpen={filesOpen}
          onPreviousSession={onPreviousSession}
          onNextSession={onNextSession}
          attentionCount={attentionCount}
          onOpenAlerts={onOpenAlerts}
          sessionName={sessionName}
          ownerName={ownerName}
          hostId={hostId}
          windowLabel={windowLabel}
          paneLabel={paneLabel}
          toolStatus={toolStatus}
          connectionState={workspaceConnection}
          canCopy={hasSelection}
          canPaste={canPaste}
          showScrollToBottom={!isAtBottom || hasNewOutput}
          fullscreen={fullscreen}
          zenMode={zenMode}
          mic={mic.available ? { state: mic.state, error: mic.error, onToggle: toggleMic } : undefined}
          onSearch={openSearch}
          onCopy={() => void copy().catch(() => {})}
          onPaste={() => void paste().catch(() => {})}
          onFontSize={adjustFontSize}
          onScrollToBottom={scrollToBottom}
          onToggleFullscreen={onToggleFullscreen}
          onToggleZen={onToggleZen}
          onMore={showMenuAtButton}
        />
      )}
      {showTalk &&
        talkSlot &&
        createPortal(
          renderTalk(
            !talkAgent || talkMic?.state !== 'on' || workspaceConnection !== 'connected' || talkLocked,
            embedded ? targetLabel : undefined,
            embedded || sharedMic ? 'below-right' : 'above-left',
          ),
          talkSlot,
        )}
      {!embedded && zenMode && mic.state !== 'off' && (
        <button
          type="button"
          aria-label="Désactiver le micro"
          onClick={toggleMic}
          className="absolute right-24 top-3 z-20 min-h-11 rounded border border-destructive bg-card/95 px-3 text-xs text-destructive"
        >
          Micro activé
        </button>
      )}
      {!embedded && zenMode && onToggleZen && (
        <button
          type="button"
          aria-label="Quitter le mode Zen"
          onClick={onToggleZen}
          className="absolute right-3 top-3 z-20 min-h-11 rounded border border-border bg-card/95 px-3 text-xs text-muted-foreground"
        >
          Quitter Zen
        </button>
      )}
      {!embedded && !zenMode && (
        <WindowTabs
          windows={windows}
          windowAttention={windowAttention}
          onSelectWindow={onSelectWindow}
          onSelectPane={onSelectPane}
        />
      )}
      {searchOpen && (
        <TerminalSearchBar
          state={searchState}
          onLoad={ensureSearchAddon}
          onFindNext={findNext}
          onFindPrevious={findPrevious}
          onRetry={ensureSearchAddon}
          onClose={closeSearch}
        />
      )}
      <div
        className="relative min-h-0 w-full flex-1 bg-card"
        onContextMenu={event => {
          event.preventDefault()
          setContextMenu({ x: event.clientX, y: event.clientY })
        }}
      >
        {/* FitAddon measures this box, so its spacing must use offsets rather than padding. */}
        <div
          ref={containerRef}
          data-terminal-surface
          className="absolute inset-y-0 left-1 right-0 overflow-hidden md:left-2 md:top-1"
          onClick={embedded ? focus : undefined}
        />
        {workspaceConnection !== 'connected' && (
          <div className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center rounded bg-background/80">
            <div className="pointer-events-auto flex max-w-[90%] items-center gap-3 rounded border border-border bg-card px-4 py-3 font-mono text-sm">
              <span className="h-2 w-2 shrink-0 animate-[pulse_1.5s_ease-in-out_infinite] rounded-full bg-warning" />
              <span>{terminalStateMessages[workspaceConnection]}</span>
              {(workspaceConnection === 'reconnecting' || workspaceConnection === 'hub-offline') && (
                <button
                  type="button"
                  onClick={onRetry || reconnect}
                  className="min-h-9 rounded border border-primary/50 px-3 text-primary"
                >
                  Réessayer
                </button>
              )}
            </div>
          </div>
        )}
        {imagePasteStatus && (
          <div role="status" className="px-3 py-2 text-sm">
            {imagePasteStatus}
          </div>
        )}
        {pendingPaste && (
          <TerminalPasteConfirmation
            text={pendingPaste.text}
            targetLabel={targetLabel}
            error={pasteError}
            onConfirm={confirmPaste}
            onCancel={() => {
              setPendingPaste(null)
              setPasteError('')
              focus()
            }}
          />
        )}
      </div>

      {!embedded && compact && (
        <div className="mobile-terminal-bar shrink-0 border-t border-border bg-card pb-[var(--safe-area-inset-bottom)]">
          {composerOpen ? (
            <MobileInputComposer
              key={targetKey}
              targetKey={targetKey}
              targetLabel={targetLabel}
              initialDraft={getDraft(targetKey)}
              onDraftChange={setDraft}
              onSend={sendComposerCommand}
              expanded
              onClose={() => {
                setComposerOpen(false)
                focus()
              }}
            />
          ) : (
            <div className="flex h-11 items-center" role="toolbar" aria-label="Touches du terminal">
              <div className="mobile-terminal-keys scrollbar-none flex min-w-0 flex-1 items-center gap-1 overflow-x-auto px-1.5">
                {mobileKeys.map(key =>
                  key.modifier ? (
                    <button
                      key={key.label}
                      type="button"
                      aria-label={`Modificateur ${key.modifier}`}
                      aria-pressed={modifiers[key.modifier] !== 'off'}
                      data-mode={modifiers[key.modifier]}
                      title={
                        modifiers[key.modifier] === 'locked'
                          ? `${key.content} verrouillé : touchez pour relâcher`
                          : `${key.content} pour la prochaine touche ; deux fois pour verrouiller`
                      }
                      className="mobile-key mobile-key--modifier"
                      onMouseDown={event => event.preventDefault()}
                      onClick={() => cycleModifier(key.modifier)}
                    >
                      {key.content}
                    </button>
                  ) : (
                    <button
                      key={key.label}
                      type="button"
                      aria-label={key.label}
                      className={key.wide ? 'mobile-key mobile-key--wide' : 'mobile-key'}
                      onMouseDown={event => event.preventDefault()}
                      onClick={() => {
                        sendInput(key.value)
                        if (keyboardOpen) focus()
                      }}
                    >
                      {key.content}
                    </button>
                  ),
                )}
              </div>
              <div className="flex shrink-0 items-center gap-0.5 border-l border-border pl-1 pr-1">
                <button
                  type="button"
                  aria-label={keyboardOpen ? 'Masquer le clavier' : 'Afficher le clavier'}
                  aria-pressed={keyboardOpen}
                  title={keyboardOpen ? 'Masquer le clavier' : 'Afficher le clavier'}
                  className="mobile-bar-action"
                  onMouseDown={event => event.preventDefault()}
                  onClick={() => (keyboardOpen ? hideSoftKeyboard() : focus())}
                >
                  {keyboardOpen ? <KeyboardOff size={18} aria-hidden /> : <Keyboard size={18} aria-hidden />}
                </button>
                <button
                  type="button"
                  aria-label="Composer un message"
                  aria-expanded={composerOpen}
                  title="Écrire un message complet avant de l’envoyer"
                  className="mobile-bar-action"
                  onClick={() => setComposerOpen(true)}
                >
                  <MessageSquareText size={18} aria-hidden />
                </button>
                {talkMic?.available && hostLocal && voiceAllowed && (
                  <>
                    {!sharedMic && (
                      <button
                        type="button"
                        aria-label={mic.state === 'off' ? 'Activer le micro' : 'Désactiver le micro'}
                        aria-pressed={mic.state !== 'off'}
                        className="mobile-bar-action"
                        onClick={toggleMic}
                      >
                        <Mic size={18} aria-hidden />
                      </button>
                    )}
                    {renderTalk(
                      !talkAgent || talkMic.state !== 'on' || workspaceConnection !== 'connected',
                      undefined,
                      'above-right',
                    )}
                  </>
                )}
              </div>
            </div>
          )}
        </div>
      )}
      {(toolbarError || toolbarFeedback) && (
        <p className="px-2 text-xs" role={toolbarError ? 'alert' : 'status'}>
          {toolbarError || toolbarFeedback}
        </p>
      )}

      {contextMenu && (
        <TerminalContextMenu
          position={contextMenu}
          canCopy={hasSelection}
          canPaste={canPaste}
          onCopy={() => void copy().catch(() => {})}
          onPaste={() => void paste().catch(() => {})}
          onFind={openSearch}
          onFontSize={adjustFontSize}
          onScrollToBottom={scrollToBottom}
          showScrollToBottom={!isAtBottom || hasNewOutput}
          onSelectAll={() => {
            selectAll()
            focus()
          }}
          onClose={() => {
            setContextMenu(null)
            focus()
          }}
        />
      )}
    </div>
  )
}
