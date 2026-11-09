/** A file dropped from the desktop, with its path relative to the drop target. */
export interface DroppedFile {
  relativePath: string
  file: File
}

export interface DroppedItems {
  files: DroppedFile[]
  /** Folders (relative paths) that must exist even when they hold no file. */
  folders: string[]
}

/** MIME type used to drag an explorer item onto another folder. */
export const INTERNAL_DRAG_TYPE = 'application/x-tmuxatlas-path'

export function hasExternalFiles(dataTransfer: DataTransfer | null) {
  if (!dataTransfer) return false
  return Array.from(dataTransfer.types).includes('Files') && !isInternalDrag(dataTransfer)
}

export function isInternalDrag(dataTransfer: DataTransfer | null) {
  return Boolean(dataTransfer && Array.from(dataTransfer.types).includes(INTERNAL_DRAG_TYPE))
}

type Entry = FileSystemEntry
const isFileEntry = (entry: Entry): entry is FileSystemFileEntry => entry.isFile
const isDirectoryEntry = (entry: Entry): entry is FileSystemDirectoryEntry => entry.isDirectory

function readFile(entry: FileSystemFileEntry) {
  return new Promise<File>((resolve, reject) => entry.file(resolve, reject))
}

/** readEntries returns at most ~100 entries per call, so it is called until empty. */
async function readAllEntries(directory: FileSystemDirectoryEntry) {
  const reader = directory.createReader()
  const entries: Entry[] = []
  for (;;) {
    const batch = await new Promise<Entry[]>((resolve, reject) => reader.readEntries(resolve, reject))
    if (batch.length === 0) return entries
    entries.push(...batch)
  }
}

async function walk(entry: Entry, prefix: string, out: DroppedItems) {
  const path = prefix ? `${prefix}/${entry.name}` : entry.name
  if (isFileEntry(entry)) {
    out.files.push({ relativePath: path, file: await readFile(entry) })
  } else if (isDirectoryEntry(entry)) {
    const children = await readAllEntries(entry)
    if (children.length === 0) out.folders.push(path)
    for (const child of children) await walk(child, path, out)
  }
}

/**
 * Collects everything dropped, folders included. The entries must be taken
 * synchronously inside the drop handler: the DataTransfer empties right after.
 */
export async function collectDropped(dataTransfer: DataTransfer): Promise<DroppedItems> {
  const out: DroppedItems = { files: [], folders: [] }
  const items = Array.from(dataTransfer.items || [])
  const entries = items
    .filter(item => item.kind === 'file')
    .map(item => (typeof item.webkitGetAsEntry === 'function' ? item.webkitGetAsEntry() : null))
  if (entries.length > 0 && entries.every(Boolean)) {
    for (const entry of entries) await walk(entry as Entry, '', out)
    return out
  }
  // Without the entries API, only plain files can be read.
  for (const file of Array.from(dataTransfer.files)) out.files.push({ relativePath: file.name, file })
  return out
}

/** Files picked with <input type="file" webkitdirectory> carry their relative path. */
export function collectPicked(files: FileList): DroppedItems {
  return {
    files: Array.from(files).map(file => ({
      relativePath: (file as File & { webkitRelativePath?: string }).webkitRelativePath || file.name,
      file,
    })),
    folders: [],
  }
}

/**
 * Lets a row be dragged out of the browser: Chromium browsers turn
 * DownloadURL into a real file on the desktop. Every browser also gets the
 * link and, inside the explorer, the path for moves.
 */
export function setDragOut(dataTransfer: DataTransfer, item: { name: string; path: string; isDir: boolean }, url: string) {
  const absolute = new URL(url, window.location.origin).toString()
  const name = item.isDir ? `${item.name}.zip` : item.name
  const mime = item.isDir ? 'application/zip' : 'application/octet-stream'
  dataTransfer.effectAllowed = 'copyMove'
  dataTransfer.setData(INTERNAL_DRAG_TYPE, item.path)
  dataTransfer.setData('DownloadURL', `${mime}:${name}:${absolute}`)
  dataTransfer.setData('text/uri-list', absolute)
  dataTransfer.setData('text/plain', item.path)
}
