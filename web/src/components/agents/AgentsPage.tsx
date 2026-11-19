import { useEffect, useMemo, useState } from 'react'
import type { AgentState, AgentTeam, TeamAgent } from './types'
import { useAgentTeams } from './useAgentTeams'

const STATE_LABEL: Record<AgentState, string> = {
  running: 'En cours',
  quiet: 'Silencieux',
  stopped: 'Arrêté',
  done: 'Terminé',
}

const STATE_COLOR: Record<AgentState, string> = {
  running: 'var(--primary)',
  quiet: 'var(--warning)',
  stopped: 'var(--muted-foreground)',
  done: 'var(--success)',
}

/** How many agents fit around the sun; the others stay in the list. */
const MAX_STARS = 8
const CENTER_X = 50
const CENTER_Y = 46
const RADIUS = 35
/** Diameter of a star, in percent of the sky. Links stop at its edge. */
const RING_WIDTH = 15

function leadStatus(status: string): string {
  if (status === 'busy') return 'travaille'
  if (status === 'waiting') return 'attend ta réponse'
  if (status === 'idle') return 'au repos'
  return status || 'état inconnu'
}

export function initials(text: string): string {
  const words = text
    .split(/[^\p{L}\p{N}]+/u)
    .filter(word => word.length > 0)
  if (words.length === 0) return '?'
  if (words.length === 1) return words[0].slice(0, 2).toUpperCase()
  return (words[0][0] + words[1][0]).toUpperCase()
}

export function formatDuration(ms: number): string {
  const seconds = Math.max(0, Math.round(ms / 1000))
  if (seconds < 60) return `${seconds} s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes} min`
  return `${Math.floor(minutes / 60)} h ${String(minutes % 60).padStart(2, '0')}`
}

function stateLine(agent: TeamAgent, now: number): string {
  const updated = Date.parse(agent.updated_at)
  const started = Date.parse(agent.started_at)
  if (agent.state === 'running') return `En cours depuis ${formatDuration(now - started)}`
  if (agent.state === 'done') return `Terminé en ${formatDuration(updated - started)}`
  return `${STATE_LABEL[agent.state]} depuis ${formatDuration(now - updated)}`
}

function activeCount(team: AgentTeam): number {
  return team.agents.filter(agent => agent.state === 'running' || agent.state === 'quiet').length
}

/** Stars are decoration: fixed pseudo-random positions so they do not jump between renders. */
const STARS = (() => {
  let seed = 7
  const next = () => {
    seed = (seed * 9301 + 49297) % 233280
    return seed / 233280
  }
  return Array.from({ length: 44 }, () => ({
    x: next() * 100,
    y: next() * 100,
    size: 1 + Math.round(next() * 2),
    delay: (next() * 3).toFixed(1),
  }))
})()

function Ring({ agent, selected }: { agent: TeamAgent; selected: boolean }) {
  const color = STATE_COLOR[agent.state]
  const background =
    agent.state === 'running'
      ? `conic-gradient(${color} 0 28%, var(--border) 28% 100%)`
      : agent.state === 'done'
        ? color
        : `repeating-conic-gradient(${color} 0 6%, var(--border) 6% 12%)`
  return (
    <span
      className={`relative block aspect-square w-full rounded-full ${agent.state === 'running' ? 'agents-aura' : ''}`}
      style={{ outline: selected ? '2px solid var(--foreground)' : 'none', outlineOffset: 4 }}
    >
      <span
        className={`absolute inset-0 rounded-full ${agent.state === 'running' ? 'agents-spin' : ''}`}
        style={{ background }}
      />
      <span
        className="absolute inset-[9%] flex items-center justify-center rounded-full bg-card font-semibold"
        style={{ color, fontSize: 'clamp(11px, 4.4cqw, 22px)' }}
      >
        {initials(agent.description)}
      </span>
    </span>
  )
}

