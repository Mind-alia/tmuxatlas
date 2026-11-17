import { useEffect, useRef, useState } from 'react'
import { ArrowUp, Square } from 'lucide-react'
import { agyPromptHasText, readAgyVoiceStatus } from '../lib/agyVoiceStatus'
import { readClaudeVoiceStatus } from '../lib/claudeVoiceStatus'
import type { DictatingAgent } from '../lib/talkAgent'
import { TALK_HOTKEY_HOLD_MS, talkHotkeyShort, useTalkHotkey, useTalkKey } from '../lib/talkHotkey'

const POLL_MS = 100
const RELEASE_SETTLE_MS = 300
const TRANSCRIPT_TIMEOUT_MS = 30_000
const IDLE_TICKS_BEFORE_SEND = 2
// agy reports a failed dictation in its conversation, not its footer: an empty
// prompt this long after it stops means nothing was transcribed.
const EMPTY_TICKS_BEFORE_GIVING_UP = 10

interface Voice {
  name: string
  hint: string
  read: (rows: string[]) => 'recording' | 'processing' | 'failed' | 'idle'
  /** Whether the transcript reached the prompt, when the footer cannot tell. */
  transcribed?: (rows: string[]) => boolean
}

const voices: Record<DictatingAgent, Voice> = {
  claude: { name: 'Claude', hint: 'Maintient la barre espace jusqu’à l’arrêt ou l’envoi', read: readClaudeVoiceStatus },
  agy: {
    name: 'agy',
    hint: 'Lance la dictée d’agy (F5) jusqu’à l’arrêt ou l’envoi',
    read: readAgyVoiceStatus,
    transcribed: agyPromptHasText,
  },
}

type Phase = 'idle' | 'holding' | 'sending'

const ROUND = 'grid h-11 w-11 shrink-0 place-items-center rounded-full disabled:opacity-40 md:h-7 md:w-7'
const TALK = 'relative min-h-11 shrink-0 overflow-hidden rounded border border-border px-2 text-muted-foreground hover:text-foreground disabled:opacity-40 md:min-h-8'
const HOLD_SECONDS = TALK_HOTKEY_HOLD_MS / 1000

/** The Talk key named for tooltips, or '' when this button does not answer it. */
function useTalkKeyLabel(hotkey: boolean | undefined) {
  const key = useTalkHotkey()
  return hotkey && key !== 'off' ? talkHotkeyShort(key) : ''
}

/** Fills the Talk button while the Talk key is held, until dictation starts. */
function ArmingFill() {
  return (
    <span
      aria-hidden="true"
      className="atlas-talk-arming absolute inset-0 origin-left bg-primary/25"
      style={{ animationDuration: `${TALK_HOTKEY_HOLD_MS}ms` }}
    />
  )
}

/**
 * Runs the agent's own dictation until Stop or Send: onHold holds Claude Code's
 * hold-to-talk space bar, or toggles agy's F5. Send presses Enter once the
 * screen shows the transcript is in.
 */
