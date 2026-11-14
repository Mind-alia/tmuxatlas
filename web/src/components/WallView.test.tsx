import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { TerminalProps } from './Terminal'
import type { WorkspaceViewModel } from '../workspace/model'
import { WallView } from './WallView'

const terminalProps = new Map<string, TerminalProps>()
vi.mock('./Terminal', () => ({
  Terminal: (props: TerminalProps) => {
    terminalProps.set(props.sessionName, props)
    return (
      <div data-testid="terminal">
        {props.sessionName} {String(props.embedded)}
      </div>
    )
  },
}))
const mic = {
  available: false,
  state: 'off' as 'off' | 'starting' | 'on',
  error: '',
  toggle: vi.fn(),
}
vi.mock('../lib/hostLabel', () => ({
  useHostLabel: () => (_id: string, fallback: string) => fallback,
}))
const session = (name: string, status: 'idle' | 'waiting') => ({
  key: `host/${name}`,
  hostId: 'host',
  hostName: 'Machine',
  name,
  status,
  lastActivity: null,
  agents: [],
  searchText: '',
  source: {} as never,
})
const sessions = [session('une', 'idle'), session('deux', 'waiting'), session('trois', 'idle')]
const workspace: WorkspaceViewModel = {
  sessions,
  hosts: [],
  byTarget: new Map(sessions.map(item => [item.key, item])),
}
describe('Mur', () => {
  afterEach(cleanup)
  beforeEach(() => localStorage.clear())
  it('affiche toutes les sessions, priorise celles à traiter et ouvre une session', () => {
    const open = vi.fn()
    render(<WallView workspace={workspace} onOpenSession={open} />)
    expect(screen.getAllByTestId('terminal').map(node => node.textContent)).toEqual(['deux true', 'une true', 'trois true'])
    fireEvent.click(screen.getAllByRole('button', { name: 'Agrandir' })[0])
    expect(open).toHaveBeenCalledWith('host/deux')
  })
  it('affiche le nom dans le sélecteur sans répéter l’hôte unique', () => {
    render(<WallView workspace={workspace} onOpenSession={vi.fn()} />)
    const selected = screen.getAllByRole('combobox')[0] as HTMLSelectElement
    expect(selected.selectedOptions[0]).toHaveTextContent('deux')
    expect(selected.selectedOptions[0]).not.toHaveTextContent('Machine')
  })
  it('persiste les choix et retire les sessions disparues', () => {
    const view = render(<WallView workspace={workspace} onOpenSession={vi.fn()} />)
    fireEvent.click(screen.getAllByRole('button', { name: 'Retirer' })[2])
    fireEvent.change(screen.getAllByRole('combobox')[0], {
      target: { value: 'host/trois' },
    })
    expect(localStorage.getItem('tmuxatlas:wall-targets:v2')).toContain('host/trois')
    view.rerender(
      <WallView
        workspace={{
          ...workspace,
          byTarget: new Map([['host/une', sessions[0]]]),
        }}
        onOpenSession={vi.fn()}
      />,
    )
    expect(screen.queryByText('trois true')).not.toBeInTheDocument()
  })
  describe('sessions des autres propriétaires', () => {
    const owned = (name: string, status: 'idle' | 'waiting', ownerName: string | null) => ({
      ...session(name, status),
      ownerName,
    })
    const mixed = [owned('mienne', 'idle', 'aris'), owned('autre', 'waiting', 'novanns'), owned('libre', 'idle', null)]
    const mixedWorkspace: WorkspaceViewModel = {
      sessions: mixed,
      hosts: [],
      byTarget: new Map(mixed.map(item => [item.key, item])),
    }
    it('ne propose par défaut que les sessions du compte', () => {
      render(<WallView workspace={mixedWorkspace} owner="aris" onOpenSession={vi.fn()} />)
      expect(screen.getAllByTestId('terminal').map(node => node.textContent)).toEqual(['mienne true'])
    })
    it('permet d’ajouter une session d’un autre propriétaire et la garde', () => {
      const view = render(<WallView workspace={mixedWorkspace} owner="aris" onOpenSession={vi.fn()} />)
      fireEvent.click(screen.getByRole('button', { name: 'Ajouter une session au mur' }))
      fireEvent.click(screen.getByRole('menuitem', { name: 'autre · novanns' }))
      expect(screen.getAllByTestId('terminal').map(node => node.textContent)).toEqual(['mienne true', 'autre true'])
      view.unmount()
      render(<WallView workspace={mixedWorkspace} owner="aris" onOpenSession={vi.fn()} />)
      expect(screen.getAllByTestId('terminal').map(node => node.textContent)).toEqual(['mienne true', 'autre true'])
    })
    it('affiche un mur vide à compléter quand le compte n’a aucune session', () => {
      render(<WallView workspace={mixedWorkspace} owner="camille" onOpenSession={vi.fn()} />)
      expect(screen.queryAllByTestId('terminal')).toEqual([])
      expect(screen.getByRole('combobox', { name: 'Choisir une session' })).toBeInTheDocument()
    })
  })
  describe('remplissage automatique', () => {
    const owned = (name: string, ownerName = 'aris', hostId = 'host') => ({
      ...session(name, 'idle'), ownerName, hostId, key: `${hostId}/${name}`,
    })
    const model = (items: ReturnType<typeof owned>[]): WorkspaceViewModel => ({
      sessions: items, hosts: [], byTarget: new Map(items.map(item => [item.key, item])),
    })
    const names = () => screen.queryAllByTestId('terminal').map(node => node.textContent)

    it('charge toutes les sessions du profil, au-delà de quatre et sur plusieurs hôtes', () => {
      const mine = Array.from({ length: 6 }, (_, index) => owned(`tâche-${index}`, 'aris', `host-${index % 2}`))
      render(<WallView workspace={model([...mine, owned('aris-autre', 'novanns')])} owner="aris" onOpenSession={vi.fn()} />)
      expect(names()).toEqual(mine.map(item => `${item.name} true`))
    })

    it('complète un ancien mur enregistré, y compris vide', () => {
      localStorage.setItem('tmuxatlas:wall-targets:v1', '[]')
      render(<WallView workspace={model([owned('une'), owned('deux')])} owner="aris" onOpenSession={vi.fn()} />)
      expect(names()).toEqual(['une true', 'deux true'])
    })

    it('attend les sessions chargées après le premier affichage', () => {
      localStorage.setItem('tmuxatlas:wall-targets:v1', '[]')
      const view = render(<WallView workspace={model([])} owner="aris" onOpenSession={vi.fn()} />)
      view.rerender(<WallView workspace={model([owned('une'), owned('deux')])} owner="aris" onOpenSession={vi.fn()} />)
      expect(names()).toEqual(['une true', 'deux true'])
    })

    it('garde les tuiles enregistrées pendant le chargement initial', () => {
      localStorage.setItem('tmuxatlas:wall-targets:v1', JSON.stringify(['host/autre']))
      const view = render(<WallView workspace={model([])} owner="aris" onOpenSession={vi.fn()} />)
      view.rerender(<WallView workspace={model([owned('une'), owned('autre', 'novanns')])} owner="aris" onOpenSession={vi.fn()} />)
      expect(names()).toEqual(['autre true', 'une true'])
    })

    it('ajoute la session choisie dans le menu +, sans doublons ni plafond', () => {
      const initial = Array.from({ length: 4 }, (_, index) => owned(`tâche-${index}`))
      const view = render(<WallView workspace={model(initial)} owner="aris" onOpenSession={vi.fn()} />)
      view.rerender(<WallView workspace={model([
        ...initial, owned('autre', 'novanns'), owned('nouvelle'), owned('suivante'),
      ])} owner="aris" onOpenSession={vi.fn()} />)
      expect(names()).toHaveLength(4)
      for (const name of ['nouvelle', 'suivante', 'autre · novanns']) {
        fireEvent.click(screen.getByRole('button', { name: 'Ajouter une session au mur' }))
        fireEvent.click(screen.getByRole('menuitem', { name }))
      }
      expect(names()).toEqual([...initial.map(item => `${item.name} true`), 'nouvelle true', 'suivante true', 'autre true'])
      expect(screen.queryByRole('button', { name: 'Ajouter une session au mur' })).toBeNull()
    })

    it('conserve un remplacement manuel et permet de réajouter la session retirée avec +', () => {
      const items = model([owned('mienne'), owned('autre', 'novanns')])
      const view = render(<WallView workspace={items} owner="aris" onOpenSession={vi.fn()} />)
      fireEvent.change(screen.getByRole('combobox'), { target: { value: 'host/autre' } })
      view.unmount()
      render(<WallView workspace={items} owner="aris" onOpenSession={vi.fn()} />)
      expect(names()).toEqual(['autre true'])
      fireEvent.click(screen.getByRole('button', { name: 'Ajouter une session au mur' }))
      fireEvent.click(screen.getByRole('menuitem', { name: 'mienne' }))
      expect(names()).toEqual(['autre true', 'mienne true'])
    })

    it('garde les choix du mur propres à chaque profil', () => {
      const items = model([owned('aris'), owned('novanns', 'novanns')])
      const view = render(<WallView key="aris" workspace={items} owner="aris" onOpenSession={vi.fn()} />)
      fireEvent.change(screen.getByRole('combobox'), { target: { value: 'host/novanns' } })
      view.rerender(<WallView key="novanns" workspace={items} owner="novanns" onOpenSession={vi.fn()} />)
      expect(names()).toEqual(['novanns true'])
      view.rerender(<WallView key="aris" workspace={items} owner="aris" onOpenSession={vi.fn()} />)
      expect(names()).toEqual(['novanns true'])
    })
  })
  describe('Talk', () => {
    const withPane = (name: string, command: string) => ({
      ...session(name, 'idle'),
      source: {
        windows: [{ active: true, panes: [{ active: true, current_command: command }] }],
      } as never,
    })
    const tiles = [
      withPane('claude-a', 'claude'),
      withPane('claude-b', 'claude'),
      withPane('shell', 'zsh'),
      withPane('codex', 'codex'),
      {
        ...session('agy', 'idle'),
        source: {
          windows: [{ active: true, panes: [{ active: true, current_command: 'python3', agent: 'agy' }] }],
        } as never,
      },
    ]
    const talkWorkspace: WorkspaceViewModel = {
      sessions: tiles,
      hosts: [],
      byTarget: new Map(tiles.map(item => [item.key, item])),
    }
    const show = (keys: string[], localHosts?: ReadonlySet<string>) => {
      localStorage.setItem('tmuxatlas:wall-targets:v2', JSON.stringify({
        targets: keys,
        omitted: tiles.map(tile => tile.key).filter(key => !keys.includes(key)),
      }))
      return render(<WallView workspace={talkWorkspace} localHosts={localHosts} sharedMic={mic} onOpenSession={vi.fn()} />)
    }
    beforeEach(() => {
      terminalProps.clear()
      mic.available = true
      mic.state = 'on'
      mic.error = ''
    })

    it('partage le micro du header et donne à chaque tuile sa place pour Talk', () => {
      show(['host/claude-a', 'host/shell'], new Set(['host']))
      const a = terminalProps.get('claude-a')!
      expect(a).toMatchObject({
        hostLocal: true,
        claudeActive: true,
        sharedMic: mic,
        talkLocked: false,
      })
      expect(a.talkSlot).toBeInstanceOf(HTMLElement)
      expect(a.talkSlot?.closest('header')).not.toBeNull()
      expect(terminalProps.get('shell')).toMatchObject({ claudeActive: false })
      expect(terminalProps.get('claude-a')).toMatchObject({ agyActive: false })
      expect(screen.queryByRole('button', { name: /micro/ })).toBeNull()
    })

    it('reconnaît agy derrière son lanceur, et Codex', () => {
      show(['host/agy', 'host/codex'], new Set(['host']))
      expect(terminalProps.get('agy')).toMatchObject({ claudeActive: false, agyActive: true, codexActive: false })
      expect(terminalProps.get('codex')).toMatchObject({ claudeActive: false, codexActive: true })
    })

    it('ne permet pas la dictée sur un hôte distant', () => {
      mic.available = false
      show(['host/claude-a'])
      expect(terminalProps.get('claude-a')).toMatchObject({ hostLocal: false })
      expect(screen.queryByRole('button', { name: /micro/ })).toBeNull()
    })

    it('ne laisse dicter qu’une tuile à la fois', () => {
      show(['host/claude-a', 'host/claude-b'], new Set(['host']))
      act(() => terminalProps.get('claude-a')!.onTalkHold!(true))
      expect(terminalProps.get('claude-a')!.talkLocked).toBe(false)
      expect(terminalProps.get('claude-b')!.talkLocked).toBe(true)
      act(() => terminalProps.get('claude-a')!.onTalkHold!(false))
      expect(terminalProps.get('claude-b')!.talkLocked).toBe(false)
    })
  })
})
