import { useMemo, useState } from 'react'
import { formatClock, formatMoney, formatNumber, formatPercent, formatRelative, formatTokens } from './format'
import { Kpi, Meter, StatusBadge, pctTone, toneColor, type Tone } from './primitives'
import type { MonitorCLIAccount, MonitorQuotaWindow, MonitorTokenCounts } from './types'

export type CLIFilter = 'all' | 'claude' | 'codex' | 'agy'

const cliStyles: Record<string, { label: string; short: string; className: string }> = {
  claude: {
    label: 'Claude Code',
    short: 'Claude',
    className: 'border-amber-500/40 bg-amber-500/10 text-amber-300',
  },
  codex: {
    label: 'Codex CLI',
    short: 'Codex',
    className: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300',
  },
  agy: {
    label: 'Antigravity',
    short: 'Agy',
    className: 'border-sky-500/40 bg-sky-500/10 text-sky-300',
  },
}

function cliMeta(cli: string) {
  return cliStyles[cli] ?? { label: cli, short: cli, className: 'border-border bg-secondary text-foreground' }
}

function isWindowExpired(w: MonitorQuotaWindow, nowSec: number): boolean {
  return Boolean(w.expired || (w.resets_at && w.resets_at <= nowSec))
}

export function isAccountSaturated(account: MonitorCLIAccount, nowSec = Math.floor(Date.now() / 1000)): boolean {
  if (!account.connected) return false
  return (account.windows ?? []).some(w => !isWindowExpired(w, nowSec) && w.used_percent >= 95)
}

function isAccountLive(account: MonitorCLIAccount, nowSec: number): boolean {
  if ((account.live_sessions ?? []).length > 0) return true
  return Boolean(account.last_activity && nowSec - account.last_activity <= 300)
}

function hasActivitySinceReset(account: MonitorCLIAccount, w: MonitorQuotaWindow, nowSec: number): boolean {
  if (!isWindowExpired(w, nowSec)) return false
  return Boolean(w.resets_at && account.last_activity && account.last_activity > w.resets_at)
}

function accountStatus(account: MonitorCLIAccount, nowSec: number): { tone: Tone; label: string } {
  if (!account.connected) return { tone: 'neutral', label: 'Non connecté' }
  const windows = account.windows ?? []
  const active = windows.filter(w => !isWindowExpired(w, nowSec))
  if (active.some(w => w.used_percent >= 100)) {
    if (account.cli === 'agy' && active.some(w => w.used_percent < 100)) {
      return { tone: 'warning', label: 'Partiel' }
    }
    return { tone: 'critical', label: 'Saturé' }
  }
  if (active.some(w => w.used_percent >= 80)) return { tone: 'warning', label: 'Élevé' }
  if (account.quota_source === 'local_logs_only') return { tone: 'neutral', label: 'Suivi local' }
  if (windows.some(w => hasActivitySinceReset(account, w, nowSec))) {
    return { tone: 'neutral', label: 'À actualiser' }
  }
  return { tone: 'ok', label: 'Disponible' }
}

function StaleMeter() {
  return (
    <div
      className="h-1 w-full rounded-full bg-secondary opacity-60"
      role="presentation"
      title="Quota expiré depuis la dernière activité"
    />
  )
}

function disabledReasonText(reason?: string): string {
  switch (reason) {
    case 'out_of_credits':
      return 'crédits épuisés'
    case 'org_level_disabled':
    case 'org_level_disabled_until':
      return 'désactivé par l’organisation'
    case 'user_disabled':
      return 'désactivé'
    default:
      return reason || 'désactivé'
  }
}

function addTokens(acc: MonitorTokenCounts, item: MonitorTokenCounts) {
  acc.input += item.input
  acc.cache_read += item.cache_read
  acc.cache_write = (acc.cache_write ?? 0) + (item.cache_write ?? 0)
  acc.output += item.output
  acc.reasoning = (acc.reasoning ?? 0) + (item.reasoning ?? 0)
  acc.total += item.total
  acc.requests = (acc.requests ?? 0) + (item.requests ?? 0)
}

function emptyTokens(): MonitorTokenCounts {
  return { input: 0, cache_read: 0, cache_write: 0, output: 0, reasoning: 0, total: 0, requests: 0 }
}

