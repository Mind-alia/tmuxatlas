import { useCallback, useEffect, useMemo, useState } from 'react'
import { Terminal, User } from 'lucide-react'
import {
  fetchAdminUsers,
  fetchCLIAssignments,
  setCLIAssignment,
  type AccountUser,
  type CLIPersonAssignment,
} from '../lib/accountsApi'

const CLI_LABELS: Record<string, string> = { claude: 'Claude', codex: 'Codex', agy: 'Antigravity' }

interface CLIAssignmentsSectionProps {
  users?: AccountUser[]
}

/**
 * Admin section that links each ai-accounts CLI person (Aris, Aris2, Novanns…)
 * to a TmuxAtlas account.
 */
export function CLIAssignmentsSection({ users }: CLIAssignmentsSectionProps) {
  const [loadedUsers, setLoadedUsers] = useState<AccountUser[]>([])
  const [persons, setPersons] = useState<CLIPersonAssignment[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [mutationError, setMutationError] = useState<string | null>(null)
  const [pendingPerson, setPendingPerson] = useState<string | null>(null)

  const needsUsersFetch = users === undefined

  const load = useCallback(async () => {
    try {
      const [loadedPersons, fetchedUsers] = await Promise.all([
        fetchCLIAssignments(),
        needsUsersFetch ? fetchAdminUsers() : Promise.resolve<AccountUser[] | null>(null),
      ])
      setPersons(loadedPersons)
      if (fetchedUsers !== null) setLoadedUsers(fetchedUsers)
      setLoadError(null)
    } catch (err) {
      setLoadError(err instanceof Error ? err.message : 'Impossible de charger les sessions CLI.')
    } finally {
      setLoading(false)
    }
  }, [needsUsersFetch])

  useEffect(() => {
    void load()
  }, [load])

  const effectiveUsers = users ?? loadedUsers
  const usersById = useMemo(() => new Map(effectiveUsers.map(user => [user.id, user])), [effectiveUsers])

  const handleChange = async (person: string, owner: string) => {
    setMutationError(null)
    setPendingPerson(person)
    try {
      setPersons(await setCLIAssignment(person, owner))
    } catch (err) {
      setMutationError(err instanceof Error ? err.message : 'Impossible d’assigner la session CLI.')
    } finally {
      setPendingPerson(null)
    }
  }

  return (
    <section id="cli" aria-labelledby="admin-cli-heading" className="atlas-surface rounded-xl p-4 sm:p-5 scroll-mt-4">
      <h2 id="admin-cli-heading" className="text-sm font-medium text-foreground">
        Sessions CLI ({persons.length})
      </h2>
      <p className="mt-1 text-xs text-muted-foreground">
        Associez chaque session CLI (comptes Claude, Codex, Antigravity) à un compte TmuxAtlas pour pouvoir
        filtrer le monitoring par utilisateur.
      </p>
      {mutationError && (
        <p
          role="alert"
          className="mt-3 rounded border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          {mutationError}
        </p>
      )}
      {!mutationError && loadError && (
        <p
          role="status"
          className="mt-3 rounded border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          {loadError}
        </p>
      )}
      {loading ? (
        <p className="mt-4 text-xs text-muted-foreground">Chargement des sessions CLI…</p>
      ) : persons.length === 0 ? (
        <p className="mt-4 text-xs text-muted-foreground">Aucune session CLI trouvée sur cet hôte.</p>
      ) : (
        <div className="mt-4 divide-y divide-border">
          {persons.map(entry => {
            const owner = entry.owner ? usersById.get(entry.owner) : undefined
            const selectId = `cli-assign-${entry.person}`
            return (
              <div
                key={entry.person}
                data-testid={`admin-cli-row-${entry.person}`}
                className="flex flex-col gap-3 py-3 sm:flex-row sm:items-center sm:justify-between"
              >
                <div className="min-w-0 space-y-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <Terminal size={14} aria-hidden="true" className="shrink-0 text-muted-foreground" />
                    <span className="font-medium text-foreground">{entry.person}</span>
                    {owner ? (
                      <span className="inline-flex items-center gap-1 rounded bg-secondary px-1.5 py-0.5 text-2xs text-muted-foreground">
                        <User size={12} aria-hidden="true" />
                        <span>{owner.username}</span>
                      </span>
                    ) : (
                      <span className="rounded bg-muted px-1.5 py-0.5 text-2xs text-muted-foreground">
                        Non assignée
                      </span>
                    )}
                    {entry.missing && (
                      <span className="rounded bg-destructive/15 px-1.5 py-0.5 text-2xs text-destructive">
                        Introuvable sur cet hôte
                      </span>
                    )}
                  </div>
                  {entry.profiles.length > 0 && (
                    <div className="flex flex-wrap gap-1.5">
                      {entry.profiles.map(profile => (
                        <span
                          key={`${profile.cli}/${profile.name}`}
                          title={profile.account || profile.name}
                          className={`rounded px-1.5 py-0.5 text-2xs ${
                            profile.connected ? 'bg-success/15 text-success' : 'bg-muted text-muted-foreground'
                          }`}
                        >
                          {CLI_LABELS[profile.cli] ?? profile.cli}
                          {profile.connected ? '' : ' · déconnecté'}
                        </span>
                      ))}
                    </div>
                  )}
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <label htmlFor={selectId} className="sr-only">
                    Assigner la session CLI {entry.person} à un compte
                  </label>
                  <select
                    id={selectId}
                    aria-label={`Assigner la session CLI ${entry.person} à un compte`}
                    value={entry.owner && usersById.has(entry.owner) ? entry.owner : ''}
                    disabled={pendingPerson === entry.person}
                    onChange={event => void handleChange(entry.person, event.target.value)}
                    className="ui-focus-ring min-h-11 rounded border border-border bg-input px-2.5 text-xs text-foreground"
                  >
                    <option value="">Non assignée</option>
                    {effectiveUsers.map(user => (
                      <option key={user.id} value={user.id}>
                        {user.username}
                        {user.disabled ? ' (désactivé)' : ''}
                      </option>
                    ))}
                  </select>
                </div>
              </div>
            )
          })}
        </div>
      )}
    </section>
  )
}
