import { useEffect, useState, type FormEvent } from 'react'
import { acceptInvitation, inspectInvitation } from '../lib/accountsApi'
import { Button } from './ui'

/** Reads the invitation token from the URL fragment, which browsers never send to the server. */
export function invitationTokenFromHash(hash: string): string {
  const token = hash.replace(/^#/, '').trim()
  return /^[A-Za-z0-9_-]{20,128}$/.test(token) ? token : ''
}

interface InvitePageProps {
  hash?: string
  onDone?: () => void
}

/** Lets an invited person choose their password from a one-time link. */
export function InvitePage({ hash = window.location.hash, onDone }: InvitePageProps) {
  const [token] = useState(() => invitationTokenFromHash(hash))
  const [username, setUsername] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(token ? null : 'Ce lien d’invitation est incomplet.')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [saving, setSaving] = useState(false)
  const [done, setDone] = useState(false)

  useEffect(() => {
    // Drop the token from the address bar and history as soon as it is read.
    try {
      window.history.replaceState(null, '', '/invite')
    } catch {
      /* ignore */
    }
    if (!token) return
    let cancelled = false
    inspectInvitation(token)
      .then(info => {
        if (!cancelled) setUsername(info.username)
      })
      .catch(err => {
        if (!cancelled) setError(err instanceof Error ? err.message : 'Lien invalide.')
      })
    return () => {
      cancelled = true
    }
  }, [token])

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    setError(null)
    if (password.length < 10 || password.length > 200) {
      setError('Le mot de passe doit contenir entre 10 et 200 caractères.')
      return
    }
    if (password !== confirm) {
      setError('Les mots de passe ne correspondent pas.')
      return
    }
    setSaving(true)
    try {
      await acceptInvitation(token, password)
      setDone(true)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Impossible d’enregistrer le mot de passe.')
    } finally {
      setSaving(false)
    }
  }

  const input =
    'ui-focus-ring min-h-11 w-full rounded bg-input px-3 text-sm text-foreground placeholder:text-muted-foreground'
  return (
    <main className="app-shell flex items-center justify-center overflow-y-auto bg-background p-4 text-sm text-foreground">
      <section className="atlas-surface w-full max-w-sm rounded-xl p-5 sm:p-6">
        <h1 className="text-lg tracking-tight">Invitation Atlas</h1>
        {done ? (
          <div className="mt-3 space-y-3">
            <p role="status" className="text-xs text-success">
              Mot de passe enregistré. Vous pouvez vous connecter en tant que {username}.
            </p>
            <Button type="button" variant="primary" onClick={() => (onDone ? onDone() : window.location.replace('/'))}>
              Se connecter
            </Button>
          </div>
        ) : (
          <form onSubmit={submit} className="mt-3 space-y-3">
            {username && (
              <p className="text-xs text-muted-foreground">
                Choisissez le mot de passe du compte <strong className="text-foreground">{username}</strong>. Ce lien ne
                sert qu’une fois.
              </p>
            )}
            <label className="block space-y-1 text-xs text-muted-foreground">
              <span>Mot de passe</span>
              <input
                type="password"
                autoComplete="new-password"
                value={password}
                disabled={!username}
                onChange={event => setPassword(event.target.value)}
                placeholder="10 à 200 caractères"
                className={input}
              />
            </label>
            <label className="block space-y-1 text-xs text-muted-foreground">
              <span>Confirmer le mot de passe</span>
              <input
                type="password"
                autoComplete="new-password"
                value={confirm}
                disabled={!username}
                onChange={event => setConfirm(event.target.value)}
                className={input}
              />
            </label>
            {error && (
              <p role="alert" className="text-xs text-destructive">
                {error}
              </p>
            )}
            <Button type="submit" variant="primary" disabled={!username} loading={saving}>
              Enregistrer le mot de passe
            </Button>
          </form>
        )}
      </section>
    </main>
  )
}
