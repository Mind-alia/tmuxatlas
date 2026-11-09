import { forwardRef, useDeferredValue, useMemo, useState } from 'react'
import { BookOpen, Code, Columns2, Download, FileQuestion, Image as ImageIcon, Lock, WrapText } from 'lucide-react'
import { cn } from '../../lib/utils'
import { Button } from '../ui'
import { CodeEditor, type CodeEditorHandle, type CursorInfo } from './CodeEditor'
import { detectIndent } from './editorCommands'
import { dirname, downloadURL, formatSize } from './filesApi'
import { languageFor, languageLabel } from './highlight'
import { MarkdownView } from './markdown'
import { AudioViewer, ImageViewer, VideoViewer } from './MediaViewer'
import type { FileTab, MarkdownMode } from './tabs'

interface FileViewerProps {
  tab: FileTab
  onChange: (text: string) => void
  onSave: () => void
  onUpdate: (change: Partial<FileTab>) => void
  onLoadText: () => void
  onOpenPath: (path: string) => void
}

const segment =
  'ui-focus-ring inline-flex items-center gap-1.5 rounded px-2 py-1 text-xs text-muted-foreground hover:text-foreground'

export const FileViewer = forwardRef<CodeEditorHandle, FileViewerProps>(function FileViewer(
  { tab, onChange, onSave, onUpdate, onLoadText, onOpenPath },
  editorRef,
) {
  const [cursor, setCursor] = useState<CursorInfo | null>(null)
  const language = useMemo(() => languageFor(tab.name), [tab.name])
  const indent = useMemo(() => detectIndent(tab.saved), [tab.saved])
  const previewSource = useDeferredValue(tab.text)
  const isSVG = tab.name.toLowerCase().endsWith('.svg')
  const readOnly = !tab.writable

  if (tab.status === 'loading' && tab.kind !== 'image' && tab.kind !== 'video' && tab.kind !== 'audio') {
    return (
      <div role="status" className="grid flex-1 place-items-center text-sm text-muted-foreground">
        Ouverture…
      </div>
    )
  }
  if (tab.status === 'error') {
    return (
      <div className="grid flex-1 place-items-center p-6 text-center">
        <div>
          <p className="text-sm text-destructive">{tab.error || 'Ouverture impossible.'}</p>
          <Button className="mt-4" size="sm" onClick={onLoadText}>
            Réessayer
          </Button>
        </div>
      </div>
    )
  }

  const editor = (
    <CodeEditor
      ref={editorRef}
      value={tab.text}
      onChange={onChange}
      language={language}
      readOnly={readOnly}
      wrap={tab.wrap}
      tabSize={indent.tabSize}
      insertSpaces={indent.insertSpaces}
      onSave={onSave}
      onCursorChange={setCursor}
      ariaLabel={`Contenu de ${tab.name}`}
      className="min-h-0 flex-1"
    />
  )

  let body
  const showEditor =
    (tab.kind === 'text' && !tab.binary && !tab.tooLarge) ||
    (tab.kind === 'markdown' && !tab.binary && !tab.tooLarge) ||
    (isSVG && tab.showSource)
  if (tab.kind === 'markdown' && showEditor) {
    const preview = (
      <div className="min-h-0 flex-1 overflow-y-auto bg-background px-5 py-6 sm:px-8">
        <MarkdownView source={previewSource} baseDir={dirname(tab.path)} onOpenPath={onOpenPath} />
      </div>
    )
    body =
      tab.markdownMode === 'preview' ? (
        preview
      ) : tab.markdownMode === 'code' ? (
        editor
      ) : (
        <div className="flex min-h-0 flex-1 flex-col lg:flex-row">
          <div className="flex min-h-0 flex-1 flex-col border-b border-border lg:border-r lg:border-b-0">{editor}</div>
          {preview}
        </div>
      )
  } else if (isSVG && tab.showSource) {
    body = tab.textLoaded ? (
      tab.binary || tab.tooLarge ? (
        <BinaryCard tab={tab} />
      ) : (
        editor
      )
    ) : (
      <div role="status" className="grid flex-1 place-items-center text-sm text-muted-foreground">
        Ouverture…
      </div>
    )
  } else if (tab.kind === 'image') {
    body = <ImageViewer path={tab.path} name={tab.name} version={tab.version || String(tab.mtime)} />
  } else if (tab.kind === 'video') {
    body = <VideoViewer path={tab.path} />
  } else if (tab.kind === 'audio') {
    body = <AudioViewer path={tab.path} name={tab.name} />
  } else if (showEditor) {
    body = editor
  } else {
    body = <BinaryCard tab={tab} />
  }

  const modeButton = (mode: MarkdownMode, label: string, Icon: typeof Code) => (
    <button
      type="button"
      className={cn(segment, tab.markdownMode === mode && 'bg-secondary text-foreground')}
      aria-pressed={tab.markdownMode === mode}
      onClick={() => onUpdate({ markdownMode: mode })}
    >
      <Icon size={13} aria-hidden />
      <span className="hidden sm:inline">{label}</span>
    </button>
  )

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {(tab.kind === 'markdown' || isSVG) && (
        <div className="flex shrink-0 items-center gap-1 border-b border-border px-2 py-1">
          {tab.kind === 'markdown' ? (
            <>
              {modeButton('preview', 'Aperçu', BookOpen)}
              {modeButton('code', 'Code', Code)}
              {modeButton('split', 'Côte à côte', Columns2)}
            </>
          ) : (
            <>
              <button
                type="button"
                className={cn(segment, !tab.showSource && 'bg-secondary text-foreground')}
                aria-pressed={!tab.showSource}
                onClick={() => onUpdate({ showSource: false })}
              >
                <ImageIcon size={13} aria-hidden /> Image
              </button>
              <button
                type="button"
                className={cn(segment, tab.showSource && 'bg-secondary text-foreground')}
                aria-pressed={tab.showSource}
                onClick={() => {
                  onUpdate({ showSource: true })
                  if (!tab.textLoaded) onLoadText()
                }}
              >
                <Code size={13} aria-hidden /> Source
              </button>
            </>
          )}
        </div>
      )}
      {body}
      <footer className="flex shrink-0 items-center gap-3 overflow-x-auto border-t border-border bg-card px-3 py-1 text-2xs whitespace-nowrap text-muted-foreground scrollbar-none">
        {readOnly && (
          <span className="inline-flex items-center gap-1 text-warning">
            <Lock size={11} aria-hidden /> Lecture seule
          </span>
        )}
        {showEditor && (tab.kind !== 'markdown' || tab.markdownMode !== 'preview') && cursor && (
          <span className="tabular-nums">
            Ln {cursor.line}, Col {cursor.column}
            {cursor.selected > 0 && ` (${cursor.selected} sélectionnés)`}
          </span>
        )}
        <div className="flex-1" />
        <span className="tabular-nums">{formatSize(tab.textLoaded ? new Blob([tab.text]).size : tab.size)}</span>
        {showEditor && (
          <>
            <span>{indent.insertSpaces ? `Espaces : ${indent.tabSize}` : `Tabulations : ${indent.tabSize}`}</span>
            <span>UTF-8</span>
            <span>{tab.eol === '\r\n' ? 'CRLF' : 'LF'}</span>
            <span>{languageLabel(language)}</span>
            <button
              type="button"
              className={cn('ui-focus-ring inline-flex items-center gap-1 rounded px-1 hover:text-foreground', tab.wrap && 'text-foreground')}
              aria-pressed={tab.wrap}
              onClick={() => onUpdate({ wrap: !tab.wrap })}
              title="Retour à la ligne automatique (Alt+Z)"
            >
              <WrapText size={12} aria-hidden /> Retour à la ligne
            </button>
          </>
        )}
      </footer>
    </div>
  )
})

