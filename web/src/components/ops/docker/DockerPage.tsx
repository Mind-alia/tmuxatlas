import { useCallback, useEffect, useRef, useState } from 'react'
import { runAction, openTerminal } from './api'
import { ContainerDetail } from './ContainerDetail'
import { ContainersList, InlineError, SummaryTiles, type ListCallbacks } from './ContainersList'
import { isActive } from './model'
import { ConfirmDialog, Tabs, type ConfirmRequest, type DetailTab, type PendingKind } from './parts'
import { ImagesPanel, VolumesPanel } from './ResourcesPanels'
import { useDockerContainers } from './useDockerContainers'
import type { DockerAction, DockerContainer } from './types'

const detailTabs: DetailTab[] = ['logs', 'inspect', 'stats', 'terminal']

type Section = 'containers' | 'images' | 'volumes'

const sections: { id: Section; label: string }[] = [
  { id: 'containers', label: 'Conteneurs' },
  { id: 'images', label: 'Images' },
  { id: 'volumes', label: 'Volumes' },
]

/** The section lives in the URL too (?vue=images), so a reload keeps it. */
function readSection(): Section {
  const value = new URLSearchParams(window.location.search).get('vue')
  return value === 'images' || value === 'volumes' ? value : 'containers'
}

function writeSection(section: Section) {
  const params = new URLSearchParams(window.location.search)
  if (section === 'containers') params.delete('vue')
  else params.set('vue', section)
  params.delete('c')
  params.delete('onglet')
  const query = params.toString()
  window.history.pushState(window.history.state, '', `${window.location.pathname}${query ? `?${query}` : ''}`)
}

/** The open container and tab live in the URL (?c=…&onglet=…), so Back returns to the list. */
function readLocation(): { id: string | null; tab: DetailTab } {
  const params = new URLSearchParams(window.location.search)
  const tab = params.get('onglet') as DetailTab | null
  return { id: params.get('c'), tab: tab && detailTabs.includes(tab) ? tab : 'logs' }
}

function writeLocation(id: string | null, tab: DetailTab, push: boolean) {
  const params = new URLSearchParams(window.location.search)
  if (id) {
    params.set('c', id)
    if (tab === 'logs') params.delete('onglet')
    else params.set('onglet', tab)
  } else {
    params.delete('c')
    params.delete('onglet')
  }
  const query = params.toString()
  const url = `${window.location.pathname}${query ? `?${query}` : ''}`
  if (url === `${window.location.pathname}${window.location.search}`) return
  if (push) window.history.pushState(window.history.state, '', url)
  else window.history.replaceState(window.history.state, '', url)
}

/** Stop and restart of a running container, and every delete, are confirmed first. */
export function needsConfirmation(c: DockerContainer, action: DockerAction) {
  return action === 'delete' || ((action === 'stop' || action === 'restart') && isActive(c))
}

function omit<T>(record: Record<string, T>, key: string): Record<string, T> {
  if (!(key in record)) return record
  const next = { ...record }
  delete next[key]
  return next
}

