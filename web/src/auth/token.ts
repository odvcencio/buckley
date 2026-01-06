const tokenStorageKey = 'buckley_token'

export function getStoredAuthToken(): string {
  try {
    return localStorage.getItem(tokenStorageKey) || ''
  } catch {
    return ''
  }
}

export function setStoredAuthToken(token: string) {
  const trimmed = token.trim()
  if (!trimmed) return
  try {
    localStorage.setItem(tokenStorageKey, trimmed)
  } catch {
    // ignore storage errors (private mode, disabled storage, etc.)
  }
}

export function clearStoredAuthToken() {
  try {
    localStorage.removeItem(tokenStorageKey)
  } catch {
    // ignore
  }
}

export function getAuthToken(): string | null {
  try {
    const params = new URLSearchParams(window.location.search)
    const urlToken = params.get('token')
    if (urlToken && urlToken.trim()) return urlToken.trim()
  } catch {
    // ignore
  }

  const stored = getStoredAuthToken().trim()
  return stored ? stored : null
}

export function consumeAuthTokenFromURL(): string | null {
  try {
    const url = new URL(window.location.href)
    const token = url.searchParams.get('token')
    if (!token || !token.trim()) return null
    url.searchParams.delete('token')
    window.history.replaceState({}, '', url.toString())
    return token.trim()
  } catch {
    return null
  }
}

