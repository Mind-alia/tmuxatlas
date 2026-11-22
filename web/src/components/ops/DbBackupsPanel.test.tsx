import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import DbBackupsPanel, { type DbBackupRecord, type DbBackupsView } from './DbBackupsPanel'

const now = Math.floor(Date.now() / 1000)
const iso = (secondsAgo: number) => new Date((now - secondsAgo) * 1000).toISOString()

const record = (id: string, extra: Partial<DbBackupRecord> = {}): DbBackupRecord => ({
  id, container: 'shop-db-1', engine: 'postgres', origin: 'scheduled', state: 'ok', started_at: iso(3630),
  duration_ms: 4200, size: 5 * 1024 * 1024, sha256: 'ab'.repeat(32), tool: 'pg_dumpall', ...extra,
})

const view = (extra: Partial<DbBackupsView> = {}): DbBackupsView => ({
  available: true, restore_enabled: true, dir: '/srv/atlas/db-backups', free_bytes: 40 * 1024 ** 3, min_free_bytes: 1024 ** 3, now,
  databases: [
    { container: 'blog-db', engine: 'mariadb', image: 'mariadb:11', present: true,
      settings: { mode: 'off', time: '03:00', weekday: 0, keep: 7 }, backups: [] },
    { container: 'shop-db-1', engine: 'postgres', image: 'postgres:16', present: true,
      settings: { mode: 'daily', time: '03:15', weekday: 0, keep: 5 }, next_run: now + 7230,
      backups: [record('20261122T021500Z'), record('20261121T021500Z', { started_at: iso(90000), state: 'failed', size: 0, origin: 'manual', error: 'pg_dumpall a échoué (code 1)' })] },
  ],
  ...extra,
})

/** Answers GET with the current view and records every other request. */
function stubFetch(current: { view: DbBackupsView }, answers: Record<string, { status: number; body: unknown }> = {}) {
  const calls: { method: string; url: string; body?: string }[] = []
  const fetch = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input)
    const method = (init.method ?? 'GET').toUpperCase()
    if (method === 'GET') return new Response(JSON.stringify(current.view), { status: 200 })
    calls.push({ method, url, body: typeof init.body === 'string' ? init.body : undefined })
    const answer = answers[`${method} ${url}`] ?? { status: 202, body: { started: true } }
    return new Response(JSON.stringify(answer.body), { status: answer.status })
  })
  vi.stubGlobal('fetch', fetch)
  return calls
}

