import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MarkdownView, parseMarkdown, slugify, type Block, type Inline } from './markdown'

vi.mock('./highlight', () => ({
  highlightToHTML: (code: string, lang: string | null) =>
    `<span data-lang="${lang ?? ''}">${code.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')}</span>`,
  languageFor: (name: string) => {
    const ext = name.slice(name.lastIndexOf('.') + 1)
    return ({ ts: 'typescript', go: 'go', sh: 'shell', py: 'python' } as Record<string, string>)[ext] ?? null
  },
}))

afterEach(cleanup)

beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
})

const view = (source: string, props: { baseDir?: string; onOpenPath?: (path: string) => void } = {}) =>
  render(<MarkdownView source={source} baseDir={props.baseDir ?? '/home/me/docs'} onOpenPath={props.onOpenPath} />)

const first = <T extends Block['type']>(blocks: Block[], type: T) =>
  blocks.find(b => b.type === type) as Extract<Block, { type: T }>

describe('slugify', () => {
  it.each([
    ['Hello World', 'hello-world'],
    ['  Café crème  ', 'café-crème'],
    ['API: v2 (beta)!', 'api-v2-beta'],
    ['snake_case & kebab-case', 'snake_case--kebab-case'],
    ['Ünïcödé 東京', 'ünïcödé-東京'],
  ])('%s → %s', (input, expected) => {
    expect(slugify(input)).toBe(expected)
  })
})

describe('headings', () => {
  it('parses ATX and setext headings with deduplicated ids', () => {
    const blocks = parseMarkdown('# Intro\n\n## Intro ##\n\nTitre é\n===\n\nSous-titre\n---\n\n# Intro')
    const headings = blocks.filter(b => b.type === 'heading') as Extract<Block, { type: 'heading' }>[]
    expect(headings.map(h => [h.level, h.id])).toEqual([
      [1, 'intro'],
      [2, 'intro-1'],
      [1, 'titre-é'],
      [2, 'sous-titre'],
      [1, 'intro-2'],
    ])
  })

  it('renders ids on heading elements, using plain text of inline markup', () => {
    view('## The `config` *file*')
    const heading = screen.getByRole('heading', { level: 2, name: 'The config file' })
    expect(heading).toHaveAttribute('id', 'the-config-file')
  })

  it('does not treat #hashtag as a heading', () => {
    expect(first(parseMarkdown('#hashtag'), 'paragraph')).toBeTruthy()
  })
})

describe('inline formatting', () => {
  it('nests emphasis, strong and strikethrough', () => {
    const { container } = view('***both*** and **bold _inner_** and ~~gone~~ and snake_case_word')
    expect(container.querySelector('em > strong, strong > em')).toHaveTextContent('both')
    expect(container.querySelector('strong em')).toHaveTextContent('inner')
    expect(container.querySelector('del')).toHaveTextContent('gone')
    expect(container.querySelectorAll('em')).toHaveLength(2)
    expect(container).toHaveTextContent('snake_case_word')
  })

  it('parses multi-backtick code spans without formatting inside', () => {
    const { container } = view('Use ``a ` b`` and `*x*`')
    const codes = container.querySelectorAll('code')
    expect(Array.from(codes, c => c.textContent)).toEqual(['a ` b', '*x*'])
    expect(container.querySelector('em')).toBeNull()
  })

  it('handles hard breaks, escapes and entities', () => {
    const { container } = view('line one  \nline two\\\nline three\n\n\\*not em\\* &copy; &#169; &amp; &lt;b&gt;')
    expect(container.querySelectorAll('br')).toHaveLength(2)
    expect(container.querySelector('em')).toBeNull()
    expect(container).toHaveTextContent('*not em* © © & <b>')
  })
})

