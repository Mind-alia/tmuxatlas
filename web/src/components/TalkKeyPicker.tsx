import { useEffect, useState } from 'react'
import {
  DEFAULT_TALK_HOTKEY,
  TALK_HOTKEY_HOLD_MS,
  isTalkKeyCode,
  setTalkHotkey,
  talkHotkeyLabel,
  useTalkHotkey,
} from '../lib/talkHotkey'
import { Button } from './ui'

/**
 * Picks the Talk key by pressing it. Pressing it also shows whether this computer
 * delivers the key to the browser: an app that keeps it for itself, such as a
 * dictation app, leaves the picker waiting.
 */
export function TalkKeyPicker() {
  const hotkey = useTalkHotkey()
  const [listening, setListening] = useState(false)
  const [message, setMessage] = useState('')

  useEffect(() => {
    if (!listening) return
    const onKeyDown = (event: KeyboardEvent) => {
      event.preventDefault()
      event.stopPropagation()
      if (event.code === 'Escape') {
        setListening(false)
        setMessage('')
        return
      }
      if (!isTalkKeyCode(event.code)) {
        setMessage(`Cette touche sert à taper du texte : choisis ⌥, ⌘, ⇧, ⌃ ou une touche F.`)
        return
      }
      setTalkHotkey(event.code)
      setListening(false)
      setMessage(`Touche reçue : ${talkHotkeyLabel(event.code)}.`)
    }
    window.addEventListener('keydown', onKeyDown, true)
    return () => window.removeEventListener('keydown', onKeyDown, true)
  }, [listening])

  const choose = (value: string) => {
    setListening(false)
    setMessage('')
    setTalkHotkey(value)
  }

  return (
    <div className="flex max-w-md flex-col items-end gap-1 text-right">
      <div className="flex flex-wrap items-center justify-end gap-2">
        <span className="text-sm text-foreground" data-testid="talk-key">
          {talkHotkeyLabel(hotkey)}
        </span>
        <Button
          size="sm"
          variant={listening ? 'primary' : 'secondary'}
          aria-pressed={listening}
          onClick={() => {
            setMessage('')
            setListening(value => !value)
          }}
        >
          {listening ? 'Appuie sur la touche… (Échap : annuler)' : 'Choisir une touche'}
        </Button>
        {hotkey !== DEFAULT_TALK_HOTKEY && (
          <Button size="sm" variant="ghost" onClick={() => choose(DEFAULT_TALK_HOTKEY)}>
            Par défaut
          </Button>
        )}
        {hotkey !== 'off' && (
          <Button size="sm" variant="ghost" onClick={() => choose('off')}>
            Désactiver
          </Button>
        )}
      </div>
      {listening && (
        <p className="text-xs text-muted-foreground">
          Si rien ne se passe quand tu appuies, ton Mac garde cette touche pour lui (une app de dictée par exemple) :
          essaies-en une autre.
        </p>
      )}
      {message && (
        <p role="status" className="text-xs text-muted-foreground">
          {message}
        </p>
      )}
      {!listening && !message && hotkey !== 'off' && (
        <p className="text-xs text-muted-foreground">
          Dans un terminal, maintiens-la {TALK_HOTKEY_HOLD_MS / 1000} s : le bouton Talk affiche un compte à rebours.
        </p>
      )}
    </div>
  )
}
