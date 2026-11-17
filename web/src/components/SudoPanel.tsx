import { useEffect, useRef, useState } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { KeyRound, ShieldAlert, X } from 'lucide-react'
import { Button } from './ui'
import { usePreferences } from '../hooks/usePreferences'
import { useSudoRequests } from '../hooks/useSudoRequests'
import { getXtermTheme } from '../theme'
import {
  OPEN_SUDO_STATUSES,
  openSudoSocket,
  sudoApi,
  type SudoPeer,
  type SudoRequest,
  type SudoStatus,
} from '../lib/sudoApi'
import { sha256Hex } from '../lib/sha256'

const statusLabels: Record<SudoStatus, string> = {
  pending: 'En attente',
  approved: 'Approuvée',
  running: 'Terminal ouvert',
  closed: 'Terminal fermé',
  done: 'Terminé',
  refused: 'Refusée',
  expired: 'Expirée',
  cancelled: 'Annulée par l’agent',
}

interface Props {
  hostId: string
  sessionName: string
  /** Only administrators may see and approve requests. */
  enabled: boolean
}

/**
 * Sudo requests made by the agent of this session: edit, approve, then work in
 * a dedicated interactive terminal where the proposed command waits at the
 * prompt, and mark the request done or refused. The agent only receives that
 * status and the note. Nothing here is stored in the browser.
 */
export function SudoPanel({ hostId, sessionName, enabled }: Props) {
  const { requests, available, error, refresh } = useSudoRequests(hostId, sessionName, enabled)
  const [dismissed, setDismissed] = useState<string | null>(null)
  const open = requests.filter(request => OPEN_SUDO_STATUSES.includes(request.status))
  const current = open[0]
  if (!available || !current || dismissed === `${current.id}:${current.status}:${current.version}`) return null
  return (
    <section
      aria-label="Demande sudo"
      className="shrink-0 border-b border-warning/40 bg-warning/5 px-3 py-2 text-sm"
      data-testid="sudo-panel"
    >
      <header className="mb-2 flex items-center gap-2">
        <KeyRound size={16} className="text-warning" aria-hidden="true" />
        <strong>Demande sudo de l’agent</strong>
        <span className="rounded border border-border px-2 text-xs">{statusLabels[current.status]}</span>
        {open.length > 1 && <span className="text-xs text-muted-foreground">+{open.length - 1} autre(s)</span>}
        <button
          type="button"
          aria-label="Masquer"
          className="ml-auto text-muted-foreground"
          onClick={() => setDismissed(`${current.id}:${current.status}:${current.version}`)}
        >
          <X size={16} />
        </button>
      </header>
      {error && <p className="mb-2 text-destructive">{error}</p>}
      <SudoRequestView key={current.id} request={current} onChange={refresh} />
    </section>
  )
}

