import { RefreshCw } from 'lucide-react'
import { useMemo, useState } from 'react'
import { formatClock, formatMoney, formatNumber, formatPercent, formatRelative, formatTokens } from './format'
import { Kpi, Meter, StatusBadge, pctTone, toneColor, type Tone } from './primitives'
import type { MonitorCLIAccount, MonitorCLIUser, MonitorQuotaWindow, MonitorTokenCounts } from './types'

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

function isShortQuotaWindow(w: MonitorQuotaWindow): boolean {
  return w.label.includes('5 h') || ((w.window_minutes ?? 0) > 0 && (w.window_minutes ?? 0) <= 360)
}

function isLongQuotaWindow(w: MonitorQuotaWindow): boolean {
  return w.label.includes('7 j') || (w.window_minutes ?? 0) >= 1440
}

type CompactWindowSlot =
  | { kind: 'window'; window: MonitorQuotaWindow }
  | { kind: 'fallback'; label: '5 h' | '7 j'; tokens: number }

function resolveCompactWindowSlots(account: MonitorCLIAccount): CompactWindowSlot[] {
  const windows = account.windows ?? []
  if (windows.length === 0) {
    return [
      { kind: 'fallback', label: '5 h', tokens: account.tokens_5h.total },
      { kind: 'fallback', label: '7 j', tokens: account.tokens_7d.total },
    ]
  }

  if (account.cli === 'agy') {
    const visible = windows.filter(w => !w.label.startsWith('Argon') || w.used_percent > 0)
    if (visible.length === 0) {
      return [
        { kind: 'fallback', label: '5 h', tokens: account.tokens_5h.total },
        { kind: 'fallback', label: '7 j', tokens: account.tokens_7d.total },
      ]
    }
    const has5h = visible.some(isShortQuotaWindow)
    const has7d = visible.some(isLongQuotaWindow)
    const windowSlots: CompactWindowSlot[] = visible.map(window => ({ kind: 'window', window }))
    if (has5h && has7d) return windowSlots
    if (has5h && !has7d) {
      return [...windowSlots, { kind: 'fallback', label: '7 j', tokens: account.tokens_7d.total }]
    }
    if (!has5h && has7d) {
      return [{ kind: 'fallback', label: '5 h', tokens: account.tokens_5h.total }, ...windowSlots]
    }
    return windowSlots
  }

  const shortWin = windows.find(isShortQuotaWindow)
  const longWin = windows.find(isLongQuotaWindow)
  if (!shortWin && !longWin) {
    return windows.map(window => ({ kind: 'window', window }))
  }
  return [
    shortWin
      ? { kind: 'window', window: shortWin }
      : { kind: 'fallback', label: '5 h', tokens: account.tokens_5h.total },
    longWin
      ? { kind: 'window', window: longWin }
      : { kind: 'fallback', label: '7 j', tokens: account.tokens_7d.total },
  ]
}

function CompactQuotaSlot({
  account,
  slot,
  nowSec,
}: {
  account: MonitorCLIAccount
  slot: CompactWindowSlot
  nowSec: number
}) {
  if (slot.kind === 'fallback') {
    return (
      <div className="flex min-w-0 flex-col gap-1">
        <div className="flex items-baseline justify-between gap-1.5 text-2xs">
          <span className="font-medium text-foreground">{slot.label}</span>
          <span className="shrink-0 tabular-nums text-muted-foreground">{formatTokens(slot.tokens)} tok</span>
        </div>
        <div className="h-1 w-full rounded-full bg-secondary opacity-60" role="presentation" />
      </div>
    )
  }

  const w = slot.window
  const expired = isWindowExpired(w, nowSec)
  const activitySinceReset = hasActivitySinceReset(account, w, nowSec)

  return (
    <div className="flex min-w-0 flex-col gap-1">
      <div className="flex items-baseline justify-between gap-1.5 text-2xs">
        <span className="flex min-w-0 items-baseline gap-1 truncate">
          <span className="font-medium text-foreground">{w.label}</span>
          {!expired && Boolean(w.resets_at) && (
            <span className="truncate text-muted-foreground">· {formatRelative(w.resets_at, nowSec * 1000)}</span>
          )}
        </span>
        <span
          className={`shrink-0 tabular-nums ${
            expired ? 'font-medium text-muted-foreground' : 'font-semibold text-foreground'
          }`}
        >
          {expired ? (activitySinceReset ? 'À actualiser' : 'Réinitialisé') : formatPercent(w.used_percent)}
        </span>
      </div>
      {activitySinceReset ? (
        <StaleMeter />
      ) : (
        <Meter
          value={expired ? 0 : w.used_percent}
          tone={expired ? 'ok' : pctTone(w.used_percent, 75, 95)}
        />
      )}
    </div>
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
  const slots = resolveCompactWindowSlots(account)

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
          {account.owner_name && (
            <span
              title={`Utilisateur Atlas : ${account.owner_name}`}
              className="shrink-0 rounded bg-secondary px-1.5 py-[1px] text-2xs text-muted-foreground"
            >
              {account.owner_name}
            </span>
          )}
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

      <div className="grid grid-cols-2 gap-x-3 gap-y-1.5">
        {slots.map((slot, idx) => (
          <CompactQuotaSlot
            key={slot.kind === 'window' ? slot.window.label : `fallback-${slot.label}-${idx}`}
            account={account}
            slot={slot}
            nowSec={nowSec}
          />
        ))}
      </div>

      <div className="flex items-center justify-between gap-2 text-2xs text-muted-foreground">
        <span className="truncate">
          {account.last_model || (account.quota_source === 'local_logs_only' ? 'Suivi local' : '')}
        </span>
        <span className="shrink-0 tabular-nums">
          5 h : {formatTokens(account.tokens_5h.total)} · 7 j : {formatTokens(account.tokens_7d.total)}
        </span>
      </div>
    </button>
  )
}