function tokenTooltip(label: string, t: MonitorTokenCounts): string {
  const parts = [
    `${label} : ${formatNumber(t.total)} tokens`,
    `Entrée : ${formatNumber(t.input)}`,
    `Cache lu : ${formatNumber(t.cache_read)}`,
  ]
  if (t.cache_write) parts.push(`Cache écrit : ${formatNumber(t.cache_write)}`)
  parts.push(`Sortie : ${formatNumber(t.output)}`)
  if (t.reasoning) parts.push(`Raisonnement : ${formatNumber(t.reasoning)}`)
  if (t.requests) parts.push(`Requêtes : ${formatNumber(t.requests)}`)
  return parts.join('\n')
}

function shortPath(path?: string): string {
  if (!path) return ''
  const parts = path.replace(/\/+$/, '').split('/').filter(Boolean)
  return parts[parts.length - 1] || path
}

function QuotaWindowRow({
  account,
  window: w,
  nowSec,
}: {
  account: MonitorCLIAccount
  window: MonitorQuotaWindow
  nowSec: number
}) {
  const expired = isWindowExpired(w, nowSec)
  const activitySinceReset = expired && Boolean(w.resets_at && account.last_activity && account.last_activity > w.resets_at)

  if (expired) {
    const isShortWindow = (w.window_minutes ?? 0) > 0 && (w.window_minutes ?? 0) <= 360
    const windowTokens = isShortWindow ? account.tokens_5h.total : account.tokens_7d.total
    const windowLabel = isShortWindow ? '5 h' : '7 j'
    return (
      <div className="flex flex-col gap-1">
        <div className="flex items-baseline justify-between gap-2 text-xs">
          <span className="font-medium text-foreground">Fenêtre {w.label}</span>
          <span className="tabular-nums text-2xs font-medium text-muted-foreground">
            {activitySinceReset ? 'Inconnu · à actualiser' : 'Réinitialisé'}
          </span>
        </div>
        {activitySinceReset ? <StaleMeter /> : <Meter value={0} tone="ok" />}
        <div className="flex flex-wrap justify-between gap-x-2 text-2xs text-muted-foreground">
          <span>
            {activitySinceReset
              ? `Reset passé · ${formatTokens(windowTokens)} tok (${windowLabel}) depuis`
              : `Fenêtre réinitialisée ${w.resets_at ? formatRelative(w.resets_at, nowSec * 1000) : ''}`}
          </span>
          <span className="tabular-nums">dernière mesure : {formatPercent(w.used_percent)}</span>
        </div>
      </div>
    )
  }

  const tone = pctTone(w.used_percent, 75, 95)
  const remaining = Math.max(0, 100 - w.used_percent)
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-baseline justify-between gap-2 text-xs">
        <span className="font-medium text-foreground">Fenêtre {w.label}</span>
        <span className="tabular-nums font-semibold text-foreground">{formatPercent(w.used_percent)}</span>
      </div>
      <Meter value={w.used_percent} tone={tone} />
      <div className="flex flex-wrap justify-between gap-x-2 text-2xs text-muted-foreground">
        <span>
          {w.resets_at
            ? `Reset ${formatRelative(w.resets_at, nowSec * 1000)} (${formatClock(w.resets_at * 1000)})`
            : 'Fenêtre active'}
        </span>
        <span className="tabular-nums">{formatPercent(remaining)} restant</span>
      </div>
    </div>
  )
}

function TokenCell({ label, counts }: { label: string; counts: MonitorTokenCounts }) {
  const fresh = counts.input + counts.output + (counts.cache_write ?? 0)
  return (
    <div
      className="flex min-w-0 flex-col gap-0.5 rounded border border-border/60 bg-background/50 px-2 py-1.5"
      title={tokenTooltip(label, counts)}
    >
      <div className="flex items-baseline justify-between gap-1 text-2xs text-muted-foreground">
        <span>{label}</span>
        {counts.requests ? <span className="tabular-nums">{formatNumber(counts.requests)} req</span> : null}
      </div>
      <div className="truncate text-sm font-semibold tabular-nums text-foreground">{formatTokens(counts.total)}</div>
      <div className="truncate text-2xs tabular-nums text-muted-foreground">{formatTokens(fresh)} in/out</div>
    </div>
  )
}