describe('code blocks', () => {
  it('parses fenced and indented code with info strings', () => {
    const blocks = parseMarkdown('```ts title\nconst a = 1\n```\n\n~~~\nplain\n~~~\n\n    indented\n    code')
    expect(blocks).toEqual([
      { type: 'code', info: 'ts title', code: 'const a = 1' },
      { type: 'code', info: '', code: 'plain' },
      { type: 'code', info: '', code: 'indented\ncode' },
    ])
  })

  it('highlights with the language resolved from the fence info', () => {
    const { container } = view('```bash\necho <hi>\n```\n\n```golang\nfunc main() {}\n```\n\n```ts\nlet x\n```')
    const spans = container.querySelectorAll('pre code span[data-lang]')
    expect(Array.from(spans, s => s.getAttribute('data-lang'))).toEqual(['shell', 'go', 'typescript'])
    expect(spans[0].textContent).toBe('echo <hi>')
    expect(screen.getByText('bash')).toBeInTheDocument()
  })

  it('copies the code and confirms for a moment', async () => {
    vi.useFakeTimers()
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } })
    view('```\nmake build\n```')
    fireEvent.click(screen.getByRole('button', { name: 'Copier' }))
    expect(writeText).toHaveBeenCalledWith('make build')
    await vi.waitFor(() => expect(screen.getByRole('button', { name: 'Copié' })).toBeInTheDocument())
    await act(() => vi.advanceTimersByTimeAsync(1600))
    expect(screen.getByRole('button', { name: 'Copier' })).toBeInTheDocument()
    vi.useRealTimers()
  })

  it('renders front matter as a muted block', () => {
    const blocks = parseMarkdown('---\ntitle: Doc\n---\n# Hello')
    expect(blocks[0]).toEqual({ type: 'frontmatter', code: 'title: Doc' })
    expect(blocks[1]).toMatchObject({ type: 'heading', id: 'hello' })
  })
})

describe('lists', () => {
  it('nests lists by indentation and keeps ordered start numbers', () => {
    const src = '3. three\n4. four\n   - nested a\n   - nested b\n     1. deep\n5. five'
    const list = first(parseMarkdown(src), 'list')
    expect(list).toMatchObject({ ordered: true, start: 3, loose: false })
    expect(list.items).toHaveLength(3)
    const nested = first(list.items[1].children, 'list')
    expect(nested.ordered).toBe(false)
    expect(nested.items).toHaveLength(2)
    expect(first(nested.items[1].children, 'list').ordered).toBe(true)

    const { container } = view(src)
    expect(container.querySelector('ol')).toHaveAttribute('start', '3')
    expect(container.querySelector('ol')?.children).toHaveLength(3)
    expect(container.querySelectorAll('ul > li')).toHaveLength(2)
    expect(container.querySelectorAll('ul ol > li')).toHaveLength(1)
    // Tight lists do not wrap items in paragraphs.
    expect(container.querySelector('li p')).toBeNull()
  })

  it('detects loose lists', () => {
    const list = first(parseMarkdown('- a\n\n- b\n'), 'list')
    expect(list.loose).toBe(true)
    const { container } = view('- a\n\n- b\n')
    expect(container.querySelectorAll('li p')).toHaveLength(2)
  })

  it('does not merge a following list item lazily', () => {
    const list = first(parseMarkdown('1. a\n2. b\n3. c'), 'list')
    expect(list.items).toHaveLength(3)
  })

  it('renders task lists as disabled checkboxes', () => {
    const list = first(parseMarkdown('- [ ] todo\n- [x] done\n- plain'), 'list')
    expect(list.items.map(i => i.checked)).toEqual([false, true, null])
    view('- [ ] todo\n- [x] done')
    const boxes = screen.getAllByRole('checkbox')
    expect(boxes).toHaveLength(2)
    expect(boxes[0]).not.toBeChecked()
    expect(boxes[1]).toBeChecked()
    boxes.forEach(box => expect(box).toBeDisabled())
    expect(screen.getByText('done')).toBeInTheDocument()
  })
})

describe('tables', () => {
  it('parses alignment and inline formatting in cells', () => {
    const src = '| Left | Center | Right | None |\n|:-----|:------:|------:|------|\n| **a** | `b` | c \\| d | e |\n| 1 |'
    const table = first(parseMarkdown(src), 'table')
    expect(table.align).toEqual(['left', 'center', 'right', null])
    expect(table.rows).toHaveLength(2)
    expect(table.rows[1]).toHaveLength(4)

    view(src)
    const grid = screen.getByRole('table')
    const headers = within(grid).getAllByRole('columnheader')
    expect(headers.map(h => h.style.textAlign)).toEqual(['left', 'center', 'right', 'left'])
    const cells = within(grid).getAllByRole('cell')
    expect(cells[0].querySelector('strong')).toHaveTextContent('a')
    expect(cells[1].querySelector('code')).toHaveTextContent('b')
    expect(cells[2]).toHaveTextContent('c | d')
    expect(cells[2].style.textAlign).toBe('right')
  })
})

