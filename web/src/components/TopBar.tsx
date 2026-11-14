import { useEffect, useRef, useState } from 'react'
import { Bell, CircleHelp, LogOut, Mic, MicOff, PanelLeft, Plus, RefreshCw, Search, Settings, X } from 'lucide-react'
import type { useMicStream } from '../hooks/useMicStream'
import type { AccountRole, AuthMode } from '../hooks/useAuth'
import type { ToolEvent } from '../hooks/useToolEvents'
import { usePreferences } from '../hooks/usePreferences'
import { formatShortcut } from '../commands/registry'
import { AtlasMark } from './Brand'
import { Kbd, Menu, StatusIcon } from './ui'

interface TopBarProps {
  currentView: string
  /** True when the desktop Sidebar has no width left to hold its own toggle. */
  sidebarHidden: boolean
  role?: AccountRole | null
  authMode?: AuthMode
  username?: string | null
  onToggleCollapse: () => void
  onOverview: () => void
  onMonitoring: () => void
  onWall: () => void
  onFiles?: () => void
  onAdmin?: () => void
  onOpenPalette: () => void
  onSettings: () => void
  onLogout?: () => void
  onNewSession?: () => void
  mic?: ReturnType<typeof useMicStream>
  onTalkSlotChange?: (slot: HTMLElement | null) => void
  onHelp?: () => void
  updateAvailable?: boolean
  events: ToolEvent[]
  onJumpToSession: (session: string, windowIndex?: number, pane?: string) => void
  onDismiss: (evt: ToolEvent) => void
  onDismissAll: () => void
}

function relativeTime(timestamp: string) {
  const date = Date.parse(timestamp)
  if (Number.isNaN(date)) return ''
  const seconds = Math.round((date - Date.now()) / 1000)
  const format = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' })
  if (Math.abs(seconds) < 60) return format.format(seconds, 'second')
  if (Math.abs(seconds) < 3600) return format.format(Math.round(seconds / 60), 'minute')
  if (Math.abs(seconds) < 86400) return format.format(Math.round(seconds / 3600), 'hour')
  return format.format(Math.round(seconds / 86400), 'day')
}

