import { useEffect, useState } from 'react'
import type { Host } from '../../hooks/useHosts'
import { apiFetch } from '../../lib/api'
import { FilesPage } from './FilesPage'

/** The file explorer of a session, opened in the working directory of its active pane. */
export function SessionFiles({ hosts, paneId }: { hosts: Host[]; paneId?: string }) {
  // The folder is read once: the explorer then lets the user move freely.
  const [initialPath, setInitialPath] = useState<string | null>(paneId ? null : '')

  useEffect(() => {
    if (!paneId || initialPath !== null) return
    const controller = new AbortController()
    apiFetch(`/api/files/pane-dir?pane=${encodeURIComponent(paneId)}`, { signal: controller.signal, cache: 'no-store' })
      .then(async response => (response.ok ? ((await response.json()) as { path?: string }).path || '' : ''))
      .catch(() => '')
      .then(path => {
        if (!controller.signal.aborted) setInitialPath(path)
      })
    return () => controller.abort()
  }, [initialPath, paneId])

  if (initialPath === null) {
    return (
      <div role="status" className="grid flex-1 place-items-center text-sm text-muted-foreground">
        Ouverture du dossier…
      </div>
    )
  }
  return <FilesPage hosts={hosts} embedded initialPath={initialPath || undefined} />
}
