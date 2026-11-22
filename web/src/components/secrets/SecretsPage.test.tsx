import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { REVEAL_MS, SecretsPage, formatRemaining } from './SecretsPage'

interface Call {
  url: string
  method: string
  body?: string
}

type Answer = unknown | (() => unknown)

const NOW = '2026-01-02T03:00:00Z'
const locked = { initialized: true, unlocked: false, now: NOW, ttl_seconds: 600, min_passphrase_length: 12 }
const unlocked = { ...locked, unlocked: true, expires_at: '2026-01-02T03:10:00Z' }
const shop = {
  name: 'shop',
  target: { env_file: '/home/hub/shop/.env', container: 'shop-web' },
  variables: [
    { name: 'API_TOKEN', updated_at: NOW },
    { name: 'DB_URL', updated_at: NOW },
  ],
}
const audit = { entries: [{ time: NOW, actor: 'root', action: 'var_set', app: 'shop', name: 'API_TOKEN' }] }

/** Answers by "METHOD url"; an error is { status, body }. Unknown routes fail the test. */
function mockApi(routes: Record<string, Answer>) {
  const calls: Call[] = []
  vi.spyOn(window, 'fetch').mockImplementation(async (input, init) => {
    const url = typeof input === 'string' ? input : input.toString()
    const method = (init?.method ?? 'GET').toUpperCase()
    calls.push({ url, method, body: typeof init?.body === 'string' ? init.body : undefined })
    let answer = routes[`${method} ${url.split('?')[0]}`]
    if (typeof answer === 'function') answer = (answer as () => unknown)()
    if (answer === undefined) return new Response(JSON.stringify({ error: `route inattendue ${method} ${url}` }), { status: 500 })
    const failure = answer as { status?: number; body?: unknown }
    if (typeof failure.status === 'number') return new Response(JSON.stringify(failure.body), { status: failure.status })
    return new Response(JSON.stringify(answer), { status: 200 })
  })
  return calls
}

