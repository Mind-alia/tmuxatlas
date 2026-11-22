export interface VaultStatus {
  initialized: boolean
  unlocked: boolean
  /** When the private key is forgotten (RFC 3339); absent while locked. */
  expires_at?: string
  /** The server clock, so the countdown does not depend on the browser's. */
  now: string
  ttl_seconds: number
  public_key?: string
  retry_after_seconds?: number
  min_passphrase_length: number
}

export interface SecretTarget {
  env_file: string
  container?: string
}

export interface SecretVariable {
  name: string
  updated_at: string
}

/** An application as the API shows it: names only, never a value. */
export interface SecretApp {
  name: string
  target?: SecretTarget
  variables: SecretVariable[]
}

export interface ImportResult {
  app: SecretApp
  imported: string[]
}

export interface ApplyResult {
  env_file: string
  variables: number
  backup: boolean
  container?: string
  restarted: boolean
  restart_error?: string
}

export interface AuditEntry {
  time: string
  actor: string
  action: string
  app?: string
  name?: string
  detail?: string
}
