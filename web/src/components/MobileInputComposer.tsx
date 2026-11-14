import { useEffect, useLayoutEffect, useRef, useState, type CompositionEvent, type KeyboardEvent } from 'react'
import { ArrowUp, X } from 'lucide-react'
import { MAX_TERMINAL_COMMAND_BODY_BYTES, terminalCommandBodyByteLength } from '../lib/terminalInput'

interface MobileInputComposerProps {
  expanded?: boolean
  onClose?: () => void
  targetKey: string
  targetLabel: string
  initialDraft: string
  onDraftChange: (targetKey: string, value: string) => void
  onSend: (value: string) => void | Promise<void>
}

export function MobileInputComposer({
  expanded: controlledExpanded,
  onClose,
  targetKey,
  targetLabel,
  initialDraft,
  onDraftChange,
  onSend,
}: MobileInputComposerProps) {
  const [localExpanded, setExpanded] = useState(false)
  const expanded = controlledExpanded ?? localExpanded
  const [draft, setDraft] = useState(initialDraft)
  const [sending, setSending] = useState(false)
  const [error, setError] = useState('')
  const [feedback, setFeedback] = useState('')
  const draftRef = useRef(initialDraft)
  const composingRef = useRef(false)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const byteLength = terminalCommandBodyByteLength(draft)
  const tooLarge = byteLength > MAX_TERMINAL_COMMAND_BODY_BYTES

  useLayoutEffect(() => {
    const textarea = textareaRef.current
    if (!textarea) return
    textarea.style.height = 'auto'
    // Three lines of 22px plus the vertical padding and border, then the field scrolls.
    const minHeight = 44
    const maxHeight = 22 * 3 + 24
    textarea.style.height = `${Math.max(minHeight, Math.min((textarea.scrollHeight || 0) + 2, maxHeight))}px`
  }, [draft, expanded])

  useLayoutEffect(() => {
    const textarea = textareaRef.current
    if (!textarea) return
    const persistNativeDraft = () => {
      const nextDraft = textarea.value
      draftRef.current = nextDraft
      onDraftChange(targetKey, nextDraft)
    }
    textarea.addEventListener('input', persistNativeDraft)
    return () => {
      persistNativeDraft()
      textarea.removeEventListener('input', persistNativeDraft)
    }
  }, [expanded, onDraftChange, targetKey])

  useEffect(() => {
    if (!expanded) return
    const frame = window.requestAnimationFrame(() => textareaRef.current?.focus())
    return () => window.cancelAnimationFrame(frame)
  }, [expanded])

  useEffect(() => {
    if (!feedback) return
    const timer = window.setTimeout(() => setFeedback(''), 2500)
    return () => window.clearTimeout(timer)
  }, [feedback])

  const toggle = () => {
    if (controlledExpanded !== undefined) onClose?.()
    else setExpanded(value => !value)
  }

  const submit = async () => {
    if (sending || composingRef.current) return
    const submittedDraft = textareaRef.current?.value ?? draftRef.current
    draftRef.current = submittedDraft
    const submittedByteLength = terminalCommandBodyByteLength(submittedDraft)
    setFeedback('')
    if (submittedByteLength > MAX_TERMINAL_COMMAND_BODY_BYTES) {
      setError(
        `Le brouillon contient ${submittedByteLength.toLocaleString()} octets UTF-8 ; le maximum est ${MAX_TERMINAL_COMMAND_BODY_BYTES.toLocaleString()}.`,
      )
      return
    }
    setError('')
    setSending(true)
    try {
      await onSend(submittedDraft)
      if (draftRef.current === submittedDraft) {
        draftRef.current = ''
        setDraft('')
        onDraftChange(targetKey, '')
      }
      setFeedback(`Envoyé à ${targetLabel}.`)
      window.requestAnimationFrame(() => textareaRef.current?.focus())
    } catch (sendError) {
      setError(sendError instanceof Error ? sendError.message : 'Impossible d’envoyer la commande.')
    } finally {
      setSending(false)
    }
  }

  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (
      event.key === 'Enter' &&
      (event.metaKey || event.ctrlKey) &&
      !composingRef.current &&
      !event.nativeEvent.isComposing
    ) {
      event.preventDefault()
      void submit()
    }
  }

  const startComposition = (_event: CompositionEvent<HTMLTextAreaElement>) => {
    composingRef.current = true
  }
  const endComposition = (_event: CompositionEvent<HTMLTextAreaElement>) => {
    composingRef.current = false
  }

  // The byte budget only matters near its limit: below it the counter is noise on a phone.
  const nearLimit = byteLength > MAX_TERMINAL_COMMAND_BODY_BYTES * 0.9

  return (
    <div className="mobile-composer bg-card">
      {controlledExpanded === undefined && (
        <button
          type="button"
          aria-label={expanded ? 'Replier le compositeur' : 'Ouvrir le compositeur'}
          aria-expanded={expanded}
          onClick={toggle}
          className="flex min-h-11 w-full items-center justify-between px-3 text-xs font-mono"
        >
          <span>Compositeur</span>
          <span className="text-muted-foreground">{expanded ? 'Replier' : draft ? 'Brouillon' : 'Afficher'}</span>
        </button>
      )}
      {expanded && (
        <div className="border-t border-border px-1.5 py-1.5">
          {(error || tooLarge || feedback || nearLimit) && (
            <div className="flex min-w-0 items-center justify-between gap-2 px-2 pb-1 text-2xs">
              {error || tooLarge ? (
                <p className="min-w-0 text-destructive" role="alert">
                  {error || `Le brouillon dépasse ${MAX_TERMINAL_COMMAND_BODY_BYTES.toLocaleString()} octets UTF-8.`}
                </p>
              ) : feedback ? (
                <p className="min-w-0 truncate text-success" role="status">
                  {feedback}
                </p>
              ) : (
                <span />
              )}
              {nearLimit && (
                <span className={tooLarge ? 'shrink-0 text-destructive' : 'shrink-0 text-muted-foreground'}>
                  {byteLength.toLocaleString()} / {MAX_TERMINAL_COMMAND_BODY_BYTES.toLocaleString()} octets
                </span>
              )}
            </div>
          )}
          <div className="flex items-end gap-1.5">
            {controlledExpanded !== undefined && (
              <button
                type="button"
                aria-label="Fermer le compositeur"
                title="Fermer le compositeur"
                onMouseDown={event => event.preventDefault()}
                onClick={onClose}
                className="ui-focus-ring grid h-11 w-11 shrink-0 place-items-center rounded-full text-muted-foreground hover:text-foreground"
              >
                <X size={18} aria-hidden />
              </button>
            )}
            <textarea
              ref={textareaRef}
              rows={1}
              value={draft}
              aria-label={`Brouillon pour ${targetLabel}`}
              placeholder={`Message pour ${targetLabel.split('/').pop() || targetLabel}`}
              enterKeyHint="enter"
              autoCorrect="off"
              autoCapitalize="none"
              autoComplete="off"
              spellCheck={false}
              onChange={event => {
                const nextDraft = event.target.value
                draftRef.current = nextDraft
                setDraft(nextDraft)
                onDraftChange(targetKey, nextDraft)
                setError('')
                setFeedback('')
              }}
              onBlur={event => {
                const nextDraft = event.currentTarget.value
                draftRef.current = nextDraft
                onDraftChange(targetKey, nextDraft)
              }}
              onKeyDown={onKeyDown}
              onCompositionStart={startComposition}
              onCompositionEnd={endComposition}
              className="min-h-11 max-h-[90px] min-w-0 flex-1 resize-none overflow-y-auto rounded-[1.375rem] border border-border bg-input px-4 py-[11px] text-base leading-[22px] text-foreground outline-none placeholder:text-muted-foreground focus:border-primary md:text-sm"
            />
            <button
              type="button"
              aria-label={`Envoyer la commande à ${targetLabel}`}
              title="Envoyer (le texte est suivi d’Entrée)"
              disabled={sending || tooLarge}
              onMouseDown={event => event.preventDefault()}
              onClick={() => void submit()}
              className="ui-focus-ring grid h-11 w-11 shrink-0 place-items-center rounded-full bg-primary text-primary-foreground transition-opacity disabled:opacity-35"
            >
              {sending ? <span className="text-2xs">…</span> : <ArrowUp size={20} aria-hidden />}
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