function Sky({
  team,
  stars,
  selectedId,
  onSelect,
}: {
  team: AgentTeam
  stars: TeamAgent[]
  selectedId: string | null
  onSelect: (id: string) => void
}) {
  return (
    <div
      className="agents-sky relative mx-auto aspect-square w-full max-w-[44rem] overflow-hidden rounded-lg border border-border"
      style={{
        containerType: 'inline-size',
        background: `radial-gradient(circle at ${CENTER_X}% ${CENTER_Y}%, var(--secondary) 0, var(--background) 62%)`,
      }}
    >
      <div aria-hidden="true">
        {STARS.map((star, index) => (
          <span
            key={index}
            className="agents-twinkle absolute rounded-full"
            style={{
              left: `${star.x}%`,
              top: `${star.y}%`,
              width: star.size,
              height: star.size,
              background: 'var(--accent)',
              animationDelay: `${star.delay}s`,
            }}
          />
        ))}
        <span
          className="agents-orbit absolute rounded-full border border-dashed"
          style={{
            left: `${CENTER_X - RADIUS}%`,
            top: `${CENTER_Y - RADIUS}%`,
            width: `${RADIUS * 2}%`,
            height: `${RADIUS * 2}%`,
            borderColor: 'color-mix(in oklch, var(--muted-foreground) 40%, transparent)',
          }}
        />
        <span
          className="agents-orbit-reverse absolute rounded-full border border-dashed"
          style={{
            left: `${CENTER_X - RADIUS * 0.58}%`,
            top: `${CENTER_Y - RADIUS * 0.58}%`,
            width: `${RADIUS * 1.16}%`,
            height: `${RADIUS * 1.16}%`,
            borderColor: 'color-mix(in oklch, var(--warning) 38%, transparent)',
          }}
        />
        {stars.map((agent, index) => {
          const angle = -90 + (index * 360) / stars.length
          const color = STATE_COLOR[agent.state]
          const moving = agent.state === 'running'
          return (
            <span
              key={agent.id}
              className="absolute h-0.5 origin-left"
              style={{
                left: `${CENTER_X}%`,
                top: `${CENTER_Y}%`,
                width: `${RADIUS - RING_WIDTH / 2}%`,
                transform: `rotate(${angle}deg)`,
                opacity: moving || agent.state === 'done' ? 0.8 : 0.45,
                background:
                  moving || agent.state === 'done'
                    ? `linear-gradient(90deg, var(--warning), ${color})`
                    : `repeating-linear-gradient(90deg, ${color} 0 6px, transparent 6px 14px)`,
              }}
            >
              {moving && (
                <>
                  <span className="agents-travel absolute -top-[3px] size-2 rounded-full bg-white" style={{ boxShadow: `0 0 10px 2px ${color}` }} />
                  <span
                    className="agents-travel absolute -top-[3px] size-2 rounded-full bg-white"
                    style={{ boxShadow: `0 0 10px 2px ${color}`, animationDelay: '0.8s' }}
                  />
                </>
              )}
            </span>
          )
        })}
      </div>

      <div
        className="agents-sun absolute flex aspect-square -translate-x-1/2 -translate-y-1/2 flex-col items-center justify-center rounded-full text-center font-semibold leading-tight"
        style={{
          left: `${CENTER_X}%`,
          top: `${CENTER_Y}%`,
          width: '21%',
          color: 'var(--primary-foreground)',
          fontSize: 'clamp(10px, 2.6cqw, 15px)',
          background:
            'radial-gradient(circle at 40% 35%, color-mix(in oklch, var(--warning) 60%, white) 0, var(--warning) 45%, color-mix(in oklch, var(--warning) 70%, black) 100%)',
        }}
      >
        <span>Chef</span>
        <span className="font-normal" style={{ fontSize: 'clamp(9px, 2cqw, 12px)' }}>
          {leadStatus(team.status)}
        </span>
      </div>

      {stars.map((agent, index) => {
        const angle = ((-90 + (index * 360) / stars.length) * Math.PI) / 180
        const selected = agent.id === selectedId
        return (
          <button
            key={agent.id}
            type="button"
            aria-pressed={selected}
            aria-label={`${agent.description}, ${STATE_LABEL[agent.state]}`}
            onClick={() => onSelect(agent.id)}
            className="ui-focus-ring absolute -translate-x-1/2 -translate-y-1/2 rounded-full"
            style={{
              left: `${CENTER_X + Math.cos(angle) * RADIUS}%`,
              top: `${CENTER_Y + Math.sin(angle) * RADIUS}%`,
              width: `${RING_WIDTH}%`,
            }}
          >
            <Ring agent={agent} selected={selected} />
            <span
              className="pointer-events-none absolute left-1/2 top-full mt-1.5 flex -translate-x-1/2 justify-center text-center leading-tight"
              style={{ width: '26cqw', fontSize: 'clamp(10px, 2.2cqw, 13px)' }}
            >
              <span
                className="flex flex-col items-center rounded px-1.5 py-0.5"
                style={{ background: 'color-mix(in oklch, var(--background) 78%, transparent)' }}
              >
                <span className="line-clamp-2 font-medium text-foreground">{agent.description}</span>
                <span className="font-semibold" style={{ color: STATE_COLOR[agent.state] }}>
                  {STATE_LABEL[agent.state]}
                </span>
              </span>
            </span>
          </button>
        )
      })}

      {stars.length === 0 && (
        <p className="absolute inset-x-4 bottom-6 text-center text-xs text-muted-foreground">
          Aucun sous-agent lancé par cette session depuis 24 h. Dès qu’elle en lance un, il apparaît ici.
        </p>
      )}
    </div>
  )
}

