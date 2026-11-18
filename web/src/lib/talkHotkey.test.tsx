import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TalkButton } from '../components/TalkButton'
import { TalkKeyPicker } from '../components/TalkKeyPicker'
import { DEFAULT_TALK_HOTKEY, TALK_HOTKEY_HOLD_MS, isTalkKeyCode, setTalkHotkey, talkHotkeyMatches } from './talkHotkey'

function renderTalk(props: { disabled?: boolean; hotkey?: boolean } = {}) {
  const onHold = vi.fn()
  render(
    <TalkButton
      agent="claude"
      disabled={props.disabled ?? false}
      onHold={onHold}
      readScreen={() => []}
      onSubmit={vi.fn()}
      hotkey={props.hotkey ?? true}
    />,
  )
  return onHold
}

const down = (code: string, init: Partial<KeyboardEventInit> = {}) =>
  fireEvent.keyDown(document.body, { code, key: code, ...init })
const up = (code: string) => fireEvent.keyUp(document.body, { code, key: code })
const wait = (ms: number) => act(() => void vi.advanceTimersByTime(ms))
const dictating = () => screen.queryByRole('group', { name: 'Dictée' }) !== null

describe('Talk key', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    setTalkHotkey(DEFAULT_TALK_HOTKEY)
  })
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
    setTalkHotkey(DEFAULT_TALK_HOTKEY)
  })

  it.each(['AltRight', 'AltLeft'])('starts dictation after %s is held for the hold time', code => {
    const onHold = renderTalk()
    down(code)
    wait(TALK_HOTKEY_HOLD_MS - 1)
    expect(onHold).not.toHaveBeenCalled()
    wait(1)
    expect(onHold).toHaveBeenCalledWith(true)
    expect(dictating()).toBe(true)
  })

  it('counts the seconds down on the Talk button while the key is held', () => {
    renderTalk()
    down('AltRight')
    expect(screen.getByRole('button', { name: /Talk/ }).textContent).toBe('3 s…')
    wait(1100)
    expect(screen.getByRole('button', { name: /Talk/ }).textContent).toBe('2 s…')
    up('AltRight')
    expect(screen.getByRole('button', { name: /Talk/ }).textContent).toBe('Talk')
  })

  it('does nothing when the key is released early', () => {
    const onHold = renderTalk()
    down('AltRight')
    wait(TALK_HOTKEY_HOLD_MS / 2)
    up('AltRight')
    wait(TALK_HOTKEY_HOLD_MS)
    expect(onHold).not.toHaveBeenCalled()
    expect(dictating()).toBe(false)
  })

  it('lets the key work in shortcuts: another key cancels the hold', () => {
    const onHold = renderTalk()
    down('AltRight')
    down('KeyE', { altKey: true })
    wait(TALK_HOTKEY_HOLD_MS)
    expect(onHold).not.toHaveBeenCalled()
  })

  it('lets Option-click select text: a click cancels the hold', () => {
    const onHold = renderTalk()
    down('AltLeft')
    fireEvent.pointerDown(document.body)
    wait(TALK_HOTKEY_HOLD_MS)
    expect(onHold).not.toHaveBeenCalled()
  })

  it('stops dictation and keeps the text when the key is pressed again', () => {
    const onHold = renderTalk()
    down('AltRight')
    wait(TALK_HOTKEY_HOLD_MS)
    up('AltRight')
    expect(dictating()).toBe(true)
    down('AltRight')
    expect(onHold).toHaveBeenLastCalledWith(false)
    expect(dictating()).toBe(false)
    expect(screen.getByRole('button', { name: /Talk/ })).toBeTruthy()
  })

  it('says why when Talk is unavailable', () => {
    const onHold = renderTalk({ disabled: true })
    down('AltRight')
    wait(TALK_HOTKEY_HOLD_MS)
    expect(onHold).not.toHaveBeenCalled()
    expect(screen.getByRole('alert').textContent).toMatch(/Talk indisponible ici/)
  })

  it('ignores the key on a Talk button that does not own it', () => {
    const onHold = renderTalk({ hotkey: false })
    down('AltRight')
    wait(TALK_HOTKEY_HOLD_MS)
    expect(onHold).not.toHaveBeenCalled()
  })

  it('answers only the chosen key', () => {
    setTalkHotkey('MetaRight')
    const onHold = renderTalk()
    down('AltRight')
    wait(TALK_HOTKEY_HOLD_MS)
    expect(onHold).not.toHaveBeenCalled()
    up('AltRight')
    down('MetaRight')
    wait(TALK_HOTKEY_HOLD_MS)
    expect(onHold).toHaveBeenCalledWith(true)
  })

  it('can be turned off', () => {
    setTalkHotkey('off')
    const onHold = renderTalk()
    down('AltRight')
    wait(TALK_HOTKEY_HOLD_MS)
    expect(onHold).not.toHaveBeenCalled()
  })

  it('keeps a function key out of the terminal', () => {
    setTalkHotkey('F5')
    const onHold = renderTalk()
    const event = new KeyboardEvent('keydown', { code: 'F5', key: 'F5', bubbles: true, cancelable: true })
    act(() => void document.body.dispatchEvent(event))
    expect(event.defaultPrevented).toBe(true)
    wait(TALK_HOTKEY_HOLD_MS)
    expect(onHold).toHaveBeenCalledWith(true)
  })
})

describe('Talk key choice', () => {
  afterEach(() => {
    cleanup()
    setTalkHotkey(DEFAULT_TALK_HOTKEY)
  })

  it('accepts only keys that type nothing', () => {
    expect(isTalkKeyCode('AltRight')).toBe(true)
    expect(isTalkKeyCode('MetaLeft')).toBe(true)
    expect(isTalkKeyCode('F5')).toBe(true)
    expect(isTalkKeyCode('KeyA')).toBe(false)
    expect(isTalkKeyCode('Space')).toBe(false)
    expect(isTalkKeyCode('Escape')).toBe(false)
    expect(talkHotkeyMatches('Option', 'AltLeft')).toBe(true)
    expect(talkHotkeyMatches('Option', 'MetaLeft')).toBe(false)
  })

  it('records the key pressed after "Choisir une touche"', () => {
    render(<TalkKeyPicker />)
    expect(screen.getByTestId('talk-key').textContent).toBe('⌥ Option (gauche ou droite)')
    fireEvent.click(screen.getByRole('button', { name: 'Choisir une touche' }))
    down('KeyA')
    expect(screen.getByRole('status').textContent).toMatch(/sert à taper du texte/)
    down('MetaRight')
    expect(screen.getByTestId('talk-key').textContent).toBe('⌘ Commande droite')
    expect(screen.getByRole('status').textContent).toBe('Touche reçue : ⌘ Commande droite.')
  })
})
