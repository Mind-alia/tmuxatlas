const LAST_RECOVERY_KEY = 'atlas.module-recovery-at'
const RECOVERY_COOLDOWN_MS = 60_000

// A failed lazy import stays rejected until navigation, even after the server
// recovers. Reload once, then let the error boundary offer a manual retry.
export function recoverModuleLoad(event: Event) {
  try {
    const now = Date.now()
    const previous = Number(sessionStorage.getItem(LAST_RECOVERY_KEY))
    if (previous > 0 && now - previous < RECOVERY_COOLDOWN_MS) return
    sessionStorage.setItem(LAST_RECOVERY_KEY, String(now))
  } catch {
    // Without storage we cannot guarantee that reloading will not loop.
    return
  }
  event.preventDefault()
  window.location.reload()
}