function Details({ agent, now }: { agent: TeamAgent; now: number }) {
  const color = STATE_COLOR[agent.state]
  return (
    <section aria-label="Agent sélectionné" className="flex flex-col gap-3">
      <h2 className="text-2xs font-semibold uppercase tracking-wider text-muted-foreground">Agent sélectionné</h2>
      <div>
        <p className="text-lg font-semibold leading-snug">{agent.description}</p>
        <p className="text-xs font-semibold" style={{ color }}>
          {stateLine(agent, now)}
        </p>
      </div>
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
        <dt className="text-muted-foreground">Type</dt>
        <dd>{agent.type || 'non précisé'}</dd>
        {agent.model && (
          <>
            <dt className="text-muted-foreground">Modèle</dt>
            <dd>{agent.model}</dd>
          </>
        )}
        <dt className="text-muted-foreground">Actions</dt>
        <dd className="tabular-nums">{agent.steps}</dd>
      </dl>
      <div className="rounded-lg border border-border bg-input p-3 font-mono text-xs leading-relaxed text-muted-foreground">
        {agent.log.map((line, index) => (
          <p key={index} className="break-words">
            {line}
          </p>
        ))}
        <p className="break-words text-foreground">
          {agent.action || 'pas encore d’action'}
          {agent.state === 'running' && (
            <span
              aria-hidden="true"
              className="agents-cursor ml-1 inline-block h-3 w-1.5 align-[-2px]"
              style={{ background: color }}
            />
          )}
        </p>
      </div>
      {agent.state === 'quiet' && (
        <p className="text-2xs text-muted-foreground">
          Silencieux : rien d’écrit depuis plus de 3 minutes. C’est souvent une longue commande, parfois un agent
          bloqué.
        </p>
      )}
    </section>
  )
}

function readSessionParam(): string | null {
  return new URLSearchParams(window.location.search).get('session')
}

