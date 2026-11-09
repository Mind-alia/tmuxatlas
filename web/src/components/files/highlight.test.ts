import { describe, it, expect } from 'vitest'
import { commentToken, escapeHTML, highlightLines, highlightToHTML, languageFor, languageLabel } from './highlight'

const unescape = (s: string) =>
  s.replace(/<[^>]*>/g, '').replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&amp;/g, '&')

const LANGS = [
  'javascript', 'typescript', 'json', 'go', 'python', 'rust', 'c', 'cpp', 'java', 'kotlin', 'csharp', 'php',
  'ruby', 'lua', 'shell', 'sql', 'css', 'scss', 'less', 'html', 'xml', 'yaml', 'toml', 'ini', 'conf',
  'markdown', 'dockerfile', 'makefile', 'diff', 'ignore', 'gomod',
]

const SAMPLES = [
  '',
  '<',
  'a < b && c > "d" \'e\' &amp;',
  'const s = "unterminated\nnext line',
  "x = 'it\nmore",
  'const a = `a ${b + `nested ${c}`} z`\nlet d = 1',
  '/* multi\nline\ncomment */ int x;\r\nint y; // end\r\n',
  '"""py\ndoc""" x = 1',
  '<div class="a\n<span>x</span><!-- c\n-->',
  '# Title\n```js\ncode\n```\n**bold** *em* `c` [l](u)\n- item',
  'key: "v\nlist:\n  - a: 1\n  - |\n    block\n',
  '<script>let a = 1</script><style>a{color:#fff}</style><b',
  '</script> `${ {',
  '\\\\\\ "\\',
  '/regex[/]+/g.test(x) / 2',
]

describe('languageFor', () => {
  it.each([
    ['main.go', 'go'], ['a/b/App.tsx', 'typescript'], ['x.mjs', 'javascript'], ['x.py', 'python'],
    ['Dockerfile', 'dockerfile'], ['Dockerfile.dev', 'dockerfile'], ['Makefile', 'makefile'], ['GNUmakefile', 'makefile'],
    ['.bashrc', 'shell'], ['.env', 'shell'], ['.env.local', 'shell'], ['run.sh', 'shell'],
    ['a.yml', 'yaml'], ['a.toml', 'toml'], ['x.service', 'ini'], ['nginx.conf', 'conf'], ['Caddyfile', 'conf'],
    ['.gitignore', 'ignore'], ['go.mod', 'gomod'], ['go.sum', null], ['Cargo.lock', 'toml'], ['yarn.lock', null],
    ['README.md', 'markdown'], ['x.svg', 'xml'], ['index.html', 'html'], ['a.vue', 'html'], ['x.patch', 'diff'],
    ['style.scss', 'scss'], ['data.json', 'json'], ['notes.txt', null], ['noext', null], ['', null],
  ])('%s -> %s', (name, lang) => {
    expect(languageFor(name)).toBe(lang)
  })

  it('labels and comment tokens', () => {
    expect(languageLabel('go')).toBe('Go')
    expect(languageLabel(null)).toBe('Texte brut')
    expect(commentToken('go')).toEqual({ line: '//', block: ['/*', '*/'] })
    expect(commentToken('python')).toEqual({ line: '#' })
    expect(commentToken('html')).toEqual({ block: ['<!--', '-->'] })
    expect(commentToken(null)).toEqual({})
  })
})

