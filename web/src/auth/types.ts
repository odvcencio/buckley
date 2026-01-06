export type AuthPrincipal = {
  name: string
  scope: string
  tokenId?: string
}

export type AuthState =
  | { status: 'checking' }
  | { status: 'needs_token'; message?: string }
  | { status: 'ready'; principal: AuthPrincipal }

