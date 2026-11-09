import { describe, expect, it } from 'vitest'
import { agyPromptHasText, readAgyVoiceStatus } from './agyVoiceStatus'

// Captured from a real agy pane while dictating with /voice.
const rule = '─'.repeat(76)
const footer = '? for shortcuts                                    Gemini 3.8 Flash · medium'
const screen = (prompt: string, ...below: string[]) => [
  '> /voice',
  '  ⎿  No speech detected',
  '',
  rule,
  prompt,
  rule,
  ...below,
  footer,
]

describe('readAgyVoiceStatus', () => {
  it.each([
    ['recording', screen('>', '● Recording 00:00  enter or f5 to insert · esc to discard'), 'recording'],
    ['recording, blinking', screen('>', '○ Recording 00:01  enter or f5 to insert · esc to discard'), 'recording'],
    [
      'finishing',
      screen('>', '  Finishing up... enter or f5 to insert what you have · esc to discard'),
      'processing',
    ],
    ['at its prompt', screen('>'), 'idle'],
  ])('reads %s', (_name, rows, status) => {
    expect(readAgyVoiceStatus(rows)).toBe(status)
  })

  it('ignores words in the conversation above the prompt', () => {
    expect(readAgyVoiceStatus(['● Recording 00:02 was printed by a tool', rule, '>', rule, footer])).toBe('idle')
  })
})

describe('agyPromptHasText', () => {
  it('finds the transcript in the prompt, not in the conversation', () => {
    expect(agyPromptHasText(screen('>'))).toBe(false)
    expect(agyPromptHasText(screen('> Corrige le test du mur'))).toBe(true)
    expect(agyPromptHasText(['> /voice', '  ⎿  No speech detected'])).toBe(false)
  })
})
