import type { Pane, Session } from '../hooks/useSessions'

/**
 * The agents Talk speaks to: Claude holds its space bar, agy toggles F5, and
 * Codex, without dictation of its own, gets a transcript from a hidden Claude.
 */
export type TalkAgent = 'claude' | 'agy' | 'codex'

/** The agents whose own dictation the Terminal drives from the browser. */
export type DictatingAgent = Exclude<TalkAgent, 'codex'>

/** The agent Talk can speak to in a pane, even behind a launcher. */
export function talkAgentOf(pane: Pane | undefined): TalkAgent | null {
  for (const name of [pane?.current_command, pane?.agent]) {
    if (name === 'claude' || name === 'agy' || name === 'codex') return name
  }
  return null
}

/** The pane a session shows: the active pane of its active window. */
export function activePaneOf(session: Session | undefined): Pane | undefined {
  return session?.windows?.find(window => window.active)?.panes?.find(pane => pane.active)
}
