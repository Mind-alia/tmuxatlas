import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ImagesPanel, VolumesPanel } from './ResourcesPanels'

interface Call {
  url: string
  method: string
  body?: string
}

function mockApi(routes: Record<string, unknown>) {
  const calls: Call[] = []
  vi.spyOn(window, 'fetch').mockImplementation(async (input, init) => {
    const url = typeof input === 'string' ? input : input.toString()
    const method = (init?.method ?? 'GET').toUpperCase()
    calls.push({ url, method, body: typeof init?.body === 'string' ? init.body : undefined })
    const answer = routes[`${method} ${url}`]
    if (answer === undefined) return new Response(JSON.stringify({ error: 'Docker refuse : en cours d’utilisation' }), { status: 409 })
    return new Response(JSON.stringify(answer), { status: 200 })
  })
  return calls
}

const images = {
  total_size: 300,
  collected_at: 1,
  images: [
    { id: `sha256:${'a'.repeat(64)}`, short_id: 'a'.repeat(12), tags: ['nginx:1.27'], size: 200, created: 1700000000, containers: ['web'] },
    { id: `sha256:${'b'.repeat(64)}`, short_id: 'b'.repeat(12), tags: ['app:v1'], size: 100, created: 1700000000, containers: [] },
  ],
}

const volumes = {
  collected_at: 1,
  volumes: [
    { name: 'shop_data', driver: 'local', size: 4096, containers: ['web'], compose_project: 'shop' },
    { name: 'spare', driver: 'local', size: null, containers: [] },
  ],
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('ImagesPanel', () => {
  it('liste les images, bloque la suppression de celles utilisées et supprime les autres après confirmation', async () => {
    const calls = mockApi({
      'GET /api/docker/images': images,
      [`DELETE /api/docker/images/${'b'.repeat(64)}`]: { name: 'app:v1', action: 'delete-image' },
    })
    render(<ImagesPanel onContainersChanged={() => {}} />)
    expect(await screen.findByText('nginx:1.27')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Supprimer l’image nginx:1.27' }) as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(screen.getByRole('button', { name: 'Supprimer l’image app:v1' }))
    expect(calls.some(call => call.method === 'DELETE')).toBe(false)
    fireEvent.click(await screen.findByRole('button', { name: 'Supprimer définitivement' }))
    await waitFor(() => expect(calls.some(call => call.method === 'DELETE' && call.url.endsWith('b'.repeat(64)))).toBe(true))
  })

  it('télécharge une image et lance un conteneur sur 127.0.0.1 par défaut', async () => {
    const created = vi.fn()
    const calls = mockApi({
      'GET /api/docker/images': images,
      'POST /api/docker/images/pull': { name: 'redis:7', action: 'pull', detail: 'Status: Downloaded newer image' },
      'POST /api/docker/containers': { id: 'c'.repeat(64), name: 'essai', action: 'create-start' },
    })
    render(<ImagesPanel onContainersChanged={created} />)
    await screen.findByText('app:v1')
    fireEvent.change(screen.getByLabelText('Ajouter une image'), { target: { value: ' redis:7 ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Télécharger' }))
    await waitFor(() => expect(calls.find(call => call.url.endsWith('/pull'))?.body).toBe('{"image":"redis:7"}'))
    expect(await screen.findByText(/Downloaded newer image/)).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Lancer un conteneur depuis app:v1' }))
    fireEvent.change(await screen.findByLabelText(/^Nom du conteneur/), { target: { value: 'essai' } })
    fireEvent.change(screen.getByLabelText(/^Ports/), { target: { value: '8080:80' } })
    fireEvent.click(screen.getByRole('button', { name: 'Lancer' }))
    await waitFor(() => expect(created).toHaveBeenCalled())
    const body = JSON.parse(calls.find(call => call.url === '/api/docker/containers')?.body ?? '{}')
    expect(body).toMatchObject({
      image: 'app:v1', name: 'essai', restart: 'no',
      ports: [{ host_ip: '127.0.0.1', host: 8080, container: 80, proto: 'tcp' }],
    })
  })
})

describe('VolumesPanel', () => {
  it('liste les volumes, en crée un et affiche le refus de Docker', async () => {
    const calls = mockApi({ 'GET /api/docker/volumes': volumes, 'POST /api/docker/volumes': { name: 'neuf', action: 'create-volume' } })
    render(<VolumesPanel />)
    expect(await screen.findByText('shop_data')).toBeTruthy()
    expect(screen.getByText('Calcul…')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Supprimer le volume shop_data' }) as HTMLButtonElement).disabled).toBe(true)
    fireEvent.change(screen.getByLabelText('Créer un volume'), { target: { value: 'neuf' } })
    fireEvent.click(screen.getByRole('button', { name: 'Créer' }))
    await waitFor(() => expect(calls.find(call => call.method === 'POST')?.body).toBe('{"name":"neuf"}'))
    fireEvent.click(screen.getByRole('button', { name: 'Supprimer le volume spare' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Supprimer définitivement' }))
    expect(await screen.findByText(/en cours d’utilisation/)).toBeTruthy()
  })
})
