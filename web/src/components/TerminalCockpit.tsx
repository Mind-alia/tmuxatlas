import { useRef, useState, type ReactNode } from 'react'
import {
  ArrowDown,
  ArrowLeft,
  Bell,
  ChevronDown,
  Clipboard,
  Copy,
  FolderOpen,
  Maximize,
  Mic,
  MoreHorizontal,
  Search,
  Sparkles,
  ZoomIn,
  ZoomOut,
} from 'lucide-react'
import { OwnerPicker } from './OwnerPicker'
import { SessionAgentsBadge } from './agents/agentSummary'
import { IconButton, Sheet, StatusIcon, StatusPill, Tooltip } from './ui'
import type { MicState } from '../hooks/useMicStream'
import type { TerminalWorkspaceConnectionState } from '../state/terminalConnection'
import { workspaceStatusLabel, type WorkspaceStatus } from '../workspace/model'

export interface TerminalCockpitMic {
  state: MicState
  error: string
  onToggle: () => void
}
interface Props {
  hostLabel: string
  sessionName: string
  ownerName?: string | null
  hostId?: string
  windowLabel?: string
  paneLabel?: string
  toolStatus?: string
  connectionState: TerminalWorkspaceConnectionState
  sessionStatus?: WorkspaceStatus
  canCopy: boolean
  canPaste: boolean
  showScrollToBottom: boolean
  fullscreen?: boolean
  zenMode?: boolean
  mic?: TerminalCockpitMic
  onSearch: () => void
  onCopy: () => void
  onPaste: () => void
  onFontSize: (delta: number) => void
  onScrollToBottom: () => void
  onToggleFullscreen?: () => void
  onToggleZen?: () => void
  onMore: (button: HTMLButtonElement) => void
  /** Opens the session drawer to switch session (phone layout). */
  onBackToSessions?: () => void
  /** Leaves the terminal for the home page and its navigation (phone layout). */
  onHome?: () => void
  /** Switches between the terminal and the session files (phone layout). */
  onToggleFiles?: () => void
  filesOpen?: boolean
  onPreviousSession?: () => void
  onNextSession?: () => void
  attentionCount?: number
  onOpenAlerts?: () => void
}
const labels: Record<TerminalWorkspaceConnectionState, string> = {
  connecting: 'Connexion',
  rehydrating: 'Synchronisation',
  connected: 'Connecté',
  reconnecting: 'Reconnexion',
  'hub-offline': 'Hub hors ligne',
  'agent-offline': 'Agent hors ligne',
  'session-ended': 'Session terminée',
  'auth-required': 'Connexion requise',
  'reload-required': 'Actualisation requise',
}