function SudoRequestView({ request, onChange }: { request: SudoRequest; onChange: () => Promise<void> }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [grant, setGrant] = useState<string | null>(null)
  const run = async (action: () => Promise<unknown>) => {
    setBusy(true)
    setError('')
    try {
      await action()
      await onChange()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="flex flex-col gap-2">
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs text-muted-foreground">
        <dt>Demandeur</dt>
        <dd>
          <code className="break-all text-foreground" data-testid="sudo-requester">
            {request.origin.host_id}
          </code>
          <span className="ml-1 italic">(empreinte vérifiée par le hub)</span>
        </dd>
        <dt>Raison</dt>
        <dd className="text-foreground">{request.reason || '—'}</dd>
        <dt>Origine</dt>
        <dd>
          {request.origin.host_name || request.origin.host_id} / {request.origin.session} · pane {request.origin.pane || '?'}
          {request.origin.unix_user ? ` · ${request.origin.unix_user}` : ''}
          {request.origin.cwd ? ` · ${request.origin.cwd}` : ''}
          <span className="ml-1 italic">(déclaré par l’agent)</span>
        </dd>
      </dl>
      {request.status !== 'pending' && <SudoCommand command={request.command} label={`Commande v${request.version}`} />}
      {request.status === 'pending' && <PendingStep request={request} busy={busy} run={run} onGrant={setGrant} />}
      {grant && request.status !== 'pending' ? (
        <SudoTerminal grant={grant} onClosed={() => void onChange()} />
      ) : (
        (request.status === 'approved' || request.status === 'running') && (
          <p className="text-muted-foreground">
            {request.status === 'running'
              ? 'Le terminal sudo est ouvert dans un autre onglet.'
              : 'Approuvée dans un autre onglet : le terminal s’y ouvre.'}
          </p>
        )
      )}
      {(request.status === 'running' || request.status === 'closed') && <OutcomeStep request={request} busy={busy} run={run} />}
      {error && <p className="text-destructive">{error}</p>}
    </div>
  )
}

/**
 * The exact command, wrapped so nothing hides past the edge, with its length
 * and a short digest to compare with what the sudo terminal prints.
 */
export function SudoCommand({ command, label }: { command: string; label: string }) {
  const length = [...command].length
  return (
    <figure className="flex flex-col gap-1" data-testid="sudo-command">
      <pre
        aria-label={label}
        className="max-h-56 overflow-auto whitespace-pre-wrap break-all rounded border border-border bg-card p-2 font-mono text-xs text-foreground"
      >
        {command}
      </pre>
      <figcaption className="font-mono text-[11px] text-muted-foreground">
        {length} caractère{length > 1 ? 's' : ''} · sha256 {sha256Hex(command).slice(0, 12)}
      </figcaption>
    </figure>
  )
}

interface StepProps {
  request: SudoRequest
  busy: boolean
  run: (action: () => Promise<unknown>) => Promise<void>
}

function PendingStep({ request, busy, run, onGrant }: StepProps & { onGrant: (grant: string) => void }) {
  const [command, setCommand] = useState(request.command)
  const [peers, setPeers] = useState<SudoPeer[]>([])
  const [peer, setPeer] = useState('')
  const [note, setNote] = useState('')
  useEffect(() => setCommand(request.command), [request.command, request.version])
  useEffect(() => {
    let cancelled = false
    void sudoApi
      .peers()
      .then(list => {
        if (cancelled) return
        setPeers(list)
        const online = list.filter(candidate => candidate.online && candidate.id !== request.origin.host_id)
        if (online.length === 1) setPeer(online[0].id)
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [request.origin.host_id])
  const edited = command !== request.command
  const valid = command.trim() !== '' && !/[\r\n]/.test(command)
  const choices = peers.filter(candidate => candidate.id !== request.origin.host_id)
  return (
    <div className="flex flex-col gap-2">
      <span className="text-xs text-muted-foreground">
        Commande proposée (v{request.version}) : le terminal l’affichera à l’invite, précédée de <code>sudo</code>, sans la
        lancer.
      </span>
      <SudoCommand command={request.command} label={`Commande v${request.version}`} />
      <label className="flex flex-col gap-1">
        <span className="text-xs text-muted-foreground">Modifier la commande</span>
        <input
          className="rounded border border-border bg-background px-2 py-1 font-mono"
          value={command}
          spellCheck={false}
          autoComplete="off"
          onChange={event => setCommand(event.target.value.replace(/[\r\n]/g, ' '))}
        />
      </label>
      {request.original !== request.command && (
        <div className="text-xs text-muted-foreground">
          Demandé à l’origine par l’agent :
          <SudoCommand command={request.original} label="Commande d’origine" />
        </div>
      )}
      <div className="flex flex-wrap items-center gap-2">
        {edited && (
          <Button size="sm" disabled={busy || !valid} onClick={() => void run(() => sudoApi.edit(request.id, request.version, command))}>
            Enregistrer v{request.version + 1}
          </Button>
        )}
        <select
          aria-label="Terminal sudo"
          className="min-h-8 rounded border border-border bg-background px-2 text-xs"
          value={peer}
          onChange={event => setPeer(event.target.value)}
        >
          <option value="">Terminal sudo…</option>
          {choices.map(candidate => (
            <option key={candidate.id} value={candidate.id} disabled={!candidate.online}>
              {candidate.name}
              {candidate.unix_user ? ` (${candidate.unix_user})` : ''}
              {candidate.online ? '' : ' — hors ligne'}
            </option>
          ))}
        </select>
        <Button
          size="sm"
          variant="primary"
          disabled={busy || edited || !peer}
          title={edited ? 'Enregistre d’abord la nouvelle version' : undefined}
          onClick={() =>
            void run(async () => {
              const grant = await sudoApi.approve(request.id, request.version, peer)
              onGrant(grant.grant)
            })
          }
        >
          Approuver v{request.version} et ouvrir le terminal
        </Button>
        <input
          aria-label="Motif du refus"
          placeholder="Motif (optionnel)"
          className="min-h-8 rounded border border-border bg-background px-2 text-xs"
          value={note}
          onChange={event => setNote(event.target.value)}
        />
        <Button size="sm" variant="destructive" disabled={busy} onClick={() => void run(() => sudoApi.refuse(request.id, note))}>
          Refuser
        </Button>
      </div>
      {choices.length === 0 && (
        <p className="text-xs text-warning">
          Aucun terminal sudo disponible : lance <code>tmuxatlas agent --sudo-terminal</code> sous ton compte et épingle son
          empreinte dans <code>TMUXATLAS_SUDO_PEERS</code> sur le hub.
        </p>
      )}
    </div>
  )
}

/** A dedicated xterm: no drafts, no storage, no mobile composer. */
function SudoTerminal({ grant, onClosed }: { grant: string; onClosed: () => void }) {
  const containerRef = useRef<HTMLDivElement>(null)
  const { prefs } = usePreferences()
  const [passwordMode, setPasswordMode] = useState(false)
  const [status, setStatus] = useState('Ouverture du terminal sudo…')
  // Read once: re-running the effect would reopen the socket with a used grant.
  const themeRef = useRef(prefs.theme)
  const closedRef = useRef(onClosed)
  closedRef.current = onClosed
  useEffect(() => {
    const container = containerRef.current
    if (!container) return
    const term = new XTerm({
      theme: getXtermTheme(themeRef.current),
      fontSize: 13,
      rows: 12,
      cursorBlink: true,
      scrollback: 1000,
      convertEol: false,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(container)
    fit.fit()
    const socket = openSudoSocket(grant, term.cols, term.rows)
    const encoder = new TextEncoder()
    const input = term.onData(data => {
      if (socket.readyState === WebSocket.OPEN) socket.send(encoder.encode(data))
    })
    const resize = term.onResize(({ cols, rows }) => {
      if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ type: 'resize', cols, rows }))
    })
    socket.onmessage = event => {
      if (typeof event.data === 'string') {
        try {
          const message = JSON.parse(event.data) as { type?: string; on?: boolean; message?: string }
          if (message.type === 'echo') setPasswordMode(message.on === false)
          if (message.type === 'ready') {
            setStatus('')
            term.focus()
          }
          if (message.type === 'error') setStatus(`Terminal refusé : ${message.message || 'erreur'}`)
        } catch {
          /* control frames only */
        }
        return
      }
      term.write(new Uint8Array(event.data as ArrayBuffer))
    }
    socket.onclose = () => {
      setPasswordMode(false)
      setStatus(current => current || 'Terminal fermé. Indique si c’est terminé ou refusé.')
      closedRef.current()
    }
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(() => fit.fit())
    observer?.observe(container)
    return () => {
      observer?.disconnect()
      input.dispose()
      resize.dispose()
      socket.onclose = null
      socket.close()
      term.dispose()
    }
  }, [grant])
  return (
    <div className="flex flex-col gap-1">
      {passwordMode && (
        <p role="status" className="flex items-center gap-1 text-xs text-warning">
          <ShieldAlert size={14} aria-hidden="true" /> Saisie masquée : le mot de passe va à sudo, jamais à l’agent.
        </p>
      )}
      {status && <p className="text-xs text-muted-foreground">{status}</p>}
      <div ref={containerRef} className="h-56 w-full overflow-hidden rounded border border-border bg-card" data-sudo-terminal />
    </div>
  )
}

/** Done or refused, with a note: all the agent will ever receive. */
function OutcomeStep({ request, busy, run }: StepProps) {
  const [note, setNote] = useState('')
  return (
    <div className="flex flex-col gap-2">
      <p className="text-xs text-muted-foreground">
        L’agent ne recevra que « terminé » ou « refusé » et cette note : ni la sortie, ni les frappes.
        {request.session?.reason ? ` Terminal fermé : ${request.session.reason}.` : ''}
      </p>
      <input
        aria-label="Note pour l’agent"
        placeholder="Note pour l’agent (optionnelle)"
        className="rounded border border-border bg-background px-2 py-1 text-xs"
        value={note}
        maxLength={500}
        onChange={event => setNote(event.target.value.replace(/[\r\n]/g, ' '))}
      />
      <div className="flex flex-wrap gap-2">
        <Button size="sm" variant="primary" disabled={busy} onClick={() => void run(() => sudoApi.done(request.id, note))}>
          Terminé
        </Button>
        <Button size="sm" variant="destructive" disabled={busy} onClick={() => void run(() => sudoApi.refuse(request.id, note))}>
          Refusé
        </Button>
      </div>
    </div>
  )
}