export function DockerPage({ onOpenSession }: { onOpenSession?: (sessionKey: string) => void }) {
  const { data, history, status, failure, refresh } = useDockerContainers()
  const [{ id: selectedId, tab }, setSelection] = useState(readLocation)
  const [section, setSection] = useState<Section>(readSection)
  const [pending, setPending] = useState<Record<string, PendingKind>>({})
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [confirm, setConfirm] = useState<ConfirmRequest | null>(null)
  const selectedRef = useRef(selectedId)
  useEffect(() => {
    selectedRef.current = selectedId
  }, [selectedId])

  useEffect(() => {
    const onPop = () => {
      setSelection(readLocation())
      setSection(readSection())
    }
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])

  const open = useCallback((c: DockerContainer, nextTab: DetailTab = 'logs') => {
    writeLocation(c.id.slice(0, 12), nextTab, true)
    setSelection({ id: c.id.slice(0, 12), tab: nextTab })
  }, [])
  const back = useCallback(() => {
    writeLocation(null, 'logs', true)
    setSelection({ id: null, tab: 'logs' })
  }, [])
  const changeTab = useCallback(
    (nextTab: DetailTab) => {
      writeLocation(selectedId, nextTab, false)
      setSelection(current => ({ ...current, tab: nextTab }))
    },
    [selectedId],
  )

  const perform = useCallback(
    async (c: DockerContainer, action: DockerAction) => {
      setPending(p => ({ ...p, [c.id]: action }))
      setErrors(e => omit(e, c.id))
      try {
        await runAction(c.id, action)
        // A deleted container leaves its detail view for the list.
        if (action === 'delete' && selectedRef.current && c.id.startsWith(selectedRef.current)) {
          writeLocation(null, 'logs', false)
          setSelection({ id: null, tab: 'logs' })
        }
        await refresh()
      } catch (error) {
        const message = error instanceof Error ? error.message : 'Action impossible'
        setErrors(e => ({ ...e, [c.id]: message }))
        void refresh()
      } finally {
        setPending(p => omit(p, c.id))
      }
    },
    [refresh],
  )

  const request = useCallback(
    (c: DockerContainer, action: DockerAction) => {
      if (needsConfirmation(c, action)) setConfirm({ container: c, action })
      else void perform(c, action)
    },
    [perform],
  )

  const terminal = useCallback(
    async (c: DockerContainer) => {
      setPending(p => ({ ...p, [c.id]: 'terminal' }))
      setErrors(e => omit(e, c.id))
      try {
        const session = await openTerminal(c.id)
        onOpenSession?.(`${session.host_id}/${session.session}`)
      } catch (error) {
        setErrors(e => ({ ...e, [c.id]: error instanceof Error ? error.message : 'Ouverture du terminal impossible' }))
      } finally {
        setPending(p => omit(p, c.id))
      }
    },
    [onOpenSession],
  )

  if (status === 'loading') return <p className="text-xs text-muted-foreground">Chargement…</p>
  if (status === 'forbidden') {
    return <p className="text-xs text-muted-foreground">Cette page est réservée aux administrateurs.</p>
  }
  if (status === 'error' || !data) {
    return <InlineError message={`Impossible de lire les conteneurs : ${failure ?? 'erreur inconnue'}`} />
  }
  if (!data.available) {
    return (
      <div className="max-w-2xl rounded-lg border border-border bg-card p-4 text-xs leading-relaxed">
        <p className="mb-2 font-medium">Docker est indisponible.</p>
        <p className="text-muted-foreground">{data.error || 'Le socket Docker ne répond pas.'}</p>
        <p className="mt-2 text-muted-foreground">
          Atlas lit l’API Docker sur <code>/var/run/docker.sock</code> (ou <code>TMUXATLAS_DOCKER_SOCKET</code>) ; son
          compte doit pouvoir l’ouvrir. <code>TMUXATLAS_DOCKER=off</code> désactive cette vue.
        </p>
      </div>
    )
  }

  const containers = data.containers ?? []
  const selected = selectedId ? containers.find(c => c.id.startsWith(selectedId)) : undefined
  const callbacks: ListCallbacks = {
    pending,
    errors,
    terminal: data.terminal,
    onAction: request,
    onOpen: open,
    onTerminal: c => void terminal(c),
    onDismissError: id => setErrors(e => omit(e, id)),
  }

  return (
    <>
      {failure && (
        <p className="mb-3 text-2xs text-warning" role="status">
          Dernière actualisation en échec ({failure}), nouvel essai dans 3 s.
        </p>
      )}
      {!selectedId && (
        <div className="mb-4">
          <Tabs
            items={sections}
            value={section}
            onChange={next => {
              writeSection(next)
              setSection(next)
            }}
            label="Ressources Docker"
            idPrefix="docker-section"
          />
        </div>
      )}
      {!selectedId && section === 'images' ? (
        <ImagesPanel onContainersChanged={() => void refresh()} />
      ) : !selectedId && section === 'volumes' ? (
        <VolumesPanel />
      ) : selectedId ? (
        selected ? (
          <ContainerDetail
            container={selected}
            tab={tab}
            onTab={changeTab}
            history={history.get(selected.id) ?? []}
            pending={pending[selected.id]}
            error={errors[selected.id]}
            terminal={data.terminal}
            onBack={back}
            onAction={request}
            onTerminal={c => void terminal(c)}
            onDismissError={() => setErrors(e => omit(e, selected.id))}
          />
        ) : (
          <div className="flex flex-col items-start gap-2 text-xs">
            <p className="text-muted-foreground">Ce conteneur n’existe plus.</p>
            <button type="button" onClick={back} className="ui-focus-ring rounded text-primary underline">
              Revenir à la liste
            </button>
          </div>
        )
      ) : (
        <>
          <SummaryTiles data={data} />
          <ContainersList data={data} callbacks={callbacks} />
        </>
      )}
      <ConfirmDialog
        request={confirm}
        onCancel={() => setConfirm(null)}
        onConfirm={confirmed => {
          setConfirm(null)
          void perform(confirmed.container, confirmed.action)
        }}
      />
    </>
  )
}