export function TerminalCockpit(p: Props) {
  const [sheet, setSheet] = useState(false)
  const [offset, setOffset] = useState(0)
  const [dragging, setDragging] = useState(false)
  const touch = useRef<{ x: number; y: number } | null>(null)
  const action = (label: string, icon: ReactNode, onClick?: () => void, disabled?: boolean) => (
    <Tooltip content={label}>
      <IconButton aria-label={label} size="sm" disabled={disabled} onClick={onClick}>
        {icon}
      </IconButton>
    </Tooltip>
  )
  const actions = [
    { label: 'Rechercher', icon: Search, run: p.onSearch },
    { label: 'Copier', icon: Copy, run: p.onCopy, disabled: !p.canCopy },
    { label: 'Coller', icon: Clipboard, run: p.onPaste, disabled: !p.canPaste },
    { label: 'Réduire la taille', icon: ZoomOut, run: () => p.onFontSize(-1) },
    { label: 'Augmenter la taille', icon: ZoomIn, run: () => p.onFontSize(1) },
    { label: p.fullscreen ? 'Quitter le plein écran' : 'Plein écran', icon: Maximize, run: p.onToggleFullscreen },
    { label: p.zenMode ? 'Quitter Zen' : 'Zen', icon: Sparkles, run: p.onToggleZen },
    { label: 'Nouvelle sortie', icon: ArrowDown, run: p.showScrollToBottom ? p.onScrollToBottom : undefined },
    { label: p.mic?.state === 'off' ? 'Activer le micro' : 'Désactiver le micro', icon: Mic, run: p.mic?.onToggle },
  ]
  return (
    <>
      <div
        className="terminal-cockpit terminal-cockpit__header flex h-11 shrink-0 items-center gap-1 px-2 text-sm"
        style={{
          transform: `translateX(${offset}px)`,
          opacity: offset ? 0.75 : 1,
          transition: dragging ? 'none' : 'transform var(--motion-fast), opacity var(--motion-fast)',
        }}
        onTouchStart={event => {
          const point = event.touches[0]
          if (point) {
            touch.current = { x: point.clientX, y: point.clientY }
            setDragging(true)
          }
        }}
        onTouchMove={event => {
          const point = event.touches[0]
          const start = touch.current
          if (!point || !start) return
          const dx = point.clientX - start.x
          if (Math.abs(dx) > Math.abs(point.clientY - start.y) * 1.3) setOffset(Math.max(-48, Math.min(48, dx * 0.4)))
        }}
        onTouchCancel={() => {
          touch.current = null
          setDragging(false)
          setOffset(0)
        }}
        onTouchEnd={event => {
          const point = event.changedTouches[0]
          const start = touch.current
          touch.current = null
          setDragging(false)
          setOffset(0)
          if (!point || !start) return
          const dx = point.clientX - start.x
          const dy = point.clientY - start.y
          if (Math.abs(dx) >= 60 && Math.abs(dx) > Math.abs(dy) * 1.3) {
            ;(dx > 0 ? p.onPreviousSession : p.onNextSession)?.()
          }
        }}
      >
        <div className="md:hidden">
          {p.onHome && action('Retour à l’accueil', <ArrowLeft size={18} />, p.onHome)}
        </div>
        <div className="flex min-w-0 flex-1 items-center gap-2 overflow-hidden">
          <span className="hidden truncate text-muted-foreground md:inline">{p.hostLabel} /</span>
          {p.onBackToSessions ? (
            <>
              <button
                type="button"
                aria-label={`Changer de session (actuelle : ${p.sessionName})`}
                title="Changer de session"
                onClick={p.onBackToSessions}
                className="ui-focus-ring -ml-1 flex min-h-11 min-w-0 items-center gap-1 rounded px-1 md:hidden"
              >
                <span className="truncate font-medium">{p.sessionName}</span>
                <ChevronDown size={16} aria-hidden className="shrink-0 text-muted-foreground" />
              </button>
              <span className="hidden truncate md:inline">{p.sessionName}</span>
            </>
          ) : (
            <span className="truncate">{p.sessionName}</span>
          )}
          <OwnerPicker
            hostId={p.hostId ?? ''}
            session={p.sessionName}
            ownerName={p.ownerName}
            testId="terminal-owner-badge"
          />
          {p.sessionStatus && (
            <>
              <StatusIcon status={p.sessionStatus} className="md:hidden" />
              <span className="hidden md:inline-flex">
                <StatusPill status={p.sessionStatus}>{workspaceStatusLabel(p.sessionStatus)}</StatusPill>
              </span>
            </>
          )}
          <SessionAgentsBadge hostId={p.hostId} session={p.sessionName} variant="link" />
          {p.connectionState !== 'connected' && (
            <span
              role="status"
              title={labels[p.connectionState]}
              aria-label={labels[p.connectionState]}
              className="hidden h-2 w-2 shrink-0 rounded-full bg-warning md:inline-block"
            />
          )}
        </div>
        {p.mic?.error && (
          <span role="alert" className="truncate text-xs text-destructive">
            {p.mic.error}
          </span>
        )}
        {p.showScrollToBottom && (
          <button type="button" onClick={p.onScrollToBottom} className="hidden shrink-0 text-xs text-primary md:block">
            Nouvelle sortie <ArrowDown size={14} className="inline" />
          </button>
        )}
        <div className="hidden items-center gap-1 md:flex">
          {p.mic &&
            action(
              p.mic.state === 'off' ? 'Activer le micro' : 'Désactiver le micro',
              <Mic size={16} />,
              p.mic.onToggle,
            )}
          {action('Rechercher', <Search size={16} />, p.onSearch)}
          {action('Copier', <Copy size={16} />, p.onCopy, !p.canCopy)}
          {action('Coller', <Clipboard size={16} />, p.onPaste, !p.canPaste)}
          {p.onToggleFullscreen && action('Plein écran', <Maximize size={16} />, p.onToggleFullscreen)}
          {p.onToggleZen && action('Zen', <Sparkles size={16} />, p.onToggleZen)}
          <Tooltip content="Plus">
            <IconButton aria-label="Plus" size="sm" onClick={event => p.onMore(event.currentTarget)}>
              <MoreHorizontal size={16} />
            </IconButton>
          </Tooltip>
        </div>
        <div className="flex items-center md:hidden">
          {p.onOpenAlerts && Boolean(p.attentionCount) && (
            <Tooltip content="Sessions à traiter">
              <button
                type="button"
                aria-label={`Alertes (${p.attentionCount})`}
                onClick={p.onOpenAlerts}
                className="ui-focus-ring flex min-h-11 min-w-11 items-center justify-center gap-1 rounded text-warning"
              >
                <Bell size={16} aria-hidden />
                <span className="text-xs tabular-nums">{p.attentionCount}</span>
              </button>
            </Tooltip>
          )}
          {p.onToggleFiles && (
            <Tooltip content={p.filesOpen ? 'Revenir au terminal' : 'Fichiers de la session'}>
              <button
                type="button"
                aria-label="Fichiers de la session"
                aria-pressed={Boolean(p.filesOpen)}
                onClick={p.onToggleFiles}
                className="mobile-bar-action"
              >
                <FolderOpen size={18} aria-hidden />
              </button>
            </Tooltip>
          )}
          {action('Plus', <MoreHorizontal size={18} />, () => setSheet(true))}
        </div>
      </div>
      <Sheet open={sheet} onOpenChange={setSheet} title="Actions du terminal" closeLabel="Fermer">
        <div className="flex flex-col">
          {actions
            .filter(item => item.run)
            .map(item => (
              <button
                key={item.label}
                type="button"
                disabled={item.disabled}
                className="flex min-h-11 items-center gap-3 text-left disabled:opacity-40"
                onClick={() => {
                  setSheet(false)
                  item.run?.()
                }}
              >
                <item.icon size={16} aria-hidden="true" />
                {item.label}
              </button>
            ))}
        </div>
      </Sheet>
    </>
  )
}
