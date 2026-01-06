import type { AuthPrincipal } from './types'

export const scopeOrder = ['viewer', 'member', 'operator'] as const
export type ScopeName = (typeof scopeOrder)[number]

export function hasScope(principal: AuthPrincipal | null, required: ScopeName): boolean {
  if (!principal) return false
  const normalized = (principal.scope || '').toLowerCase()
  const idx = scopeOrder.indexOf(normalized as ScopeName)
  if (idx === -1) return false
  const req = scopeOrder.indexOf(required)
  return idx >= req
}