describe('DbBackupsPanel', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('montre une carte par base avec son moteur, sa dernière sauvegarde et sa prochaine exécution', async () => {
    stubFetch({ view: view() })
    render(<DbBackupsPanel />)
    const shop = await screen.findByRole('listitem', { name: 'Base shop-db-1' })
    expect(shop).toHaveTextContent('PostgreSQL')
    expect(shop).toHaveTextContent('sauvegardée')
    expect(shop).toHaveTextContent('il y a 1 h 0 min · 5 Mo')
    expect(shop).toHaveTextContent('tous les jours à 03:15')
    expect(shop).toHaveTextContent('dans 2 h 0 min')
    expect(shop).toHaveTextContent('5 dernières')
    const blog = screen.getByRole('listitem', { name: 'Base blog-db' })
    expect(blog).toHaveTextContent('MariaDB')
    expect(blog).toHaveTextContent('jamais sauvegardée')
    expect(blog).toHaveTextContent('désactivée')
    // The list: state, origin, error of the failed run, and a download by identifier only.
    const list = within(shop).getByRole('list', { name: 'Sauvegardes de shop-db-1' })
    expect(list).toHaveTextContent('complète')
    expect(list).toHaveTextContent('planifiée')
    expect(list).toHaveTextContent('ratée')
    expect(list).toHaveTextContent('pg_dumpall a échoué (code 1)')
    const links = within(list).getAllByRole('link')
    expect(links).toHaveLength(1)
    expect(links[0]).toHaveAttribute('href', '/api/db-backups/shop-db-1/files/20261122T021500Z/download')
    expect(screen.getByText(/ne protègent pas d’une panne/)).toBeInTheDocument()
  })

  it('lance une sauvegarde et suit son avancement', async () => {
    const current = { view: view() }
    const calls = stubFetch(current)
    render(<DbBackupsPanel />)
    const blog = await screen.findByRole('listitem', { name: 'Base blog-db' })
    current.view = view()
    current.view.databases[0].running = { kind: 'backup', origin: 'manual', started_at: now - 12, bytes: 3 * 1024 * 1024 }
    fireEvent.click(within(blog).getByRole('button', { name: 'Sauvegarder maintenant' }))
    await waitFor(() => expect(calls).toEqual([{ method: 'POST', url: '/api/db-backups/blog-db/backup', body: undefined }]))
    const status = await within(blog).findByRole('status')
    expect(status).toHaveTextContent('Sauvegarde manuelle en cours depuis 12 s · 3 Mo lus')
    expect(blog).toHaveTextContent('sauvegarde en cours')
    expect(within(blog).getByRole('button', { name: 'Sauvegarder maintenant' })).toBeDisabled()
  })

  it('affiche le refus du serveur quand la sauvegarde ne démarre pas', async () => {
    stubFetch({ view: view() }, {
      'POST /api/db-backups/blog-db/backup': { status: 409, body: { error: 'une sauvegarde ou une restauration de cette base est déjà en cours' } },
    })
    render(<DbBackupsPanel />)
    const blog = await screen.findByRole('listitem', { name: 'Base blog-db' })
    fireEvent.click(within(blog).getByRole('button', { name: 'Sauvegarder maintenant' }))
    expect(await within(blog).findByRole('alert')).toHaveTextContent('Une sauvegarde ou une restauration de cette base est déjà en cours')
  })

  it('enregistre la planification et la rétention', async () => {
    const calls = stubFetch({ view: view() }, { 'PUT /api/db-backups/blog-db/settings': { status: 200, body: {} } })
    render(<DbBackupsPanel />)
    const blog = await screen.findByRole('listitem', { name: 'Base blog-db' })
    fireEvent.click(within(blog).getByRole('button', { name: 'Planification et rétention' }))
    const form = within(blog).getByRole('form', { name: 'Planification de blog-db' })
    expect(within(form).queryByLabelText('Jour')).not.toBeInTheDocument()
    fireEvent.change(within(form).getByLabelText('Planification'), { target: { value: 'weekly' } })
    fireEvent.change(within(form).getByLabelText('Jour'), { target: { value: '3' } })
    fireEvent.change(within(form).getByLabelText('Heure du serveur'), { target: { value: '04:30' } })
    fireEvent.change(within(form).getByLabelText('Sauvegardes gardées'), { target: { value: '0' } })
    expect(within(form).getByRole('button', { name: 'Enregistrer' })).toBeDisabled()
    fireEvent.change(within(form).getByLabelText('Sauvegardes gardées'), { target: { value: '3' } })
    fireEvent.click(within(form).getByRole('button', { name: 'Enregistrer' }))
    await waitFor(() => expect(calls).toHaveLength(1))
    expect(calls[0].url).toBe('/api/db-backups/blog-db/settings')
    expect(JSON.parse(calls[0].body ?? '')).toEqual({ mode: 'weekly', time: '04:30', weekday: 3, keep: 3 })
    await waitFor(() => expect(within(blog).queryByRole('form')).not.toBeInTheDocument())
  })

  it('demande une confirmation avant de supprimer', async () => {
    const calls = stubFetch({ view: view() }, {
      'DELETE /api/db-backups/shop-db-1/files/20261122T021500Z': { status: 200, body: { deleted: true } },
    })
    render(<DbBackupsPanel />)
    const shop = await screen.findByRole('listitem', { name: 'Base shop-db-1' })
    fireEvent.click(within(shop).getAllByRole('button', { name: /^Supprimer la sauvegarde/ })[0])
    const dialog = await screen.findByRole('alertdialog')
    expect(calls).toHaveLength(0)
    fireEvent.click(within(dialog).getByRole('button', { name: 'Supprimer définitivement' }))
    await waitFor(() => expect(calls).toEqual([{ method: 'DELETE', url: '/api/db-backups/shop-db-1/files/20261122T021500Z', body: undefined }]))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
  })

  it('n’envoie la restauration qu’une fois le nom exact du conteneur retapé', async () => {
    const calls = stubFetch({ view: view() })
    render(<DbBackupsPanel />)
    const shop = await screen.findByRole('listitem', { name: 'Base shop-db-1' })
    fireEvent.click(within(shop).getByRole('button', { name: /^Restaurer la sauvegarde/ }))
    const dialog = await screen.findByRole('alertdialog')
    expect(dialog).toHaveTextContent('sauvegarde de sécurité')
    const confirm = within(dialog).getByRole('button', { name: 'Restaurer cette sauvegarde' })
    expect(confirm).toBeDisabled()
    const input = within(dialog).getByRole('textbox')
    fireEvent.change(input, { target: { value: 'shop-db' } })
    expect(confirm).toBeDisabled()
    fireEvent.change(input, { target: { value: 'Shop-db-1' } })
    expect(confirm).toBeDisabled()
    fireEvent.change(input, { target: { value: 'shop-db-1' } })
    expect(confirm).toBeEnabled()
    fireEvent.click(confirm)
    await waitFor(() => expect(calls).toHaveLength(1))
    expect(calls[0]).toEqual({
      method: 'POST', url: '/api/db-backups/shop-db-1/files/20261122T021500Z/restore', body: JSON.stringify({ confirm: 'shop-db-1' }),
    })
  })

  it('désactive la restauration et dit pourquoi quand l’interrupteur est éteint', async () => {
    stubFetch({ view: view({ restore_enabled: false }) })
    render(<DbBackupsPanel />)
    const shop = await screen.findByRole('listitem', { name: 'Base shop-db-1' })
    const restore = within(shop).getByRole('button', { name: /^Restaurer la sauvegarde/ })
    expect(restore).toBeDisabled()
    expect(restore).toHaveAttribute('title', expect.stringContaining('TMUXATLAS_DB_RESTORE=on'))
    expect(screen.getByText(/Restauration désactivée sur ce serveur\./)).toHaveTextContent('TMUXATLAS_DB_RESTORE=on')
  })

  it('montre le résultat d’une restauration, sa sortie d’erreur et la sauvegarde de sécurité', async () => {
    stubFetch({
      view: view({
        restore: {
          state: 'failed', container: 'shop-db-1', backup: '20261122T021500Z', actor: 'admin', step: 'done', started_at: now - 60,
          finished_at: now - 30, safety_backup: '20261122T101500Z', exit_code: 3, error: 'psql a échoué (code 3)',
          output: 'ERROR: role "shop" already exists', output_truncated: true,
        },
      }),
    })
    render(<DbBackupsPanel />)
    const banner = await screen.findByRole('status', { name: 'État de la restauration' })
    expect(banner).toHaveTextContent('Restauration de « shop-db-1 » en échec')
    expect(banner).toHaveTextContent('psql a échoué (code 3)')
    expect(banner).toHaveTextContent('code de sortie 3')
    expect(banner).toHaveTextContent('ERROR: role "shop" already exists')
    expect(banner).toHaveTextContent('[sortie tronquée]')
    expect(banner).toHaveTextContent('20261122T101500Z')
    // No second restore meanwhile is offered while one runs: here it ended, the button is back.
    const shop = screen.getByRole('listitem', { name: 'Base shop-db-1' })
    expect(within(shop).getByRole('button', { name: /^Restaurer la sauvegarde/ })).toBeEnabled()
  })

  it('signale une sauvegarde planifiée ratée, un conteneur absent et Docker indisponible', async () => {
    const failing = view({ available: false, error: 'vue Docker désactivée sur ce serveur (TMUXATLAS_DOCKER=off)' })
    failing.databases[1] = { ...failing.databases[1], present: false, failure: { at: iso(600), detail: 'conteneur introuvable' } }
    stubFetch({ view: failing })
    render(<DbBackupsPanel />)
    const shop = await screen.findByRole('listitem', { name: 'Base shop-db-1' })
    expect(shop).toHaveTextContent('sauvegarde planifiée en échec')
    expect(shop).toHaveTextContent('conteneur introuvable')
    expect(within(shop).getByRole('button', { name: 'Sauvegarder maintenant' })).toBeDisabled()
    expect(within(shop).getByRole('button', { name: /^Restaurer la sauvegarde/ })).toBeDisabled()
    expect(within(shop).getByRole('link', { name: /^Télécharger/ })).toBeInTheDocument()
    expect(screen.getByText(/Docker n’est pas interrogeable/)).toHaveTextContent('TMUXATLAS_DOCKER=off')
  })

  it('dit quand aucune base n’est détectée et quand le hub ne répond pas', async () => {
    stubFetch({ view: view({ databases: [] }) })
    render(<DbBackupsPanel />)
    expect(await screen.findByText(/Aucune base PostgreSQL, MySQL ou MariaDB détectée/)).toBeInTheDocument()
    cleanup()
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'accès interdit' }), { status: 403 })))
    render(<DbBackupsPanel />)
    expect(await screen.findByRole('alert')).toHaveTextContent('Accès interdit')
  })
})
