import { useCallback, useRef, useState } from 'react'
import { getCSRFToken } from '../../lib/api'
import type { DroppedItems } from './dropTransfer'
import { FilesError, filesApi, joinPath, uploadFile } from './filesApi'

export type UploadStatus = 'queued' | 'uploading' | 'done' | 'skipped' | 'error' | 'cancelled'

export interface UploadItem {
  id: number
  name: string
  path: string
  size: number
  loaded: number
  status: UploadStatus
  error?: string
}

export type ConflictChoice = 'replace' | 'skip' | 'replace-all' | 'skip-all' | 'cancel'

const CONCURRENCY = 3

/**
 * Queues dropped or picked files and sends them a few at a time. A name that
 * already exists asks the page what to do, once per file or for all of them.
 */
export function useUploads({
  onUploaded,
  resolveConflict,
}: {
  onUploaded: (dirs: Set<string>) => void
  resolveConflict: (path: string) => Promise<ConflictChoice>
}) {
  const [items, setItems] = useState<UploadItem[]>([])
  const nextId = useRef(1)
  const controllers = useRef(new Map<number, AbortController>())
  const cancelled = useRef(new Set<number>())

  const patch = useCallback((id: number, change: Partial<UploadItem>) => {
    setItems(current => current.map(item => (item.id === id ? { ...item, ...change } : item)))
  }, [])

  const start = useCallback(
    async (targetDir: string, dropped: DroppedItems) => {
      const touched = new Set<string>([targetDir])
      for (const folder of dropped.folders) {
        try {
          await mkdirs(joinPath(targetDir, folder))
        } catch {
          /* An empty folder that cannot be created is not worth stopping for. */
        }
      }
      const batch = dropped.files.map(({ relativePath, file }) => ({
        id: nextId.current++,
        name: relativePath,
        path: joinPath(targetDir, relativePath),
        size: file.size,
        loaded: 0,
        status: 'queued' as UploadStatus,
        file,
      }))
      if (batch.length === 0) {
        onUploaded(touched)
        return
      }
      setItems(current => [
        ...current.filter(item => item.status === 'uploading' || item.status === 'queued'),
        ...batch.map(({ file: _file, ...item }) => item),
      ])

      let policy: 'ask' | 'replace' | 'skip' | 'cancel' = 'ask'
      let conflictChain = Promise.resolve()
      const ask = (path: string) => {
        // Conflicts are asked one at a time even with parallel uploads.
        const answer = conflictChain.then(async (): Promise<'replace' | 'skip' | 'cancel'> => {
          if (policy !== 'ask') return policy
          const choice = await resolveConflict(path)
          if (choice === 'replace-all') policy = 'replace'
          if (choice === 'skip-all') policy = 'skip'
          if (choice === 'cancel') policy = 'cancel'
          return choice === 'replace' || choice === 'replace-all' ? 'replace' : choice === 'cancel' ? 'cancel' : 'skip'
        })
        conflictChain = answer.then(() => undefined)
        return answer
      }

      const send = async (item: (typeof batch)[number], overwrite: boolean): Promise<void> => {
        const controller = new AbortController()
        controllers.current.set(item.id, controller)
        patch(item.id, { status: 'uploading', loaded: 0, error: undefined })
        try {
          await uploadFile(item.path, item.file, {
            overwrite,
            csrf: getCSRFToken(),
            signal: controller.signal,
            onProgress: loaded => patch(item.id, { loaded }),
          })
          patch(item.id, { status: 'done', loaded: item.size })
          touched.add(item.path.slice(0, item.path.lastIndexOf('/')) || '/')
        } catch (error) {
          if (error instanceof FilesError && error.code === 'exists' && !overwrite) {
            const decision = await ask(item.path)
            if (decision === 'replace') return send(item, true)
            patch(item.id, { status: decision === 'cancel' ? 'cancelled' : 'skipped' })
            return
          }
          if (error instanceof FilesError && error.code === 'aborted') {
            patch(item.id, { status: 'cancelled' })
            return
          }
          patch(item.id, { status: 'error', error: error instanceof Error ? error.message : String(error) })
        } finally {
          controllers.current.delete(item.id)
        }
      }

      let index = 0
      const worker = async () => {
        while (index < batch.length) {
          const item = batch[index++]
          if (policy === 'cancel' || cancelled.current.has(item.id)) {
            patch(item.id, { status: 'cancelled' })
            continue
          }
          await send(item, policy === 'replace')
        }
      }
      await Promise.all(Array.from({ length: Math.min(CONCURRENCY, batch.length) }, worker))
      onUploaded(touched)
    },
    [onUploaded, patch, resolveConflict],
  )

  const cancel = useCallback(
    (id: number) => {
      cancelled.current.add(id)
      controllers.current.get(id)?.abort()
      setItems(current =>
        current.map(item => (item.id === id && item.status === 'queued' ? { ...item, status: 'cancelled' } : item)),
      )
    },
    [],
  )

  const cancelAll = useCallback(() => {
    controllers.current.forEach(controller => controller.abort())
    setItems(current =>
      current.map(item => {
        if (item.status !== 'queued') return item
        cancelled.current.add(item.id)
        return { ...item, status: 'cancelled' }
      }),
    )
  }, [])

  const clear = useCallback(() => {
    setItems(current => current.filter(item => item.status === 'uploading' || item.status === 'queued'))
  }, [])

  return { items, start, cancel, cancelAll, clear }
}

/** Creates a folder and its missing parents, ignoring those that exist. */
async function mkdirs(path: string) {
  const parts = path.split('/').filter(Boolean)
  let current = ''
  for (const part of parts) {
    current += `/${part}`
    try {
      await filesApi.mkdir(current)
    } catch (error) {
      if (!(error instanceof FilesError && error.status === 409)) throw error
    }
  }
}
