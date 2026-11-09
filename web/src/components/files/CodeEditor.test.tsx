import { useState } from 'react'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { CodeEditor, type CodeEditorProps, type CursorInfo } from './CodeEditor'

vi.mock('./highlight', () => {
  const escapeHTML = (t: string) =>
    t.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;')
  return {
    escapeHTML,
    highlightLines: (code: string) => code.split('\n').map(escapeHTML),
    commentToken: (lang: string | null) => (lang === 'html' ? { block: ['<!--', '-->'] } : { line: '//' }),
  }
})

type HarnessProps = Partial<CodeEditorProps> & { initial: string; spy?: (v: string) => void }

function Harness({ initial, spy, ...props }: HarnessProps) {
  const [value, setValue] = useState(initial)
  return (
    <div style={{ height: 300 }}>
      <CodeEditor
        language="javascript"
        {...props}
        value={value}
        onChange={(v) => {
          setValue(v)
          spy?.(v)
        }}
      />
    </div>
  )
}

const textarea = () => screen.getByRole('textbox', { name: 'Éditeur de texte' }) as HTMLTextAreaElement

afterEach(cleanup)

describe('CodeEditor', () => {
  it('renders the value with line numbers', () => {
    const { container } = render(<Harness initial={'const a = 1\n<b>\nlast'} />)
    expect(textarea().value).toBe('const a = 1\n<b>\nlast')
    const gutters = [...container.querySelectorAll('[data-gutter]')].map((el) => el.textContent)
    expect(gutters).toEqual(['1', '2', '3'])
    // The overlay shows escaped text, never raw HTML.
    expect(container.querySelector('.ce-rows b')).toBeNull()
    expect(container.querySelector('.ce-rows')).toHaveTextContent('<b>')
    expect(container.querySelector('.ce-rows')).toHaveAttribute('aria-hidden', 'true')
  })

  it('calls onChange when typing', async () => {
    const spy = vi.fn()
    render(<Harness initial="" spy={spy} />)
    const user = userEvent.setup()
    await user.click(textarea())
    await user.keyboard('hi')
    expect(spy).toHaveBeenLastCalledWith('hi')
    expect(textarea().value).toBe('hi')
  })

  it('auto-closes brackets through the fallback edit path', async () => {
    const spy = vi.fn()
    render(<Harness initial="" spy={spy} />)
    const user = userEvent.setup()
    await user.click(textarea())
    await user.keyboard('f(')
    expect(textarea().value).toBe('f()')
    expect(textarea().selectionStart).toBe(2)
  })

  it('calls onSave on Ctrl+S and prevents the browser default', () => {
    const onSave = vi.fn()
    render(<Harness initial="x" onSave={onSave} />)
    const event = fireEvent.keyDown(textarea(), { key: 's', ctrlKey: true })
    expect(onSave).toHaveBeenCalledTimes(1)
    expect(event).toBe(false)
    fireEvent.keyDown(textarea(), { key: 's', metaKey: true })
    expect(onSave).toHaveBeenCalledTimes(2)
  })

  it('inserts indentation on Tab', async () => {
    const spy = vi.fn()
    render(<Harness initial="abc" spy={spy} tabSize={4} />)
    const ta = textarea()
    const user = userEvent.setup()
    await user.click(ta)
    ta.setSelectionRange(0, 0)
    await user.keyboard('{Tab}')
    expect(spy).toHaveBeenLastCalledWith('    abc')
    expect(ta.selectionStart).toBe(4)
    expect(document.activeElement).toBe(ta)
  })

  it('auto-indents on Enter', async () => {
    render(<Harness initial="  if (x) {}" />)
    const ta = textarea()
    const user = userEvent.setup()
    await user.click(ta)
    ta.setSelectionRange(10, 10)
    await user.keyboard('{Enter}')
    expect(ta.value).toBe('  if (x) {\n    \n  }')
    expect(ta.selectionStart).toBe(15)
  })

  it('toggles a line comment with Ctrl+/', () => {
    render(<Harness initial="foo()" />)
    const ta = textarea()
    ta.focus()
    ta.setSelectionRange(1, 1)
    fireEvent.keyDown(ta, { key: '/', ctrlKey: true })
    expect(ta.value).toBe('// foo()')
    fireEvent.keyDown(ta, { key: '/', ctrlKey: true })
    expect(ta.value).toBe('foo()')
  })

  it('prevents edits when readOnly', async () => {
    const spy = vi.fn()
    render(<Harness initial="abc" spy={spy} readOnly />)
    const ta = textarea()
    expect(ta).toHaveAttribute('readonly')
    const user = userEvent.setup()
    await user.click(ta)
    await user.keyboard('zz(')
    fireEvent.keyDown(ta, { key: 'd', ctrlKey: true })
    expect(spy).not.toHaveBeenCalled()
    expect(ta.value).toBe('abc')
  })

  it('opens the find bar with Ctrl+F, counts matches and closes on Escape', async () => {
    render(<Harness initial={'foo bar\nfoo baz foo'} />)
    const ta = textarea()
    ta.focus()
    ta.setSelectionRange(0, 0)
    fireEvent.keyDown(ta, { key: 'f', ctrlKey: true })
    const input = await screen.findByRole('textbox', { name: 'Rechercher' })
    await waitFor(() => expect(document.activeElement).toBe(input))

    const user = userEvent.setup()
    await user.type(input, 'foo')
    expect(screen.getByRole('status')).toHaveTextContent('1 / 3')
    expect([ta.selectionStart, ta.selectionEnd]).toEqual([0, 3])

    await user.keyboard('{Enter}')
    expect(screen.getByRole('status')).toHaveTextContent('2 / 3')
    expect([ta.selectionStart, ta.selectionEnd]).toEqual([8, 11])

    await user.keyboard('{Shift>}{Enter}{/Shift}')
    expect(screen.getByRole('status')).toHaveTextContent('1 / 3')

    await user.clear(input)
    await user.type(input, 'nope')
    expect(screen.getByRole('status')).toHaveTextContent('Aucun résultat')

    await user.keyboard('{Escape}')
    expect(screen.queryByRole('textbox', { name: 'Rechercher' })).toBeNull()
    expect(document.activeElement).toBe(ta)
  })

  it('reports invalid regular expressions', async () => {
    render(<Harness initial="abc" />)
    fireEvent.keyDown(textarea(), { key: 'f', ctrlKey: true })
    const input = await screen.findByRole('textbox', { name: 'Rechercher' })
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Expression régulière' }))
    fireEvent.change(input, { target: { value: '(' } })
    expect(screen.getByRole('status')).toHaveTextContent('Regex invalide')
  })

  it('replaces one and all matches', async () => {
    const spy = vi.fn()
    render(<Harness initial="a x a x a" spy={spy} />)
    const ta = textarea()
    ta.focus()
    ta.setSelectionRange(0, 0)
    fireEvent.keyDown(ta, { key: 'h', ctrlKey: true })
    const find = await screen.findByRole('textbox', { name: 'Rechercher' })
    fireEvent.change(find, { target: { value: 'x' } })
    fireEvent.change(screen.getByRole('textbox', { name: 'Remplacer' }), { target: { value: 'yy' } })

    fireEvent.click(screen.getByRole('button', { name: 'Remplacer' }))
    expect(ta.value).toBe('a yy a x a')
    fireEvent.click(screen.getByRole('button', { name: 'Tout remplacer' }))
    expect(ta.value).toBe('a yy a yy a')
    expect(screen.getByRole('status')).toHaveTextContent('Aucun résultat')
  })

  it('goes to a line with Ctrl+G', async () => {
    render(<Harness initial={'one\ntwo\nthree'} />)
    const ta = textarea()
    ta.focus()
    fireEvent.keyDown(ta, { key: 'g', ctrlKey: true })
    const input = await screen.findByRole('textbox', { name: 'Aller à la ligne' })
    fireEvent.change(input, { target: { value: '3' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(ta.selectionStart).toBe(8)
    expect(screen.queryByRole('textbox', { name: 'Aller à la ligne' })).toBeNull()
  })

  it('reports cursor info', async () => {
    const onCursorChange = vi.fn<(info: CursorInfo) => void>()
    render(<Harness initial={'ab\ncdef'} onCursorChange={onCursorChange} />)
    const ta = textarea()
    act(() => {
      ta.focus()
      ta.setSelectionRange(4, 6)
      fireEvent.select(ta)
    })
    await waitFor(() =>
      expect(onCursorChange).toHaveBeenLastCalledWith({ line: 2, column: 4, selected: 2, lines: 2 }),
    )
  })

  it('exposes focus and revealLine through the ref', () => {
    const ref = { current: null as import('./CodeEditor').CodeEditorHandle | null }
    render(<CodeEditor ref={ref} value={'a\nb\nc'} onChange={() => {}} language={null} ariaLabel="Code" />)
    const ta = screen.getByRole('textbox', { name: 'Code' }) as HTMLTextAreaElement
    act(() => ref.current!.focus())
    expect(document.activeElement).toBe(ta)
    act(() => ref.current!.revealLine(3))
    expect(ta.selectionStart).toBe(4)
  })

  it('renders huge documents as plain blocks', () => {
    const big = Array.from({ length: 60_000 }, (_, i) => `line ${i}`).join('\n')
    const { container } = render(<CodeEditor value={big} onChange={() => {}} language="javascript" wrap />)
    expect(container.querySelector('.ce-rows')).toBeNull()
    const pres = container.querySelectorAll('pre')
    expect(pres).toHaveLength(2)
    expect(pres[0].textContent?.split('\n')).toHaveLength(60_000)
    expect(textarea()).toHaveAttribute('wrap', 'off')
  })
})