export function TopBar({
  currentView,
  sidebarHidden,
  role,
  authMode,
  onToggleCollapse,
  onOverview,
  onMonitoring,
  onWall,
  onFiles,
  onAdmin,
  onOpenPalette,
  onSettings,
  onLogout,
  onNewSession,
  mic,
  onTalkSlotChange,
  onHelp,
  updateAvailable,
  events,
  onJumpToSession,
  onDismiss,
  onDismissAll,
}: TopBarProps) {
  const actionable = events.filter(event => event.status === 'waiting' || event.status === 'error')
  const { prefs } = usePreferences()
  const timers = useRef<Map<string, number>>(new Map())
  const [dismissedMicError, setDismissedMicError] = useState('')
  // A new attempt shows its error again, even when the message is the same.
  useEffect(() => {
    if (mic?.state === 'starting') setDismissedMicError('')
  }, [mic?.state])
  useEffect(() => {
    const seconds = prefs.agent_banner.auto_dismiss_seconds
    if (seconds <= 0) return
    events.forEach((event, index) => {
      if (event.status !== 'waiting' && event.status !== 'error') return
      const key = `${event.tool}-${event.session}-${event.pane}-${index}`
      if (timers.current.has(key)) return
      timers.current.set(
        key,
        window.setTimeout(() => {
          onDismiss(event)
          timers.current.delete(key)
        }, seconds * 1000),
      )
    })
    const current = timers.current
    return () => {
      current.forEach(clearTimeout)
      current.clear()
    }
  }, [events, prefs.agent_banner.auto_dismiss_seconds, onDismiss])

  const button =
    'ui-focus-ring flex min-h-11 min-w-11 items-center justify-center gap-2 rounded px-2 ' +
    'text-muted-foreground hover:bg-secondary hover:text-foreground md:min-h-8 md:min-w-8'
  const navigation: Array<readonly [string, string, () => void]> = [
    ['overview', 'Vue d’ensemble', onOverview],
    ['wall', 'Mur', onWall],
    ['monitoring', 'Monitoring', onMonitoring] as const,
    ...(onFiles ? ([['files', 'Fichiers', onFiles]] as const) : []),
    ...(role === 'admin' && onAdmin ? ([['admin', 'Administration', onAdmin]] as const) : []),
  ]
  const alertColor = actionable.some(event => event.status === 'error')
    ? 'text-destructive'
    : actionable.length
      ? 'text-warning'
      : 'text-muted-foreground'
  const openSidebarLabel = 'Afficher la barre latérale'
  const micLabel = mic?.state === 'on' ? 'Micro actif' : mic?.state === 'starting' ? 'Connexion du micro' : 'Réessayer le micro'

  return (
    <header className="flex min-h-12 shrink-0 items-center gap-1 border-b border-border bg-card px-2 pt-[var(--safe-area-inset-top)] font-sans text-sm md:gap-3 md:px-4">
      <button
        type="button"
        onClick={onOverview}
        aria-label="Vue d’ensemble"
        className="ui-focus-ring flex min-w-0 items-center gap-2 rounded"
      >
        <AtlasMark />
        <span>Atlas</span>
      </button>
      <nav aria-label="Navigation principale" className="hidden items-center gap-1 md:flex">
        {navigation.map(([view, label, action]) => (
          <button
            key={view}
            type="button"
            onClick={action}
            aria-current={currentView === view ? 'page' : undefined}
            className={`ui-focus-ring flex min-h-10 items-center justify-center rounded-md border px-3 transition-colors ${
              currentView === view
                ? 'border-primary/40 bg-primary/15 text-base font-semibold text-primary shadow-sm'
                : 'border-transparent text-sm text-muted-foreground hover:bg-secondary hover:text-foreground'
            }`}
          >
            {label}
          </button>
        ))}
      </nav>
      <div className="flex-1" />
      <span ref={onTalkSlotChange} className="hidden shrink-0 items-center empty:hidden md:flex" />
      {mic?.available && (
        <div className="relative flex shrink-0 items-center">
          <button
            type="button"
            disabled={mic.state !== 'off'}
            onClick={mic.toggle}
            aria-label={micLabel}
            title={mic.error || 'Le micro reste actif pour la dictée'}
            className={`ui-focus-ring flex min-h-8 min-w-8 items-center justify-center gap-2 rounded-full px-2 text-xs ${mic.state === 'on' ? 'bg-success/10 text-success' : mic.error ? 'bg-destructive/10 text-destructive' : 'text-muted-foreground'}`}
          >
            {mic.state === 'off' ? <MicOff size={16} aria-hidden /> : <Mic size={16} aria-hidden />}
            <span className="hidden xl:inline">{mic.state === 'on' ? 'Micro actif' : mic.state === 'starting' ? 'Connexion…' : 'Réessayer'}</span>
          </button>
          {mic.error && dismissedMicError !== mic.error && (
            <span
              role="alert"
              className="fixed inset-x-2 top-[calc(var(--visual-viewport-offset-top)+var(--safe-area-inset-top)+3.25rem)] z-50 flex items-start gap-2 rounded-md border border-destructive/30 bg-card p-3 text-xs text-destructive shadow-lg md:absolute md:inset-x-auto md:right-0 md:top-full md:mt-2 md:w-64"
            >
              <span className="min-w-0 flex-1">{mic.error}</span>
              <button
                type="button"
                aria-label="Masquer l’erreur du micro"
                className="ui-focus-ring -m-2 grid min-h-9 min-w-9 shrink-0 place-items-center rounded text-muted-foreground hover:text-foreground"
                onClick={() => setDismissedMicError(mic.error)}
              >
                <X size={14} aria-hidden />
              </button>
            </span>
          )}
        </div>
      )}
      {updateAvailable && (
        <button type="button" className={`${button} text-warning`} onClick={() => window.location.reload()} aria-label="Installer la mise à jour" title="Recharger pour installer la mise à jour">
          <RefreshCw size={16} aria-hidden />
        </button>
      )}
      {currentView !== 'wall' && onNewSession && (
        <button
          type="button"
          className={button}
          onClick={onNewSession}
          title="Nouvelle session"
          aria-label="Nouvelle session"
        >
          <Plus size={16} aria-hidden />
        </button>
      )}
      <div className="hidden items-center md:flex">
        {/* The Sidebar holds this toggle on desktop; it is only needed here where the Sidebar is off-screen. */}
        <button
          type="button"
          className={`${button} ${sidebarHidden ? '' : 'lg:hidden'}`}
          onClick={onToggleCollapse}
          title={openSidebarLabel}
          aria-label={openSidebarLabel}
        >
          <PanelLeft size={16} aria-hidden />
        </button>
      </div>
      <Menu
        label={`Alertes (${actionable.length})`}
        contentClassName="ui-menu__content--phone-sheet"
        trigger={
          <span className={`flex items-center gap-1 tabular-nums ${alertColor}`}>
            <Bell size={16} aria-hidden />
            {actionable.length}
          </span>
        }
      >
        <div className="max-h-[60vh] w-full overflow-y-auto p-2 md:w-[min(22rem,calc(100vw-2rem))]">
          {actionable.length === 0 && <p className="p-2 text-xs text-muted-foreground">Aucune alerte</p>}
          {actionable.map((event, index) => (
            <div
              key={`${event.host}-${event.session}-${event.pane}-${index}`}
              className="flex items-start gap-1 border-b border-border py-1 last:border-0"
            >
              <button
                type="button"
                role="menuitem"
                className="ui-focus-ring flex min-h-11 min-w-0 flex-1 items-start gap-2 rounded p-2 text-left hover:bg-secondary"
                onClick={() =>
                  onJumpToSession(
                    event.host ? `${event.host}/${event.session}` : event.session,
                    event.window,
                    event.pane,
                  )
                }
              >
                <StatusIcon
                  status={event.status === 'error' ? 'error' : 'waiting'}
                  className={event.status === 'error' ? 'text-destructive' : 'text-warning'}
                />
                <span className="min-w-0 text-xs">
                  <span className="block">
                    {event.session} · {event.tool}
                  </span>
                  <span className="block break-words text-muted-foreground">
                    {event.message || (event.status === 'error' ? 'Erreur' : 'En attente')}
                  </span>
                  <time className="text-2xs text-muted-foreground" dateTime={event.timestamp}>
                    {relativeTime(event.timestamp)}
                  </time>
                </span>
              </button>
              <button
                type="button"
                role="menuitem"
                className="ui-focus-ring min-h-11 rounded px-2 text-xs text-muted-foreground hover:text-foreground"
                onClick={() => onDismiss(event)}
              >
                Ignorer
              </button>
            </div>
          ))}
          {actionable.length > 0 && (
            <button
              type="button"
              role="menuitem"
              className="ui-focus-ring min-h-11 w-full rounded px-2 text-left text-xs hover:bg-secondary"
              onClick={onDismissAll}
            >
              Tout ignorer
            </button>
          )}
        </div>
      </Menu>
      <button type="button" className={button} onClick={onOpenPalette} aria-label="Rechercher" title="Rechercher">
        <Search size={16} aria-hidden />
        <span className="hidden lg:inline">Rechercher</span>
        <span className="hidden lg:inline">
          <Kbd className="font-normal text-muted-foreground">
            {formatShortcut(prefs.quick_switcher_shortcut || 'mod+k')}
          </Kbd>
        </span>
      </button>
      {/* On a phone, Settings and Log out live in the bottom navigation's More sheet. */}
      <button
        type="button"
        className={`${button} hidden md:flex`}
        onClick={onSettings}
        aria-label="Réglages"
        title="Réglages"
        aria-current={currentView === 'settings' ? 'page' : undefined}
      >
        <Settings size={16} aria-hidden />
      </button>
      {onHelp && (
        <button type="button" className={`${button} hidden lg:flex`} onClick={onHelp} aria-label="Raccourcis clavier" title="Raccourcis clavier">
          <CircleHelp size={16} aria-hidden />
        </button>
      )}
      {authMode === 'accounts' && onLogout && (
        <button type="button" className={`${button} hidden md:flex`} onClick={onLogout} aria-label="Se déconnecter" title="Se déconnecter">
          <LogOut size={16} aria-hidden />
          <span className="hidden lg:inline">Se déconnecter</span>
        </button>
      )}
    </header>
  )
}