function ExtraUsageBlock({ account }: { account: MonitorCLIAccount }) {
  const extra = account.extra
  if (!extra || (!extra.limit && !extra.unlimited && !extra.balance)) return null
  return (
    <div className="flex flex-col gap-1 border-t border-border/60 pt-2">
      {extra.unlimited ? (
        <div className="flex items-center justify-between text-2xs">
          <span className="text-muted-foreground">Crédits</span>
          <span className="font-medium text-foreground">Illimités</span>
        </div>
      ) : extra.limit ? (
        extra.enabled ? (
          <>
            <div className="flex items-baseline justify-between gap-2 text-2xs">
              <span className="text-muted-foreground">
                Extra {extra.limit_reached ? '· plafond atteint' : ''}
              </span>
              <span className="tabular-nums text-foreground">
                {formatMoney(extra.used ?? 0, extra.currency)} / {formatMoney(extra.limit, extra.currency)} (
                {formatPercent(extra.used_percent ?? 0)})
              </span>
            </div>
            <Meter value={extra.used_percent ?? 0} tone={pctTone(extra.used_percent ?? 0, 75, 95)} />
          </>
        ) : (
          <div className="flex items-baseline justify-between gap-2 text-2xs text-muted-foreground">
            <span>Extra · {disabledReasonText(extra.disabled_reason)}</span>
            <span className="tabular-nums">
              {formatMoney(extra.used ?? 0, extra.currency)} / {formatMoney(extra.limit, extra.currency)}
            </span>
          </div>
        )
      ) : (
        <div className="flex items-center justify-between text-2xs">
          <span className="text-muted-foreground">Solde crédits</span>
          <span className="tabular-nums text-foreground">{extra.balance || '0'}</span>
        </div>
      )}
    </div>
  )
}

function LiveSessionsList({
  account,
  onOpenSessionName,
}: {
  account: MonitorCLIAccount
  onOpenSessionName?: (sessionName: string) => void
}) {
  const live = account.live_sessions ?? []
  if (live.length === 0) {
    return (
      <div className="text-2xs text-muted-foreground">Aucune session tmux ouverte sur ce compte actuellement.</div>
    )
  }
  return (
    <div className="flex flex-wrap gap-1.5">
      {live.map(s => {
        const content = (
          <>
            <span className="h-1.5 w-1.5 shrink-0 rounded-full" style={{ background: toneColor.ok }} />
            <span className="truncate font-medium text-foreground">{s.session}</span>
            {s.path && <span className="truncate text-muted-foreground">· {shortPath(s.path)}</span>}
            {onOpenSessionName && <span aria-hidden="true" className="text-muted-foreground">↗</span>}
          </>
        )
        if (onOpenSessionName) {
          return (
            <button
              key={s.session}
              type="button"
              onClick={() => onOpenSessionName(s.session)}
              title={s.path ? `${s.session} (${s.path})` : s.session}
              className="inline-flex max-w-full items-center gap-1.5 rounded-md border border-border bg-background/70 px-2 py-1 text-2xs transition-colors hover:border-primary/50 hover:bg-secondary/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {content}
            </button>
          )
        }
        return (
          <span
            key={s.session}
            title={s.path ? `${s.session} (${s.path})` : s.session}
            className="inline-flex max-w-full items-center gap-1.5 rounded-md border border-border bg-background/70 px-2 py-1 text-2xs"
          >
            {content}
          </span>
        )
      })}
    </div>
  )
}