const OWNER_ALL = '__all__'
const OWNER_NONE = '__none__'

/** Lists the TmuxAtlas owners present in users/accounts, plus "Non assignés" when relevant. */
export function cliOwnerOptions(
  accounts: MonitorCLIAccount[],
  users?: MonitorCLIUser[],
): { value: string; label: string }[] {
  const owners = new Map<string, string>()
  for (const user of users ?? []) {
    if (user.id && user.username) owners.set(user.id, user.username)
  }
  let unassigned = false
  for (const acc of accounts) {
    if (acc.owner) owners.set(acc.owner, acc.owner_name || acc.owner)
    else unassigned = true
  }
  const options = [...owners.entries()]
    .map(([value, label]) => ({ value, label }))
    .sort((a, b) => a.label.localeCompare(b.label))
  if (unassigned && options.length > 0) options.push({ value: OWNER_NONE, label: 'Non assignés' })
  return options
}

export function filterAccountsByOwner(accounts: MonitorCLIAccount[], owner: string): MonitorCLIAccount[] {
  if (owner === OWNER_ALL) return accounts
  if (owner === OWNER_NONE) return accounts.filter(a => !a.owner)
  return accounts.filter(a => a.owner === owner)
}

export function CLIAccountsOverview({
  accounts,
  users,
  selectedId,
  onSelectAccount,
  onOpenSessionName,
  onRefresh,
  refreshing = false,
}: {
  accounts: MonitorCLIAccount[]
  users?: MonitorCLIUser[]
  selectedId?: string | null
  onSelectAccount?: (id: string) => void
  onOpenSessionName?: (sessionName: string) => void
  onRefresh?: () => void
  refreshing?: boolean
}) {
  const [filter, setFilter] = useState<CLIFilter>('all')
  const [ownerFilter, setOwnerFilter] = useState<string>(OWNER_ALL)
  const [internalSelected, setInternalSelected] = useState<string | null>(null)

  const nowSec = Math.floor(Date.now() / 1000)
  const ownerOptions = useMemo(() => cliOwnerOptions(accounts, users), [accounts, users])
  const hasOwners = ownerOptions.some(opt => opt.value !== OWNER_NONE)
  // A filter on a user who no longer owns anything falls back to everyone.
  const effectiveOwner = ownerOptions.some(opt => opt.value === ownerFilter) ? ownerFilter : OWNER_ALL
  const ownerAccounts = useMemo(
    () => filterAccountsByOwner(accounts, effectiveOwner),
    [accounts, effectiveOwner],
  )
  const filtered = useMemo(
    () => (filter === 'all' ? ownerAccounts : ownerAccounts.filter(a => a.cli === filter)),
    [ownerAccounts, filter],
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

  const saturated = ownerAccounts.filter(a => isAccountSaturated(a, nowSec)).length
  const totalLiveTmux = ownerAccounts.reduce((sum, a) => sum + (a.live_sessions?.length ?? 0), 0)
  const total5h = ownerAccounts.reduce((sum, a) => sum + a.tokens_5h.total, 0)
  const total7d = ownerAccounts.reduce((sum, a) => sum + a.tokens_7d.total, 0)

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
            {ownerAccounts.length} comptes · {totalLiveTmux} session{totalLiveTmux > 1 ? 's' : ''} tmux ·{' '}
            {formatTokens(total5h)} tok (5 h) · {formatTokens(total7d)} tok (7 j)
          </span>
          {saturated > 0 && (
            <StatusBadge tone="warning" label={`${saturated} saturé${saturated > 1 ? 's' : ''}`} />
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {hasOwners && (
            <select
              aria-label="Filtrer par utilisateur TmuxAtlas"
              value={effectiveOwner}
              onChange={event => setOwnerFilter(event.target.value)}
              className="min-h-7 rounded-md border border-border bg-card px-2 text-2xs font-medium text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <option value={OWNER_ALL}>Tout le monde</option>
              {ownerOptions.map(opt => (
                <option key={opt.value} value={opt.value}>
                  {opt.label}
                </option>
              ))}
            </select>
          )}
          <button
            type="button"
            onClick={() => onRefresh?.()}
            disabled={refreshing}
            className="inline-flex min-h-7 items-center gap-1.5 rounded-md border border-border bg-card px-2.5 text-2xs font-medium text-foreground transition-colors hover:bg-secondary/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
          >
            <RefreshCw size={12} aria-hidden="true" className={refreshing ? 'animate-spin' : ''} />
            {refreshing ? 'Actualisation…' : 'Actualiser'}
          </button>
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

      <div className="grid gap-3 lg:grid-cols-12 lg:items-start">
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:col-span-7 xl:col-span-8">
          {filtered.length === 0 ? (
            <div className="col-span-full rounded-lg border border-border bg-card px-3 py-4 text-center text-2xs text-muted-foreground">
              {effectiveOwner === OWNER_ALL ? 'Aucun compte pour ce CLI.' : 'Aucun compte pour ce filtre.'}
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
          <div className="min-w-0 lg:col-span-5 xl:col-span-4">
            <AccountDetailCard
              account={selectedAccount}
              nowSec={nowSec}
              onOpenSessionName={onOpenSessionName}
              inspector
            />
          </div>
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
