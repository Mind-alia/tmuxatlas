import { createContext, useContext } from 'react'

export type AccountRole = 'admin' | 'guest'

/** Null outside the accounts mode: sessions then have no owner to change. */
export const AccountRoleContext = createContext<AccountRole | null>(null)

export function useAccountRole(): AccountRole | null {
  return useContext(AccountRoleContext)
}
