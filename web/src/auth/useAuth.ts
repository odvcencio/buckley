import { useCallback, useEffect, useState } from 'react'

import type { AuthState } from './types'
import { consumeAuthTokenFromURL, clearStoredAuthToken, getStoredAuthToken, setStoredAuthToken } from './token'
import { ApiError, getAuthSession, logout } from '../lib/api'

export function useAuth(): {
  state: AuthState
  savedToken: string
  login: (token: string) => Promise<void>
  clearToken: () => Promise<void>
} {
  const [state, setState] = useState<AuthState>({ status: 'checking' })
  const [savedToken, setSavedToken] = useState(() => getStoredAuthToken())

  const attempt = useCallback(async () => {
    try {
      const session = await getAuthSession()
      setState({ status: 'ready', principal: session.principal })
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        setState({ status: 'needs_token', message: 'Token required to access this Buckley instance.' })
        return
      }
      setState({
        status: 'needs_token',
        message: err instanceof Error ? err.message : 'Unable to authenticate.',
      })
    }
  }, [])

  useEffect(() => {
    const token = consumeAuthTokenFromURL()
    if (token) {
      setStoredAuthToken(token)
      Promise.resolve().then(() => setSavedToken(token))
    }
    Promise.resolve().then(() => attempt())
  }, [attempt])

  const login = useCallback(
    async (token: string) => {
      const trimmed = token.trim()
      if (!trimmed) {
        setState({ status: 'needs_token', message: 'Enter a token to continue.' })
        return
      }
      setStoredAuthToken(trimmed)
      setSavedToken(trimmed)
      await attempt()
    },
    [attempt]
  )

  const clearToken = useCallback(async () => {
    try {
      await logout()
    } catch {
      // ignore
    }
    clearStoredAuthToken()
    setSavedToken('')
    setState({ status: 'needs_token', message: 'Signed out.' })
  }, [])

  return { state, savedToken, login, clearToken }
}
