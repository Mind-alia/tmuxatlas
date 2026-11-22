export interface VaultStatus {
  initialized: boolean
  /** True for this browser session only, if it gave the passphrase. */
  unlocked: boolean
  /** Another session holds an unlock; this one still has to unlock itself. */
  unlocked_elsewhere?: boolean
  /** Set for a vault that exists but cannot be read or trusted. */
  problem?: string
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
  /** The application's file is damaged: it can only be deleted. */
  unreadable?: boolean
}

export interface ImportResult {
  app: SecretApp
  imported: string[]
}

export interface ApplyResult {
  env_file: string
  variables: number
  backup: boolean
  /** The file already had this exact content. */
  unchanged?: boolean
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
