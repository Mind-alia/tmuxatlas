import type { ClipboardSelectionType, IClipboardProvider } from '@xterm/addon-clipboard'

/**
 * OSC 52 provider for every terminal. Any program running in a pane can emit
 * OSC 52, including on hosts run by autonomous agents, so:
 * - reading always returns '' — `\e]52;c;?\a` would otherwise hand the
 *   administrator's clipboard (sudo password, tokens) to the pane;
 * - writing is disabled — a pane could otherwise replace what the
 *   administrator is about to paste. Copy with the mouse selection or the
 *   Copy button instead: those require a gesture in the browser.
 */
export const osc52ClipboardProvider: IClipboardProvider = {
  readText(_selection: ClipboardSelectionType): Promise<string> {
    return Promise.resolve('')
  },
  writeText(_selection: ClipboardSelectionType, _text: string): Promise<void> {
    return Promise.resolve()
  },
}
