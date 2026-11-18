import { useState } from 'react'
import {
  Activity,
  Container,
  FolderOpen,
  Inbox,
  LayoutGrid,
  List,
  LogOut,
  Menu as MenuIcon,
  Plus,
  RefreshCw,
  Settings,
  Shield,
  type LucideIcon,
} from 'lucide-react'
import type { AccountRole } from '../hooks/useAuth'
import { Sheet } from './ui'

interface Destination {
  label: string
  icon: LucideIcon
  action: () => void
  active?: boolean
}

export function MobileNav({
  currentView,
  attentionCount,
  role,
  onOverview,
  onSessions,
  onMonitoring,
  onWall,
  onLoops,
  onFiles,
  onOps,
  onAdmin,
  onSettings,
  onNewSession,
  onLogout,
}: {
  currentView: string
  attentionCount: number
  role?: AccountRole | null
  onOverview: () => void
  onSessions: () => void
  onMonitoring: () => void
  onWall?: () => void
  onLoops?: () => void
  onFiles?: () => void
  onOps?: () => void
  onAdmin?: () => void
  onSettings: () => void
  onNewSession?: () => void
  onLogout?: () => void
}) {
  const [moreOpen, setMoreOpen] = useState(false)
  const primary: Destination[] = [
    { label: 'À traiter', icon: Inbox, action: onOverview, active: currentView === 'overview' },
    { label: 'Sessions', icon: List, action: onSessions },
    ...(onLoops ? [{ label: 'Boucles', icon: RefreshCw, action: onLoops, active: currentView === 'loops' }] : []),
    ...(onFiles ? [{ label: 'Fichiers', icon: FolderOpen, action: onFiles, active: currentView === 'files' }] : []),
    { label: 'Monitoring', icon: Activity, action: onMonitoring, active: currentView === 'monitoring' },
  ]
  const secondary: Destination[] = [
    ...(onNewSession ? [{ label: 'Nouvelle session', icon: Plus, action: onNewSession }] : []),
    ...(onWall ? [{ label: 'Mur de sessions', icon: LayoutGrid, action: onWall, active: currentView === 'wall' }] : []),
    ...(onOps ? [{ label: 'Conteneurs', icon: Container, action: onOps, active: currentView === 'ops' }] : []),
    ...(role === 'admin' && onAdmin
      ? [{ label: 'Administration', icon: Shield, action: onAdmin, active: currentView === 'admin' }]
      : []),
    { label: 'Réglages', icon: Settings, action: onSettings, active: currentView === 'settings' },
  ]
  const moreActive = secondary.some(item => item.active)

  return (
    <>
      <nav
        aria-label="Navigation mobile"
        className="flex shrink-0 border-t border-border bg-card pb-[var(--safe-area-inset-bottom)] md:hidden"
      >
        {primary.map(({ label, icon: Icon, action, active }) => (
          <button
            key={label}
            type="button"
            onClick={action}
            aria-current={active ? 'page' : undefined}
            className={`ui-focus-ring relative flex min-h-14 min-w-0 flex-1 flex-col items-center justify-center gap-1 px-0.5 text-2xs ${active ? 'text-primary' : 'text-muted-foreground'}`}
          >
            <span className="relative">
              <Icon size={20} aria-hidden />
              {label === 'À traiter' && attentionCount > 0 && (
                <span className="absolute -right-2.5 -top-1.5 min-w-4 rounded-full bg-warning px-1 text-center text-2xs font-semibold leading-4 tabular-nums text-background">
                  {attentionCount}
                </span>
              )}
            </span>
            <span className="max-w-full truncate">{label}</span>
          </button>
        ))}
        <button
          type="button"
          onClick={() => setMoreOpen(true)}
          aria-haspopup="dialog"
          aria-expanded={moreOpen}
          aria-current={moreActive ? 'page' : undefined}
          className={`ui-focus-ring flex min-h-14 min-w-0 flex-1 flex-col items-center justify-center gap-1 px-0.5 text-2xs ${moreActive ? 'text-primary' : 'text-muted-foreground'}`}
        >
          <MenuIcon size={20} aria-hidden />
          Plus
        </button>
      </nav>
      <Sheet open={moreOpen} onOpenChange={setMoreOpen} title="Plus" closeLabel="Fermer">
        <div className="flex flex-col">
          {secondary.map(({ label, icon: Icon, action, active }) => (
            <button
              key={label}
              type="button"
              aria-current={active ? 'page' : undefined}
              className={`ui-focus-ring flex min-h-12 items-center gap-3 rounded px-1 text-left text-sm ${active ? 'text-primary' : ''}`}
              onClick={() => {
                setMoreOpen(false)
                action()
              }}
            >
              <Icon size={18} aria-hidden />
              {label}
            </button>
          ))}
          {onLogout && (
            <button
              type="button"
              className="ui-focus-ring mt-1 flex min-h-12 items-center gap-3 rounded border-t border-border px-1 text-left text-sm text-destructive"
              onClick={() => {
                setMoreOpen(false)
                onLogout()
              }}
            >
              <LogOut size={18} aria-hidden />
              Se déconnecter
            </button>
          )}
        </div>
      </Sheet>
    </>
  )
}
