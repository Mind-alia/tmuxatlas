import { useEffect, useRef, useState, type DragEvent, type KeyboardEvent, type MouseEvent } from 'react'
import {
  ArrowDown,
  ArrowUp,
  File,
  FileArchive,
  FileAudio,
  FileCode,
  FileImage,
  FileJson,
  FileSymlink,
  FileText,
  FileVideo,
  Folder,
  FolderOpen,
  type LucideIcon,
} from 'lucide-react'
import { cn } from '../../lib/utils'
import { hasExternalFiles, INTERNAL_DRAG_TYPE, isInternalDrag, setDragOut } from './dropTransfer'
import { downloadURL, extension, fileKind, formatDate, formatSize, isDirectory, type FileEntry } from './filesApi'

export type SortKey = 'name' | 'size' | 'mtime'
export interface SortState {
  key: SortKey
  descending: boolean
}

const codeExt = new Set([
  'js', 'jsx', 'ts', 'tsx', 'mjs', 'cjs', 'go', 'py', 'rs', 'c', 'h', 'cpp', 'hpp', 'cc', 'java', 'kt', 'cs', 'php',
  'rb', 'lua', 'sh', 'bash', 'zsh', 'fish', 'sql', 'css', 'scss', 'less', 'html', 'htm', 'xml', 'vue', 'svelte',
  'yaml', 'yml', 'toml', 'ini', 'conf', 'mod', 'sum', 'lock', 'env', 'dockerfile', 'makefile', 'service',
])

export function iconFor(entry: Pick<FileEntry, 'name' | 'type' | 'link_dir'>, open = false): { icon: LucideIcon; tone: string } {
  if (isDirectory(entry)) return { icon: open ? FolderOpen : Folder, tone: 'text-primary' }
  if (entry.type === 'symlink') return { icon: FileSymlink, tone: 'text-muted-foreground' }
  const ext = extension(entry.name)
  switch (fileKind(entry.name)) {
    case 'image':
      return { icon: FileImage, tone: 'text-[var(--mon-b)]' }
    case 'video':
      return { icon: FileVideo, tone: 'text-[var(--chart-secondary)]' }
    case 'audio':
      return { icon: FileAudio, tone: 'text-[var(--chart-secondary)]' }
    case 'archive':
      return { icon: FileArchive, tone: 'text-warning' }
    case 'markdown':
      return { icon: FileText, tone: 'text-accent' }
  }
  if (ext === 'json') return { icon: FileJson, tone: 'text-warning' }
  if (codeExt.has(ext) || codeExt.has(entry.name.toLowerCase())) return { icon: FileCode, tone: 'text-success' }
  if (ext === 'txt' || ext === 'log' || ext === 'csv') return { icon: FileText, tone: 'text-muted-foreground' }
  return { icon: File, tone: 'text-muted-foreground' }
}

export function sortEntries(entries: FileEntry[], sort: SortState) {
  const direction = sort.descending ? -1 : 1
  const collator = new Intl.Collator('fr', { numeric: true, sensitivity: 'base' })
  return [...entries].sort((a, b) => {
    const aDir = isDirectory(a)
    const bDir = isDirectory(b)
    if (aDir !== bDir) return aDir ? -1 : 1
    let result = 0
    if (sort.key === 'size' && !aDir) result = a.size - b.size
    else if (sort.key === 'mtime') result = a.mtime - b.mtime
    return (result || collator.compare(a.name, b.name)) * direction
  })
}

interface FileListProps {
  entries: FileEntry[]
  selected: string | null
  openPaths: Set<string>
  sort: SortState
  onSort: (sort: SortState) => void
  onSelect: (entry: FileEntry) => void
  onOpen: (entry: FileEntry) => void
  onContextMenu: (entry: FileEntry, x: number, y: number) => void
  onMove: (from: string, toDir: string) => void
  onDropFiles: (dir: string, dataTransfer: DataTransfer) => void
  onParent: () => void
  emptyLabel: string
}

