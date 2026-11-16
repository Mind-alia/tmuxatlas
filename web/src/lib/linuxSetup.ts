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

/**
 * Where the reviewed binary lives. Only root writes there, through the
 * deployment procedure (docs/deploiement.md); the commands below never copy a
 * binary from the current directory, which another account may control.
 */
export const TRUSTED_BINARY = '/opt/tmuxatlas/tmuxatlas'

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
    '# 2. En root : vérifier le binaire relu, installé par la procédure de déploiement (docs/deploiement.md).',
    '#    Ne copier aucun binaire depuis le dossier courant : il peut appartenir à un autre compte.',
    `stat -c '%U:%G %a %n' /opt/tmuxatlas ${TRUSTED_BINARY}   # attendu : root:root 755, pour les deux`,
    `${TRUSTED_BINARY} --version`,
    '',
    '# 3. Sous ce compte (session complète pour systemd --user)',
    `machinectl shell ${unixUser}@`,
    `${TRUSTED_BINARY} pair --hub ${hubURL} --code ${code} --name ${unixUser}@$(hostname)`,
    '# Plafond local des fichiers, que le hub ne peut pas dépasser : off, ro ou rw',
    `printf 'TMUXATLAS_FILES=rw\\n' >> ~/.config/tmuxatlas/.env`,
    `${TRUSTED_BINARY} install --mode agent`,
  ].join('\n')
}
