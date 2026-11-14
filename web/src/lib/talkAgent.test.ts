import { describe, expect, it } from 'vitest'
import type { Pane } from '../hooks/useSessions'
import { talkAgentOf } from './talkAgent'

const pane = (current_command: string, agent?: string) => ({ current_command, agent }) as Pane

describe('talkAgentOf', () => {
  it('finds Claude, agy or Codex, even behind a launcher', () => {
    expect(talkAgentOf(pane('claude'))).toBe('claude')
    expect(talkAgentOf(pane('agy'))).toBe('agy')
    expect(talkAgentOf(pane('python3', 'agy'))).toBe('agy')
    expect(talkAgentOf(pane('codex', 'codex'))).toBe('codex')
    expect(talkAgentOf(pane('vim'))).toBeNull()
    expect(talkAgentOf(pane('zsh'))).toBeNull()
    expect(talkAgentOf(undefined)).toBeNull()
  })
})
