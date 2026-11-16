import { useEffect, useState } from 'react'
import { Button } from '../ui'
import { saveLoopPlan, type LoopItem, type PlanStep } from './loopsApi'

export function LoopPlan({ loop, onSaved, onError }: { loop: LoopItem; onSaved: (loop: LoopItem) => void; onError: (message: string) => void }) {
  const [plan, setPlan] = useState<PlanStep[]>(loop.plan || [])
  const [busy, setBusy] = useState(false)
  const savedPlan = JSON.stringify(loop.plan || [])
  useEffect(() => { setPlan(JSON.parse(savedPlan) as PlanStep[]) }, [loop.id, savedPlan])
  const active = loop.status === 'running' || loop.status === 'waiting_reset'
  return <section className="space-y-2 rounded border border-border p-3">
    <h4 className="font-medium">Plan · {plan.filter(s => s.state === 'validated').length}/{plan.length} étapes</h4>
    {active && <p className="text-xs text-muted-foreground">Mettez la boucle en pause pour modifier le plan. Les étapes cochées par l’agent sont des déclarations à examiner.</p>}
    {plan.map((step, i) => <div key={step.id} className="flex gap-2">
      <input type="checkbox" aria-label={`Terminer l’étape ${i + 1}`} disabled={active || busy} checked={step.state === 'validated'} onChange={e => setPlan(plan.map(s => s.id === step.id ? { ...s, state: e.target.checked ? 'validated' : 'todo', done: false } : s))} />
      <input aria-label={`Étape ${i + 1}`} className="min-w-0 flex-1 rounded border border-border bg-background p-2" disabled={active || busy} value={step.title} onChange={e => setPlan(plan.map(s => s.id === step.id ? { ...s, title: e.target.value } : s))} />
      <span className="text-xs">{step.state || (step.done ? 'review' : 'todo')}</span>
      {!active && <button type="button" disabled={busy} aria-label={`Supprimer l’étape ${i + 1}`} onClick={() => setPlan(plan.filter(s => s.id !== step.id))}>Supprimer</button>}
    </div>)}
    {!active && <div className="flex gap-2"><Button size="sm" variant="secondary" disabled={busy || plan.length >= 100} onClick={() => setPlan([...plan, { id: crypto.randomUUID(), title: '', done: false }])}>Ajouter une étape</Button>
      <Button size="sm" variant="primary" disabled={busy || plan.some(s => !s.title.trim())} onClick={() => { setBusy(true); void saveLoopPlan(loop.id, plan).then(onSaved).catch(e => onError(String(e))).finally(() => setBusy(false)) }}>Enregistrer le plan</Button></div>}
  </section>
}