function RecentSessionsList({
  account,
  nowSec,
  limit = 4,
}: {
  account: MonitorCLIAccount
  nowSec: number
  limit?: number
}) {
  const sessions = (account.recent_sessions ?? []).slice(0, limit)
  if (sessions.length === 0) {
    return <div className="text-2xs text-muted-foreground">Aucune session récente enregistrée sur 7 j.</div>
  }
  return (
    <ul className="divide-y divide-border/50 rounded-md border border-border/60 bg-background/50">
      {sessions.map(s => {
        const proj = shortPath(s.project) || s.title || s.id.slice(0, 8)
        const subtitle = s.title && s.project ? s.title : s.branch ? `branche ${s.branch}` : ''
        return (
          <li key={s.id} className="flex items-center justify-between gap-2 px-2.5 py-1.5 text-2xs">
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-1.5">
                {s.active && (
                  <span
                    className="h-1.5 w-1.5 shrink-0 rounded-full animate-[pulse_2s_ease-in-out_infinite]"
                    style={{ background: toneColor.ok }}
                    title="Actif récemment"
                  />
                )}
                <span className="truncate font-medium text-foreground" title={s.project || s.title || s.id}>
                  {proj}
                </span>
                {subtitle && (
                  <span className="truncate text-muted-foreground" title={subtitle}>
                    · {subtitle}
                  </span>
                )}
              </div>
              <div className="truncate text-muted-foreground">
                {s.model ? `${s.model} · ` : ''}
                {formatNumber(s.requests)} req · {formatRelative(s.last_activity, nowSec * 1000)}
              </div>
            </div>
            <span className="shrink-0 font-semibold tabular-nums text-foreground">{formatTokens(s.tokens)}</span>
          </li>
        )
      })}
    </ul>
  )
}

function AccountDetailCard({
  account,
  nowSec,
  onOpenSessionName,
  inspector = false,
}: {
  account: MonitorCLIAccount
  nowSec: number
  onOpenSessionName?: (sessionName: string) => void
  inspector?: boolean
}) {
  const meta = cliMeta(account.cli)
  const status = accountStatus(account, nowSec)
  const windows = account.windows ?? []
  const displayName = inspector ? account.name || account.person : account.person || account.name
  const showSubName = !inspector && Boolean(account.person && account.name && account.name !== account.person)
  const live = account.live_sessions ?? []
  const recent = account.recent_sessions ?? []

  return (
    <article className="flex min-w-0 flex-col gap-3 atlas-surface rounded-lg p-3.5 font-normal">
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className={`rounded border px-1.5 py-[1px] text-2xs font-medium ${meta.className}`}>
              {inspector ? meta.label : meta.short}
            </span>
            <h3 className="truncate text-sm font-semibold text-foreground" title={account.name}>
              {displayName}
            </h3>
            {account.plan && (
              <span className="rounded border border-border px-1.5 py-[1px] text-2xs text-muted-foreground">
                {account.plan}
              </span>
            )}
          </div>
          <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-2xs text-muted-foreground">
            {inspector && account.person && <span>Titulaire : {account.person}</span>}
            {showSubName && <span className="truncate">{account.name}</span>}
            {account.account && (
              <span className="truncate" title={account.account}>
                {account.account}
              </span>
            )}
            {account.last_model && (
              <span className="truncate text-foreground/80" title={account.last_model}>
                {account.last_model}
              </span>
            )}
          </div>
        </div>
        <StatusBadge tone={status.tone} label={status.label} />
      </div>

      {(inspector || live.length > 0) && (
        <div>
          {inspector && <div className="mb-1 text-2xs font-medium text-muted-foreground">Sessions tmux en cours</div>}
          <LiveSessionsList account={account} onOpenSessionName={onOpenSessionName} />
        </div>
      )}

      {windows.length > 0 ? (
        <div className="flex flex-col gap-2">
          {windows.map(w => (
            <QuotaWindowRow key={w.label} account={account} window={w} nowSec={nowSec} />
          ))}
        </div>
      ) : (
        <div className="rounded-md border border-border/60 bg-background/50 px-2.5 py-2 text-2xs text-muted-foreground">
          {account.quota_source === 'local_logs_only'
            ? 'Quota fournisseur non exposé localement — suivi des tokens locaux ci-dessous.'
            : 'Aucune donnée de quota enregistrée pour ce compte.'}
        </div>
      )}

      <ExtraUsageBlock account={account} />

      <div className="grid grid-cols-3 gap-2">
        <TokenCell label="5 h" counts={account.tokens_5h} />
        <TokenCell label="24 h" counts={account.tokens_24h} />
        <TokenCell label="7 j" counts={account.tokens_7d} />
      </div>

      {(inspector || recent.length > 0) && (
        <div>
          <div className="mb-1 text-2xs font-medium text-muted-foreground">
            {inspector ? 'Dernières activités sur ce compte' : 'Sessions récentes'}
          </div>
          <RecentSessionsList account={account} nowSec={nowSec} limit={inspector ? 4 : 2} />
        </div>
      )}

      <div className="mt-auto flex flex-wrap items-center justify-between gap-x-3 gap-y-1 border-t border-border/50 pt-2 text-2xs text-muted-foreground">
        <span>
          {account.last_activity
            ? `${inspector ? 'Dernier appel' : 'Actif'} ${formatRelative(account.last_activity, nowSec * 1000)}`
            : 'Aucune activité récente'}
        </span>
        <span>
          {account.quota_at
            ? `Quota mesuré ${formatRelative(account.quota_at, nowSec * 1000)}`
            : account.context_window
              ? `Contexte ${formatTokens(account.context_window)}`
              : account.last_turn
                ? `Dernier tour ${formatTokens(account.last_turn.total)}`
                : ''}
        </span>
      </div>
    </article>
  )
}

