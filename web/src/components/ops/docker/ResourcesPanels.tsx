import { Download, Loader2, Play, Plus, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { Button, Dialog, IconButton } from '../../ui'
import { formatBytes, formatRelative } from '../../monitoring/format'
import {
  DockerApiError,
  createContainer,
  createVolume,
  fetchImages,
  fetchVolumes,
  pruneImages,
  pullImage,
  removeImage,
  removeVolume,
} from './api'
import { InlineError } from './ContainersList'
import { parseEnv, parseMounts, parsePorts } from './model'
import type { DockerCreateRequest, DockerImage, DockerImages, DockerVolume, DockerVolumes } from './types'

const REFRESH_MS = 10_000
const inputClass =
  'ui-focus-ring h-9 w-full min-w-0 rounded-md border border-border bg-input px-2.5 text-xs text-foreground placeholder:text-muted-foreground'
const areaClass =
  'ui-focus-ring w-full min-w-0 rounded-md border border-border bg-input px-2.5 py-1.5 font-mono text-xs text-foreground placeholder:text-muted-foreground'

function message(error: unknown, fallback: string) {
  return error instanceof Error ? error.message : fallback
}

/** Loads a listing on mount, every 10 s while visible, and on demand. */
export function useListing<T>(load: (signal: AbortSignal) => Promise<T>) {
  const [data, setData] = useState<T | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  const [forbidden, setForbidden] = useState(false)
  const controllerRef = useRef<AbortController | null>(null)

  const refresh = useCallback(async () => {
    controllerRef.current?.abort()
    const controller = new AbortController()
    controllerRef.current = controller
    try {
      const next = await load(controller.signal)
      if (controller.signal.aborted) return
      setData(next)
      setFailure(null)
    } catch (error) {
      if (controller.signal.aborted) return
      if (error instanceof DockerApiError && (error.status === 401 || error.status === 403)) setForbidden(true)
      else setFailure(message(error, 'Erreur inconnue'))
    }
  }, [load])

  useEffect(() => {
    void refresh()
    const timer = setInterval(() => {
      if (document.visibilityState !== 'hidden') void refresh()
    }, REFRESH_MS)
    return () => {
      clearInterval(timer)
      controllerRef.current?.abort()
    }
  }, [refresh])

  return { data, failure, forbidden, refresh }
}

export function ListingState({ data, failure, forbidden, what }: { data: unknown; failure: string | null; forbidden: boolean; what: string }) {
  if (forbidden) return <p className="text-xs text-muted-foreground">Cette page est réservée aux administrateurs.</p>
  if (!data && failure) return <InlineError message={`Impossible de lire les ${what} : ${failure}`} />
  if (!data) return <p className="text-xs text-muted-foreground">Chargement…</p>
  return null
}

function UsedBy({ names }: { names: string[] }) {
  if (names.length === 0) return <span className="text-muted-foreground">Inutilisé</span>
  return (
    <span className="block max-w-[16rem] truncate" title={names.join(', ')}>
      {names.join(', ')}
    </span>
  )
}

interface DeleteRequest {
  title: string
  body: string
  run: () => Promise<unknown>
}

function DeleteDialog({ request, onClose, onDone }: { request: DeleteRequest | null; onClose: () => void; onDone: () => void }) {
  const cancelRef = useRef<HTMLButtonElement>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    setError(null)
    setBusy(false)
  }, [request])
  return (
    <Dialog
      open={Boolean(request)}
      onOpenChange={open => {
        if (!open && !busy) onClose()
      }}
      initialFocusRef={cancelRef}
      role="alertdialog"
      title={request?.title ?? ''}
      description={request?.body}
      footer={
        <>
          <Button ref={cancelRef} variant="ghost" onClick={onClose} disabled={busy}>
            Annuler
          </Button>
          <Button
            variant="destructive"
            disabled={busy}
            onClick={async () => {
              if (!request) return
              setBusy(true)
              setError(null)
              try {
                await request.run()
                onDone()
              } catch (failure) {
                setError(message(failure, 'Suppression impossible'))
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? 'Suppression…' : 'Supprimer définitivement'}
          </Button>
        </>
      }
    >
      {error && <InlineError message={error} />}
    </Dialog>
  )
}

export function Table({ label, head, children }: { label: string; head: string[]; children: ReactNode }) {
  return (
    <div role="region" aria-label={label} className="overflow-x-auto rounded-lg border border-border bg-card">
      <table className="w-full border-collapse text-left text-xs font-normal">
        <thead>
          <tr className="border-b border-border text-2xs text-muted-foreground">
            {head.map((title, index) => (
              <th key={index} scope="col" className="whitespace-nowrap px-3 py-2 font-medium">
                {title || <span className="sr-only">Actions</span>}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  )
}

export function Empty({ children }: { children: ReactNode }) {
  return (
    <div className="rounded-lg border border-border bg-card px-3 py-6 text-center text-xs text-muted-foreground">{children}</div>
  )
}

function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="flex flex-col gap-1 text-xs">
      <span className="font-medium text-foreground">{label}</span>
      {children}
      {hint && <span className="text-2xs text-muted-foreground">{hint}</span>}
    </label>
  )
}

/** Docker Desktop's "Run": creates and starts a container from a local image. */
function RunDialog({ image, onClose, onCreated }: { image: string | null; onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState('')
  const [ports, setPorts] = useState('')
  const [env, setEnv] = useState('')
  const [mounts, setMounts] = useState('')
  const [restart, setRestart] = useState<NonNullable<DockerCreateRequest['restart']>>('no')
  const [publicPorts, setPublicPorts] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    setName('')
    setPorts('')
    setEnv('')
    setMounts('')
    setRestart('no')
    setPublicPorts(false)
    setError(null)
    setBusy(false)
  }, [image])

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!image || busy) return
    const parsedPorts = parsePorts(ports, publicPorts ? '0.0.0.0' : '127.0.0.1')
    const parsedEnv = parseEnv(env)
    const parsedMounts = parseMounts(mounts)
    for (const parsed of [parsedPorts, parsedEnv, parsedMounts]) {
      if (typeof parsed === 'string') {
        setError(parsed)
        return
      }
    }
    setBusy(true)
    setError(null)
    try {
      await createContainer({
        image,
        name: name.trim() || undefined,
        ports: parsedPorts as Exclude<typeof parsedPorts, string>,
        env: parsedEnv as string[],
        volumes: parsedMounts as Exclude<typeof parsedMounts, string>,
        restart,
      })
      onCreated()
    } catch (failure) {
      setError(message(failure, 'Création impossible'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog
      open={Boolean(image)}
      onOpenChange={open => {
        if (!open && !busy) onClose()
      }}
      title={image ? `Lancer un conteneur depuis « ${image} »` : ''}
      description="Le conteneur est créé puis démarré. Tout est facultatif."
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Annuler
          </Button>
          <Button variant="primary" type="submit" form="docker-run-form" disabled={busy}>
            {busy ? 'Lancement…' : 'Lancer'}
          </Button>
        </>
      }
    >
      <form id="docker-run-form" onSubmit={submit} className="flex flex-col gap-3">
        <Field label="Nom du conteneur" hint="Vide : Docker en choisit un.">
          <input className={inputClass} value={name} onChange={event => setName(event.target.value)} placeholder="mon-conteneur" />
        </Field>
        <Field label="Ports" hint="Un par ligne : port du serveur, puis port du conteneur.">
          <textarea className={areaClass} rows={2} value={ports} onChange={event => setPorts(event.target.value)} placeholder="8080:80" />
        </Field>
        <label className="flex items-start gap-2 text-xs">
          <input type="checkbox" className="mt-0.5" checked={publicPorts} onChange={event => setPublicPorts(event.target.checked)} />
          <span>
            Ouvrir ces ports sur Internet
            <span className="block text-2xs text-muted-foreground">
              Décoché : ils ne répondent que sur le serveur lui-même (127.0.0.1).
            </span>
          </span>
        </label>
        <Field label="Volumes" hint="Un par ligne : nom du volume, puis chemin dans le conteneur.">
          <textarea className={areaClass} rows={2} value={mounts} onChange={event => setMounts(event.target.value)} placeholder="mes-donnees:/data" />
        </Field>
        <Field label="Variables d’environnement" hint="Une par ligne.">
          <textarea className={areaClass} rows={2} value={env} onChange={event => setEnv(event.target.value)} placeholder="NOM=valeur" />
        </Field>
        <Field label="Redémarrage automatique">
          <select className={inputClass} value={restart} onChange={event => setRestart(event.target.value as typeof restart)}>
            <option value="no">Jamais</option>
            <option value="unless-stopped">Toujours, sauf si je l’arrête</option>
            <option value="always">Toujours</option>
            <option value="on-failure">Seulement après une erreur</option>
          </select>
        </Field>
        {error && <InlineError message={error} onDismiss={() => setError(null)} />}
      </form>
    </Dialog>
  )
}

function imageName(image: DockerImage) {
  return image.tags?.[0] ?? image.short_id
}

export function ImagesPanel({ onContainersChanged }: { onContainersChanged: () => void }) {
  const listing = useListing<DockerImages>(fetchImages)
  const [reference, setReference] = useState('')
  const [pulling, setPulling] = useState<string | null>(null)
  const [pruning, setPruning] = useState(false)
  const [notice, setNotice] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [toDelete, setToDelete] = useState<DeleteRequest | null>(null)
  const [toRun, setToRun] = useState<string | null>(null)

  const pull = async (event: FormEvent) => {
    event.preventDefault()
    const image = reference.trim()
    if (!image || pulling) return
    setPulling(image)
    setError(null)
    setNotice(null)
    try {
      const result = await pullImage(image)
      setNotice(`${result.name} : ${result.detail ?? 'téléchargée'}`)
      setReference('')
      await listing.refresh()
    } catch (failure) {
      setError(message(failure, 'Téléchargement impossible'))
    } finally {
      setPulling(null)
    }
  }

  const prune = async () => {
    setPruning(true)
    setError(null)
    setNotice(null)
    try {
      const result = await pruneImages()
      setNotice(`Nettoyage terminé : ${formatBytes(Number(result.detail ?? 0))} libérés.`)
      await listing.refresh()
    } catch (failure) {
      setError(message(failure, 'Nettoyage impossible'))
    } finally {
      setPruning(false)
    }
  }

  const images = listing.data?.images ?? []
  const untagged = images.filter(image => !image.tags?.length && !image.containers?.length).length
  const pending = <ListingState {...listing} what="images" />

  return (
    <div className="flex flex-col gap-3">
      <form onSubmit={pull} className="flex flex-wrap items-end gap-2">
        <label className="flex min-w-0 flex-1 basis-64 flex-col gap-1 text-xs sm:max-w-md">
          <span className="font-medium">Ajouter une image</span>
          <input
            className={inputClass}
            value={reference}
            onChange={event => setReference(event.target.value)}
            placeholder="nginx:1.27 ou ghcr.io/org/app:latest"
            disabled={Boolean(pulling)}
          />
        </label>
        <Button type="submit" variant="primary" disabled={!reference.trim() || Boolean(pulling)}>
          {pulling ? <Loader2 size={14} aria-hidden className="animate-spin" /> : <Download size={14} aria-hidden />}
          {pulling ? 'Téléchargement…' : 'Télécharger'}
        </Button>
        {untagged > 0 && (
          <Button type="button" onClick={() => void prune()} disabled={pruning}>
            {pruning ? 'Nettoyage…' : `Nettoyer ${untagged} image${untagged > 1 ? 's' : ''} sans étiquette`}
          </Button>
        )}
        {listing.data && (
          <span className="ml-auto text-2xs text-muted-foreground" aria-live="polite">
            {images.length} image{images.length > 1 ? 's' : ''} · {formatBytes(listing.data.total_size)}
          </span>
        )}
      </form>
      {pulling && (
        <p className="text-2xs text-muted-foreground" role="status">
          Téléchargement de « {pulling} » en cours. Une grosse image peut prendre plusieurs minutes.
        </p>
      )}
      {notice && (
        <p className="text-2xs text-muted-foreground" role="status">
          {notice}
        </p>
      )}
      {error && <InlineError message={error} onDismiss={() => setError(null)} />}
      {listing.data && listing.failure && (
        <p className="text-2xs text-warning" role="status">
          Dernière actualisation en échec ({listing.failure}).
        </p>
      )}
      {!listing.data ? (
        pending
      ) : images.length === 0 ? (
        <Empty>Aucune image sur ce serveur.</Empty>
      ) : (
        <Table label="Images" head={['Image', 'Identifiant', 'Taille', 'Créée', 'Utilisée par', '']}>
          {images.map(image => {
            const used = image.containers ?? []
            const tags = image.tags ?? []
            return (
              <tr key={image.id} className="border-b border-border/60 last:border-b-0">
                <td className="max-w-[22rem] px-3 py-2">
                  {tags.length ? (
                    tags.map(tag => (
                      <span key={tag} className="block truncate font-medium text-foreground" title={tag}>
                        {tag}
                      </span>
                    ))
                  ) : (
                    <span className="text-muted-foreground">Sans étiquette</span>
                  )}
                </td>
                <td className="whitespace-nowrap px-3 py-2 font-mono text-2xs text-muted-foreground">{image.short_id}</td>
                <td className="whitespace-nowrap px-3 py-2 tabular-nums">{formatBytes(image.size)}</td>
                <td className="whitespace-nowrap px-3 py-2 text-muted-foreground">{formatRelative(image.created)}</td>
                <td className="px-3 py-2">
                  <UsedBy names={used} />
                </td>
                <td className="whitespace-nowrap px-2 py-1 text-right">
                  {tags.length > 0 && (
                    <IconButton size="sm" aria-label={`Lancer un conteneur depuis ${tags[0]}`} title="Lancer un conteneur" onClick={() => setToRun(tags[0])}>
                      <Play size={14} aria-hidden />
                    </IconButton>
                  )}
                  <IconButton
                    size="sm"
                    aria-label={`Supprimer l’image ${imageName(image)}`}
                    title={used.length ? 'Utilisée par un conteneur : supprime-le d’abord' : 'Supprimer'}
                    disabled={used.length > 0}
                    onClick={() =>
                      setToDelete({
                        title: `Supprimer l’image « ${imageName(image)} » ?`,
                        body: `Elle libère jusqu’à ${formatBytes(image.size)}. Il faudra la télécharger ou la reconstruire pour s’en resservir.`,
                        run: () => removeImage(image.id),
                      })
                    }
                  >
                    <Trash2 size={14} aria-hidden />
                  </IconButton>
                </td>
              </tr>
            )
          })}
        </Table>
      )}
      <DeleteDialog
        request={toDelete}
        onClose={() => setToDelete(null)}
        onDone={() => {
          setToDelete(null)
          void listing.refresh()
        }}
      />
      <RunDialog
        image={toRun}
        onClose={() => setToRun(null)}
        onCreated={() => {
          setNotice(`Conteneur lancé depuis ${toRun}. Il apparaît dans l’onglet Conteneurs.`)
          setToRun(null)
          void listing.refresh()
          onContainersChanged()
        }}
      />
    </div>
  )
}

function volumeLabel(volume: DockerVolume) {
  return volume.anonymous ? `${volume.name.slice(0, 12)}…` : volume.name
}

export function VolumesPanel() {
  const listing = useListing<DockerVolumes>(fetchVolumes)
  const [name, setName] = useState('')
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [toDelete, setToDelete] = useState<DeleteRequest | null>(null)

  const create = async (event: FormEvent) => {
    event.preventDefault()
    const wanted = name.trim()
    if (!wanted || creating) return
    setCreating(true)
    setError(null)
    try {
      await createVolume(wanted)
      setName('')
      await listing.refresh()
    } catch (failure) {
      setError(message(failure, 'Création impossible'))
    } finally {
      setCreating(false)
    }
  }

  const volumes = listing.data?.volumes ?? []
  const pending = <ListingState {...listing} what="volumes" />

  return (
    <div className="flex flex-col gap-3">
      <form onSubmit={create} className="flex flex-wrap items-end gap-2">
        <label className="flex min-w-0 flex-1 basis-56 flex-col gap-1 text-xs sm:max-w-xs">
          <span className="font-medium">Créer un volume</span>
          <input className={inputClass} value={name} onChange={event => setName(event.target.value)} placeholder="mes-donnees" disabled={creating} />
        </label>
        <Button type="submit" variant="primary" disabled={!name.trim() || creating}>
          <Plus size={14} aria-hidden />
          {creating ? 'Création…' : 'Créer'}
        </Button>
        {listing.data && (
          <span className="ml-auto text-2xs text-muted-foreground" aria-live="polite">
            {volumes.length} volume{volumes.length > 1 ? 's' : ''}
          </span>
        )}
      </form>
      {error && <InlineError message={error} onDismiss={() => setError(null)} />}
      {listing.data && listing.failure && (
        <p className="text-2xs text-warning" role="status">
          Dernière actualisation en échec ({listing.failure}).
        </p>
      )}
      {!listing.data ? (
        pending
      ) : volumes.length === 0 ? (
        <Empty>Aucun volume sur ce serveur.</Empty>
      ) : (
        <Table label="Volumes" head={['Volume', 'Projet', 'Taille', 'Créé', 'Utilisé par', '']}>
          {volumes.map(volume => {
            const used = volume.containers ?? []
            const created = volume.created_at ? Date.parse(volume.created_at) / 1000 : undefined
            return (
              <tr key={volume.name} className="border-b border-border/60 last:border-b-0">
                <td className="max-w-[22rem] px-3 py-2">
                  <span className="block truncate font-medium text-foreground" title={volume.name}>
                    {volumeLabel(volume)}
                  </span>
                  {volume.anonymous && <span className="text-2xs text-muted-foreground">Volume anonyme</span>}
                </td>
                <td className="whitespace-nowrap px-3 py-2 text-muted-foreground">{volume.compose_project || '—'}</td>
                <td className="whitespace-nowrap px-3 py-2 tabular-nums">
                  {volume.size === null ? <span className="text-muted-foreground">Calcul…</span> : formatBytes(volume.size)}
                </td>
                <td className="whitespace-nowrap px-3 py-2 text-muted-foreground">{created ? formatRelative(created) : '—'}</td>
                <td className="px-3 py-2">
                  <UsedBy names={used} />
                </td>
                <td className="whitespace-nowrap px-2 py-1 text-right">
                  <IconButton
                    size="sm"
                    aria-label={`Supprimer le volume ${volumeLabel(volume)}`}
                    title={used.length ? 'Monté par un conteneur : supprime-le d’abord' : 'Supprimer'}
                    disabled={used.length > 0}
                    onClick={() =>
                      setToDelete({
                        title: `Supprimer le volume « ${volumeLabel(volume)} » ?`,
                        body: 'Toutes les données qu’il contient sont perdues, sans retour possible.',
                        run: () => removeVolume(volume.name),
                      })
                    }
                  >
                    <Trash2 size={14} aria-hidden />
                  </IconButton>
                </td>
              </tr>
            )
          })}
        </Table>
      )}
      <DeleteDialog
        request={toDelete}
        onClose={() => setToDelete(null)}
        onDone={() => {
          setToDelete(null)
          void listing.refresh()
        }}
      />
    </div>
  )
}