export function FileList({
  entries,
  selected,
  openPaths,
  sort,
  onSort,
  onSelect,
  onOpen,
  onContextMenu,
  onMove,
  onDropFiles,
  onParent,
  emptyLabel,
}: FileListProps) {
  const listRef = useRef<HTMLDivElement>(null)
  const [dropTarget, setDropTarget] = useState<string | null>(null)
  const dragDepth = useRef(new Map<string, number>())
  const typeAhead = useRef({ text: '', at: 0 })

  // Keep the selected row visible during keyboard navigation.
  useEffect(() => {
    if (!selected || !listRef.current) return
    const row = listRef.current.querySelector<HTMLElement>(`[data-path="${CSS.escape(selected)}"]`)
    row?.scrollIntoView?.({ block: 'nearest' })
  }, [selected])

  const move = (delta: number) => {
    if (entries.length === 0) return
    const index = entries.findIndex(entry => entry.path === selected)
    const next = index < 0 ? (delta > 0 ? 0 : entries.length - 1) : Math.min(entries.length - 1, Math.max(0, index + delta))
    onSelect(entries[next])
  }

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const current = entries.find(entry => entry.path === selected)
    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault()
        move(1)
        return
      case 'ArrowUp':
        event.preventDefault()
        move(-1)
        return
      case 'PageDown':
        event.preventDefault()
        move(10)
        return
      case 'PageUp':
        event.preventDefault()
        move(-10)
        return
      case 'Home':
        event.preventDefault()
        if (entries[0]) onSelect(entries[0])
        return
      case 'End':
        event.preventDefault()
        if (entries.length) onSelect(entries[entries.length - 1])
        return
      case 'Enter':
      case 'ArrowRight':
        if (current && (event.key === 'Enter' || isDirectory(current))) {
          event.preventDefault()
          onOpen(current)
        }
        return
      case 'Backspace':
      case 'ArrowLeft':
        event.preventDefault()
        onParent()
        return
      case 'ContextMenu':
        if (current) {
          event.preventDefault()
          const row = listRef.current?.querySelector<HTMLElement>(`[data-path="${CSS.escape(current.path)}"]`)
          const rect = row?.getBoundingClientRect()
          onContextMenu(current, rect ? rect.left + 24 : 0, rect ? rect.bottom : 0)
        }
        return
    }
    // Type the first letters of a name to jump to it.
    if (event.key.length === 1 && !event.ctrlKey && !event.metaKey && !event.altKey) {
      const now = Date.now()
      typeAhead.current.text = (now - typeAhead.current.at > 700 ? '' : typeAhead.current.text) + event.key.toLowerCase()
      typeAhead.current.at = now
      const match = entries.find(entry => entry.name.toLowerCase().startsWith(typeAhead.current.text))
      if (match) onSelect(match)
    }
  }

  const acceptsDrop = (event: DragEvent) => hasExternalFiles(event.dataTransfer) || isInternalDrag(event.dataTransfer)

  const rowDragEnter = (event: DragEvent, entry: FileEntry) => {
    if (!isDirectory(entry) || !acceptsDrop(event)) return
    event.preventDefault()
    event.stopPropagation()
    dragDepth.current.set(entry.path, (dragDepth.current.get(entry.path) || 0) + 1)
    setDropTarget(entry.path)
  }
  const rowDragLeave = (event: DragEvent, entry: FileEntry) => {
    if (!isDirectory(entry)) return
    event.stopPropagation()
    const depth = (dragDepth.current.get(entry.path) || 1) - 1
    dragDepth.current.set(entry.path, depth)
    if (depth <= 0) setDropTarget(current => (current === entry.path ? null : current))
  }
  const rowDragOver = (event: DragEvent, entry: FileEntry) => {
    if (!isDirectory(entry) || !acceptsDrop(event)) return
    event.preventDefault()
    event.stopPropagation()
    event.dataTransfer.dropEffect = isInternalDrag(event.dataTransfer) ? 'move' : 'copy'
  }
  const rowDrop = (event: DragEvent, entry: FileEntry) => {
    if (!isDirectory(entry)) return
    event.preventDefault()
    event.stopPropagation()
    dragDepth.current.clear()
    setDropTarget(null)
    const from = event.dataTransfer.getData(INTERNAL_DRAG_TYPE)
    if (from) {
      if (from !== entry.path) onMove(from, entry.path)
      return
    }
    if (hasExternalFiles(event.dataTransfer)) onDropFiles(entry.path, event.dataTransfer)
  }

  const header = (key: SortKey, label: string, className: string) => {
    const active = sort.key === key
    const Arrow = sort.descending ? ArrowDown : ArrowUp
    return (
      <button
        type="button"
        className={cn('ui-focus-ring inline-flex items-center gap-1 rounded px-1 hover:text-foreground', active && 'text-foreground', className)}
        onClick={() => onSort({ key, descending: active ? !sort.descending : key !== 'name' })}
        aria-label={`Trier par ${label.toLowerCase()}`}
      >
        {label}
        {active && <Arrow size={11} aria-hidden />}
      </button>
    )
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b border-border px-3 py-1 text-2xs uppercase tracking-wide text-muted-foreground">
        {header('name', 'Nom', 'mr-auto')}
        {header('size', 'Taille', 'w-16 justify-end')}
        {header('mtime', 'Modifié', 'w-[4.5rem] justify-end')}
      </div>
      <div
        ref={listRef}
        role="listbox"
        aria-label="Contenu du dossier"
        tabIndex={0}
        aria-activedescendant={selected ? `file-row-${hashPath(selected)}` : undefined}
        onKeyDown={onKeyDown}
        className="ui-focus-ring min-h-0 flex-1 overflow-y-auto py-1 outline-none"
      >
        {entries.length === 0 && <p className="px-4 py-8 text-center text-xs text-muted-foreground">{emptyLabel}</p>}
        {entries.map(entry => {
          const { icon: Icon, tone } = iconFor(entry)
          const isSelected = entry.path === selected
          const isOpen = openPaths.has(entry.path)
          const dir = isDirectory(entry)
          return (
            <div
              key={entry.path}
              id={`file-row-${hashPath(entry.path)}`}
              role="option"
              aria-selected={isSelected}
              data-path={entry.path}
              draggable
              onDragStart={event => {
                setDragOut(event.dataTransfer, { name: entry.name, path: entry.path, isDir: dir }, downloadURL(entry.path, dir))
              }}
              onDragEnter={event => rowDragEnter(event, entry)}
              onDragLeave={event => rowDragLeave(event, entry)}
              onDragOver={event => rowDragOver(event, entry)}
              onDrop={event => rowDrop(event, entry)}
              onClick={() => {
                onSelect(entry)
                onOpen(entry)
              }}
              onContextMenu={(event: MouseEvent) => {
                event.preventDefault()
                onSelect(entry)
                onContextMenu(entry, event.clientX, event.clientY)
              }}
              title={entry.type === 'symlink' ? `${entry.name} → ${entry.target}` : entry.name}
              className={cn(
                'group mx-1 flex min-h-9 cursor-pointer items-center gap-2 rounded px-2 text-[13px] select-none md:min-h-7',
                isSelected ? 'bg-[var(--surface-active)] text-foreground' : 'hover:bg-[var(--surface-hover)]',
                dropTarget === entry.path && 'bg-primary/15 ring-1 ring-primary ring-inset',
                entry.hidden && !isSelected && 'opacity-70',
              )}
            >
              <Icon size={15} className={cn('shrink-0', tone)} aria-hidden />
              <span className={cn('min-w-0 flex-1 truncate', isOpen && !dir && 'font-medium')}>
                {entry.name}
                {entry.type === 'symlink' && (
                  <span className={cn('ml-1 text-2xs', entry.broken ? 'text-destructive' : 'text-muted-foreground')}>
                    → {entry.target}
                  </span>
                )}
              </span>
              <span className="w-16 shrink-0 text-right text-2xs tabular-nums text-muted-foreground">
                {dir ? '' : formatSize(entry.size)}
              </span>
              <span className="w-[4.5rem] shrink-0 text-right text-2xs tabular-nums text-muted-foreground">
                {formatDate(entry.mtime)}
              </span>
            </div>
          )
        })}
      </div>
    </div>
  )
}

/** A short, id-safe key for a path. */
function hashPath(path: string) {
  let hash = 5381
  for (let index = 0; index < path.length; index++) hash = ((hash << 5) + hash + path.charCodeAt(index)) | 0
  return (hash >>> 0).toString(36)
}