describe('safety and structure', () => {
  it('escapes html', () => {
    expect(escapeHTML('<a href="x">&\'</a>')).toBe('&lt;a href=&quot;x&quot;&gt;&amp;&#39;&lt;/a&gt;')
  })

  it('never emits raw input markup', () => {
    for (const lang of [null, ...LANGS]) {
      const html = highlightToHTML('<script>alert(1)</script><img src=x onerror=alert(1)>', lang)
      expect(html).not.toContain('<script')
      expect(html).not.toContain('<img')
      expect(html.replace(/<\/?span[^>]*>/g, '')).not.toMatch(/<(?!\/?span)/)
    }
  })

  it('round-trips every language on tricky inputs', () => {
    for (const lang of [null, ...LANGS]) {
      for (const code of SAMPLES) {
        expect(unescape(highlightToHTML(code, lang)), `${lang}: ${JSON.stringify(code)}`).toBe(code)
      }
    }
  })

  it('round-trips random garbage without hanging', () => {
    let seed = 12345
    const rnd = () => ((seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff)
    const alphabet = ['<', '>', '"', "'", '`', '/', '*', '#', '\n', '\r', ' ', '{', '}', '$', '&', ':', '-', '[', ']', '\\', 'a', '1', '=', '!', '|']
    for (let t = 0; t < 40; t++) {
      let code = ''
      for (let i = 0; i < 300; i++) code += alphabet[Math.floor(rnd() * alphabet.length)]
      for (const lang of LANGS) expect(unescape(highlightToHTML(code, lang))).toBe(code)
    }
  })

  it('never lets a span cross a newline', () => {
    for (const lang of LANGS) {
      for (const code of SAMPLES) {
        const lines = highlightLines(code, lang)
        expect(lines.length).toBe(code.split('\n').length)
        for (const l of lines) {
          expect((l.match(/<span /g) ?? []).length).toBe((l.match(/<\/span>/g) ?? []).length)
          expect(l).not.toMatch(/<span[^>]*>(?:(?!<\/span>).)*$/)
        }
      }
    }
  })

  it('null language is escaped text', () => {
    expect(highlightToHTML('a < b', null)).toBe('a &lt; b')
  })

  it('unterminated string does not swallow the file', () => {
    const html = highlightToHTML('x = "abc\ny = 1', 'javascript')
    expect(html).toContain('<span class="tok-number">1</span>')
  })
})

const has = (code: string, lang: string, kind: string, text: string) =>
  expect(highlightToHTML(code, lang)).toContain(`<span class="tok-${kind}">${text}</span>`)

describe('classification', () => {
  it('go', () => {
    const c = 'package main\nfunc main() {\n\tx := 0x1F // hi\n\tfmt.Println("s", nil)\n\ts := `raw\nmore`\n}'
    has(c, 'go', 'keyword', 'func')
    has(c, 'go', 'function', 'main')
    has(c, 'go', 'number', '0x1F')
    has(c, 'go', 'comment', '// hi')
    has(c, 'go', 'string', '&quot;s&quot;')
    has(c, 'go', 'constant', 'nil')
    has(c, 'go', 'function', 'Println')
    has(c, 'go', 'string', 'more`')
  })

  it('ts', () => {
    const c = 'export const x: string = `a ${b} c`\n@Component()\nclass Foo { run() {} }\nconst r = /ab+c/g'
    has(c, 'typescript', 'keyword', 'export')
    has(c, 'typescript', 'type', 'string')
    has(c, 'typescript', 'meta', '@Component')
    has(c, 'typescript', 'type', 'Foo')
    has(c, 'typescript', 'function', 'run')
    has(c, 'typescript', 'regexp', '/ab+c/g')
    has(c, 'typescript', 'punct', '${')
  })

  it('python', () => {
    const c = '@dec\ndef f(a):\n    """doc\n    more"""\n    return None  # c\n'
    has(c, 'python', 'meta', '@dec')
    has(c, 'python', 'keyword', 'def')
    has(c, 'python', 'function', 'f')
    has(c, 'python', 'constant', 'None')
    has(c, 'python', 'comment', '# c')
    has(c, 'python', 'string', '    more&quot;&quot;&quot;')
  })

  it('shell', () => {
    const c = '#!/bin/bash\nif [ -n "$HOME" ]; then echo ${USER}; fi # done\n'
    has(c, 'shell', 'meta', '#!/bin/bash')
    has(c, 'shell', 'keyword', 'if')
    has(c, 'shell', 'variable', '$HOME')
    has(c, 'shell', 'variable', '${USER}')
    has(c, 'shell', 'comment', '# done')
  })

  it('yaml', () => {
    const c = 'name: app # c\nenabled: true\nports:\n  - 80\nmsg: "hi"\nbase: &a\n  x: *a\n'
    has(c, 'yaml', 'property', 'name')
    has(c, 'yaml', 'comment', '# c')
    has(c, 'yaml', 'constant', 'true')
    has(c, 'yaml', 'number', '80')
    has(c, 'yaml', 'string', '&quot;hi&quot;')
    has(c, 'yaml', 'variable', '&amp;a')
    has(c, 'yaml', 'variable', '*a')
  })

  it('json', () => {
    const c = '{"a": [1, 2.5e3, true, null], "b": "x"}'
    has(c, 'json', 'property', '&quot;a&quot;')
    has(c, 'json', 'string', '&quot;x&quot;')
    has(c, 'json', 'number', '2.5e3')
    has(c, 'json', 'constant', 'null')
  })

  it('html', () => {
    const c = '<!-- c --><div class="a" id=b>&amp;<script>let x = 1</script></div>'
    has(c, 'html', 'comment', '&lt;!-- c --&gt;')
    has(c, 'html', 'tag', 'div')
    has(c, 'html', 'attr', 'class')
    has(c, 'html', 'string', '&quot;a&quot;')
    has(c, 'html', 'constant', '&amp;amp;')
    has(c, 'html', 'keyword', 'let')
  })

  it('markdown', () => {
    const c = '# Title\n\n- **bold** and *em* with `code` and [l](http://x)\n> quote\n```go\nx := 1\n```\n'
    has(c, 'markdown', 'heading', '# Title')
    has(c, 'markdown', 'strong', '**bold**')
    has(c, 'markdown', 'emphasis', '*em*')
    has(c, 'markdown', 'code', '`code`')
    has(c, 'markdown', 'link', '[l](http://x)')
    has(c, 'markdown', 'punct', '-')
    has(c, 'markdown', 'punct', '&gt;')
    has(c, 'markdown', 'code', 'x := 1')
  })

  it('css', () => {
    const c = '@media (min-width: 10px) { .a:hover { color: #fff; margin: 0 auto 4px !important; background: var(--x) } }'
    has(c, 'css', 'keyword', '@media')
    has(c, 'css', 'type', '.a')
    has(c, 'css', 'property', 'color')
    has(c, 'css', 'number', '#fff')
    has(c, 'css', 'number', '4px')
    has(c, 'css', 'function', 'var')
  })

  it('ini / makefile / diff', () => {
    has('[core]\nname = "x"\n', 'ini', 'type', '[core]')
    has('[core]\nname = "x"\n', 'ini', 'property', 'name')
    has('build: dep\n\tgo build $(FLAGS)\n', 'makefile', 'function', 'build')
    has('build: dep\n\tgo build $(FLAGS)\n', 'makefile', 'variable', '$(FLAGS)')
    has('@@ -1 +1 @@\n-a\n+b\n', 'diff', 'meta', '@@ -1 +1 @@')
    has('@@ -1 +1 @@\n-a\n+b\n', 'diff', 'string', '+b')
  })
})

describe('performance', () => {
  it('highlights 300 KB of JS in under 200 ms', () => {
    const chunk = 'function f(a, b) {\n  // comment\n  const s = "str" + `t ${a}`;\n  if (a > 0x1f) return /x+/.test(b) ? 1.5e3 : null;\n}\n'
    let code = ''
    while (code.length < 300_000) code += chunk
    // Warm up the JIT, then keep the best of six runs (CI boxes are noisy).
    highlightToHTML(code, 'javascript')
    let ms = Infinity
    let html = ''
    for (let r = 0; r < 6; r++) {
      const t = performance.now()
      html = highlightToHTML(code, 'javascript')
      ms = Math.min(ms, performance.now() - t)
    }
    expect(html.length).toBeGreaterThan(code.length)
    expect(ms).toBeLessThan(200)
  })
})
