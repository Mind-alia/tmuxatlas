import { useEffect } from 'react'

const viewportProperties = [
  '--visual-viewport-height',
  '--visual-viewport-width',
  '--visual-viewport-offset-top',
  '--visual-viewport-offset-left',
] as const

/** Fields that bring up a phone keyboard when they take focus. */
function editableFocused(): boolean {
  const element = document.activeElement
  if (!(element instanceof HTMLElement)) return false
  if (element.isContentEditable || element instanceof HTMLTextAreaElement) return true
  return element instanceof HTMLInputElement
    && !['button', 'checkbox', 'color', 'file', 'image', 'radio', 'range', 'reset', 'submit'].includes(element.type)
}

function px(value: number): string {
  return `${Math.max(0, Math.round(value))}px`
}

/**
 * Mirrors the browser's visual viewport into CSS variables. iOS moves and
 * shrinks this viewport while its software keyboard is visible, while the
 * layout viewport (and 100vh) can remain unchanged. Chrome on Android shrinks
 * the whole page instead, as index.html asks with interactive-widget=resizes-content.
 */
export function useVisualViewportVariables(): void {
  useEffect(() => {
    const root = document.documentElement
    const previous = new Map(viewportProperties.map(property => [
      property,
      root.style.getPropertyValue(property),
    ]))
    const previousConstrained = root.getAttribute('data-visual-viewport-constrained')
    const previousKeyboard = root.getAttribute('data-visual-keyboard-open')
    let frame: number | null = null
    // The page height without a keyboard, per width so that rotating starts over.
    // It is recorded only while no field has focus: Chrome can shrink the page
    // before it reports the keyboard, and that height must not become the baseline.
    const baselines = new Map<number, number>()

    const update = () => {
      frame = null
      const viewport = window.visualViewport
      const height = viewport?.height ?? window.innerHeight
      const width = viewport?.width ?? window.innerWidth
      const offsetTop = viewport?.offsetTop ?? 0
      const offsetLeft = viewport?.offsetLeft ?? 0
      root.style.setProperty(
        '--visual-viewport-height',
        px(height),
      )
      root.style.setProperty(
        '--visual-viewport-width',
        px(width),
      )
      root.style.setProperty(
        '--visual-viewport-offset-top',
        px(offsetTop),
      )
      root.style.setProperty(
        '--visual-viewport-offset-left',
        px(offsetLeft),
      )
      root.toggleAttribute('data-visual-viewport-constrained', height < 420)
      const layoutHeight = window.innerHeight
      const widthKey = Math.round(window.innerWidth)
      const focused = editableFocused()
      // The largest height wins, so a keyboard still sliding down after a blur never lowers it.
      if (!focused) baselines.set(widthKey, Math.max(baselines.get(widthKey) ?? 0, layoutHeight))
      const baseline = baselines.get(widthKey) ?? layoutHeight
      // iOS and Chrome's default shrink only the visual viewport under the keyboard.
      const overlaid = Boolean(viewport && height + offsetTop <= layoutHeight - 64)
      // With interactive-widget=resizes-content, Chrome on Android shrinks the whole page instead.
      const resized = focused && layoutHeight <= baseline - 120
      root.toggleAttribute('data-visual-keyboard-open', overlaid || resized)
    }

    const schedule = () => {
      if (frame !== null) return
      frame = window.requestAnimationFrame(update)
    }

    update()
    const viewport = window.visualViewport
    viewport?.addEventListener('resize', schedule)
    viewport?.addEventListener('scroll', schedule)
    window.addEventListener('resize', schedule)
    window.addEventListener('orientationchange', schedule)
    document.addEventListener('focusin', schedule)
    document.addEventListener('focusout', schedule)

    return () => {
      viewport?.removeEventListener('resize', schedule)
      viewport?.removeEventListener('scroll', schedule)
      window.removeEventListener('resize', schedule)
      window.removeEventListener('orientationchange', schedule)
      document.removeEventListener('focusin', schedule)
      document.removeEventListener('focusout', schedule)
      if (frame !== null) window.cancelAnimationFrame(frame)
      for (const property of viewportProperties) {
        const value = previous.get(property)
        if (value) root.style.setProperty(property, value)
        else root.style.removeProperty(property)
      }
      if (previousConstrained === null) root.removeAttribute('data-visual-viewport-constrained')
      else root.setAttribute('data-visual-viewport-constrained', previousConstrained)
      if (previousKeyboard === null) root.removeAttribute('data-visual-keyboard-open')
      else root.setAttribute('data-visual-keyboard-open', previousKeyboard)
    }
  }, [])
}
