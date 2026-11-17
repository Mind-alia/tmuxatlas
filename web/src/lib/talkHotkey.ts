import { useEffect, useRef, useState, useSyncExternalStore } from 'react'

/**
 * The keyboard key for Talk. Held alone for TALK_HOTKEY_HOLD_MS, it starts the
 * dictation of the terminal on screen; pressed again, it stops the dictation and
 * leaves the text in the prompt. The choice belongs to this browser: a phone has
 * no keyboard, and each computer has its own.
 */
export type TalkHotkey = 'AltRight' | 'MetaRight' | 'ShiftRight' | 'ControlLeft' | 'F5' | 'off'

export const TALK_HOTKEY_HOLD_MS = 3000
export const DEFAULT_TALK_HOTKEY: TalkHotkey = 'AltRight'
const STORAGE_KEY = 'tmuxatlas.talk-hotkey'

export const TALK_HOTKEYS: ReadonlyArray<{ value: TalkHotkey; label: string; short: string }> = [
  { value: 'AltRight', label: '⌥ Option droite (Alt droite)', short: '⌥ droite' },
  { value: 'MetaRight', label: '⌘ Commande droite', short: '⌘ droite' },
  { value: 'ShiftRight', label: '⇧ Maj droite', short: '⇧ droite' },
  { value: 'ControlLeft', label: '⌃ Contrôle gauche', short: '⌃ gauche' },
  { value: 'F5', label: 'F5 (fn + F5 sur un Mac)', short: 'F5' },
  { value: 'off', label: 'Désactivée', short: '' },
]

export function talkHotkeyShort(value: TalkHotkey): string {
  return TALK_HOTKEYS.find(option => option.value === value)?.short ?? ''
}

function isTalkHotkey(value: unknown): value is TalkHotkey {
  return TALK_HOTKEYS.some(option => option.value === value)
}

function readStoredHotkey(): TalkHotkey {
  try {
    const stored = window.localStorage.getItem(STORAGE_KEY)
    if (isTalkHotkey(stored)) return stored
  } catch {
    /* storage blocked: the default applies */
  }
  return DEFAULT_TALK_HOTKEY
}

let current: TalkHotkey | null = null
const listeners = new Set<() => void>()

function hotkeySnapshot(): TalkHotkey {
  if (current === null) current = readStoredHotkey()
  return current
}

function subscribeHotkey(listener: () => void) {
  listeners.add(listener)
  const onStorage = (event: StorageEvent) => {
    if (event.key !== STORAGE_KEY) return
    current = readStoredHotkey()
    listener()
  }
  window.addEventListener('storage', onStorage)
  return () => {
    listeners.delete(listener)
    window.removeEventListener('storage', onStorage)
  }
}

export function setTalkHotkey(value: TalkHotkey) {
  current = value
  try {
    window.localStorage.setItem(STORAGE_KEY, value)
  } catch {
    /* storage blocked: the choice lasts until the page reloads */
  }
  listeners.forEach(listener => listener())
}

export function useTalkHotkey(): TalkHotkey {
  return useSyncExternalStore(subscribeHotkey, hotkeySnapshot, () => DEFAULT_TALK_HOTKEY)
}

/*
 * One Talk button answers the key at a time. The session terminal always does;
 * on the Wall, the tile whose terminal was touched last does.
 */
let owner: string | null = null
const ownerListeners = new Set<() => void>()

function notifyOwner() {
  ownerListeners.forEach(listener => listener())
}

export function claimTalkKey(id: string) {
  if (owner === id) return
  owner = id
  notifyOwner()
}

export function releaseTalkKey(id: string) {
  if (owner !== id) return
  owner = null
  notifyOwner()
}

function subscribeOwner(listener: () => void) {
  ownerListeners.add(listener)
  return () => {
    ownerListeners.delete(listener)
  }
}

export function useTalkKeyOwner(): string | null {
  return useSyncExternalStore(subscribeOwner, () => owner, () => null)
}

/**
 * Watches the Talk key for one Talk button. onHeld runs when the key stays down
 * TALK_HOTKEY_HOLD_MS with no other key; onPress runs when the key goes down while
 * the dictation runs. Any other key during the hold cancels it, so a modifier keeps
 * working in shortcuts and accented characters. Returns whether a hold is under way.
 */
export function useTalkKey({
  active,
  dictating,
  onHeld,
  onPress,
}: {
  /** Whether this button answers the key now. */
  active: boolean
  dictating: boolean
  onHeld: () => void
  onPress: () => void
}): boolean {
  const key = useTalkHotkey()
  const [arming, setArming] = useState(false)
  const latest = useRef({ dictating, onHeld, onPress })
  useEffect(() => {
    latest.current = { dictating, onHeld, onPress }
  }, [dictating, onHeld, onPress])

  useEffect(() => {
    if (!active || key === 'off') return
    let timer: number | undefined
    const cancel = () => {
      if (timer === undefined) return
      window.clearTimeout(timer)
      timer = undefined
      setArming(false)
    }
    // A function key would also reach the terminal as an escape sequence.
    const swallow = (event: KeyboardEvent) => {
      if (!key.startsWith('F')) return
      event.preventDefault()
      event.stopPropagation()
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.code !== key) {
        cancel()
        return
      }
      swallow(event)
      if (event.repeat || timer !== undefined) return
      if (latest.current.dictating) {
        latest.current.onPress()
        return
      }
      setArming(true)
      timer = window.setTimeout(() => {
        timer = undefined
        setArming(false)
        latest.current.onHeld()
      }, TALK_HOTKEY_HOLD_MS)
    }
    const onKeyUp = (event: KeyboardEvent) => {
      if (event.code !== key) return
      swallow(event)
      cancel()
    }
    window.addEventListener('keydown', onKeyDown, true)
    window.addEventListener('keyup', onKeyUp, true)
    window.addEventListener('blur', cancel)
    return () => {
      cancel()
      window.removeEventListener('keydown', onKeyDown, true)
      window.removeEventListener('keyup', onKeyUp, true)
      window.removeEventListener('blur', cancel)
    }
  }, [active, key])

  return arming
}
