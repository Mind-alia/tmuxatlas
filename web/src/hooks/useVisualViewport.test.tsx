import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { useVisualViewportVariables } from './useVisualViewport'

class MockVisualViewport extends EventTarget {
  height = 640
  width = 390
  offsetTop = 12
  offsetLeft = 0
}

afterEach(() => {
  Object.defineProperty(window, 'visualViewport', {
    configurable: true,
    value: undefined,
  })
})

describe('useVisualViewportVariables', () => {
  it('tracks resize and offset changes and restores prior values', async () => {
    const viewport = new MockVisualViewport()
    Object.defineProperty(window, 'visualViewport', {
      configurable: true,
      value: viewport,
    })
    document.documentElement.style.setProperty('--visual-viewport-height', '777px')

    const { unmount } = renderHook(() => useVisualViewportVariables())

    expect(document.documentElement.style.getPropertyValue('--visual-viewport-height')).toBe('640px')
    expect(document.documentElement.style.getPropertyValue('--visual-viewport-offset-top')).toBe('12px')

    viewport.height = 420
    viewport.offsetTop = 24
    act(() => viewport.dispatchEvent(new Event('resize')))

    await waitFor(() => {
      expect(document.documentElement.style.getPropertyValue('--visual-viewport-height')).toBe('420px')
      expect(document.documentElement.style.getPropertyValue('--visual-viewport-offset-top')).toBe('24px')
    })

    unmount()
    expect(document.documentElement.style.getPropertyValue('--visual-viewport-height')).toBe('777px')
    expect(document.documentElement.style.getPropertyValue('--visual-viewport-offset-top')).toBe('')
  })

  it('detects a keyboard that shrinks the whole page, as Chrome on Android does', async () => {
    const viewport = new MockVisualViewport()
    viewport.offsetTop = 0
    viewport.height = window.innerHeight
    Object.defineProperty(window, 'visualViewport', { configurable: true, value: viewport })
    const fullHeight = window.innerHeight
    const setPageHeight = (height: number) => {
      Object.defineProperty(window, 'innerHeight', { configurable: true, value: height })
      viewport.height = height
      act(() => viewport.dispatchEvent(new Event('resize')))
    }
    const field = document.createElement('textarea')
    document.body.append(field)
    const { unmount } = renderHook(() => useVisualViewportVariables())
    const root = document.documentElement

    try {
      expect(root.hasAttribute('data-visual-keyboard-open')).toBe(false)
      act(() => field.focus())
      setPageHeight(fullHeight - 300)
      await waitFor(() => expect(root.hasAttribute('data-visual-keyboard-open')).toBe(true))

      // Chrome hides its address bar: the page grows a little, with no keyboard involved.
      act(() => field.blur())
      setPageHeight(fullHeight - 56)
      await waitFor(() => expect(root.hasAttribute('data-visual-keyboard-open')).toBe(false))
      act(() => field.focus())
      setPageHeight(fullHeight - 56)
      await waitFor(() => expect(root.hasAttribute('data-visual-keyboard-open')).toBe(false))
    } finally {
      unmount()
      field.remove()
      Object.defineProperty(window, 'innerHeight', { configurable: true, value: fullHeight })
    }
  })
})
