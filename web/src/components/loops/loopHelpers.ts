import type { LoopCLI, LoopStatus, LoopWorker, LoopsMeta, WorkerInput } from './loopsApi'

export const cliOrder: readonly LoopCLI[] = ['claude', 'codex', 'agy']

export const cliMeta: Record<LoopCLI, { title: string; subtitle: string; badge: string; accent: string }> = {
  claude: {
    title: 'Claude Code',
    subtitle: 'Anthropic CLI',
    badge: 'border-primary/35 bg-primary/10 text-primary',
    accent: 'border-primary/40',
  },
  codex: {
    title: 'Codex CLI',
    subtitle: 'OpenAI CLI',
    badge: 'border-success/35 bg-success/10 text-success',
    accent: 'border-success/40',
  },
  agy: {
    title: 'AGY',
    subtitle: 'Antigravity CLI',
    badge: 'border-warning/35 bg-warning/10 text-warning',
    accent: 'border-warning/40',
  },
}

export const statusMeta: Record<LoopStatus, { label: string; className: string }> = {
  idle: { label: 'Prête', className: 'border-border bg-secondary text-muted-foreground' },
  running: { label: 'En cours', className: 'border-primary/40 bg-primary/15 text-primary' },
  waiting_reset: { label: 'Attente reset 5h', className: 'border-warning/45 bg-warning/15 text-warning' },
  paused: { label: 'En pause', className: 'border-warning/40 bg-warning/15 text-warning' },
  completed: { label: 'Terminée', className: 'border-success/40 bg-success/15 text-success' },
  stopped: { label: 'Arrêtée', className: 'border-border bg-secondary text-muted-foreground' },
  error: { label: 'Erreur', className: 'border-destructive/40 bg-destructive/15 text-destructive' },
}

export function makeWorkerId(cli: LoopCLI, person?: string): string {
  const clean = person?.trim()
  return clean ? `${cli}:${clean}` : cli
}

/**
 * Returns all available subscriptions across Claude, Codex and AGY.
 * Falls back to deriving from meta.accounts + meta.clis when meta.subscriptions is omitted.
 */
export function deriveAvailableSubscriptions(meta: LoopsMeta | null): LoopWorker[] {
  if (!meta) return []
  if (meta.subscriptions && meta.subscriptions.length > 0) {
    return meta.subscriptions
  }
  const availMap = new Map(meta.clis.map(c => [c.cli, c.available]))
  const out: LoopWorker[] = []
  for (const cli of cliOrder) {
    const profiles = meta.accounts[cli] ?? []
    if (profiles.length > 0) {
      const seen = new Set<string>()
      for (const prof of profiles) {
        const person = prof.person?.trim() || ''
        const id = makeWorkerId(cli, person)
        if (seen.has(id.toLowerCase())) continue
        seen.add(id.toLowerCase())
        out.push({
          id,
          cli,
          person,
          label: prof.name || person || cliMeta[cli].title,
          enabled: true,
          paused: false,
          status: 'ready',
          run_count: 0,
        })
      }
    } else if (availMap.get(cli) ?? true) {
      out.push({
        id: cli,
        cli,
        label: cliMeta[cli].title,
        enabled: true,
        paused: false,
        status: 'ready',
        run_count: 0,
      })
    }
  }
  return out
}

export function toWorkerInputs(workers: LoopWorker[]): WorkerInput[] {
  return workers.map(w => ({
    cli: w.cli,
    ...(w.person ? { person: w.person } : {}),
    ...(w.label ? { label: w.label } : {}),
    ...(w.model ? { model: w.model } : {}),
    enabled: w.enabled,
    paused: w.paused,
  }))
}

export function formatDuration(ms: number): string {
  if (!ms || ms < 1000) return `${ms || 0} ms`
  const sec = Math.round(ms / 1000)
  if (sec < 60) return `${sec} s`
  const min = Math.floor(sec / 60)
  const rem = sec % 60
  if (min < 60) return `${min} min ${rem} s`
  const h = Math.floor(min / 60)
  const remMin = min % 60
  return `${h} h ${remMin} min`
}

export function formatCountdown(iso?: string, nowMs = Date.now()): string {
  if (!iso) return ''
  const target = Date.parse(iso)
  if (Number.isNaN(target)) return ''
  const diffSec = Math.max(0, Math.round((target - nowMs) / 1000))
  if (diffSec === 0) return 'imminent'
  if (diffSec < 60) return `${diffSec} s`
  const mins = Math.floor(diffSec / 60)
  const secs = diffSec % 60
  if (mins < 60) return secs > 0 ? `${mins} min ${secs} s` : `${mins} min`
  const hours = Math.floor(mins / 60)
  const remMins = mins % 60
  return remMins > 0 ? `${hours} h ${remMins} min` : `${hours} h`
}

export function formatClockTime(iso?: string): string {
  if (!iso) return ''
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return ''
  return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

export function formatRelative(iso?: string): string {
  if (!iso) return ''
  const parsed = Date.parse(iso)
  if (Number.isNaN(parsed)) return ''
  const diffSec = Math.round((parsed - Date.now()) / 1000)
  const fmt = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' })
  if (Math.abs(diffSec) < 60) return fmt.format(diffSec, 'second')
  if (Math.abs(diffSec) < 3600) return fmt.format(Math.round(diffSec / 60), 'minute')
  if (Math.abs(diffSec) < 86400) return fmt.format(Math.round(diffSec / 3600), 'hour')
  return fmt.format(Math.round(diffSec / 86400), 'day')
}
