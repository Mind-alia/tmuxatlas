import { HeartPulse, Loader2, MoreVertical, Play, RotateCw, Square, SquareTerminal, Trash2 } from 'lucide-react'
import { useLayoutEffect, useRef, useState, type KeyboardEvent, type ReactNode, type RefObject } from 'react'
import { Button, Dialog, IconButton, Menu, MenuItem } from '../../ui'
import { toneColor } from '../../monitoring/primitives'
import { canDelete, canStop, healthLabel, type DotTone } from './model'
import type { DockerAction, DockerContainer, DockerHealth } from './types'

export type PendingKind = DockerAction | 'terminal'

const dotColor: Record<DotTone, string> = {
  running: 'var(--success)',
  stopped: 'var(--muted-foreground)',
  transition: 'var(--warning)',
  error: 'var(--destructive)',
}

/**
 * The status dot. Its label is read by screen readers unless decorative is
 * set, for places where the same label is already written next to it.
 */
export function StatusDot({ tone, label, pulse, decorative }: { tone: DotTone; label: string; pulse?: boolean; decorative?: boolean }) {
  return (
    <span className="relative inline-flex size-2.5 shrink-0" title={label}>
      {pulse && (
        <span
          aria-hidden
          className="absolute inline-flex size-full animate-ping rounded-full opacity-60"
          style={{ background: dotColor[tone] }}
        />
      )}
      <span
        aria-hidden
        data-tone={tone}
        className="relative inline-flex size-2.5 rounded-full"
        style={{ background: dotColor[tone], boxShadow: tone === 'stopped' ? 'inset 0 0 0 1px var(--border)' : undefined }}
      />
      {!decorative && <span className="sr-only">{label}</span>}
    </span>
  )
}

export function HealthBadge({ health }: { health?: DockerHealth }) {
  if (!health) return null
  const tone = health === 'healthy' ? 'ok' : health === 'unhealthy' ? 'critical' : 'warning'
  if (tone === 'ok') {
    return (
      <span className="inline-flex items-center gap-1 whitespace-nowrap text-2xs text-muted-foreground">
        <HeartPulse size={12} aria-hidden />
        {healthLabel[health]}
      </span>
    )
  }
  return (
    <span
      className="inline-flex items-center gap-1 whitespace-nowrap rounded px-1.5 py-[1px] text-2xs font-medium"
      style={{ color: toneColor[tone], background: `color-mix(in oklch, ${toneColor[tone]} 12%, transparent)` }}
    >
      <HeartPulse size={12} aria-hidden />
      {healthLabel[health]}
    </span>
  )
}

export interface TabItem<T extends string> {
  id: T
  label: ReactNode
}

/** Underlined tabs with arrow-key navigation (the panel carries role="tabpanel"). */
export function Tabs<T extends string>({
  items,
  value,
  onChange,
  label,
  idPrefix,
}: {
  items: TabItem<T>[]
  value: T
  onChange: (value: T) => void
  label: string
  idPrefix: string
}) {
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const index = items.findIndex(item => item.id === value)
    let next = index
    if (event.key === 'ArrowRight') next = (index + 1) % items.length
    else if (event.key === 'ArrowLeft') next = (index - 1 + items.length) % items.length
    else if (event.key === 'Home') next = 0
    else if (event.key === 'End') next = items.length - 1
    else return
    event.preventDefault()
    onChange(items[next].id)
    event.currentTarget.querySelector<HTMLElement>(`[data-tab="${items[next].id}"]`)?.focus()
  }
  return (
    <div
      role="tablist"
      aria-label={label}
      onKeyDown={onKeyDown}
      className="flex min-w-0 items-end gap-1 overflow-x-auto border-b border-border text-xs"
    >
      {items.map(item => (
        <button
          key={item.id}
          type="button"
          role="tab"
          id={`${idPrefix}-tab-${item.id}`}
          aria-controls={`${idPrefix}-panel`}
          data-tab={item.id}
          aria-selected={value === item.id}
          tabIndex={value === item.id ? 0 : -1}
          onClick={() => onChange(item.id)}
          className="ui-focus-ring relative flex h-9 shrink-0 items-center gap-1.5 whitespace-nowrap px-3 text-muted-foreground after:absolute after:inset-x-2 after:bottom-0 after:h-0.5 hover:text-foreground aria-selected:text-foreground aria-selected:after:bg-primary"
        >
          {item.label}
        </button>
      ))}
    </div>
  )
}

export interface ConfirmRequest {
  container: DockerContainer
  action: DockerAction
}

const confirmText: Record<'stop' | 'restart' | 'delete', { title: string; body: string; button: string }> = {
  stop: {
    title: 'Arrêter',
    body: 'Le service peut être coupé pendant l’opération. Docker envoie le signal d’arrêt, puis tue le conteneur s’il ne s’arrête pas à temps.',
    button: 'Arrêter',
  },
  restart: {
    title: 'Redémarrer',
    body: 'Le service peut être coupé pendant l’opération, le temps que le conteneur s’arrête puis redémarre.',
    button: 'Redémarrer',
  },
  delete: {
    title: 'Supprimer',
    body: 'La suppression est définitive : le conteneur et les fichiers écrits hors de ses volumes sont perdus. Les volumes, eux, sont conservés.',
    button: 'Supprimer définitivement',
  },
}

