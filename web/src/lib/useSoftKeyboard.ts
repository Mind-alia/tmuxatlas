import { useEffect, useState } from 'react'

const ATTRIBUTE = 'data-visual-keyboard-open'

/**
 * Whether a phone keyboard covers part of the page. useVisualViewportVariables
 * sets the attribute on <html> when the visual viewport shrinks under the layout one.
 */
export function useSoftKeyboardOpen(): boolean {
  const [open, setOpen] = useState(() => document.documentElement.hasAttribute(ATTRIBUTE))
  useEffect(() => {
    const root = document.documentElement
    const update = () => setOpen(root.hasAttribute(ATTRIBUTE))
    update()
    const observer = new MutationObserver(update)
    observer.observe(root, { attributes: true, attributeFilter: [ATTRIBUTE] })
    return () => observer.disconnect()
  }, [])
  return open
}

/** Closes the phone keyboard by taking focus away from the field that holds it. */
export function hideSoftKeyboard(): void {
  const active = document.activeElement
  if (active instanceof HTMLElement) active.blur()
}