export function TalkButton({
  agent = 'claude',
  disabled,
  onHold,
  readScreen,
  onSubmit,
  targetLabel,
  hotkey,
}: {
  agent?: DictatingAgent
  disabled: boolean
  onHold: (on: boolean) => void
  readScreen: () => string[]
  onSubmit: () => void
  /** Names the session when several Talk buttons share the page, as on the Wall. */
  targetLabel?: string
  /** Whether this button answers the Talk key: one button per page does. */
  hotkey?: boolean
}) {
  const voice = voices[agent]
  const [phase, setPhase] = useState<Phase>('idle')
  const [notice, setNotice] = useState('')
  const keyLabel = useTalkKeyLabel(hotkey)
  const start = () => {
    setNotice('')
    heard.current = false
    setPhase('holding')
    onHold(true)
  }
  const arming = useTalkKey({
    active: Boolean(hotkey),
    dictating: phase === 'holding',
    onHeld: () => {
      if (!disabled && phase === 'idle') start()
    },
    onPress: () => setPhase('idle'),
  })
  const latest = useRef({ onHold, readScreen, onSubmit, voice })
  useEffect(() => {
    latest.current = { onHold, readScreen, onSubmit, voice }
  }, [onHold, onSubmit, readScreen, voice])
  const heard = useRef(false)

  // Leaving the holding phase, by any path, releases the space bar.
  useEffect(() => {
    if (phase !== 'holding') return
    return () => latest.current.onHold(false)
  }, [phase])

  useEffect(() => {
    if (disabled) setPhase('idle')
  }, [disabled])

  useEffect(() => {
    if (phase !== 'holding') return
    const timer = window.setInterval(() => {
      if (latest.current.voice.read(latest.current.readScreen()) === 'recording') heard.current = true
    }, POLL_MS)
    return () => window.clearInterval(timer)
  }, [phase])

  useEffect(() => {
    if (phase !== 'sending') return
    const startedAt = Date.now()
    let idleTicks = 0
    let emptyTicks = 0
    const finish = (message: string) => {
      window.clearInterval(timer)
      setNotice(message)
      setPhase('idle')
    }
    const timer = window.setInterval(() => {
      const elapsed = Date.now() - startedAt
      if (elapsed < RELEASE_SETTLE_MS) return
      const { voice, readScreen } = latest.current
      const rows = readScreen()
      const status = voice.read(rows)
      if (status === 'recording') heard.current = true
      if (status === 'failed') {
        finish(`${voice.name} n’a pas pu transcrire. Rien n’a été envoyé.`)
      } else if (elapsed > TRANSCRIPT_TIMEOUT_MS) {
        finish(`${voice.name} n’a pas terminé la transcription. Rien n’a été envoyé.`)
      } else if (status !== 'idle') {
        idleTicks = 0
        emptyTicks = 0
      } else if (!heard.current) {
        finish(`${voice.name} n’écoutait pas. Rien n’a été envoyé.`)
      } else if (voice.transcribed && !voice.transcribed(rows)) {
        idleTicks = 0
        if (++emptyTicks >= EMPTY_TICKS_BEFORE_GIVING_UP) {
          finish(`${voice.name} n’a rien transcrit. Rien n’a été envoyé.`)
        }
      } else if (++idleTicks >= IDLE_TICKS_BEFORE_SEND) {
        latest.current.onSubmit()
        finish('')
      }
    }, POLL_MS)
    return () => window.clearInterval(timer)
  }, [phase])

  if (phase === 'idle') {
    return (
      <>
        <button
          type="button"
          disabled={disabled}
          aria-label={`Talk : parler à ${voice.name}${targetLabel ? ` dans ${targetLabel}` : ''}`}
          title={keyLabel ? `${voice.hint} · ou maintiens ${keyLabel} ${HOLD_SECONDS} s` : voice.hint}
          className={TALK}
          onClick={start}
        >
          {arming && <ArmingFill />}
          <span className="relative">Talk</span>
        </button>
        {notice && (
          <span role="alert" title={notice} className="ml-2 min-w-0 truncate font-normal text-destructive">
            {notice}
          </span>
        )}
      </>
    )
  }

  const sending = phase === 'sending'
  return (
    <div role="group" aria-label="Dictée" className="flex items-center gap-2">
      <button
        type="button"
        disabled={sending}
        aria-label="Arrêter la dictée et modifier le texte"
        title={keyLabel ? `Arrêter et modifier le texte (ou appuie sur ${keyLabel})` : 'Arrêter et modifier le texte'}
        className={`${ROUND} border border-border bg-muted text-foreground hover:opacity-80`}
        onClick={() => setPhase('idle')}
      >
        <Square aria-hidden="true" size={14} />
      </button>
      <button
        type="button"
        disabled={sending}
        aria-label="Arrêter la dictée et envoyer le texte"
        title="Arrêter et envoyer le texte"
        className={`${ROUND} bg-blue-600 text-white hover:opacity-90`}
        onClick={() => setPhase('sending')}
      >
        <ArrowUp aria-hidden="true" size={16} />
      </button>
    </div>
  )
}

/** A dictation the server runs for an agent without its own (see pkg/dictation). */
export interface DictationRelay {
  start: () => Promise<void>
  /** Types the transcript in the pane, and presses Enter when submit is true. */
  stop: (submit: boolean) => Promise<void>
  cancel: () => void
}

type RelayPhase = 'idle' | 'starting' | 'holding' | 'sending'

const livePlacements = {
  'above-left': 'bottom-full mb-2 left-0',
  'above-right': 'bottom-full mb-2 right-0',
  'below-right': 'top-full mt-2 right-0',
}

/**
 * Talk for an agent without dictation, such as Codex: a hidden Claude Code
 * transcribes, and the server types its transcript in this pane.
 */