describe('blockquotes and rules', () => {
  it('nests blockquotes containing other blocks', () => {
    const blocks = parseMarkdown('> # Title\n> outer\n> > inner\n> > - item\n\n---\n***')
    const outer = first(blocks, 'blockquote')
    expect(outer.children.map(b => b.type)).toEqual(['heading', 'paragraph', 'blockquote'])
    const inner = first(outer.children, 'blockquote')
    expect(inner.children.map(b => b.type)).toEqual(['paragraph', 'list'])
    expect(blocks.filter(b => b.type === 'hr')).toHaveLength(2)

    const { container } = view('> a\n> > b')
    expect(container.querySelectorAll('blockquote blockquote')).toHaveLength(1)
  })

  it('continues a quoted paragraph lazily', () => {
    const outer = first(parseMarkdown('> one\ntwo'), 'blockquote')
    expect(outer.children).toHaveLength(1)
  })
})

describe('links and safety', () => {
  it('renders raw HTML as literal text', () => {
    const { container } = view('<script>alert(1)</script>\n\nHi <b onclick="x()">there</b> <img src=x onerror=alert(1)>')
    expect(container.querySelector('script')).toBeNull()
    expect(container.querySelector('b')).toBeNull()
    expect(container.querySelector('img')).toBeNull()
    expect(container).toHaveTextContent('<script>alert(1)</script>')
    expect(container).toHaveTextContent('<b onclick="x()">')
  })

  it('drops unsafe link schemes', () => {
    const { container } = view(
      '[a](javascript:alert(1)) [b](JaVaScRiPt:alert(1)) [c](java\tscript:x) [d](data:text/html,x) [e](vbscript:x) <javascript:alert(1)>',
    )
    expect(container.querySelectorAll('a')).toHaveLength(0)
    expect(container).toHaveTextContent('a b')
  })

  it('opens external links in a new tab', () => {
    view('[site](https://example.com "Example") <https://auto.example> see https://bare.example/path. [mail](mailto:a@b.c)')
    const site = screen.getByRole('link', { name: 'site' })
    expect(site).toHaveAttribute('href', 'https://example.com')
    expect(site).toHaveAttribute('title', 'Example')
    expect(site).toHaveAttribute('target', '_blank')
    expect(site).toHaveAttribute('rel', 'noopener noreferrer')
    expect(screen.getByRole('link', { name: 'https://auto.example' })).toHaveAttribute('href', 'https://auto.example')
    expect(screen.getByRole('link', { name: 'https://bare.example/path' })).toHaveAttribute(
      'href',
      'https://bare.example/path',
    )
    expect(screen.getByRole('link', { name: 'mail' })).toHaveAttribute('target', '_blank')
  })

  it('resolves reference-style links', () => {
    view('[Docs][d] and [d] and [Collapsed][]\n\n[d]: https://docs.example "T"\n[collapsed]: ./c.md')
    expect(screen.getByRole('link', { name: 'Docs' })).toHaveAttribute('href', 'https://docs.example')
    expect(screen.getByRole('link', { name: 'd' })).toHaveAttribute('title', 'T')
    expect(screen.getByRole('link', { name: 'Collapsed' })).toHaveAttribute('href', '#')
    expect(screen.queryByText(/\[d\]:/)).toBeNull()
  })

  it('opens relative links through onOpenPath with the resolved absolute path', () => {
    const onOpenPath = vi.fn()
    view('[guide](../guide/Read%20Me.md#install) and [abs](/etc/hosts)', { onOpenPath })
    fireEvent.click(screen.getByRole('link', { name: 'guide' }))
    expect(onOpenPath).toHaveBeenCalledWith('/home/me/guide/Read Me.md')
    fireEvent.click(screen.getByRole('link', { name: 'abs' }))
    expect(onOpenPath).toHaveBeenLastCalledWith('/etc/hosts')
  })

  it('scrolls to anchors inside the view', () => {
    view('[Jump](#détails-utiles)\n\n## Détails utiles')
    const link = screen.getByRole('link', { name: 'Jump' })
    expect(link).toHaveAttribute('href', '#détails-utiles')
    const event = new MouseEvent('click', { bubbles: true, cancelable: true })
    link.dispatchEvent(event)
    expect(event.defaultPrevented).toBe(true)
    const heading = screen.getByRole('heading', { name: 'Détails utiles' })
    expect(heading.scrollIntoView).toHaveBeenCalled()
    expect(vi.mocked(Element.prototype.scrollIntoView).mock.contexts[0]).toBe(heading)
  })
})

