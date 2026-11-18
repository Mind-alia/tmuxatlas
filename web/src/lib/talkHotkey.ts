import { useEffect, useRef, useState, useSyncExternalStore } from 'react'

/**
 * The keyboard key for Talk, as a KeyboardEvent.code, 'Option' for either Option
 * key, or 'off'. Held alone for TALK_HOTKEY_HOLD_MS, it starts the dictation of
 * the terminal on screen; pressed again, it stops the dictation and leaves the
 * text in the prompt. The choice belongs to this browser: a phone has no
 * keyboard, and each computer has its own.
 */
export type TalkHotkey = string

export const TALK_HOTKEY_HOLD_MS = 3000
export const DEFAULT_TALK_HOTKEY: TalkHotkey = 'Option'
const STORAGE_KEY = 'tmuxatlas.talk-hotkey'

const MODIFIER_LABELS: Record<string, [label: string, short: string]> = {
  Option: ['⌥ Option (gauche ou droite)', '⌥ Option'],
  AltLeft: ['⌥ Option gauche (Alt gauche)', '⌥ gauche'],
  AltRight: ['⌥ Option droite (Alt droite)', '⌥ droite'],
  MetaLeft: ['⌘ Commande gauche', '⌘ gauche'],
  MetaRight: ['⌘ Commande droite', '⌘ droite'],
  ShiftLeft: ['⇧ Maj gauche', '⇧ gauche'],
  ShiftRight: ['⇧ Maj droite', '⇧ droite'],
  ControlLeft: ['⌃ Contrôle gauche', '⌃ gauche'],
  ControlRight: ['⌃ Contrôle droite', '⌃ droite'],
}

const FUNCTION_KEY = /^F([1-9]|1[0-9]|2[0-4])$/

/** Whether a key can serve as the Talk key: a modifier or a function key, which type nothing. */
export function isTalkKeyCode(code: string): boolean {
  return (code in MODIFIER_LABELS && code !== 'Option') || FUNCTION_KEY.test(code)
}

function isTalkHotkey(value: unknown): value is TalkHotkey {
  return typeof value === 'string' && (value === 'off' || value === 'Option' || isTalkKeyCode(value))
}

export function talkHotkeyMatches(hotkey: TalkHotkey, code: string): boolean {
  if (hotkey === 'off') return false
  if (hotkey === 'Option') return code === 'AltLeft' || code === 'AltRight'
  return code === hotkey
}

export function talkHotkeyLabel(hotkey: TalkHotkey): string {
  if (hotkey === 'off') return 'Désactivée'
  if (FUNCTION_KEY.test(hotkey)) return `${hotkey} (fn + ${hotkey} sur un Mac)`
  return MODIFIER_LABELS[hotkey]?.[0] ?? hotkey
}

export function talkHotkeyShort(hotkey: TalkHotkey): string {
  if (hotkey === 'off') return ''
  if (FUNCTION_KEY.test(hotkey)) return hotkey
  return MODIFIER_LABELS[hotkey]?.[1] ?? hotkey
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
  if (!isTalkHotkey(value)) return
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
 * the dictation runs. Any other key, or a click (Option-click selects text in the
 * terminal), cancels the hold, so a modifier keeps working in shortcuts and
 * accented characters. Returns when the current hold started, or null.
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
}): number | null {
  const key = useTalkHotkey()
  const [armedAt, setArmedAt] = useState<number | null>(null)
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
      setArmedAt(null)
    }
    // A function key would also reach the terminal as an escape sequence.
    const swallow = (event: KeyboardEvent) => {
      if (!FUNCTION_KEY.test(event.code)) return
      event.preventDefault()
      event.stopPropagation()
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (!talkHotkeyMatches(key, event.code)) {
        cancel()
        return
      }
      swallow(event)
      if (event.repeat || timer !== undefined) return
      if (latest.current.dictating) {
        latest.current.onPress()
        return
      }
      setArmedAt(Date.now())
      timer = window.setTimeout(() => {
        timer = undefined
        setArmedAt(null)
        latest.current.onHeld()
      }, TALK_HOTKEY_HOLD_MS)
    }
    const onKeyUp = (event: KeyboardEvent) => {
      if (!talkHotkeyMatches(key, event.code)) return
      swallow(event)
      cancel()
    }
    window.addEventListener('keydown', onKeyDown, true)
    window.addEventListener('keyup', onKeyUp, true)
    window.addEventListener('pointerdown', cancel, true)
    window.addEventListener('blur', cancel)
    return () => {
      cancel()
      window.removeEventListener('keydown', onKeyDown, true)
      window.removeEventListener('keyup', onKeyUp, true)
      window.removeEventListener('pointerdown', cancel, true)
      window.removeEventListener('blur', cancel)
    }
  }, [active, key])

  return armedAt
}

/** Whole seconds left before a hold started at armedAt starts dictation, ticking while it runs. */
export function useHoldCountdown(armedAt: number | null): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (armedAt === null) return
    setNow(Date.now())
    const timer = window.setInterval(() => setNow(Date.now()), 100)
    return () => window.clearInterval(timer)
  }, [armedAt])
  if (armedAt === null) return 0
  return Math.max(1, Math.ceil((TALK_HOTKEY_HOLD_MS - (now - armedAt)) / 1000))
}