function CompactAccountRow({
  account,
  selected,
  nowSec,
  onSelect,
}: {
  account: MonitorCLIAccount
  selected: boolean
  nowSec: number
  onSelect: () => void
}) {
  const meta = cliMeta(account.cli)
  const status = accountStatus(account, nowSec)
  const liveCount = (account.live_sessions ?? []).length
  const liveNow = isAccountLive(account, nowSec)
  const windows = account.windows ?? []
  const primary = windows[0]
  const secondary = windows[1]

  const primaryExpired = primary ? isWindowExpired(primary, nowSec) : false
  const primaryActivitySinceReset =
    primary && primaryExpired && Boolean(primary.resets_at && account.last_activity && account.last_activity > primary.resets_at)

  return (
    <button
      type="button"
      onClick={onSelect}
      aria-pressed={selected}
      className={`flex w-full flex-col gap-1.5 rounded-lg border px-3 py-2 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
        selected
          ? 'border-primary/60 bg-primary/10'
          : 'border-border bg-card/80 hover:border-border hover:bg-secondary/40'
      }`}
    >
      <div className="flex items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-1.5">
          <span className={`shrink-0 rounded border px-1.5 py-[1px] text-2xs font-medium ${meta.className}`}>
            {meta.short}
          </span>
          <span className="truncate text-xs font-semibold text-foreground">{account.person || account.name}</span>
          {account.plan && (
            <span className="shrink-0 rounded border border-border px-1 text-2xs text-muted-foreground">
              {account.plan}
            </span>
          )}
          {liveNow && (
            <span
              className="h-1.5 w-1.5 shrink-0 rounded-full animate-[pulse_2s_ease-in-out_infinite]"
              style={{ background: toneColor.ok }}
              title="Activité ou session en cours"
            />
          )}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {liveCount > 0 && (
            <span className="rounded bg-secondary px-1.5 py-[1px] text-2xs tabular-nums text-foreground">
              {liveCount} tmux
            </span>
          )}
          <StatusBadge tone={status.tone} label={status.label} />
        </div>
      </div>

      {primary ? (
        <div className="flex items-center gap-2 text-2xs">
          <span className="min-w-11 shrink-0 text-muted-foreground">{primary.label}</span>
          <div className="min-w-0 flex-1">
            {primaryActivitySinceReset ? (
              <StaleMeter />
            ) : (
              <Meter
                value={primaryExpired ? 0 : primary.used_percent}
                tone={primaryExpired ? 'ok' : pctTone(primary.used_percent, 75, 95)}
              />
            )}
          </div>
          <span className="w-20 shrink-0 text-right tabular-nums text-foreground">
            {primaryExpired ? (primaryActivitySinceReset ? 'À actualiser' : 'Réinitialisé') : formatPercent(primary.used_percent)}
          </span>
          {secondary && (
            <span className="hidden shrink-0 tabular-nums text-muted-foreground sm:inline">
              · {secondary.label}{' '}
              {isWindowExpired(secondary, nowSec)
                ? hasActivitySinceReset(account, secondary, nowSec)
                  ? '—'
                  : '0 %'
                : formatPercent(secondary.used_percent)}
            </span>
          )}
          <span className="shrink-0 tabular-nums text-muted-foreground">
            · {formatTokens(account.tokens_5h.total)}
          </span>
        </div>
      ) : (
        <div className="flex items-center justify-between gap-2 text-2xs text-muted-foreground">
          <span className="truncate">{account.last_model || 'Suivi local'}</span>
          <span className="tabular-nums text-foreground">
            5 h : {formatTokens(account.tokens_5h.total)} · 7 j : {formatTokens(account.tokens_7d.total)}
          </span>
        </div>
      )}
    </button>
  )
}

