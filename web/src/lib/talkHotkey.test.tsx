import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TalkButton } from '../components/TalkButton'
import { DEFAULT_TALK_HOTKEY, TALK_HOTKEY_HOLD_MS, setTalkHotkey } from './talkHotkey'

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

  it('starts dictation after the key is held for the hold time', () => {
    const onHold = renderTalk()
    down('AltRight')
    wait(TALK_HOTKEY_HOLD_MS - 1)
    expect(onHold).not.toHaveBeenCalled()
    wait(1)
    expect(onHold).toHaveBeenCalledWith(true)
    expect(dictating()).toBe(true)
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

  it('ignores the key while Talk is unavailable', () => {
    const onHold = renderTalk({ disabled: true })
    down('AltRight')
    wait(TALK_HOTKEY_HOLD_MS)
    expect(onHold).not.toHaveBeenCalled()
  })

  it('ignores the key on a Talk button that does not own it', () => {
    const onHold = renderTalk({ hotkey: false })
    down('AltRight')
    wait(TALK_HOTKEY_HOLD_MS)
    expect(onHold).not.toHaveBeenCalled()
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
