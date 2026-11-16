import { useCallback, useEffect, useRef, useState } from 'react'
import { ArrowUp, Download, File, Folder, FolderPlus, Link2, Pencil, RefreshCw, Save, Trash2, Upload, X } from 'lucide-react'
import {
  MAX_UPLOAD_BYTES,
  deleteFile,
  downloadURL,
  fetchFileHosts,
  fetchFileInfo,
  joinPath,
  listFiles,
  makeDirectory,
  parentPath,
  readFile,
  renameFile,
  saveFile,
  uploadFile,
  type FileContent,
  type FileEntry,
  type FileHost,
  type FileInfo,
} from '../lib/filesApi'
import { useHostLabel } from '../lib/hostLabel'
import { getBrandStorage, setBrandStorage } from '../lib/brandStorage'
import { Button } from './ui'

function errorText(err: unknown, fallback: string): string {
  return err instanceof Error ? err.message : fallback
}

function formatSize(size: number): string {
  if (size < 1024) return `${size} o`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} Kio`
  return `${(size / 1024 / 1024).toFixed(1)} Mio`
}

const modeLabels: Record<FileInfo['mode'], string> = {
  off: 'fichiers désactivés sur ce compte',
  ro: 'lecture seule',
  rw: 'lecture et écriture',
}

/**
 * File browser. The account selector lists the Unix accounts (hosts) the
 * signed-in person may browse; each one's files are served by that account's
 * own process, within the ceiling it sets for itself.
 */
export function FilesPage() {
  const hostLabel = useHostLabel()
  const [hosts, setHosts] = useState<FileHost[]>([])
  const [host, setHost] = useState<string>('')
  const [info, setInfo] = useState<FileInfo | null>(null)
  const [path, setPath] = useState('/')
  const [entries, setEntries] = useState<FileEntry[]>([])
  const [truncated, setTruncated] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [opened, setOpened] = useState<FileContent | null>(null)
  const [draft, setDraft] = useState('')
  const [saving, setSaving] = useState(false)
  const uploadRef = useRef<HTMLInputElement>(null)
  const writable = info?.mode === 'rw'

  useEffect(() => {
    let cancelled = false
    fetchFileHosts()
      .then(list => {
        if (cancelled) return
        setHosts(list)
        let remembered = ''
        try {
          remembered = getBrandStorage('files-host') || ''
        } catch {
          /* ignore */
        }
        const first = list.find(item => item.id === remembered) ?? list.find(item => item.online) ?? list[0]
        if (first) setHost(first.id)
      })
      .catch(err => !cancelled && setError(errorText(err, 'Impossible de charger les comptes.')))
    return () => {
      cancelled = true
    }
  }, [])

  const load = useCallback(
    async (target: string, signal?: AbortSignal) => {
      if (!host) return
      setLoading(true)
      setError(null)
      try {
        const listing = await listFiles(host, target, signal)
        setPath(listing.path || '/')
        setEntries(listing.entries)
        setTruncated(listing.truncated)
      } catch (err) {
        if (!signal?.aborted) {
          setEntries([])
          setError(errorText(err, 'Impossible de lire ce dossier.'))
        }
      } finally {
        if (!signal?.aborted) setLoading(false)
      }
    },
    [host],
  )

  useEffect(() => {
    if (!host) return
    try {
      setBrandStorage('files-host', host)
    } catch {
      /* ignore */
    }
    const controller = new AbortController()
    setOpened(null)
    setInfo(null)
    fetchFileInfo(host)
      .then(value => !controller.signal.aborted && setInfo(value))
      .catch(err => !controller.signal.aborted && setError(errorText(err, 'Hôte indisponible.')))
    void load('/', controller.signal)
    return () => controller.abort()
  }, [host, load])

  const open = async (entry: FileEntry) => {
    const target = joinPath(path, entry.name)
    if (entry.type === 'dir') {
      setOpened(null)
      await load(target)
      return
    }
    if (entry.type !== 'file') {
      setError('Les liens symboliques et les fichiers spéciaux ne sont pas ouverts.')
      return
    }
    setError(null)
    try {
      const content = await readFile(host, target)
      setOpened(content)
      setDraft(content.content)
    } catch (err) {
      setError(errorText(err, 'Impossible d’ouvrir ce fichier.'))
    }
  }

  const save = async () => {
    if (!opened) return
    setSaving(true)
    setError(null)
    try {
      const saved = await saveFile(host, opened.path, draft, opened.version)
      setOpened({ ...opened, ...saved, content: draft })
      await load(path)
    } catch (err) {
      setError(errorText(err, 'Impossible d’enregistrer.'))
    } finally {
      setSaving(false)
    }
  }

  const run = async (action: () => Promise<void>, fallback: string) => {
    setError(null)
    try {
      await action()
      await load(path)
    } catch (err) {
      setError(errorText(err, fallback))
    }
  }

  const validName = (name: string | null): name is string =>
    Boolean(name && name.trim() && !name.includes('/') && name !== '.' && name !== '..')

  const createFolder = () => {
    const name = window.prompt('Nom du nouveau dossier')
    if (!validName(name)) return
    void run(() => makeDirectory(host, joinPath(path, name.trim())), 'Impossible de créer le dossier.')
  }

  const rename = (entry: FileEntry) => {
    const name = window.prompt('Nouveau nom', entry.name)
    if (!validName(name) || name === entry.name) return
    void run(() => renameFile(host, joinPath(path, entry.name), joinPath(path, name.trim())), 'Impossible de renommer.')
  }

  const remove = (entry: FileEntry) => {
    if (!window.confirm(`Supprimer « ${entry.name} » ? Il n’y a pas de corbeille.`)) return
    void run(() => deleteFile(host, joinPath(path, entry.name)), 'Impossible de supprimer.')
  }

  const upload = async (list: FileList | null) => {
    if (!list) return
    for (const file of Array.from(list)) {
      if (file.size > MAX_UPLOAD_BYTES) {
        setError(`« ${file.name} » dépasse 1 Mio par envoi.`)
        continue
      }
      const target = joinPath(path, file.name)
      const exists = entries.some(entry => entry.name === file.name)
      if (exists && !window.confirm(`Remplacer « ${file.name} » ?`)) continue
      await run(() => uploadFile(host, target, file, exists), `Impossible d’envoyer « ${file.name} ».`)
    }
    if (uploadRef.current) uploadRef.current.value = ''
  }

  const current = hosts.find(item => item.id === host)
  const breadcrumbs = path === '/' ? [] : path.split('/').filter(Boolean)

  return (
    <main className="flex-1 overflow-y-auto p-4 text-sm sm:p-6">
      <div className="mx-auto max-w-5xl space-y-4">
        <header className="flex flex-wrap items-end justify-between gap-3">
          <div className="space-y-1">
            <h1 className="text-2xl tracking-tight text-foreground">Fichiers</h1>
            <p className="text-xs text-muted-foreground">
              Chaque compte est servi par son propre processus, sous son propre utilisateur Linux.
            </p>
          </div>
          <label className="space-y-1 text-xs text-muted-foreground">
            <span className="block">Compte</span>
            <select
              aria-label="Compte dont parcourir les fichiers"
              value={host}
              onChange={event => {
                setHost(event.target.value)
                setPath('/')
              }}
              className="ui-focus-ring min-h-11 rounded border border-border bg-input px-2.5 text-sm text-foreground"
            >
              {hosts.length === 0 && <option value="">Aucun compte disponible</option>}
              {hosts.map(item => (
                <option key={item.id} value={item.id}>
                  {hostLabel(item.id, item.name)}
                  {item.local ? ' (hub)' : ''}
                  {item.online ? '' : ' — hors ligne'}
                </option>
              ))}
            </select>
          </label>
        </header>

        {current && info && (
          <p data-testid="files-mode" className="text-xs text-muted-foreground">
            {info.root} · {modeLabels[info.mode]}
          </p>
        )}

        {error && (
          <p role="alert" className="rounded border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
            {error}
          </p>
        )}

        <div className="flex flex-wrap items-center gap-2">
          <Button
            type="button"
            variant="secondary"
            size="sm"
            disabled={path === '/' || !host}
            leadingIcon={<ArrowUp size={14} aria-hidden="true" />}
            onClick={() => void load(parentPath(path))}
          >
            Dossier parent
          </Button>
          <Button
            type="button"
            variant="secondary"
            size="sm"
            disabled={!host}
            leadingIcon={<RefreshCw size={14} aria-hidden="true" />}
            onClick={() => void load(path)}
          >
            Actualiser
          </Button>
          {writable && (
            <>
              <Button type="button" variant="secondary" size="sm" leadingIcon={<FolderPlus size={14} aria-hidden="true" />} onClick={createFolder}>
                Nouveau dossier
              </Button>
              <Button
                type="button"
                variant="secondary"
                size="sm"
                leadingIcon={<Upload size={14} aria-hidden="true" />}
                onClick={() => uploadRef.current?.click()}
              >
                Envoyer
              </Button>
              <input
                ref={uploadRef}
                type="file"
                multiple
                hidden
                aria-label="Fichiers à envoyer"
                onChange={event => void upload(event.target.files)}
              />
            </>
          )}
          <nav aria-label="Chemin" className="ml-1 flex flex-wrap items-center gap-1 font-mono text-xs text-muted-foreground">
            <button type="button" className="ui-focus-ring rounded px-1 hover:text-foreground" onClick={() => void load('/')}>
              ~
            </button>
            {breadcrumbs.map((part, index) => (
              <span key={index} className="flex items-center gap-1">
                <span>/</span>
                <button
                  type="button"
                  className="ui-focus-ring rounded px-1 hover:text-foreground"
                  onClick={() => void load('/' + breadcrumbs.slice(0, index + 1).join('/'))}
                >
                  {part}
                </button>
              </span>
            ))}
          </nav>
        </div>

        <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.2fr)]">
          <section aria-label="Contenu du dossier" className="atlas-surface rounded-xl p-2">
            {loading && <p className="p-2 text-xs text-muted-foreground">Chargement…</p>}
            {!loading && entries.length === 0 && !error && <p className="p-2 text-xs text-muted-foreground">Dossier vide.</p>}
            <ul className="divide-y divide-border">
              {entries.map(entry => (
                <li key={entry.name} className="flex items-center gap-2 px-2 py-1.5">
                  <button
                    type="button"
                    onClick={() => void open(entry)}
                    className="ui-focus-ring flex min-h-9 min-w-0 flex-1 items-center gap-2 rounded text-left"
                  >
                    {entry.type === 'dir' ? (
                      <Folder size={14} aria-hidden="true" className="shrink-0 text-muted-foreground" />
                    ) : entry.type === 'symlink' ? (
                      <Link2 size={14} aria-hidden="true" className="shrink-0 text-muted-foreground" />
                    ) : (
                      <File size={14} aria-hidden="true" className="shrink-0 text-muted-foreground" />
                    )}
                    <span className="truncate font-mono text-xs text-foreground">{entry.name}</span>
                  </button>
                  {entry.type === 'file' && (
                    <span className="shrink-0 text-2xs text-muted-foreground tabular-nums">{formatSize(entry.size)}</span>
                  )}
                  {entry.type === 'file' && (
                    <a
                      href={downloadURL(host, joinPath(path, entry.name))}
                      download={entry.name}
                      aria-label={`Télécharger ${entry.name}`}
                      className="ui-focus-ring rounded p-1 text-muted-foreground hover:text-foreground"
                    >
                      <Download size={14} aria-hidden="true" />
                    </a>
                  )}
                  {writable && (
                    <>
                      <button
                        type="button"
                        aria-label={`Renommer ${entry.name}`}
                        onClick={() => rename(entry)}
                        className="ui-focus-ring rounded p-1 text-muted-foreground hover:text-foreground"
                      >
                        <Pencil size={14} aria-hidden="true" />
                      </button>
                      <button
                        type="button"
                        aria-label={`Supprimer ${entry.name}`}
                        onClick={() => remove(entry)}
                        className="ui-focus-ring rounded p-1 text-muted-foreground hover:text-destructive"
                      >
                        <Trash2 size={14} aria-hidden="true" />
                      </button>
                    </>
                  )}
                </li>
              ))}
            </ul>
            {truncated && <p className="p-2 text-2xs text-muted-foreground">Liste tronquée à 2000 entrées.</p>}
          </section>

          {opened && (
            <section aria-label={`Fichier ${opened.path}`} className="atlas-surface flex min-h-80 flex-col rounded-xl p-3">
              <div className="mb-2 flex items-center justify-between gap-2">
                <span className="truncate font-mono text-xs text-foreground">{opened.path}</span>
                <div className="flex items-center gap-2">
                  {writable && opened.writable && (
                    <Button
                      type="button"
                      variant="primary"
                      size="sm"
                      loading={saving}
                      disabled={draft === opened.content}
                      leadingIcon={<Save size={14} aria-hidden="true" />}
                      onClick={() => void save()}
                    >
                      Enregistrer
                    </Button>
                  )}
                  <button
                    type="button"
                    aria-label="Fermer le fichier"
                    onClick={() => setOpened(null)}
                    className="ui-focus-ring rounded p-1 text-muted-foreground hover:text-foreground"
                  >
                    <X size={14} aria-hidden="true" />
                  </button>
                </div>
              </div>
              <textarea
                aria-label="Contenu du fichier"
                value={draft}
                readOnly={!(writable && opened.writable)}
                spellCheck={false}
                onChange={event => setDraft(event.target.value)}
                className="ui-focus-ring min-h-72 flex-1 resize-none rounded bg-input p-2 font-mono text-xs text-foreground"
              />
            </section>
          )}
        </div>
      </div>
    </main>
  )
}
