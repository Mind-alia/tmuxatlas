import type { FileKind } from './filesApi'

export type MarkdownMode = 'preview' | 'code' | 'split'

/** One open file in the explorer. Text is kept with LF line endings. */
export interface FileTab {
  path: string
  name: string
  kind: FileKind
  status: 'loading' | 'ready' | 'error'
  error?: string
  text: string
  saved: string
  eol: '\n' | '\r\n'
  version: string | null
  writable: boolean
  size: number
  mtime: number
  mode: string
  binary: boolean
  tooLarge: boolean
  saving: boolean
  /** Whether the text was fetched: images load it only to show the SVG source. */
  textLoaded: boolean
  markdownMode: MarkdownMode
  showSource: boolean
  wrap: boolean
}

export const isDirty = (tab: FileTab) => tab.textLoaded && tab.text !== tab.saved

export function detectEOL(text: string): '\n' | '\r\n' {
  const crlf = text.match(/\r\n/g)?.length || 0
  const lf = (text.match(/\n/g)?.length || 0) - crlf
  return crlf > lf ? '\r\n' : '\n'
}

export function toEditorText(text: string) {
  return text.replace(/\r\n/g, '\n')
}

export function fromEditorText(text: string, eol: '\n' | '\r\n') {
  return eol === '\r\n' ? text.replace(/\n/g, '\r\n') : text
}
