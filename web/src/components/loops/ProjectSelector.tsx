import { useMemo, useState } from 'react'
import { Check, FolderGit2, GitBranch, Search } from 'lucide-react'
import type { LoopItem, ProjectEntry } from './loopsApi'

interface ProjectSelectorProps {
  projects: ProjectEntry[]
  value: string
  onChange: (path: string) => void
  loops?: LoopItem[]
  inputId?: string
  compact?: boolean
}

export function ProjectSelector({
  projects,
  value,
  onChange,
  loops = [],
  inputId = 'loops-project-search',
  compact = false,
}: ProjectSelectorProps) {
  const [query, setQuery] = useState('')
  const [customMode, setCustomMode] = useState(false)
  const [pickerOpen, setPickerOpen] = useState(false)

  const loopCountsByProject = useMemo(() => {
    const map = new Map<string, { total: number; active: number }>()
    for (const loop of loops) {
      const prev = map.get(loop.project_dir) || { total: 0, active: 0 }
      prev.total++
      if (loop.status === 'running' || loop.status === 'waiting_reset') prev.active++
      map.set(loop.project_dir, prev)
    }
    return map
  }, [loops])

  const selectedProject = useMemo(() => projects.find(p => p.path === value), [projects, value])

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return projects
    return projects.filter(
      p =>
        p.name.toLowerCase().includes(q) ||
        p.path.toLowerCase().includes(q) ||
        (p.branch && p.branch.toLowerCase().includes(q)),
    )
  }, [projects, query])

  if (customMode || projects.length === 0) {
    return (
      <div className="flex flex-col gap-1.5">
        <div className="flex items-center gap-2">
          <input
            id={inputId}
            type="text"
            value={value}
            onChange={e => onChange(e.target.value)}
            placeholder="/root/Project/mon-projet"
            className="min-w-0 flex-1 rounded-md border border-border bg-background px-3 py-2 font-mono text-xs text-foreground focus:border-primary focus:outline-none"
          />
          {projects.length > 0 && (
            <button
              type="button"
              onClick={() => setCustomMode(false)}
              className="shrink-0 rounded-md border border-border bg-secondary/70 px-2.5 py-2 text-xs text-foreground hover:bg-secondary"
            >
              Liste des projets
            </button>
          )}
        </div>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-2">
      {/* Active selection bar */}
      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          onClick={() => setPickerOpen(v => !v)}
          aria-expanded={pickerOpen}
          className="flex min-w-0 flex-1 items-center justify-between gap-2 rounded-lg border border-border bg-background/80 px-3 py-2 text-left transition-colors hover:border-primary/50"
        >
          <div className="flex min-w-0 items-center gap-2.5">
            <FolderGit2 size={16} className="shrink-0 text-primary" />
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <span className="truncate text-xs font-semibold text-foreground">
                  {selectedProject ? selectedProject.name : value || 'Choisir un projet'}
                </span>
                {selectedProject?.branch && (
                  <span className="inline-flex items-center gap-1 rounded border border-primary/30 bg-primary/10 px-1.5 py-0.5 text-2xs font-medium text-primary">
                    <GitBranch size={10} />
                    {selectedProject.branch}
                  </span>
                )}
              </div>
              {value && <div className="truncate font-mono text-2xs text-muted-foreground">{value}</div>}
            </div>
          </div>
          <span className="shrink-0 rounded border border-border bg-card px-2 py-0.5 text-2xs text-muted-foreground">
            {pickerOpen ? 'Fermer' : `Changer (${projects.length})`}
          </span>
        </button>

        <button
          type="button"
          onClick={() => setCustomMode(true)}
          className="shrink-0 rounded-lg border border-border bg-background/60 px-2.5 py-2 text-xs text-muted-foreground hover:bg-secondary hover:text-foreground"
        >
          Chemin libre
        </button>
      </div>

      {/* Expandable searchable project cards */}
      {(pickerOpen || !compact) && (
        <div className="rounded-lg border border-border bg-background/60 p-2.5">
          <div className="relative mb-2">
            <Search size={13} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground" />
            <input
              id={inputId}
              type="text"
              value={query}
              onChange={e => setQuery(e.target.value)}
              placeholder="Filtrer un projet par nom, branche ou chemin…"
              className="w-full rounded-md border border-border bg-card py-1.5 pl-8 pr-3 text-xs text-foreground placeholder:text-muted-foreground focus:border-primary focus:outline-none"
            />
          </div>

          <div className={`grid gap-1.5 overflow-y-auto ${compact ? 'max-h-56 grid-cols-1 sm:grid-cols-2 lg:grid-cols-3' : 'max-h-52 grid-cols-1 sm:grid-cols-2'}`}>
            {filtered.length === 0 ? (
              <div className="col-span-full py-3 text-center text-xs text-muted-foreground">
                Aucun projet ne correspond à « {query} ».
              </div>
            ) : (
              filtered.map(project => {
                const selected = project.path === value
                const counts = loopCountsByProject.get(project.path)
                return (
                  <button
                    key={project.path}
                    type="button"
                    onClick={() => {
                      onChange(project.path)
                      setPickerOpen(false)
                    }}
                    className={`flex items-start justify-between gap-2 rounded-md border px-2.5 py-2 text-left transition-colors ${
                      selected
                        ? 'border-primary/60 bg-primary/10 text-foreground'
                        : 'border-border/80 bg-card/80 text-foreground hover:border-primary/40 hover:bg-secondary/50'
                    }`}
                  >
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-1.5">
                        <span className="truncate text-xs font-medium">{project.name}</span>
                        {project.branch && (
                          <span className="inline-flex shrink-0 items-center gap-0.5 rounded bg-secondary px-1 py-0.5 text-2xs text-muted-foreground">
                            <GitBranch size={9} />
                            {project.branch}
                          </span>
                        )}
                      </div>
                      <div className="mt-0.5 truncate font-mono text-2xs text-muted-foreground">{project.path}</div>
                    </div>
                    <div className="flex shrink-0 flex-col items-end gap-1">
                      {selected && <Check size={13} className="text-primary" />}
                      {counts && counts.active > 0 && (
                        <span className="rounded-full bg-primary/15 px-1.5 py-0.5 text-2xs font-medium text-primary">
                          {counts.active} active
                        </span>
                      )}
                    </div>
                  </button>
                )
              })
            )}
          </div>
        </div>
      )}
    </div>
  )
}
