/**
 * Commands an administrator copies to give an invited person their own Linux
 * account and peer. TmuxAtlas never runs them: creating a Linux account needs
 * root, which stays a human step.
 */
export interface LinuxSetupInput {
  unixUser: string
  hubURL: string
  pairingCode?: string
}

const UNIX_USER_RE = /^[a-z_][a-z0-9_-]{0,31}$/

export function isValidUnixUser(value: string): boolean {
  return UNIX_USER_RE.test(value)
}

export function linuxSetupCommands({ unixUser, hubURL, pairingCode }: LinuxSetupInput): string {
  if (!isValidUnixUser(unixUser)) return ''
  const code = pairingCode || '<CODE>'
  const home = `/home/${unixUser}`
  return [
    '# 1. En root : un compte Linux pour cette personne seule, sans sudo ni docker',
    `useradd --create-home --shell /bin/bash ${unixUser}`,
    `chmod 700 ${home}`,
    `gpasswd -d ${unixUser} docker 2>/dev/null; gpasswd -d ${unixUser} sudo 2>/dev/null`,
    `id ${unixUser}   # vérifier : ni docker, ni sudo, ni wheel, ni adm`,
    `loginctl enable-linger ${unixUser}`,
    '',
    '# 2. En root : installer un binaire relu, que ni ce compte ni agents ne peuvent modifier',
    `install -d -o root -g root -m 755 /opt/tmuxatlas`,
    `install -o root -g root -m 755 ./tmuxatlas /opt/tmuxatlas/tmuxatlas`,
    '',
    '# 3. Sous ce compte (session complète pour systemd --user)',
    `machinectl shell ${unixUser}@`,
    `/opt/tmuxatlas/tmuxatlas pair --hub ${hubURL} --code ${code} --name ${unixUser}@$(hostname)`,
    '# Plafond local des fichiers, que le hub ne peut pas dépasser : off, ro ou rw',
    `printf 'TMUXATLAS_FILES=rw\\n' >> ~/.config/tmuxatlas/.env`,
    `/opt/tmuxatlas/tmuxatlas install --mode agent`,
  ].join('\n')
}
