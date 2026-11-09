import type { Pane, Session } from '../hooks/useSessions'

/** The agents whose own dictation Talk drives. */
export type TalkAgent = 'claude' | 'agy'

/** The agent Talk can speak to in a pane: Claude holds the space bar, agy toggles F5. */
export function talkAgentOf(pane: Pane | undefined): TalkAgent | null {
  for (const name of [pane?.current_command, pane?.agent]) {
    if (name === 'claude' || name === 'agy') return name
  }
  return null
}

/** The pane a session shows: the active pane of its active window. */
export function activePaneOf(session: Session | undefined): Pane | undefined {
  return session?.windows?.find(window => window.active)?.panes?.find(pane => pane.active)
}