export function RelayTalkButton({
  agentName,
  disabled,
  relay,
  onHold,
  targetLabel,
  liveText = '',
  livePlacement = 'above-left',
  hotkey,
}: {
  agentName: string
  disabled: boolean
  relay: DictationRelay
  /** Reports when this dictation starts and ends, as the Wall needs. */
  onHold?: (on: boolean) => void
  targetLabel?: string
  /** The transcript as Claude writes it, shown while the person speaks. */
  liveText?: string
  /** Where the transcript shows: below the app or Wall tile header,
   * above the mobile toolbar. */
  livePlacement?: 'above-left' | 'above-right' | 'below-right'
  /** Whether this button answers the Talk key: one button per page does. */
  hotkey?: boolean
}) {
  const keyLabel = useTalkKeyLabel(hotkey)
  const [phase, setPhase] = useState<RelayPhase>('idle')
  const [notice, setNotice] = useState('')
  const latest = useRef({ relay, onHold })
  useEffect(() => {
    latest.current = { relay, onHold }
  }, [relay, onHold])
  const phaseRef = useRef<RelayPhase>('idle')
  const move = (next: RelayPhase) => {
    phaseRef.current = next
    setPhase(next)
  }

  // Leaving the page or losing the target while recording drops the dictation.
  useEffect(() => {
    if (!disabled) return
    if (phaseRef.current === 'starting' || phaseRef.current === 'holding') latest.current.relay.cancel()
    if (phaseRef.current !== 'idle') latest.current.onHold?.(false)
    phaseRef.current = 'idle'
    setPhase('idle')
  }, [disabled])
  useEffect(
    () => () => {
      if (phaseRef.current === 'starting' || phaseRef.current === 'holding') latest.current.relay.cancel()
      if (phaseRef.current !== 'idle') latest.current.onHold?.(false)
    },
    [],
  )

  const finish = (message: string) => {
    setNotice(message)
    move('idle')
    latest.current.onHold?.(false)
  }
  const start = () => {
    setNotice('')
    move('starting')
    onHold?.(true)
    relay.start().then(
      () => {
        if (phaseRef.current === 'starting') move('holding')
      },
      (error: Error) => {
        if (phaseRef.current === 'starting') finish(error.message)
      },
    )
  }
  const stop = (submit: boolean) => {
    if (phaseRef.current === 'starting') {
      relay.cancel()
      finish('')
      return
    }
    move('sending')
    relay.stop(submit).then(
      () => finish(''),
      (error: Error) => finish(error.message),
    )
  }
  const arming = useTalkKey({
    active: Boolean(hotkey),
    dictating: phase === 'starting' || phase === 'holding',
    onHeld: () => {
      if (!disabled && phaseRef.current === 'idle') start()
    },
    onPress: () => stop(false),
  })
  const hint = `Claude transcrit ta voix, puis le texte est tapé dans ${agentName}`

  if (phase === 'idle') {
    return (
      <>
        <button
          type="button"
          disabled={disabled}
          aria-label={`Talk : parler à ${agentName}${targetLabel ? ` dans ${targetLabel}` : ''}`}
          title={keyLabel ? `${hint} · ou maintiens ${keyLabel} ${HOLD_SECONDS} s` : hint}
          className={TALK}
          onClick={start}
        >
          {arming && <ArmingFill />}
          <span className="relative">Talk</span>
        </button>
        {notice && (
          <span role="alert" title={notice} className="ml-2 min-w-0 truncate font-normal text-destructive">
            {notice}
          </span>
        )}
      </>
    )
  }

  const busy = phase === 'sending'
  return (
    <div role="group" aria-label="Dictée" aria-busy={phase !== 'holding'} className="relative flex items-center gap-2">
      {phase === 'starting' && <span className="text-xs text-muted-foreground">Préparation…</span>}
      {phase !== 'starting' && (
        <p
          aria-live="polite"
          aria-label="Transcription en direct"
          className={`absolute z-30 max-h-48 w-max max-w-[min(36rem,85vw)] overflow-y-auto whitespace-pre-wrap break-words rounded border border-border bg-card px-3 py-2 text-left text-sm font-normal text-foreground shadow-lg ${livePlacements[livePlacement]}`}
        >
          {liveText || <span className="text-muted-foreground">Je t’écoute…</span>}
        </p>
      )}
      <button
        type="button"
        disabled={busy}
        aria-label="Arrêter la dictée et modifier le texte"
        title={`${phase === 'starting' ? 'Annuler la dictée' : 'Arrêter et modifier le texte'}${keyLabel ? ` (ou appuie sur ${keyLabel})` : ''}`}
        className={`${ROUND} border border-border bg-muted text-foreground hover:opacity-80`}
        onClick={() => stop(false)}
      >
        <Square aria-hidden="true" size={14} />
      </button>
      <button
        type="button"
        disabled={phase !== 'holding'}
        aria-label="Arrêter la dictée et envoyer le texte"
        title="Arrêter et envoyer le texte"
        className={`${ROUND} bg-blue-600 text-white hover:opacity-90`}
        onClick={() => stop(true)}
      >
        <ArrowUp aria-hidden="true" size={16} />
      </button>
    </div>
  )
}
