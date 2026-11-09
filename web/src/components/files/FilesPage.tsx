import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type DragEvent,
  type FormEvent,
  type PointerEvent as ReactPointerEvent,
} from 'react'
import {
  ArrowLeft,
  ChevronRight,
  CloudUpload,
  Copy,
  Download,
  Eye,
  EyeOff,
  FilePlus,
  FolderInput,
  FolderOpen,
  FolderPlus,
  House,
  Pencil,
  RefreshCw,
  Save,
  Search,
  Trash2,
  Upload,
  X,
} from 'lucide-react'
import type { Host } from '../../hooks/useHosts'
import { cn } from '../../lib/utils'
import { Button, Dialog, IconButton } from '../ui'
import type { CodeEditorHandle } from './CodeEditor'
import { collectDropped, collectPicked, hasExternalFiles, INTERNAL_DRAG_TYPE, isInternalDrag } from './dropTransfer'
import { FileList, iconFor, sortEntries, type SortState } from './FileList'
import {
  basename,
  breadcrumbs,
  dirname,
  downloadURL,
  fileKind,
  FilesError,
  filesApi,
  formatSize,
  isDirectory,
  joinPath,
  type FileEntry,
  type Listing,
} from './filesApi'
import { FileViewer } from './FileViewer'
import { detectEOL, fromEditorText, isDirty, toEditorText, type FileTab, type MarkdownMode } from './tabs'
import { useUploads, type ConflictChoice } from './useUploads'
import './files.css'

const storageKeys = {
  hidden: 'atlas.files.hidden',
  sort: 'atlas.files.sort',
  markdownMode: 'atlas.files.markdown-mode',
  lastDir: 'atlas.files.last-dir',
  paneWidth: 'atlas.files.pane-width',
  wrap: 'atlas.files.wrap',
}

function readStorage<T>(key: string, fallback: T): T {
  try {
    const raw = window.localStorage.getItem(key)
    return raw == null ? fallback : (JSON.parse(raw) as T)
  } catch {
    return fallback
  }
}

function writeStorage(key: string, value: unknown) {
  try {
    window.localStorage.setItem(key, JSON.stringify(value))
  } catch {
    /* Storage can be unavailable in private windows. */
  }
}

// Open tabs survive a visit to another view of the app.
let memory: { dir: string; tabs: FileTab[]; active: string | null } | null = null

function errorText(error: unknown) {
  return error instanceof Error ? error.message : String(error)
}

function newTab(path: string, entry?: Partial<FileEntry>): FileTab {
  const kind = fileKind(basename(path))
  const media = kind === 'image' || kind === 'video' || kind === 'audio'
  return {
    path,
    name: basename(path),
    kind,
    status: media ? 'ready' : 'loading',
    text: '',
    saved: '',
    eol: '\n',
    version: null,
    writable: true,
    size: entry?.size ?? 0,
    mtime: entry?.mtime ?? Date.now(),
    mode: entry?.mode ?? '',
    binary: false,
    tooLarge: false,
    saving: false,
    textLoaded: false,
    markdownMode: readStorage<MarkdownMode>(storageKeys.markdownMode, 'preview'),
    showSource: false,
    wrap: readStorage(storageKeys.wrap, false),
  }
}

/** Rewrites a path after its folder or itself was renamed or moved. */
function movedPath(path: string, from: string, to: string) {
  if (path === from) return to
  if (path.startsWith(`${from}/`)) return to + path.slice(from.length)
  return null
}

type NameDialog = { mode: 'file' | 'folder' } | { mode: 'rename'; entry: FileEntry }
type ContextMenuState = { entry: FileEntry; x: number; y: number }

