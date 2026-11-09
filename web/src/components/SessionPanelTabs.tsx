import { FolderOpen, SquareTerminal } from 'lucide-react'
import type { KeyboardEvent } from 'react'

export type SessionPanel = 'terminal' | 'files'

const panels = [
  { id: 'terminal', label: 'Terminal', icon: SquareTerminal },
  { id: 'files', label: 'Fichiers', icon: FolderOpen },
] as const

/** Switches a session between its terminal and the file explorer of its host. */
export function SessionPanelTabs({ value, onChange }: { value: SessionPanel; onChange: (panel: SessionPanel) => void }) {
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
    event.preventDefault()
    const next = value === 'terminal' ? 'files' : 'terminal'
    onChange(next)
    event.currentTarget.querySelector<HTMLElement>(`[data-panel="${next}"]`)?.focus()
  }
  return (
    <div
      role="tablist"
      aria-label="Vue de la session"
      onKeyDown={onKeyDown}
      className="flex h-9 shrink-0 items-end gap-1 border-b border-border bg-card px-2 text-xs"
    >
      {panels.map(({ id, label, icon: Icon }) => (
        <button
          key={id}
          type="button"
          role="tab"
          data-panel={id}
          aria-selected={value === id}
          tabIndex={value === id ? 0 : -1}
          onClick={() => onChange(id)}
          className="ui-focus-ring relative flex h-9 items-center gap-1.5 px-3 text-muted-foreground after:absolute after:inset-x-2 after:bottom-0 after:h-0.5 hover:text-foreground aria-selected:text-foreground aria-selected:after:bg-primary"
        >
          <Icon size={14} aria-hidden />
          {label}
        </button>
      ))}
    </div>
  )
}