export function CLIAccountsOverview({
  accounts,
  selectedId,
  onSelectAccount,
  onOpenSessionName,
}: {
  accounts: MonitorCLIAccount[]
  selectedId?: string | null
  onSelectAccount?: (id: string) => void
  onOpenSessionName?: (sessionName: string) => void
}) {
  const [filter, setFilter] = useState<CLIFilter>('all')
  const [internalSelected, setInternalSelected] = useState<string | null>(null)

  const nowSec = Math.floor(Date.now() / 1000)
  const filtered = useMemo(
    () => (filter === 'all' ? accounts : accounts.filter(a => a.cli === filter)),
    [accounts, filter],
  )

  const activeId = selectedId ?? internalSelected
  const selectedAccount = useMemo(() => {
    if (filtered.length === 0) return null
    const explicit = activeId ? filtered.find(a => a.id === activeId) : undefined
    if (explicit) return explicit
    // Default to the first account with a live tmux session or saturated quota, else first account.
    return (
      filtered.find(a => (a.live_sessions ?? []).length > 0) ??
      filtered.find(a => isAccountSaturated(a, nowSec)) ??
      filtered[0]
    )
  }, [filtered, activeId, nowSec])

  if (accounts.length === 0) return null

  const saturated = accounts.filter(a => isAccountSaturated(a, nowSec)).length
  const totalLiveTmux = accounts.reduce((sum, a) => sum + (a.live_sessions?.length ?? 0), 0)
  const total5h = accounts.reduce((sum, a) => sum + a.tokens_5h.total, 0)

  const handleSelect = (id: string) => {
    setInternalSelected(id)
    onSelectAccount?.(id)
  }

  return (
    <section aria-labelledby="cli-overview-title" className="min-w-0">
      <div className="mb-2.5 flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <h2 id="cli-overview-title" className="text-sm font-semibold text-foreground">
            Comptes CLI
          </h2>
          <span className="text-2xs text-muted-foreground tabular-nums">
            {accounts.length} comptes · {totalLiveTmux} session{totalLiveTmux > 1 ? 's' : ''} tmux · {formatTokens(total5h)} tok (5 h)
          </span>
          {saturated > 0 && (
            <StatusBadge tone="warning" label={`${saturated} saturé${saturated > 1 ? 's' : ''}`} />
          )}
        </div>
        <div className="flex items-center gap-2">
          <div className="inline-flex rounded-md bg-secondary p-0.5" role="group" aria-label="Filtre CLI vue d'ensemble">
            {(
              [
                { value: 'all', label: 'Tous' },
                { value: 'claude', label: 'Claude' },
                { value: 'codex', label: 'Codex' },
                { value: 'agy', label: 'Agy' },
              ] as const
            ).map(opt => (
              <button
                key={opt.value}
                type="button"
                aria-pressed={filter === opt.value}
                onClick={() => setFilter(opt.value)}
                className={`min-h-6 rounded px-2 text-2xs font-medium transition-colors ${
                  filter === opt.value
                    ? 'bg-card text-foreground shadow-[var(--elevation-sm)]'
                    : 'text-muted-foreground hover:text-foreground'
                }`}
              >
                {opt.label}
              </button>
            ))}
          </div>
        </div>
      </div>

      <div className="grid gap-3 xl:grid-cols-2 xl:items-start">
        <div className="flex flex-col gap-1.5">
          {filtered.length === 0 ? (
            <div className="rounded-lg border border-border bg-card px-3 py-4 text-center text-2xs text-muted-foreground">
              Aucun compte pour ce CLI.
            </div>
          ) : (
            filtered.map(acc => (
              <CompactAccountRow
                key={acc.id}
                account={acc}
                selected={selectedAccount?.id === acc.id}
                nowSec={nowSec}
                onSelect={() => handleSelect(acc.id)}
              />
            ))
          )}
        </div>

        {selectedAccount && (
          <AccountDetailCard
            account={selectedAccount}
            nowSec={nowSec}
            onOpenSessionName={onOpenSessionName}
            inspector
          />
        )}
      </div>
    </section>
  )
}