describe('images', () => {
  it('resolves relative images to the raw file endpoint', () => {
    view('![Logo](./img/logo.png?v=2 "The logo") ![remote](https://x.example/a.png) ![data](data:image/png;base64,AAAA)')
    const logo = screen.getByRole('img', { name: 'Logo' })
    expect(logo).toHaveAttribute('src', `/api/files/raw?path=${encodeURIComponent('/home/me/docs/img/logo.png')}`)
    expect(logo).toHaveAttribute('title', 'The logo')
    expect(logo).toHaveAttribute('loading', 'lazy')
    expect(screen.getByRole('img', { name: 'remote' })).toHaveAttribute('src', 'https://x.example/a.png')
    expect(screen.getByRole('img', { name: 'data' })).toHaveAttribute('src', 'data:image/png;base64,AAAA')
  })

  it('refuses unsafe image sources and shows the alt text when broken', () => {
    view('![bad](javascript:alert(1)) ![html](data:text/html,x) ![broken](missing.png)')
    expect(screen.queryByRole('img', { name: 'bad' })).toBeNull()
    expect(screen.getByText('bad')).toBeInTheDocument()
    expect(screen.queryByRole('img', { name: 'html' })).toBeNull()
    fireEvent.error(screen.getByRole('img', { name: 'broken' }))
    expect(screen.queryByRole('img', { name: 'broken' })).toBeNull()
    expect(screen.getByText('broken')).toBeInTheDocument()
  })

  it('supports images inside links', () => {
    view('[![badge](https://img.example/b.svg)](https://ci.example)')
    const link = screen.getByRole('link')
    expect(link).toHaveAttribute('href', 'https://ci.example')
    expect(within(link).getByRole('img', { name: 'badge' })).toBeInTheDocument()
  })
})

describe('footnotes', () => {
  it('numbers referenced footnotes and lists them at the end', () => {
    const blocks = parseMarkdown('Text[^n] again[^a].\n\n[^a]: Second.\n[^n]: First.')
    const notes = blocks[blocks.length - 1] as Extract<Block, { type: 'footnotes' }>
    expect(notes.type).toBe('footnotes')
    expect(notes.items.map(i => [i.label, i.index])).toEqual([
      ['n', 1],
      ['a', 2],
    ])
    view('Text[^n]\n\n[^n]: First.')
    expect(screen.getByRole('link', { name: '[1]' })).toHaveAttribute('href', '#fn-n')
  })
})

describe('performance', () => {
  it('parses a large document quickly and survives pathological input', () => {
    const chunk =
      '## Section\n\nSome *text* with `code`, a [link](https://x.example) and **bold** words.\n\n' +
      '- item one\n- item two\n  - nested\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```js\nconst x = 1\n```\n\n'
    const big = chunk.repeat(Math.ceil(200_000 / chunk.length))
    const t0 = performance.now()
    const blocks = parseMarkdown(big)
    const parseMs = performance.now() - t0
    expect(blocks.length).toBeGreaterThan(1000)
    expect(parseMs).toBeLessThan(1500)

    const nasty = '*a '.repeat(5000) + '`'.repeat(3000) + '['.repeat(5000) + '>'.repeat(2000) + ' x'
    const t1 = performance.now()
    parseMarkdown(nasty)
    parseMarkdown('<a ' + 'b '.repeat(20000))
    expect(performance.now() - t1).toBeLessThan(1500)
  })

  it('keeps all text when collecting inline nodes', () => {
    const para = first(parseMarkdown('a *b **c** d* e'), 'paragraph')
    const text = (nodes: Inline[]): string =>
      nodes.map(n => ('text' in n ? n.text : 'children' in n ? text(n.children) : '')).join('')
    expect(text(para.content)).toBe('a b c d e')
  })
})