function BinaryCard({ tab }: { tab: FileTab }) {
  return (
    <div className="grid flex-1 place-items-center p-6">
      <div className="w-full max-w-sm rounded-lg border border-border bg-card p-6 text-center">
        <FileQuestion size={32} className="mx-auto mb-3 text-muted-foreground" aria-hidden />
        <p className="truncate text-sm font-medium" title={tab.name}>
          {tab.name}
        </p>
        <p className="mt-1 text-xs text-muted-foreground">
          {tab.tooLarge
            ? `Ce fichier texte dépasse 5 Mio : il est trop volumineux pour l’éditeur.`
            : 'Ce fichier n’est pas du texte : il ne peut pas être affiché ici.'}
        </p>
        <dl className="mt-4 grid grid-cols-2 gap-x-3 gap-y-1 text-left text-xs">
          <dt className="text-muted-foreground">Taille</dt>
          <dd className="tabular-nums">{formatSize(tab.size)}</dd>
          <dt className="text-muted-foreground">Permissions</dt>
          <dd className="font-mono">{tab.mode}</dd>
          <dt className="text-muted-foreground">Modifié</dt>
          <dd>{new Date(tab.mtime).toLocaleString('fr-FR')}</dd>
        </dl>
        <a href={downloadURL(tab.path)} download className="ui-button ui-focus-ring mt-5 w-full" data-variant="primary" data-size="md">
          <Download size={14} aria-hidden /> Télécharger
        </a>
      </div>
    </div>
  )
}
