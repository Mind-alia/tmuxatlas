import { useCallback, useEffect, useRef, useState, type PointerEvent, type WheelEvent } from 'react'
import { Expand, Minus, Plus, Scan } from 'lucide-react'
import { IconButton } from '../ui'
import { rawURL } from './filesApi'

const MIN_ZOOM = 0.05
const MAX_ZOOM = 32

type Fit = 'fit' | 'manual'

/**
 * Shows an image with fit-to-view, actual size, wheel and pinch zoom and
 * drag to pan. A checkerboard reveals transparent areas.
 */
export function ImageViewer({ path, name, version }: { path: string; name: string; version?: string }) {
  const viewportRef = useRef<HTMLDivElement>(null)
  const [natural, setNatural] = useState<{ width: number; height: number } | null>(null)
  const [failed, setFailed] = useState(false)
  const [fit, setFit] = useState<Fit>('fit')
  const [zoom, setZoom] = useState(1)
  const [offset, setOffset] = useState({ x: 0, y: 0 })
  const [viewport, setViewport] = useState({ width: 0, height: 0 })
  const drag = useRef<{ x: number; y: number; ox: number; oy: number } | null>(null)
  const pointers = useRef(new Map<number, { x: number; y: number }>())
  const pinch = useRef<{ distance: number; zoom: number } | null>(null)
  const src = `${rawURL(path)}${version ? `&v=${encodeURIComponent(version)}` : ''}`

  useEffect(() => {
    setNatural(null)
    setFailed(false)
    setFit('fit')
    setOffset({ x: 0, y: 0 })
  }, [src])

  useEffect(() => {
    const element = viewportRef.current
    if (!element) return
    const update = () => setViewport({ width: element.clientWidth, height: element.clientHeight })
    update()
    if (typeof ResizeObserver === 'undefined') {
      window.addEventListener('resize', update)
      return () => window.removeEventListener('resize', update)
    }
    const observer = new ResizeObserver(update)
    observer.observe(element)
    return () => observer.disconnect()
  }, [])

  const fitZoom =
    natural && viewport.width && viewport.height
      ? Math.min(1, (viewport.width - 48) / natural.width, (viewport.height - 48) / natural.height)
      : 1
  const scale = fit === 'fit' ? Math.max(fitZoom, MIN_ZOOM) : zoom

  const setManualZoom = useCallback(
    (next: number) => {
      setFit('manual')
      setZoom(Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, next)))
    },
    [],
  )

  const onWheel = (event: WheelEvent) => {
    if (!natural) return
    event.preventDefault()
    const factor = Math.exp(-event.deltaY * (event.ctrlKey ? 0.01 : 0.0025))
    setManualZoom(scale * factor)
  }

  const onPointerDown = (event: PointerEvent) => {
    pointers.current.set(event.pointerId, { x: event.clientX, y: event.clientY })
    ;(event.currentTarget as HTMLElement).setPointerCapture(event.pointerId)
    if (pointers.current.size === 2) {
      const [a, b] = Array.from(pointers.current.values())
      pinch.current = { distance: Math.hypot(a.x - b.x, a.y - b.y), zoom: scale }
      drag.current = null
    } else {
      drag.current = { x: event.clientX, y: event.clientY, ox: offset.x, oy: offset.y }
    }
  }
  const onPointerMove = (event: PointerEvent) => {
    if (!pointers.current.has(event.pointerId)) return
    pointers.current.set(event.pointerId, { x: event.clientX, y: event.clientY })
    if (pinch.current && pointers.current.size === 2) {
      const [a, b] = Array.from(pointers.current.values())
      setManualZoom(pinch.current.zoom * (Math.hypot(a.x - b.x, a.y - b.y) / pinch.current.distance))
      return
    }
    if (drag.current) {
      setOffset({
        x: drag.current.ox + event.clientX - drag.current.x,
        y: drag.current.oy + event.clientY - drag.current.y,
      })
    }
  }
  const onPointerUp = (event: PointerEvent) => {
    pointers.current.delete(event.pointerId)
    if (pointers.current.size < 2) pinch.current = null
    if (pointers.current.size === 0) drag.current = null
  }

  const isSVG = name.toLowerCase().endsWith('.svg')

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-center gap-1 border-b border-border px-3 py-1.5 text-xs text-muted-foreground">
        <span className="tabular-nums">
          {natural ? `${natural.width} × ${natural.height} px` : failed ? 'Aperçu impossible' : 'Chargement…'}
        </span>
        <div className="flex-1" />
        <IconButton aria-label="Dézoomer" size="sm" onClick={() => setManualZoom(scale / 1.25)} disabled={!natural}>
          <Minus size={14} aria-hidden />
        </IconButton>
        <button
          type="button"
          className="ui-focus-ring min-w-14 rounded px-1.5 py-1 text-center tabular-nums hover:bg-secondary hover:text-foreground"
          onClick={() => setManualZoom(1)}
          title="Taille réelle"
          disabled={!natural}
        >
          {Math.round(scale * 100)} %
        </button>
        <IconButton aria-label="Zoomer" size="sm" onClick={() => setManualZoom(scale * 1.25)} disabled={!natural}>
          <Plus size={14} aria-hidden />
        </IconButton>
        <IconButton
          aria-label="Ajuster à la fenêtre"
          size="sm"
          onClick={() => {
            setFit('fit')
            setOffset({ x: 0, y: 0 })
          }}
          disabled={!natural}
        >
          <Scan size={14} aria-hidden />
        </IconButton>
        <a
          className="ui-focus-ring inline-flex size-8 items-center justify-center rounded hover:bg-secondary hover:text-foreground"
          href={src}
          target="_blank"
          rel="noopener noreferrer"
          aria-label="Ouvrir dans un nouvel onglet"
          title="Ouvrir dans un nouvel onglet"
        >
          <Expand size={14} aria-hidden />
        </a>
      </div>
      <div
        ref={viewportRef}
        className="files-checkerboard relative min-h-0 flex-1 cursor-grab touch-none overflow-hidden select-none active:cursor-grabbing"
        onWheel={onWheel}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerUp}
        onDoubleClick={() => (fit === 'fit' ? setManualZoom(1) : setFit('fit'))}
      >
        {failed ? (
          <div className="absolute inset-0 grid place-items-center p-6 text-center text-sm text-muted-foreground">
            Le navigateur ne sait pas afficher cette image.
          </div>
        ) : (
          <img
            src={src}
            alt={name}
            draggable={false}
            onLoad={event => {
              const image = event.currentTarget
              // An SVG without intrinsic size reports 0: give it a sensible box.
              setNatural({ width: image.naturalWidth || 512, height: image.naturalHeight || 512 })
            }}
            onError={() => setFailed(true)}
            className="absolute top-1/2 left-1/2 max-w-none"
            style={{
              width: natural ? natural.width * scale : undefined,
              height: natural ? natural.height * scale : undefined,
              transform: `translate(calc(-50% + ${offset.x}px), calc(-50% + ${offset.y}px))`,
              imageRendering: scale >= 4 && !isSVG ? 'pixelated' : 'auto',
              visibility: natural ? 'visible' : 'hidden',
            }}
          />
        )}
      </div>
    </div>
  )
}

export function VideoViewer({ path }: { path: string }) {
  return (
    <div className="grid min-h-0 flex-1 place-items-center bg-black/80 p-4">
      <video src={rawURL(path)} controls className="max-h-full max-w-full rounded" preload="metadata" />
    </div>
  )
}

export function AudioViewer({ path, name }: { path: string; name: string }) {
  return (
    <div className="grid min-h-0 flex-1 place-items-center p-6">
      <div className="w-full max-w-md rounded-lg border border-border bg-card p-5">
        <p className="mb-3 truncate text-sm">{name}</p>
        <audio src={rawURL(path)} controls className="w-full" preload="metadata" />
      </div>
    </div>
  )
}