export function AgentsPage({ onOpenSession }: { onOpenSession?: (sessionKey: string) => void }) {
  const { teams, status, failed } = useAgentTeams()
  const [teamId, setTeamId] = useState<string | null>(null)
  const [wantedSession] = useState(readSessionParam)
  const [agentId, setAgentId] = useState<string | null>(null)
  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [])

  const team = useMemo(
    () =>
      teams.find(candidate => candidate.session_id === teamId) ??
      (teamId === null && wantedSession ? teams.find(candidate => candidate.tmux_session === wantedSession) : undefined) ??
      teams[0] ??
      null,
    [teams, teamId, wantedSession],
  )
  const stars = useMemo(() => (team ? team.agents.slice(0, MAX_STARS) : []), [team])
  const selected = team?.agents.find(agent => agent.id === agentId) ?? team?.agents[0] ?? null

  return (
    <main className="flex-1 overflow-y-auto p-4 text-sm sm:p-6">
      <h1 className="mb-1 text-2xl font-medium">Équipes d’agents</h1>
      <p className="mb-4 max-w-2xl text-xs text-muted-foreground">
        Les sous-agents lancés par tes sessions Claude Code, lus en direct dans leurs journaux.
      </p>

      {status === 'loading' && <p className="text-xs text-muted-foreground">Chargement…</p>}
      {status === 'forbidden' && (
        <p className="text-xs text-muted-foreground">Cette page est réservée aux administrateurs.</p>
      )}
      {status === 'error' && <p className="text-xs text-destructive">Impossible de joindre le hub.</p>}
      {failed && status === 'ready' && (
        <p className="mb-3 text-2xs text-warning">Dernière actualisation en échec, nouvel essai dans 3 s.</p>
      )}
      {status === 'ready' && teams.length === 0 && (
        <p className="text-xs text-muted-foreground">Aucune session Claude Code n’est ouverte sur ce compte.</p>
      )}

      {status === 'ready' && team && (
        <>
          <div className="mb-4 flex flex-wrap gap-2" role="group" aria-label="Sessions Claude Code">
            {teams.map(candidate => {
              const current = candidate.session_id === team.session_id
              const active = activeCount(candidate)
              return (
                <button
                  key={candidate.session_id}
                  type="button"
                  aria-pressed={current}
                  onClick={() => {
                    setTeamId(candidate.session_id)
                    setAgentId(null)
                  }}
                  className={`ui-focus-ring flex min-h-9 items-center gap-2 rounded-full border px-3 text-xs ${
                    current
                      ? 'border-primary bg-secondary text-foreground'
                      : 'border-border text-muted-foreground hover:bg-secondary hover:text-foreground'
                  }`}
                >
                  <span className="max-w-48 truncate">{candidate.tmux_session || candidate.name}</span>
                  {active > 0 && (
                    <span
                      className="rounded-full px-1.5 text-2xs font-semibold tabular-nums"
                      style={{
                        color: 'var(--primary)',
                        background: 'color-mix(in oklch, var(--primary) 16%, transparent)',
                      }}
                    >
                      {active}
                      <span className="sr-only"> en cours</span>
                    </span>
                  )}
                </button>
              )
            })}
          </div>

          <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_22rem]">
            <Sky team={team} stars={stars} selectedId={selected?.id ?? null} onSelect={setAgentId} />

            <div className="flex min-w-0 flex-col gap-5">
              <div>
                <p className="font-mono text-xs text-muted-foreground">{team.cwd}</p>
                <p className="text-lg font-semibold">{team.name || team.tmux_session || 'Session'}</p>
                {team.tmux_session && onOpenSession && (
                  <button
                    type="button"
                    onClick={() => onOpenSession(team.tmux_session as string)}
                    className="ui-focus-ring mt-2 min-h-9 rounded border border-border px-3 text-xs hover:bg-secondary"
                  >
                    Ouvrir le terminal de « {team.tmux_session} »
                  </button>
                )}
              </div>

              {selected && <Details agent={selected} now={now} />}

              {team.agents.length > 0 && (
                <section aria-label="Toute l’équipe" className="flex flex-col gap-2">
                  <h2 className="text-2xs font-semibold uppercase tracking-wider text-muted-foreground">
                    Toute l’équipe ({team.agents.length})
                  </h2>
                  <ul className="flex flex-col">
                    {team.agents.map(agent => (
                      <li key={agent.id}>
                        <button
                          type="button"
                          aria-pressed={agent.id === selected?.id}
                          onClick={() => setAgentId(agent.id)}
                          className={`ui-focus-ring flex min-h-9 w-full items-center gap-2.5 rounded px-2 text-left text-xs hover:bg-secondary ${
                            agent.id === selected?.id ? 'bg-secondary' : ''
                          }`}
                        >
                          <span
                            aria-hidden="true"
                            className="size-2 shrink-0 rounded-full"
                            style={{ background: STATE_COLOR[agent.state] }}
                          />
                          <span className="min-w-0 flex-1 truncate">{agent.description}</span>
                          <span className="shrink-0 font-semibold" style={{ color: STATE_COLOR[agent.state] }}>
                            {STATE_LABEL[agent.state]}
                          </span>
                        </button>
                      </li>
                    ))}
                  </ul>
                  {team.agents.length > MAX_STARS && (
                    <p className="text-2xs text-muted-foreground">
                      Le ciel montre les {MAX_STARS} premiers ; la liste les montre tous.
                    </p>
                  )}
                </section>
              )}
            </div>
          </div>
        </>
      )}
    </main>
  )
}
