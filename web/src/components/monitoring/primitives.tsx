import { Check, Circle, TriangleAlert, X } from 'lucide-react'
import type { ReactNode } from 'react'

// Two-series chart palette. Each preset provides its own pair, validated for CVD
// separation and contrast on its card surface (see --mon-a / --mon-b in theme.ts).
export const A = 'var(--mon-a)'
export const B = 'var(--mon-b)'

export type Tone = 'ok' | 'warning' | 'critical' | 'neutral'

export const toneColor: Record<Tone, string> = {
  ok: 'var(--success)',
  warning: 'var(--warning)',
  critical: 'var(--destructive)',
  neutral: 'var(--muted-foreground)',
}

const toneIcon = { ok: Check, warning: TriangleAlert, critical: X, neutral: Circle }

export function StatusBadge({ tone, label }: { tone: Tone; label: string }) {
  const Icon = toneIcon[tone]
  // Healthy is the expected state: it stays quiet so problems stand out.
  if (tone === 'ok') {
    return (
      <span className="inline-flex items-center gap-1.5 whitespace-nowrap text-2xs text-muted-foreground">
        <Icon size={14} aria-hidden="true" />
        {label}
      </span>
    )
  }
  return (
    <span
      className="inline-flex items-center gap-1 whitespace-nowrap rounded px-1.5 py-[1px] text-2xs font-medium"
      style={{ color: toneColor[tone], background: `color-mix(in oklch, ${toneColor[tone]} 12%, transparent)` }}
    >
      <Icon size={14} aria-hidden="true" />
      {label}
    </span>
  )
}

export function pctTone(value: number, warn: number, crit: number): Tone {
  return value >= crit ? 'critical' : value >= warn ? 'warning' : 'ok'
}

export function Meter({ value, tone }: { value: number; tone: Tone }) {
  return (
    <div className="h-1 w-full overflow-hidden rounded-full bg-secondary" role="presentation">
      <div
        className="h-full rounded-full transition-[width] duration-300"
        style={{
          width: `${Math.min(100, Math.max(0, value))}%`,
          background: tone === 'warning' || tone === 'critical' ? toneColor[tone] : A,
        }}
      />
    </div>
  )
}

export function Kpi({
  label,
  value,
  tone = 'neutral',
  badge,
  meter,
  facts,
  children,
}: {
  label: string
  value: string
  tone?: Tone
  badge?: string
  meter?: number
  facts?: [string, string][]
  children?: ReactNode
}) {
  const alerting = tone === 'warning' || tone === 'critical'
  return (
    <div className="flex min-w-0 flex-col gap-2 atlas-surface rounded-lg p-3 font-normal">
      <div className="flex min-h-5 items-center justify-between gap-2 text-xs text-muted-foreground">
        <span className="truncate">{label}</span>
        {alerting && <StatusBadge tone={tone} label={badge ?? (tone === 'critical' ? 'Critique' : 'Élevé')} />}
      </div>
      <div className="text-2xl font-semibold leading-none text-foreground">{value}</div>
      {facts && (meter !== undefined ? <Meter value={meter} tone={tone} /> : <div aria-hidden className="h-1" />)}
      {facts && (
        <dl className="flex flex-col gap-1 text-2xs">
          {facts.map(([name, fact]) => (
            <div key={name} className="flex items-baseline justify-between gap-2">
              <dt className="truncate text-muted-foreground">{name}</dt>
              <dd className="whitespace-nowrap tabular-nums text-foreground">{fact}</dd>
            </div>
          ))}
        </dl>
      )}
      {children && <div className="text-2xs leading-relaxed text-muted-foreground">{children}</div>}
    </div>
  )
}