export function FilesPage({
  hosts,
  embedded = false,
  initialPath,
}: {
  hosts: Host[]
  /** Shown inside a session: the URL is left alone and the page is not a landmark. */
  embedded?: boolean
  /** Folder to open first, such as the working directory of a pane. */
  initialPath?: string
}) {
  const initialDir = useMemo(() => {
    if (embedded) return initialPath || memory?.dir || readStorage<string>(storageKeys.lastDir, '')
    const params = new URLSearchParams(window.location.search)
    return params.get('path') || memory?.dir || readStorage<string>(storageKeys.lastDir, '')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  const [dir, setDir] = useState(initialDir)
  const [listing, setListing] = useState<Listing | null>(null)
  const [loading, setLoading] = useState(true)
  const [listError, setListError] = useState<FilesError | null>(null)
  const [tabs, setTabs] = useState<FileTab[]>(() => memory?.tabs ?? [])
  const [active, setActive] = useState<string | null>(() => memory?.active ?? null)
  const [selected, setSelected] = useState<string | null>(null)
  const [filter, setFilter] = useState('')
  const [showHidden, setShowHidden] = useState(() => readStorage(storageKeys.hidden, false))
  const [sort, setSort] = useState<SortState>(() => readStorage(storageKeys.sort, { key: 'name', descending: false }))
  const [paneWidth, setPaneWidth] = useState(() => readStorage(storageKeys.paneWidth, 320))
  const [mobileViewer, setMobileViewer] = useState(false)
  const [editingPath, setEditingPath] = useState(false)
  const [pathDraft, setPathDraft] = useState('')
  const [nameDialog, setNameDialog] = useState<NameDialog | null>(null)
  const [nameDraft, setNameDraft] = useState('')
  const [deleteTarget, setDeleteTarget] = useState<FileEntry | null>(null)
  const [conflict, setConflict] = useState<{ path: string; deleted: boolean } | null>(null)
  const [closeConfirm, setCloseConfirm] = useState<string | null>(null)
  const [uploadConflict, setUploadConflict] = useState<{ path: string; resolve: (choice: ConflictChoice) => void } | null>(null)
  const [contextMenu, setContextMenu] = useState<ContextMenuState | null>(null)
  const [notice, setNotice] = useState<{ text: string; tone: 'info' | 'error' } | null>(null)
  const [dragActive, setDragActive] = useState(false)
  const [busy, setBusy] = useState(false)

  const tabsRef = useRef(tabs)
  tabsRef.current = tabs
  const listController = useRef<AbortController | null>(null)
  const savingPaths = useRef(new Set<string>())
  const editorRef = useRef<CodeEditorHandle>(null)
  const dragDepth = useRef(0)
  const fileInput = useRef<HTMLInputElement>(null)
  const folderInput = useRef<HTMLInputElement>(null)
  const nameInput = useRef<HTMLInputElement>(null)
  const crumbsRef = useRef<HTMLElement>(null)
  const listingDir = listing?.path ?? dir
  const localHost = hosts.find(host => host.local)

  const notify = useCallback((text: string, tone: 'info' | 'error' = 'info') => setNotice({ text, tone }), [])
  useEffect(() => {
    if (!notice) return
    const timer = window.setTimeout(() => setNotice(null), notice.tone === 'error' ? 7000 : 3500)
    return () => window.clearTimeout(timer)
  }, [notice])

  // ---------------------------------------------------------------------------
  // Folders

  const loadDir = useCallback(async (path: string, options: { quiet?: boolean; select?: string } = {}) => {
    listController.current?.abort()
    const controller = new AbortController()
    listController.current = controller
    if (!options.quiet) setLoading(true)
    try {
      const next = await filesApi.list(path, controller.signal)
      if (controller.signal.aborted) return
      setListing(next)
      setDir(next.path)
      setListError(null)
      if (!options.quiet) setFilter('')
      if (options.select) setSelected(options.select)
      else if (!options.quiet) setSelected(null)
      writeStorage(storageKeys.lastDir, next.path)
    } catch (error) {
      if (controller.signal.aborted) return
      const failure = error instanceof FilesError ? error : new FilesError(errorText(error), 0)
      if (options.quiet) notify(failure.message, 'error')
      else setListError(failure)
    } finally {
      if (!controller.signal.aborted) setLoading(false)
    }
  }, [notify])

  useEffect(() => {
    void loadDir(initialDir)
    return () => listController.current?.abort()
  }, [initialDir, loadDir])

  const reload = useCallback(() => loadDir(listingDir, { quiet: true, select: selected ?? undefined }), [listingDir, loadDir, selected])

  // Keep the URL shareable and the open tabs for the next visit.
  useEffect(() => {
    memory = { dir: listingDir, tabs, active }
    if (window.location.pathname !== '/files') return
    const params = new URLSearchParams()
    if (listingDir) params.set('path', listingDir)
    if (active) params.set('file', active)
    const next = `/files?${params}`
    if (`${window.location.pathname}${window.location.search}` !== next) window.history.replaceState(null, '', next)
  }, [active, listingDir, tabs])

  const dirtyCount = tabs.filter(isDirty).length
  useEffect(() => {
    if (dirtyCount === 0) return
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ''
    }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [dirtyCount])

  // ---------------------------------------------------------------------------
  // Tabs

  const updateTab = useCallback((path: string, change: Partial<FileTab>) => {
    setTabs(current => current.map(tab => (tab.path === path ? { ...tab, ...change } : tab)))
  }, [])

  const loadText = useCallback(
    async (path: string) => {
      updateTab(path, { status: 'loading', error: undefined })
      try {
        const content = await filesApi.read(path)
        const text = toEditorText(content.content)
        updateTab(path, {
          status: 'ready',
          text,
          saved: text,
          eol: detectEOL(content.content),
          version: content.version,
          writable: content.writable,
          size: content.size,
          mtime: content.mtime,
          mode: content.mode,
          binary: content.binary,
          tooLarge: content.too_large,
          textLoaded: !content.binary && !content.too_large,
        })
      } catch (error) {
        updateTab(path, { status: 'error', error: errorText(error) })
      }
    },
    [updateTab],
  )

  const openFile = useCallback(
    (path: string, entry?: Partial<FileEntry>) => {
      setMobileViewer(true)
      setActive(path)
      if (tabsRef.current.some(tab => tab.path === path)) return
      const tab = newTab(path, entry)
      setTabs(current => [...current, tab])
      tabsRef.current = [...tabsRef.current, tab]
      if (tab.status === 'loading') void loadText(path)
    },
    [loadText],
  )

  // Reopen the file named in the URL on first load.
  useEffect(() => {
    // Tabs restored while they were still loading would otherwise wait forever.
    for (const tab of tabsRef.current) if (tab.status === 'loading') void loadText(tab.path)
    const file = embedded ? null : new URLSearchParams(window.location.search).get('file')
    if (file) openFile(file)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const forgetTab = useCallback((path: string) => {
    setTabs(current => {
      const index = current.findIndex(tab => tab.path === path)
      if (index < 0) return current
      const next = current.filter(tab => tab.path !== path)
      setActive(activePath => {
        if (activePath !== path) return activePath
        const neighbor = next[Math.min(index, next.length - 1)]
        if (!neighbor) setMobileViewer(false)
        return neighbor?.path ?? null
      })
      return next
    })
  }, [])

  const closeTab = useCallback(
    (path: string) => {
      const tab = tabsRef.current.find(candidate => candidate.path === path)
      if (tab && isDirty(tab)) setCloseConfirm(path)
      else forgetTab(path)
    },
    [forgetTab],
  )

  const save = useCallback(
    async (path: string, force = false): Promise<boolean> => {
      const tab = tabsRef.current.find(candidate => candidate.path === path)
      if (!tab || !tab.textLoaded || tab.saving || savingPaths.current.has(path)) return false
      if (!tab.writable && !force) {
        notify('Ce fichier est en lecture seule pour TmuxAtlas.', 'error')
        return false
      }
      const sent = tab.text
      savingPaths.current.add(path)
      updateTab(path, { saving: true })
      try {
        const saved = await filesApi.write(path, fromEditorText(sent, tab.eol), tab.version, force)
        updateTab(path, { saving: false, saved: sent, version: saved.version, size: saved.size, mtime: saved.mtime })
        if (dirname(path) === listingDir) void loadDir(listingDir, { quiet: true, select: selected ?? undefined })
        notify(`${tab.name} enregistré`)
        return true
      } catch (error) {
        updateTab(path, { saving: false })
        if (error instanceof FilesError && (error.code === 'conflict' || error.code === 'deleted')) {
          setConflict({ path, deleted: error.code === 'deleted' })
        } else {
          notify(`Enregistrement impossible : ${errorText(error)}`, 'error')
        }
        return false
      } finally {
        savingPaths.current.delete(path)
      }
    },
    [listingDir, loadDir, notify, selected, updateTab],
  )

  const activeTab = tabs.find(tab => tab.path === active) ?? null

  // Ctrl+S anywhere on the page saves the active file; Alt+W closes it.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      const mod = event.ctrlKey || event.metaKey
      if (mod && !event.altKey && event.key.toLowerCase() === 's') {
        // The editor already saved when it handled the shortcut itself.
        if (event.defaultPrevented) return
        event.preventDefault()
        if (active) void save(active)
      } else if (event.altKey && !mod && event.key.toLowerCase() === 'w' && active) {
        event.preventDefault()
        closeTab(active)
      } else if (event.altKey && !mod && event.key.toLowerCase() === 'z' && activeTab) {
        event.preventDefault()
        updateTab(activeTab.path, { wrap: !activeTab.wrap })
        writeStorage(storageKeys.wrap, !activeTab.wrap)
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [active, activeTab, closeTab, save, updateTab])

  // ---------------------------------------------------------------------------
  // Entries

  const visibleEntries = useMemo(() => {
    const entries = listing?.entries ?? []
    const needle = filter.trim().toLowerCase()
    return sortEntries(
      entries.filter(entry => (showHidden || !entry.hidden) && (!needle || entry.name.toLowerCase().includes(needle))),
      sort,
    )
  }, [filter, listing, showHidden, sort])
  const hiddenCount = (listing?.entries ?? []).filter(entry => entry.hidden).length

  const openEntry = useCallback(
    (entry: FileEntry) => {
      if (isDirectory(entry)) void loadDir(entry.path)
      else if (entry.type === 'symlink' && entry.broken) notify(`Lien cassé : ${entry.target}`, 'error')
      else openFile(entry.path, entry)
    },
    [loadDir, notify, openFile],
  )

  /** Opens what a Markdown link points to: a folder or a file. */
  const openPath = useCallback(
    async (path: string) => {
      try {
        await filesApi.list(path)
        void loadDir(path)
      } catch {
        openFile(path)
      }
    },
    [loadDir, openFile],
  )

  const applyMove = useCallback((from: string, to: string) => {
    setTabs(current =>
      current.map(tab => {
        const next = movedPath(tab.path, from, to)
        return next ? { ...tab, path: next, name: basename(next), kind: fileKind(basename(next)) } : tab
      }),
    )
    setActive(current => (current ? (movedPath(current, from, to) ?? current) : current))
  }, [])

  const move = useCallback(
    async (from: string, toDir: string) => {
      const to = joinPath(toDir, basename(from))
      if (dirname(from) === toDir || to === from) return
      if (toDir === from || toDir.startsWith(`${from}/`)) {
        notify('Un dossier ne peut pas être déplacé dans lui-même.', 'error')
        return
      }
      try {
        await filesApi.rename(from, to)
        applyMove(from, to)
        notify(`${basename(from)} déplacé dans ${basename(toDir) || '/'}`)
        void reload()
      } catch (error) {
        notify(`Déplacement impossible : ${errorText(error)}`, 'error')
      }
    },
    [applyMove, notify, reload],
  )

  const download = (entry: Pick<FileEntry, 'path' | 'name'> & { dir?: boolean }) => {
    const link = document.createElement('a')
    link.href = downloadURL(entry.path, entry.dir)
    link.download = entry.dir ? `${entry.name}.zip` : entry.name
    document.body.appendChild(link)
    link.click()
    link.remove()
  }

  const copyPath = async (path: string) => {
    try {
      await navigator.clipboard.writeText(path)
      notify('Chemin copié')
    } catch {
      notify('Le navigateur refuse l’accès au presse-papiers.', 'error')
    }
  }

  // ---------------------------------------------------------------------------
  // Dialogs

  const openNameDialog = (dialog: NameDialog) => {
    setContextMenu(null)
    setNameDialog(dialog)
    setNameDraft(dialog.mode === 'rename' ? dialog.entry.name : '')
    window.requestAnimationFrame(() => {
      const input = nameInput.current
      if (!input) return
      input.focus()
      if (dialog.mode === 'rename' && !isDirectory(dialog.entry)) {
        const dot = dialog.entry.name.lastIndexOf('.')
        input.setSelectionRange(0, dot > 0 ? dot : dialog.entry.name.length)
      } else input.select()
    })
  }

  const submitName = async (event: FormEvent) => {
    event.preventDefault()
    if (!nameDialog) return
    const name = nameDraft.trim()
    if (!name || name.includes('/') || name === '.' || name === '..') {
      notify('Nom invalide.', 'error')
      return
    }
    setBusy(true)
    try {
      if (nameDialog.mode === 'rename') {
        const from = nameDialog.entry.path
        const to = joinPath(dirname(from), name)
        if (to !== from) {
          await filesApi.rename(from, to)
          applyMove(from, to)
          await loadDir(listingDir, { quiet: true, select: to })
        }
      } else {
        const path = joinPath(listingDir, name)
        if (nameDialog.mode === 'folder') await filesApi.mkdir(path)
        else await filesApi.create(path)
        await loadDir(listingDir, { quiet: true, select: path })
        if (nameDialog.mode === 'file') openFile(path, { size: 0, mtime: Date.now() })
      }
      setNameDialog(null)
    } catch (error) {
      notify(errorText(error), 'error')
    } finally {
      setBusy(false)
    }
  }

  const confirmDelete = async () => {
    if (!deleteTarget) return
    setBusy(true)
    try {
      await filesApi.remove(deleteTarget.path, deleteTarget.type === 'dir')
      for (const tab of tabsRef.current) if (movedPath(tab.path, deleteTarget.path, deleteTarget.path)) forgetTab(tab.path)
      notify(`${deleteTarget.name} supprimé`)
      setDeleteTarget(null)
      void loadDir(listingDir, { quiet: true })
    } catch (error) {
      notify(`Suppression impossible : ${errorText(error)}`, 'error')
    } finally {
      setBusy(false)
    }
  }

  // ---------------------------------------------------------------------------
  // Uploads

  const resolveConflict = useCallback(
    (path: string) => new Promise<ConflictChoice>(resolve => setUploadConflict({ path, resolve })),
    [],
  )
  const onUploaded = useCallback(
    (dirs: Set<string>) => {
      const current = listingDir
      if ([...dirs].some(path => path === current || path.startsWith(`${current}/`) || current.startsWith(`${path}/`))) {
        void loadDir(current, { quiet: true, select: selected ?? undefined })
      }
      // An open file that was replaced by an upload is refreshed when unchanged here.
      for (const tab of tabsRef.current) if (tab.textLoaded && !isDirty(tab) && dirs.has(dirname(tab.path))) void loadText(tab.path)
    },
    [listingDir, loadDir, loadText, selected],
  )
  const uploads = useUploads({ onUploaded, resolveConflict })

  const uploadDropped = useCallback(
    (targetDir: string, dataTransfer: DataTransfer) => {
      void collectDropped(dataTransfer)
        .then(items => {
          if (items.files.length === 0 && items.folders.length === 0) return
          return uploads.start(targetDir, items)
        })
        .catch(error => notify(`Lecture des fichiers déposés impossible : ${errorText(error)}`, 'error'))
    },
    [notify, uploads],
  )

  const pageDragEnter = (event: DragEvent) => {
    if (!hasExternalFiles(event.dataTransfer)) return
    event.preventDefault()
    dragDepth.current++
    setDragActive(true)
  }
  const pageDragLeave = () => {
    dragDepth.current = Math.max(0, dragDepth.current - 1)
    if (dragDepth.current === 0) setDragActive(false)
  }
  const pageDragOver = (event: DragEvent) => {
    if (!hasExternalFiles(event.dataTransfer)) return
    event.preventDefault()
    event.dataTransfer.dropEffect = 'copy'
  }
  const pageDrop = (event: DragEvent) => {
    dragDepth.current = 0
    setDragActive(false)
    if (!hasExternalFiles(event.dataTransfer)) return
    event.preventDefault()
    uploadDropped(listingDir, event.dataTransfer)
  }

  // A crumb accepts both explorer items (move) and desktop files (upload).
  const crumbDropProps = (path: string) => ({
    onDragOver: (event: DragEvent) => {
      if (!isInternalDrag(event.dataTransfer) && !hasExternalFiles(event.dataTransfer)) return
      event.preventDefault()
      event.stopPropagation()
      event.dataTransfer.dropEffect = isInternalDrag(event.dataTransfer) ? 'move' : 'copy'
    },
    onDrop: (event: DragEvent) => {
      event.preventDefault()
      event.stopPropagation()
      dragDepth.current = 0
      setDragActive(false)
      const from = event.dataTransfer.getData(INTERNAL_DRAG_TYPE)
      if (from) void move(from, path)
      else if (hasExternalFiles(event.dataTransfer)) uploadDropped(path, event.dataTransfer)
    },
  })

  // ---------------------------------------------------------------------------
  // Layout

  const startResize = (event: ReactPointerEvent<HTMLDivElement>) => {
    event.preventDefault()
    const startX = event.clientX
    const startWidth = paneWidth
    let width = startWidth
    const onMove = (moveEvent: PointerEvent) => {
      width = Math.min(640, Math.max(220, startWidth + moveEvent.clientX - startX))
      setPaneWidth(width)
    }
    const onUp = () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      writeStorage(storageKeys.paneWidth, width)
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
  }

  const crumbs = breadcrumbs(listingDir || '/')
  // Long paths keep the current folder in view.
  useEffect(() => {
    const nav = crumbsRef.current
    if (nav) nav.scrollLeft = nav.scrollWidth
  }, [listingDir, editingPath])
  const toolbarButton = 'shrink-0'

  const Root = embedded ? 'div' : 'main'
  return (
    <Root
      className="relative flex min-h-0 flex-1 overflow-hidden"
      onDragEnter={pageDragEnter}
      onDragLeave={pageDragLeave}
      onDragOver={pageDragOver}
      onDrop={pageDrop}
    >
      <input
        ref={fileInput}
        type="file"
        multiple
        hidden
        onChange={event => {
          if (event.target.files?.length) void uploads.start(listingDir, collectPicked(event.target.files))
          event.target.value = ''
        }}
      />
      <input
        ref={folderInput}
        type="file"
        hidden
        {...({ webkitdirectory: '' } as Record<string, string>)}
        onChange={event => {
          if (event.target.files?.length) void uploads.start(listingDir, collectPicked(event.target.files))
          event.target.value = ''
        }}
      />

      {/* Folder pane */}
      <aside
        aria-label="Explorateur de fichiers"
        className={cn(
          'min-h-0 w-full flex-col border-border bg-card md:flex md:w-[var(--files-pane)] md:shrink-0 md:border-r',
          mobileViewer && activeTab ? 'hidden' : 'flex',
        )}
        style={{ '--files-pane': `${paneWidth}px` } as React.CSSProperties}
      >
        <div className="flex shrink-0 items-center gap-1 px-2 pt-2 pb-1">
          <h1 className="min-w-0 flex-1 truncate pl-1 text-sm font-medium">
            Fichiers
            {localHost && <span className="ml-1.5 text-xs font-normal text-muted-foreground">{localHost.name}</span>}
          </h1>
          <IconButton aria-label="Dossier personnel" title="Dossier personnel" size="sm" className={toolbarButton} onClick={() => void loadDir(listing?.home || '')}>
            <House size={15} aria-hidden />
          </IconButton>
          <IconButton aria-label="Actualiser" title="Actualiser" size="sm" className={toolbarButton} onClick={() => void loadDir(listingDir, { quiet: true, select: selected ?? undefined })}>
            <RefreshCw size={14} className={loading ? 'animate-spin' : undefined} aria-hidden />
          </IconButton>
        </div>
        <div className="flex shrink-0 items-center gap-1 px-2 pb-2">
          <Button size="sm" variant="ghost" leadingIcon={<FilePlus size={14} aria-hidden />} onClick={() => openNameDialog({ mode: 'file' })} disabled={!listing?.writable}>
            Fichier
          </Button>
          <Button size="sm" variant="ghost" leadingIcon={<FolderPlus size={14} aria-hidden />} onClick={() => openNameDialog({ mode: 'folder' })} disabled={!listing?.writable}>
            Dossier
          </Button>
          <div className="flex-1" />
          <IconButton aria-label="Envoyer des fichiers" title="Envoyer des fichiers" size="sm" onClick={() => fileInput.current?.click()} disabled={!listing?.writable}>
            <Upload size={15} aria-hidden />
          </IconButton>
          <IconButton aria-label="Envoyer un dossier" title="Envoyer un dossier" size="sm" onClick={() => folderInput.current?.click()} disabled={!listing?.writable}>
            <FolderInput size={15} aria-hidden />
          </IconButton>
          <IconButton
            aria-label={showHidden ? 'Masquer les fichiers cachés' : 'Afficher les fichiers cachés'}
            title={showHidden ? 'Masquer les fichiers cachés' : `Afficher les fichiers cachés${hiddenCount ? ` (${hiddenCount})` : ''}`}
            aria-pressed={showHidden}
            size="sm"
            onClick={() => {
              setShowHidden(value => {
                writeStorage(storageKeys.hidden, !value)
                return !value
              })
            }}
          >
            {showHidden ? <Eye size={15} aria-hidden /> : <EyeOff size={15} aria-hidden />}
          </IconButton>
        </div>

        {/* Breadcrumb, or a path field after a click on its empty end */}
        {editingPath ? (
          <form
            className="shrink-0 px-2 pb-2"
            onSubmit={event => {
              event.preventDefault()
              setEditingPath(false)
              const target = pathDraft.trim()
              if (target) void loadDir(target)
            }}
          >
            <input
              autoFocus
              value={pathDraft}
              onChange={event => setPathDraft(event.target.value)}
              onBlur={() => setEditingPath(false)}
              onKeyDown={event => event.key === 'Escape' && setEditingPath(false)}
              aria-label="Aller au chemin"
              spellCheck={false}
              autoCapitalize="off"
              className="ui-focus-ring w-full rounded border border-border bg-input px-2 py-1 font-mono text-xs"
            />
          </form>
        ) : (
          <nav
            ref={crumbsRef}
            aria-label="Chemin"
            className="flex shrink-0 cursor-text items-center overflow-x-auto px-2 pb-2 text-xs scrollbar-none"
            onClick={event => {
              if (event.target !== event.currentTarget) return
              setPathDraft(listingDir)
              setEditingPath(true)
            }}
            title="Cliquez à droite du chemin pour le saisir"
          >
            {crumbs.map((crumb, index) => (
              <span key={crumb.path} className="flex shrink-0 items-center">
                {index > 1 && <ChevronRight size={12} className="text-muted-foreground" aria-hidden />}
                <button
                  type="button"
                  className={cn(
                    'ui-focus-ring max-w-40 truncate rounded px-1 py-0.5 hover:bg-secondary',
                    index === crumbs.length - 1 ? 'text-foreground' : 'text-muted-foreground',
                  )}
                  onClick={() => void loadDir(crumb.path)}
                  {...crumbDropProps(crumb.path)}
                >
                  {crumb.name}
                </button>
              </span>
            ))}
            <span className="min-w-6 flex-1 self-stretch" onClick={() => {
              setPathDraft(listingDir)
              setEditingPath(true)
            }} />
          </nav>
        )}

        <div className="relative shrink-0 px-2 pb-2">
          <Search size={13} className="pointer-events-none absolute top-1/2 left-4 -translate-y-1/2 text-muted-foreground" aria-hidden />
          <input
            value={filter}
            onChange={event => setFilter(event.target.value)}
            onKeyDown={event => {
              if (event.key === 'Escape') setFilter('')
              if (event.key === 'Enter' && visibleEntries[0]) openEntry(visibleEntries[0])
            }}
            placeholder="Filtrer ce dossier"
            aria-label="Filtrer ce dossier"
            className="ui-focus-ring w-full rounded border border-border bg-input py-1 pr-2 pl-7 text-xs"
          />
        </div>

        {listError ? (
          <div className="flex-1 p-4 text-center text-xs">
            <p className="text-destructive">{listError.message}</p>
            {listError.code !== 'disabled' && (
              <Button className="mt-3" size="sm" onClick={() => void loadDir(listing?.home || '')}>
                Revenir au dossier personnel
              </Button>
            )}
          </div>
        ) : !listing && loading ? (
          <div role="status" className="flex-1 p-4 text-center text-xs text-muted-foreground">
            Chargement…
          </div>
        ) : (
          <FileList
            entries={visibleEntries}
            selected={selected}
            openPaths={new Set(tabs.map(tab => tab.path))}
            sort={sort}
            onSort={next => {
              setSort(next)
              writeStorage(storageKeys.sort, next)
            }}
            onSelect={entry => setSelected(entry.path)}
            onOpen={openEntry}
            onContextMenu={(entry, x, y) => setContextMenu({ entry, x, y })}
            onMove={(from, toDir) => void move(from, toDir)}
            onDropFiles={uploadDropped}
            parentPath={listing?.parent}
            onParent={() => listing?.parent && void loadDir(listing.parent, { select: listing.path })}
            emptyLabel={filter ? 'Aucun élément ne correspond au filtre.' : 'Ce dossier est vide. Glissez des fichiers ici pour les envoyer.'}
          />
        )}
        <div className="flex shrink-0 items-center gap-2 border-t border-border px-3 py-1 text-2xs text-muted-foreground">
          <span className="tabular-nums">
            {visibleEntries.length} élément{visibleEntries.length > 1 ? 's' : ''}
            {!showHidden && hiddenCount > 0 && ` · ${hiddenCount} caché${hiddenCount > 1 ? 's' : ''}`}
          </span>
          {listing?.truncated && <span className="text-warning">liste tronquée</span>}
          {listing && !listing.writable && <span className="ml-auto text-warning">lecture seule</span>}
        </div>
      </aside>

      <div
        role="separator"
        aria-orientation="vertical"
        aria-label="Redimensionner l’explorateur"
        className="hidden w-1 shrink-0 cursor-col-resize bg-transparent hover:bg-primary/40 md:block"
        onPointerDown={startResize}
        style={{ marginLeft: -2, marginRight: -2, zIndex: 1 }}
      />

      {/* Open files */}
      <section
        aria-label="Fichiers ouverts"
        className={cn('min-w-0 flex-1 flex-col bg-background md:flex', mobileViewer && activeTab ? 'flex' : 'hidden')}
      >
        {tabs.length > 0 && (
          <div className="flex shrink-0 items-stretch border-b border-border bg-card">
            <IconButton aria-label="Retour à la liste" size="sm" className="m-1 md:hidden" onClick={() => setMobileViewer(false)}>
              <ArrowLeft size={16} aria-hidden />
            </IconButton>
            <div role="tablist" aria-label="Fichiers ouverts" className="flex min-w-0 flex-1 overflow-x-auto scrollbar-none">
              {tabs.map(tab => {
                const { icon: Icon, tone } = iconFor({ name: tab.name, type: 'file' })
                const dirty = isDirty(tab)
                const isActive = tab.path === active
                return (
                  <div
                    key={tab.path}
                    role="tab"
                    aria-selected={isActive}
                    tabIndex={isActive ? 0 : -1}
                    title={tab.path}
                    onClick={() => setActive(tab.path)}
                    onKeyDown={event => {
                      if (event.key === 'Enter' || event.key === ' ') setActive(tab.path)
                    }}
                    onAuxClick={event => {
                      if (event.button === 1) closeTab(tab.path)
                    }}
                    className={cn(
                      'group relative flex max-w-56 shrink-0 cursor-pointer items-center gap-1.5 border-r border-border py-1.5 pr-1 pl-3 text-xs select-none',
                      isActive ? 'bg-background text-foreground' : 'text-muted-foreground hover:bg-[var(--surface-hover)]',
                    )}
                  >
                    {isActive && <span className="absolute inset-x-0 top-0 h-0.5 bg-primary" aria-hidden />}
                    <Icon size={13} className={cn('shrink-0', tone)} aria-hidden />
                    <span className={cn('truncate', !tab.textLoaded && tab.kind !== 'image' && tab.status === 'ready' && 'italic')}>{tab.name}</span>
                    <button
                      type="button"
                      aria-label={`Fermer ${tab.name}${dirty ? ' (modifié)' : ''}`}
                      className="ui-focus-ring relative grid size-5 shrink-0 place-items-center rounded hover:bg-secondary"
                      onClick={event => {
                        event.stopPropagation()
                        closeTab(tab.path)
                      }}
                    >
                      {dirty && <span className="absolute size-2 rounded-full bg-foreground group-hover:hidden" aria-hidden />}
                      <X size={12} className={dirty ? 'hidden group-hover:block' : ''} aria-hidden />
                    </button>
                  </div>
                )
              })}
            </div>
            {activeTab && (
              <div className="flex shrink-0 items-center gap-1 px-2">
                {activeTab.textLoaded && (isDirty(activeTab) || activeTab.saving) && (
                  <Button
                    size="sm"
                    variant="primary"
                    loading={activeTab.saving}
                    loadingLabel="Enregistrement…"
                    leadingIcon={<Save size={13} aria-hidden />}
                    onClick={() => void save(activeTab.path)}
                    title="Enregistrer (Ctrl+S)"
                  >
                    Enregistrer
                  </Button>
                )}
                <IconButton aria-label="Télécharger" title="Télécharger" size="sm" onClick={() => download(activeTab)}>
                  <Download size={14} aria-hidden />
                </IconButton>
                <IconButton aria-label="Afficher dans le dossier" title="Afficher dans le dossier" size="sm" onClick={() => {
                  void loadDir(dirname(activeTab.path), { select: activeTab.path })
                  setMobileViewer(false)
                }}>
                  <FolderOpen size={14} aria-hidden />
                </IconButton>
              </div>
            )}
          </div>
        )}
        {activeTab ? (
          <FileViewer
            key={activeTab.path}
            ref={editorRef}
            tab={activeTab}
            onChange={text => updateTab(activeTab.path, { text })}
            onSave={() => void save(activeTab.path)}
            onUpdate={change => {
              updateTab(activeTab.path, change)
              if (change.markdownMode) writeStorage(storageKeys.markdownMode, change.markdownMode)
              if (change.wrap !== undefined) writeStorage(storageKeys.wrap, change.wrap)
            }}
            onLoadText={() => void loadText(activeTab.path)}
            onOpenPath={path => void openPath(path)}
          />
        ) : (
          <div className="grid flex-1 place-items-center p-8 text-center">
            <div className="max-w-sm">
              <CloudUpload size={36} className="mx-auto mb-4 text-muted-foreground" aria-hidden />
              <p className="text-sm">Choisissez un fichier pour l’ouvrir.</p>
              <p className="mt-2 text-xs leading-relaxed text-muted-foreground">
                Glissez des fichiers ou des dossiers depuis votre ordinateur pour les envoyer dans le dossier courant.
                Glissez un élément de la liste vers votre bureau pour le télécharger, ou sur un dossier pour l’y déplacer.
              </p>
            </div>
          </div>
        )}
      </section>

      {dragActive && (
        <div className="pointer-events-none absolute inset-2 z-20 grid place-items-center rounded-xl border-2 border-dashed border-primary bg-primary/10 backdrop-blur-[1px]">
          <div className="rounded-lg bg-card px-5 py-3 text-center shadow-lg">
            <CloudUpload size={28} className="mx-auto mb-1 text-primary" aria-hidden />
            <p className="text-sm">Déposez pour envoyer dans</p>
            <p className="mt-0.5 max-w-[70vw] truncate font-mono text-xs text-muted-foreground">{listingDir}</p>
            <p className="mt-1 text-2xs text-muted-foreground">ou sur un dossier de la liste</p>
          </div>
        </div>
      )}

      {contextMenu && (
        <ContextMenu
          state={contextMenu}
          onClose={() => setContextMenu(null)}
          onOpen={() => openEntry(contextMenu.entry)}
          onDownload={() => download({ ...contextMenu.entry, dir: isDirectory(contextMenu.entry) })}
          onRename={() => openNameDialog({ mode: 'rename', entry: contextMenu.entry })}
          onDelete={() => setDeleteTarget(contextMenu.entry)}
          onCopyPath={() => void copyPath(contextMenu.entry.path)}
        />
      )}

      <UploadPanel uploads={uploads} />

      {notice && (
        <div
          role={notice.tone === 'error' ? 'alert' : 'status'}
          className={cn(
            'absolute bottom-4 left-1/2 z-30 max-w-[90%] -translate-x-1/2 rounded-md border bg-popover px-4 py-2 text-xs shadow-lg',
            notice.tone === 'error' ? 'border-destructive/60 text-destructive' : 'border-border',
          )}
        >
          {notice.text}
        </div>
      )}

      <Dialog
        open={nameDialog !== null}
        onOpenChange={open => !open && setNameDialog(null)}
        title={nameDialog?.mode === 'rename' ? 'Renommer' : nameDialog?.mode === 'folder' ? 'Nouveau dossier' : 'Nouveau fichier'}
        description={nameDialog?.mode === 'rename' ? nameDialog.entry.path : `Dans ${listingDir}`}
        initialFocusRef={nameInput}
        footer={
          <>
            <Button variant="secondary" onClick={() => setNameDialog(null)}>
              Annuler
            </Button>
            <Button variant="primary" type="submit" form="files-name-form" loading={busy}>
              {nameDialog?.mode === 'rename' ? 'Renommer' : 'Créer'}
            </Button>
          </>
        }
      >
        <form id="files-name-form" onSubmit={event => void submitName(event)}>
          <input
            ref={nameInput}
            value={nameDraft}
            onChange={event => setNameDraft(event.target.value)}
            aria-label="Nom"
            placeholder={nameDialog?.mode === 'folder' ? 'nouveau-dossier' : 'notes.md'}
            spellCheck={false}
            autoCapitalize="off"
            className="ui-focus-ring w-full rounded border border-border bg-input px-3 py-2 font-mono text-sm"
          />
        </form>
      </Dialog>

      <Dialog
        open={deleteTarget !== null}
        role="alertdialog"
        onOpenChange={open => !open && setDeleteTarget(null)}
        title={deleteTarget?.type === 'dir' ? 'Supprimer ce dossier et tout son contenu ?' : 'Supprimer ce fichier ?'}
        description={`${deleteTarget?.path ?? ''} sera supprimé définitivement du serveur. Il n’y a pas de corbeille.`}
        footer={
          <>
            <Button variant="secondary" onClick={() => setDeleteTarget(null)}>
              Annuler
            </Button>
            <Button variant="destructive" loading={busy} onClick={() => void confirmDelete()}>
              Supprimer
            </Button>
          </>
        }
      >
        {null}
      </Dialog>

      <Dialog
        open={conflict !== null}
        role="alertdialog"
        onOpenChange={open => !open && setConflict(null)}
        title={conflict?.deleted ? 'Le fichier a été supprimé du serveur' : 'Le fichier a changé sur le serveur'}
        description={
          conflict?.deleted
            ? 'Il a été supprimé ou déplacé depuis son ouverture. Enregistrer le recréera avec votre version.'
            : 'Quelqu’un ou un programme l’a modifié depuis son ouverture. Écraser remplace sa version par la vôtre.'
        }
        footer={
          <>
            <Button variant="secondary" onClick={() => setConflict(null)}>
              Annuler
            </Button>
            {!conflict?.deleted && (
              <Button
                variant="secondary"
                onClick={() => {
                  if (conflict) void loadText(conflict.path)
                  setConflict(null)
                }}
              >
                Recharger et perdre mes modifications
              </Button>
            )}
            <Button
              variant="destructive"
              onClick={() => {
                if (conflict) void save(conflict.path, true)
                setConflict(null)
              }}
            >
              {conflict?.deleted ? 'Recréer' : 'Écraser'}
            </Button>
          </>
        }
      >
        {null}
      </Dialog>

      <Dialog
        open={closeConfirm !== null}
        role="alertdialog"
        onOpenChange={open => !open && setCloseConfirm(null)}
        title={`Enregistrer les modifications de ${closeConfirm ? basename(closeConfirm) : ''} ?`}
        description="Vos modifications seront perdues si vous fermez sans enregistrer."
        footer={
          <>
            <Button variant="secondary" onClick={() => setCloseConfirm(null)}>
              Annuler
            </Button>
            <Button
              variant="destructive"
              onClick={() => {
                if (closeConfirm) forgetTab(closeConfirm)
                setCloseConfirm(null)
              }}
            >
              Ne pas enregistrer
            </Button>
            <Button
              variant="primary"
              onClick={() => {
                const path = closeConfirm
                setCloseConfirm(null)
                if (path) void save(path).then(ok => ok && forgetTab(path))
              }}
            >
              Enregistrer
            </Button>
          </>
        }
      >
        {null}
      </Dialog>

      <Dialog
        open={uploadConflict !== null}
        role="alertdialog"
        closeOnBackdrop={false}
        onOpenChange={open => {
          if (!open && uploadConflict) {
            uploadConflict.resolve('skip')
            setUploadConflict(null)
          }
        }}
        title="Ce fichier existe déjà"
        description={uploadConflict?.path}
        footer={
          <>
            {(['cancel', 'skip-all', 'skip', 'replace-all', 'replace'] as const).map(choice => (
              <Button
                key={choice}
                variant={choice === 'replace' ? 'primary' : choice === 'replace-all' ? 'destructive' : 'secondary'}
                onClick={() => {
                  uploadConflict?.resolve(choice)
                  setUploadConflict(null)
                }}
              >
                {
                  {
                    cancel: 'Tout annuler',
                    'skip-all': 'Ignorer tous',
                    skip: 'Ignorer',
                    'replace-all': 'Remplacer tous',
                    replace: 'Remplacer',
                  }[choice]
                }
              </Button>
            ))}
          </>
        }
      >
        {null}
      </Dialog>
    </Root>
  )
}

function ContextMenu({
  state,
  onClose,
  onOpen,
  onDownload,
  onRename,
  onDelete,
  onCopyPath,
}: {
  state: ContextMenuState
  onClose: () => void
  onOpen: () => void
  onDownload: () => void
  onRename: () => void
  onDelete: () => void
  onCopyPath: () => void
}) {
  const ref = useRef<HTMLDivElement>(null)
  const [position, setPosition] = useState({ left: state.x, top: state.y })

  useEffect(() => {
    const menu = ref.current
    if (!menu) return
    const rect = menu.getBoundingClientRect()
    setPosition({
      left: Math.max(8, Math.min(state.x, window.innerWidth - rect.width - 8)),
      top: Math.max(8, Math.min(state.y, window.innerHeight - rect.height - 8)),
    })
    menu.querySelector<HTMLElement>('[role="menuitem"]')?.focus()
    const onPointerDown = (event: PointerEvent) => {
      if (!menu.contains(event.target as Node)) onClose()
    }
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKey)
    window.addEventListener('resize', onClose)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKey)
      window.removeEventListener('resize', onClose)
    }
  }, [onClose, state.x, state.y])

  const dir = isDirectory(state.entry)
  const items: Array<[string, typeof Copy, () => void, boolean?]> = [
    [dir ? 'Ouvrir le dossier' : 'Ouvrir', FolderOpen, onOpen],
    [dir ? 'Télécharger en .zip' : 'Télécharger', Download, onDownload],
    ['Renommer', Pencil, onRename],
    ['Copier le chemin', Copy, onCopyPath],
    ['Supprimer', Trash2, onDelete, true],
  ]
  return (
    <div
      ref={ref}
      role="menu"
      aria-label={`Actions pour ${state.entry.name}`}
      className="fixed z-40 min-w-48 rounded-md border border-border bg-popover p-1 shadow-lg"
      style={position}
      onKeyDown={event => {
        const all = Array.from(ref.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? [])
        const index = all.indexOf(document.activeElement as HTMLElement)
        if (event.key === 'ArrowDown') {
          event.preventDefault()
          all[(index + 1) % all.length]?.focus()
        } else if (event.key === 'ArrowUp') {
          event.preventDefault()
          all[(index - 1 + all.length) % all.length]?.focus()
        }
      }}
    >
      <p className="truncate px-2 py-1 text-2xs text-muted-foreground">{state.entry.name}</p>
      {items.map(([label, Icon, action, destructive]) => (
        <button
          key={label}
          type="button"
          role="menuitem"
          className="ui-menu__item ui-focus-ring w-full"
          data-variant={destructive ? 'destructive' : 'default'}
          onClick={() => {
            onClose()
            action()
          }}
        >
          <Icon size={14} aria-hidden /> {label}
        </button>
      ))}
    </div>
  )
}

function UploadPanel({ uploads }: { uploads: ReturnType<typeof useUploads> }) {
  const { items, cancel, cancelAll, clear } = uploads
  const [collapsed, setCollapsed] = useState(false)
  if (items.length === 0) return null
  const running = items.filter(item => item.status === 'uploading' || item.status === 'queued')
  const total = items.reduce((sum, item) => sum + item.size, 0)
  const loaded = items.reduce((sum, item) => sum + (item.status === 'done' ? item.size : item.loaded), 0)
  const failed = items.filter(item => item.status === 'error').length
  const done = items.filter(item => item.status === 'done').length
  return (
    <div className="absolute right-3 bottom-3 z-30 w-[min(22rem,calc(100%-1.5rem))] overflow-hidden rounded-lg border border-border bg-popover shadow-lg">
      <div className="flex items-center gap-2 border-b border-border px-3 py-2 text-xs">
        <CloudUpload size={14} className={running.length ? 'text-primary' : 'text-success'} aria-hidden />
        <button type="button" className="ui-focus-ring min-w-0 flex-1 truncate rounded text-left" onClick={() => setCollapsed(value => !value)}>
          {running.length
            ? `Envoi de ${running.length} fichier${running.length > 1 ? 's' : ''}… ${total ? Math.round((loaded / total) * 100) : 0} %`
            : `${done} fichier${done > 1 ? 's' : ''} envoyé${done > 1 ? 's' : ''}${failed ? `, ${failed} en échec` : ''}`}
        </button>
        {running.length ? (
          <Button size="sm" variant="ghost" onClick={cancelAll}>
            Tout annuler
          </Button>
        ) : (
          <IconButton aria-label="Fermer" size="sm" onClick={clear}>
            <X size={14} aria-hidden />
          </IconButton>
        )}
      </div>
      <div className="h-0.5 bg-secondary">
        <div className="h-full bg-primary transition-[width]" style={{ width: `${total ? (loaded / total) * 100 : 100}%` }} />
      </div>
      {!collapsed && (
        <ul className="max-h-56 overflow-y-auto py-1 text-xs">
          {items.map(item => (
            <li key={item.id} className="flex items-center gap-2 px-3 py-1">
              <span className="min-w-0 flex-1 truncate" title={item.path}>
                {item.name}
              </span>
              <span
                className={cn(
                  'shrink-0 tabular-nums',
                  item.status === 'error' ? 'text-destructive' : item.status === 'done' ? 'text-success' : 'text-muted-foreground',
                )}
                title={item.error}
              >
                {item.status === 'uploading'
                  ? `${item.size ? Math.round((item.loaded / item.size) * 100) : 0} %`
                  : item.status === 'queued'
                    ? 'en attente'
                    : item.status === 'done'
                      ? formatSize(item.size)
                      : item.status === 'skipped'
                        ? 'ignoré'
                        : item.status === 'cancelled'
                          ? 'annulé'
                          : 'échec'}
              </span>
              {(item.status === 'uploading' || item.status === 'queued') && (
                <button type="button" className="ui-focus-ring rounded text-muted-foreground hover:text-foreground" aria-label={`Annuler ${item.name}`} onClick={() => cancel(item.id)}>
                  <X size={12} aria-hidden />
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