export function CLIAccountsPanel({
  accounts,
  filter,
  onOpenSessionName,
}: {
  accounts: MonitorCLIAccount[]
  filter: CLIFilter
  onOpenSessionName?: (sessionName: string) => void
}) {
  const nowSec = Math.floor(Date.now() / 1000)
  const filtered = filter === 'all' ? accounts : accounts.filter(a => a.cli === filter)

  if (accounts.length === 0) {
    return (
      <div className="rounded-lg border border-border bg-card px-4 py-6 text-center text-xs text-muted-foreground">
        Aucun compte CLI détecté dans ai-accounts sur cet hôte.
      </div>
    )
  }

  const sum5h = emptyTokens()
  const sum24h = emptyTokens()
  const sum7d = emptyTokens()
  const byCLI7d = { claude: 0, codex: 0, agy: 0 }
  const countByCLI = { claude: 0, codex: 0, agy: 0 }
  let saturated = 0
  let available = 0

  for (const acc of filtered) {
    addTokens(sum5h, acc.tokens_5h)
    addTokens(sum24h, acc.tokens_24h)
    addTokens(sum7d, acc.tokens_7d)
    if (acc.cli === 'claude' || acc.cli === 'codex' || acc.cli === 'agy') {
      byCLI7d[acc.cli] += acc.tokens_7d.total
      countByCLI[acc.cli]++
    }
    if (isAccountSaturated(acc, nowSec)) saturated++
    else if (acc.connected) available++
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Kpi
          label="Comptes CLI"
          value={formatNumber(filtered.length)}
          tone={saturated > 0 ? 'warning' : 'ok'}
          badge={`${saturated} saturé${saturated > 1 ? 's' : ''}`}
          facts={[
            ['Disponibles', formatNumber(available)],
            ['Saturés (≥ 95 %)', formatNumber(saturated)],
            ['Claude / Codex / Agy', `${countByCLI.claude} / ${countByCLI.codex} / ${countByCLI.agy}`],
          ]}
        />
        <Kpi
          label="Tokens (5 h)"
          value={formatTokens(sum5h.total)}
          facts={[
            ['Nouveaux (in + out)', formatTokens(sum5h.input + sum5h.output + (sum5h.cache_write ?? 0))],
            ['Cache lu', formatTokens(sum5h.cache_read)],
            ['Requêtes', formatNumber(sum5h.requests ?? 0)],
          ]}
        />
        <Kpi
          label="Tokens (24 h)"
          value={formatTokens(sum24h.total)}
          facts={[
            ['Nouveaux (in + out)', formatTokens(sum24h.input + sum24h.output + (sum24h.cache_write ?? 0))],
            ['Cache lu', formatTokens(sum24h.cache_read)],
            ['Requêtes', formatNumber(sum24h.requests ?? 0)],
          ]}
        />
        <Kpi
          label="Tokens (7 j)"
          value={formatTokens(sum7d.total)}
          facts={[
            ['Claude Code', formatTokens(byCLI7d.claude)],
            ['Codex CLI', formatTokens(byCLI7d.codex)],
            ['Antigravity', formatTokens(byCLI7d.agy)],
          ]}
        />
      </div>

      {filtered.length === 0 ? (
        <div className="rounded-lg border border-border bg-card px-4 py-6 text-center text-xs text-muted-foreground">
          Aucun compte pour ce CLI.
        </div>
      ) : (
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          {filtered.map(acc => (
            <AccountDetailCard
              key={acc.id}
              account={acc}
              nowSec={nowSec}
              onOpenSessionName={onOpenSessionName}
            />
          ))}
        </div>
      )}
    </div>
  )
}
