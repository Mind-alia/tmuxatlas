import { describe, expect, it } from 'vitest'
import { linuxSetupCommands } from './linuxSetup'

describe('linuxSetupCommands', () => {
  it('creates a private account without sudo or docker and pairs it with the given code', () => {
    const text = linuxSetupCommands({ unixUser: 'inv-alice', hubURL: 'https://atlas.example', pairingCode: 'BA-BE' })
    expect(text).toContain('useradd --create-home --shell /bin/bash inv-alice')
    expect(text).toContain('chmod 700 /home/inv-alice')
    expect(text).toContain('gpasswd -d inv-alice docker')
    expect(text).toContain('loginctl enable-linger inv-alice')
    expect(text).toContain('pair --hub https://atlas.example --code BA-BE --name inv-alice@$(hostname)')
    expect(text).toContain("TMUXATLAS_FILES=rw")
    expect(text).not.toMatch(/usermod -aG|--groups/)
  })

  it('never installs a binary from the current directory', () => {
    const text = linuxSetupCommands({ unixUser: 'inv-alice', hubURL: 'https://atlas.example' })
    expect(text).not.toMatch(/\.\/tmuxatlas|install -o root/)
    expect(text).toContain("stat -c '%U:%G %a %n' /opt/tmuxatlas /opt/tmuxatlas/tmuxatlas")
    for (const line of text.split('\n').filter(line => /tmuxatlas (pair|install)/.test(line))) {
      expect(line.startsWith('/opt/tmuxatlas/tmuxatlas ')).toBe(true)
    }
  })

  it('refuses names that could inject shell syntax', () => {
    expect(linuxSetupCommands({ unixUser: 'alice; rm -rf /', hubURL: 'https://h' })).toBe('')
    expect(linuxSetupCommands({ unixUser: 'Root', hubURL: 'https://h' })).toBe('')
  })
})