const made = (calls: Call[], method: string, url: string) => calls.filter(call => call.method === method && call.url === url)

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('SecretsPage', () => {
  it('propose de créer le coffre et exige une phrase de passe confirmée', async () => {
    let status: unknown = { initialized: false, unlocked: false, now: NOW, ttl_seconds: 600, min_passphrase_length: 12 }
    const calls = mockApi({
      'GET /api/secrets/status': () => status,
      'POST /api/secrets/init': () => (status = locked),
      'GET /api/secrets/apps': { apps: [] },
      'GET /api/secrets/audit': { entries: [] },
    })
    render(<SecretsPage />)
    const passphrase = (await screen.findByLabelText(/^Phrase de passe/)) as HTMLInputElement
    const confirmation = screen.getByLabelText('Confirmer la phrase de passe') as HTMLInputElement
    const create = screen.getByRole('button', { name: 'Créer le coffre' }) as HTMLButtonElement
    // Passphrase fields are never offered to the browser's memory.
    for (const field of [passphrase, confirmation]) {
      expect(field.type).toBe('password')
      expect(field.autocomplete).toBe('new-password')
    }
    fireEvent.change(passphrase, { target: { value: 'trop court' } })
    fireEvent.change(confirmation, { target: { value: 'trop court' } })
    fireEvent.click(screen.getByRole('checkbox'))
    expect(create.disabled).toBe(true)
    fireEvent.change(passphrase, { target: { value: 'une phrase assez longue' } })
    expect(create.disabled).toBe(true)
    expect(screen.getByText('Les deux phrases de passe ne sont pas identiques.')).toBeTruthy()
    fireEvent.change(confirmation, { target: { value: 'une phrase assez longue' } })
    expect(create.disabled).toBe(false)
    fireEvent.click(create)
    expect(await screen.findByText('Verrouillé')).toBeTruthy()
    expect(made(calls, 'POST', '/api/secrets/init')[0].body).toBe(JSON.stringify({ passphrase: 'une phrase assez longue' }))
    expect(screen.getByText('Aucune application')).toBeTruthy()
  })

  it('verrouillé : montre les noms, jamais de valeur, et bloque révéler, appliquer et exporter', async () => {
    const calls = mockApi({
      'GET /api/secrets/status': locked,
      'GET /api/secrets/apps': { apps: [shop] },
      'GET /api/secrets/audit': audit,
    })
    render(<SecretsPage />)
    expect(await screen.findByText('DB_URL')).toBeTruthy()
    expect(screen.getByText('Verrouillé')).toBeTruthy()
    expect(screen.getAllByLabelText('Valeur masquée')).toHaveLength(2)
    expect((screen.getByRole('button', { name: 'Révéler API_TOKEN' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Appliquer…' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Exporter en .env' }) as HTMLButtonElement).disabled).toBe(true)
    // The audit trail is shown.
    const trail = screen.getByRole('region', { name: 'Journal d’audit' })
    expect(within(trail).getByText('Valeur définie')).toBeTruthy()
    expect(within(trail).getByText('shop / API_TOKEN')).toBeTruthy()
    expect(calls.every(call => call.method === 'GET')).toBe(true)
  })

  it('ajoute une valeur sans déverrouiller, dans un champ que le navigateur ne mémorise pas', async () => {
    const calls = mockApi({
      'GET /api/secrets/status': locked,
      'GET /api/secrets/apps': { apps: [shop] },
      'GET /api/secrets/audit': audit,
      'PUT /api/secrets/apps/shop/vars/NEW_KEY': shop,
    })
    render(<SecretsPage />)
    const value = (await screen.findByLabelText('Valeur')) as HTMLInputElement
    expect(value.type).toBe('password')
    expect(value.autocomplete).toBe('off')
    expect((screen.getByLabelText(/Importer un fichier \.env/) as HTMLTextAreaElement).autocomplete).toBe('off')
    fireEvent.change(screen.getByLabelText('Nom'), { target: { value: ' NEW_KEY ' } })
    fireEvent.change(value, { target: { value: 'la-valeur' } })
    fireEvent.click(screen.getByRole('button', { name: 'Chiffrer et enregistrer' }))
    await waitFor(() => expect(made(calls, 'PUT', '/api/secrets/apps/shop/vars/NEW_KEY')).toHaveLength(1))
    expect(made(calls, 'PUT', '/api/secrets/apps/shop/vars/NEW_KEY')[0].body).toBe(JSON.stringify({ value: 'la-valeur' }))
    // The field is emptied once the value is stored.
    await waitFor(() => expect(value.value).toBe(''))
  })

  it('déverrouille, affiche le compte à rebours, révèle une valeur puis la masque de nouveau', async () => {
    // The countdown starts with the unlock: the clock is faked from the start.
    vi.useFakeTimers({ shouldAdvanceTime: true })
    let status: unknown = locked
    const calls = mockApi({
      'GET /api/secrets/status': () => status,
      'POST /api/secrets/unlock': () => (status = unlocked),
      'GET /api/secrets/apps': { apps: [shop] },
      'GET /api/secrets/audit': audit,
      'POST /api/secrets/apps/shop/vars/API_TOKEN/reveal': { value: 'valeur-révélée' },
    })
    render(<SecretsPage />)
    const passphrase = (await screen.findByLabelText('Phrase de passe du coffre')) as HTMLInputElement
    expect(passphrase.type).toBe('password')
    expect(passphrase.autocomplete).toBe('new-password')
    fireEvent.change(passphrase, { target: { value: 'une phrase assez longue' } })
    fireEvent.click(screen.getByRole('button', { name: 'Déverrouiller' }))
    expect(await screen.findByText('Déverrouillé')).toBeTruthy()
    expect(made(calls, 'POST', '/api/secrets/unlock')[0].body).toBe(JSON.stringify({ passphrase: 'une phrase assez longue' }))
    expect(screen.getByRole('timer').textContent).toContain('10 min 00 s')
    // The passphrase does not stay in the page.
    expect(screen.queryByLabelText('Phrase de passe du coffre')).toBeNull()

    fireEvent.click(await screen.findByRole('button', { name: 'Révéler API_TOKEN' }))
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10)
    })
    expect(screen.getByTestId('value-API_TOKEN').textContent).toBe('valeur-révélée')
    expect(screen.getAllByLabelText('Valeur masquée')).toHaveLength(1)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(REVEAL_MS + 100)
    })
    expect(screen.queryByTestId('value-API_TOKEN')).toBeNull()
    expect(screen.queryByText('valeur-révélée')).toBeNull()
    expect(screen.getAllByLabelText('Valeur masquée')).toHaveLength(2)
    // The countdown went down meanwhile.
    expect(screen.getByRole('timer').textContent).toContain('9 min 4')
  })

  it('masque tout et repasse en verrouillé quand le délai est écoulé', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    let status: unknown = { ...unlocked, expires_at: '2026-01-02T03:00:05Z' }
    mockApi({
      'GET /api/secrets/status': () => status,
      'GET /api/secrets/apps': { apps: [shop] },
      'GET /api/secrets/audit': audit,
      'POST /api/secrets/apps/shop/vars/API_TOKEN/reveal': { value: 'valeur-révélée' },
    })
    render(<SecretsPage />)
    await screen.findByText('Déverrouillé')
    fireEvent.click(await screen.findByRole('button', { name: 'Révéler API_TOKEN' }))
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10)
    })
    expect(screen.getByTestId('value-API_TOKEN')).toBeTruthy()
    status = locked
    await act(async () => {
      await vi.advanceTimersByTimeAsync(6000)
    })
    expect(screen.getByText('Verrouillé')).toBeTruthy()
    expect(screen.queryByText('valeur-révélée')).toBeNull()
    expect((screen.getByRole('button', { name: 'Révéler API_TOKEN' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('n’applique qu’après une confirmation qui dit ce qui sera écrit et redémarré', async () => {
    const calls = mockApi({
      'GET /api/secrets/status': unlocked,
      'GET /api/secrets/apps': { apps: [shop] },
      'GET /api/secrets/audit': audit,
      'POST /api/secrets/apps/shop/apply': {
        env_file: '/home/hub/shop/.env',
        variables: 2,
        backup: true,
        container: 'shop-web',
        restarted: false,
        restart_error: 'conteneur introuvable',
      },
    })
    render(<SecretsPage />)
    fireEvent.click(await screen.findByRole('button', { name: 'Appliquer…' }))
    const dialog = await screen.findByRole('alertdialog')
    expect(made(calls, 'POST', '/api/secrets/apps/shop/apply')).toHaveLength(0)
    const text = dialog.textContent ?? ''
    expect(text).toContain('/home/hub/shop/.env')
    expect(text).toContain('2 variables')
    expect(text).toContain('en clair')
    expect(text).toContain('/home/hub/shop/.env.bak')
    expect(text).toContain('shop-web')
    expect(text).toContain('redémarré')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Écrire et redémarrer' }))
    await waitFor(() => expect(made(calls, 'POST', '/api/secrets/apps/shop/apply')).toHaveLength(1))
    // A file written without its restart is said, not hidden.
    expect(await screen.findByText(/n’a pas été redémarré : conteneur introuvable/)).toBeTruthy()
    expect(screen.queryByRole('alertdialog')).toBeNull()
  })

  it('annuler la confirmation n’applique rien', async () => {
    const calls = mockApi({
      'GET /api/secrets/status': unlocked,
      'GET /api/secrets/apps': { apps: [{ ...shop, target: { env_file: '/home/hub/shop/.env' } }] },
      'GET /api/secrets/audit': audit,
    })
    render(<SecretsPage />)
    fireEvent.click(await screen.findByRole('button', { name: 'Appliquer…' }))
    const dialog = await screen.findByRole('alertdialog')
    expect(dialog.textContent).toContain('Aucun conteneur ne sera redémarré.')
    expect(within(dialog).getByRole('button', { name: 'Écrire le fichier' })).toBeTruthy()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Annuler' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(calls.every(call => call.method === 'GET')).toBe(true)
  })

  it('importe un texte dotenv collé puis vide le champ', async () => {
    const calls = mockApi({
      'GET /api/secrets/status': locked,
      'GET /api/secrets/apps': { apps: [shop] },
      'GET /api/secrets/audit': audit,
      'POST /api/secrets/apps/shop/import': { app: shop, imported: ['A', 'B'] },
    })
    render(<SecretsPage />)
    const area = (await screen.findByLabelText(/Importer un fichier \.env/)) as HTMLTextAreaElement
    fireEvent.change(area, { target: { value: 'A=1\nB="deux"\n' } })
    fireEvent.click(screen.getByRole('button', { name: 'Importer' }))
    expect(await screen.findByText('2 variables importées et chiffrées.')).toBeTruthy()
    expect(made(calls, 'POST', '/api/secrets/apps/shop/import')[0].body).toBe(JSON.stringify({ text: 'A=1\nB="deux"\n' }))
    expect(area.value).toBe('')
  })

  it('supprime une variable seulement après confirmation', async () => {
    const calls = mockApi({
      'GET /api/secrets/status': locked,
      'GET /api/secrets/apps': { apps: [shop] },
      'GET /api/secrets/audit': audit,
      'DELETE /api/secrets/apps/shop/vars/DB_URL': shop,
    })
    render(<SecretsPage />)
    fireEvent.click(await screen.findByRole('button', { name: 'Supprimer DB_URL' }))
    expect(calls.some(call => call.method === 'DELETE')).toBe(false)
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Supprimer' }))
    await waitFor(() => expect(made(calls, 'DELETE', '/api/secrets/apps/shop/vars/DB_URL')).toHaveLength(1))
  })

  it('affiche les refus du serveur : mauvaise phrase de passe, page réservée', async () => {
    mockApi({
      'GET /api/secrets/status': locked,
      'GET /api/secrets/apps': { apps: [] },
      'GET /api/secrets/audit': { entries: [] },
      'POST /api/secrets/unlock': { status: 403, body: { error: 'phrase de passe incorrecte', code: 'wrong_passphrase' } },
    })
    render(<SecretsPage />)
    fireEvent.change(await screen.findByLabelText('Phrase de passe du coffre'), { target: { value: 'pas la bonne phrase' } })
    fireEvent.click(screen.getByRole('button', { name: 'Déverrouiller' }))
    expect((await screen.findByRole('alert')).textContent).toContain('Phrase de passe incorrecte')
    expect(screen.getByText('Verrouillé')).toBeTruthy()

    cleanup()
    vi.restoreAllMocks()
    mockApi({ 'GET /api/secrets/status': { status: 403, body: { error: 'accès interdit', code: 'forbidden' } } })
    render(<SecretsPage />)
    expect(await screen.findByText('Cette page est réservée aux administrateurs.')).toBeTruthy()
  })
})

describe('SecretsPage, sessions et coffre abîmé', () => {
  it('dit quand le coffre est déverrouillé dans une autre session et garde tout verrouillé ici', async () => {
    mockApi({
      'GET /api/secrets/status': { ...locked, unlocked_elsewhere: true },
      'GET /api/secrets/apps': { apps: [shop] },
      'GET /api/secrets/audit': audit,
    })
    render(<SecretsPage />)
    expect(await screen.findByText(/Déverrouillé dans une autre session, pas dans celle-ci/)).toBeTruthy()
    expect(screen.getByText('Verrouillé')).toBeTruthy()
    expect(screen.getByLabelText('Phrase de passe du coffre')).toBeTruthy()
    expect(((await screen.findByRole('button', { name: 'Révéler API_TOKEN' })) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Appliquer…' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('n’offre pas d’appliquer une application sans variable', async () => {
    mockApi({
      'GET /api/secrets/status': unlocked,
      'GET /api/secrets/apps': { apps: [{ ...shop, variables: [] }] },
      'GET /api/secrets/audit': audit,
    })
    render(<SecretsPage />)
    const apply = (await screen.findByRole('button', { name: 'Appliquer…' })) as HTMLButtonElement
    expect(apply.disabled).toBe(true)
    expect(apply.title).toBe('Aucune variable à appliquer')
  })

  it('prévient dans la confirmation que le fichier écrit est un nouveau fichier privé', async () => {
    mockApi({
      'GET /api/secrets/status': unlocked,
      'GET /api/secrets/apps': { apps: [shop] },
      'GET /api/secrets/audit': audit,
    })
    render(<SecretsPage />)
    fireEvent.click(await screen.findByRole('button', { name: 'Appliquer…' }))
    const text = (await screen.findByRole('alertdialog')).textContent ?? ''
    expect(text).toContain('nouveau fichier')
    expect(text).toContain('monte ce fichier lui-même')
    expect(text).toContain('sous un autre utilisateur')
    expect(text).toContain('.bak.1')
  })

  it('ignore une valeur révélée qui arrive après le verrouillage', async () => {
    let status: unknown = unlocked
    let release: (value: unknown) => void = () => {}
    const pending = new Promise(resolve => (release = resolve))
    const calls = mockApi({
      'GET /api/secrets/status': () => status,
      'POST /api/secrets/lock': () => (status = locked),
      'GET /api/secrets/apps': { apps: [shop] },
      'GET /api/secrets/audit': audit,
    })
    const answered = vi.mocked(window.fetch).getMockImplementation()!
    vi.mocked(window.fetch).mockImplementation(async (input, init) => {
      if (String(input).endsWith('/reveal')) {
        calls.push({ url: String(input), method: 'POST' })
        await pending
        return new Response(JSON.stringify({ value: 'valeur-en-retard' }), { status: 200 })
      }
      return answered(input, init)
    })
    render(<SecretsPage />)
    fireEvent.click(await screen.findByRole('button', { name: 'Révéler API_TOKEN' }))
    await waitFor(() => expect(made(calls, 'POST', '/api/secrets/apps/shop/vars/API_TOKEN/reveal')).toHaveLength(1))
    fireEvent.click(screen.getByRole('button', { name: 'Verrouiller' }))
    expect(await screen.findByText('Verrouillé')).toBeTruthy()
    await act(async () => {
      release(null)
      await pending
      await Promise.resolve()
    })
    await waitFor(() =>
      expect((screen.getByRole('button', { name: 'Révéler API_TOKEN' }) as HTMLButtonElement).getAttribute('aria-busy')).toBeNull(),
    )
    expect(screen.queryByText('valeur-en-retard')).toBeNull()
    expect(screen.queryByTestId('value-API_TOKEN')).toBeNull()
    expect(screen.getAllByLabelText('Valeur masquée')).toHaveLength(2)
  })

  it('montre un coffre incohérent au lieu de proposer de le déverrouiller', async () => {
    const calls = mockApi({
      'GET /api/secrets/status': {
        ...locked,
        problem: 'le coffre est illisible ou incohérent : sa clé publique a changé depuis le démarrage du hub',
      },
    })
    render(<SecretsPage />)
    expect((await screen.findByRole('alert')).textContent).toContain('clé publique a changé')
    expect(screen.queryByLabelText('Phrase de passe du coffre')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Créer le coffre' })).toBeNull()
    expect(calls.every(call => call.url === '/api/secrets/status')).toBe(true)
  })

  it('une application illisible ne cache pas les autres et peut être supprimée', async () => {
    const calls = mockApi({
      'GET /api/secrets/status': locked,
      'GET /api/secrets/apps': { apps: [{ name: 'blog', variables: [], unreadable: true }, shop] },
      'GET /api/secrets/audit': audit,
      'DELETE /api/secrets/apps/blog': { deleted: true },
    })
    render(<SecretsPage />)
    expect(await screen.findByText(/illisible ou a été modifié à la main/)).toBeTruthy()
    expect(screen.getByRole('button', { name: /^shop/ })).toBeTruthy()
    expect(screen.queryByLabelText('Valeur')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Supprimer l’application' }))
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Supprimer définitivement' }))
    await waitFor(() => expect(made(calls, 'DELETE', '/api/secrets/apps/blog')).toHaveLength(1))
  })
})

describe('formatRemaining', () => {
  it('écrit les minutes et les secondes', () => {
    expect(formatRemaining(600)).toBe('10 min 00 s')
    expect(formatRemaining(61.2)).toBe('1 min 02 s')
    expect(formatRemaining(59)).toBe('59 s')
    expect(formatRemaining(-3)).toBe('0 s')
  })
})