/** Asks before an action that can cut a service or lose data. */
export function ConfirmDialog({
  request,
  onCancel,
  onConfirm,
}: {
  request: ConfirmRequest | null
  onCancel: () => void
  onConfirm: (request: ConfirmRequest) => void
}) {
  const text = request && request.action !== 'start' ? confirmText[request.action] : null
  // The safe choice has the focus: Enter alone never stops or deletes.
  const cancelRef = useRef<HTMLButtonElement>(null)
  return (
    <Dialog
      open={Boolean(request && text)}
      onOpenChange={open => {
        if (!open) onCancel()
      }}
      initialFocusRef={cancelRef}
      role="alertdialog"
      title={request && text ? `${text.title} « ${request.container.name} » ?` : ''}
      description={text?.body}
      footer={
        request &&
        text && (
          <>
            <Button ref={cancelRef} variant="ghost" onClick={onCancel}>
              Annuler
            </Button>
            <Button
              variant={request.action === 'restart' ? 'primary' : 'destructive'}
              onClick={() => onConfirm(request)}
            >
              {text.button}
            </Button>
          </>
        )
      }
    >
      {request?.container.compose_project && (
        <p className="text-xs text-muted-foreground">
          Ce conteneur fait partie du projet Compose « {request.container.compose_project} »
          {request.container.compose_service ? ` (service ${request.container.compose_service})` : ''}.
        </p>
      )}
    </Dialog>
  )
}

/** Start/stop, restart, terminal and the ⋮ menu of one container. */
export function ContainerActions({
  container: c,
  pending,
  terminal,
  onAction,
  onOpen,
  onTerminal,
  compact = false,
}: {
  container: DockerContainer
  pending?: PendingKind
  terminal: boolean
  onAction: (c: DockerContainer, action: DockerAction) => void
  onOpen: (c: DockerContainer, tab?: DetailTab) => void
  onTerminal: (c: DockerContainer) => void
  compact?: boolean
}) {
  const busy = pending !== undefined
  const running = canStop(c)
  const terminalHint = !terminal
    ? 'Terminal indisponible sur ce serveur'
    : c.state !== 'running'
      ? 'Démarre le conteneur pour ouvrir un terminal'
      : `Ouvrir un terminal dans ${c.name}`
  return (
    <div
      className="flex items-center justify-end gap-0.5"
      onClick={event => event.stopPropagation()}
      onKeyDown={event => event.stopPropagation()}
    >
      {pending !== undefined && (
        <span role="status" className="mr-1 inline-flex items-center gap-1 text-2xs text-muted-foreground">
          <Loader2 size={14} className="animate-spin" aria-hidden />
          {pendingLabel[pending]}
        </span>
      )}
      <IconButton
        size="sm"
        aria-label={running ? `Arrêter ${c.name}` : `Démarrer ${c.name}`}
        title={running ? 'Arrêter' : 'Démarrer'}
        disabled={busy}
        onClick={() => onAction(c, running ? 'stop' : 'start')}
      >
        {running ? <Square size={15} aria-hidden /> : <Play size={15} aria-hidden />}
      </IconButton>
      {!compact && (
        <IconButton
          size="sm"
          aria-label={`Redémarrer ${c.name}`}
          title="Redémarrer"
          disabled={busy}
          onClick={() => onAction(c, 'restart')}
        >
          <RotateCw size={15} aria-hidden />
        </IconButton>
      )}
      <IconButton
        size="sm"
        aria-label={`Ouvrir un terminal dans ${c.name}`}
        title={terminalHint}
        disabled={busy || !terminal || c.state !== 'running'}
        onClick={() => onTerminal(c)}
      >
        <SquareTerminal size={15} aria-hidden />
      </IconButton>
      <Menu label={`Plus d’actions pour ${c.name}`} trigger={<MoreVertical size={16} aria-hidden />}>
        <MenuItem onSelect={() => onOpen(c)}>Détails</MenuItem>
        <MenuItem onSelect={() => onOpen(c, 'logs')}>Journaux</MenuItem>
        <MenuItem onSelect={() => onOpen(c, 'inspect')}>Inspecter</MenuItem>
        <MenuItem onSelect={() => onOpen(c, 'stats')}>Statistiques</MenuItem>
        {compact && (
          <MenuItem disabled={busy} onSelect={() => onAction(c, 'restart')}>
            Redémarrer
          </MenuItem>
        )}
        <MenuItem
          variant="destructive"
          disabled={busy || !canDelete(c)}
          title={canDelete(c) ? undefined : 'Arrête le conteneur avant de le supprimer'}
          onSelect={() => onAction(c, 'delete')}
        >
          <Trash2 size={14} aria-hidden />
          Supprimer
        </MenuItem>
      </Menu>
    </div>
  )
}

export type DetailTab = 'logs' | 'inspect' | 'stats' | 'terminal'

export const pendingLabel: Record<PendingKind, string> = {
  start: 'Démarrage…',
  stop: 'Arrêt…',
  restart: 'Redémarrage…',
  delete: 'Suppression…',
  terminal: 'Ouverture…',
}

/** True when the element is narrower than threshold (cards instead of the table). */
export function useNarrow(ref: RefObject<HTMLElement | null>, threshold: number) {
  const [narrow, setNarrow] = useState(false)
  useLayoutEffect(() => {
    const element = ref.current
    if (!element) return
    const update = () => {
      const width = element.getBoundingClientRect().width
      setNarrow(width > 0 && width < threshold)
    }
    update()
    if (typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(update)
    observer.observe(element)
    return () => observer.disconnect()
  }, [ref, threshold])
  return narrow
}
