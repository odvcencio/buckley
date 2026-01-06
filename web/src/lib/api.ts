import { getAuthToken } from '../auth/token'

type Principal = {
  name: string
  scope: string
  tokenId?: string
}

export type AuthSessionResponse = {
  principal: Principal
  session: {
    expiresAt?: string
  }
}

export class ApiError extends Error {
  status: number

  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

function createAuthHeaders(extra?: Record<string, string>): HeadersInit {
  const headers: Record<string, string> = { ...extra }
  const token = getAuthToken()
  if (token) {
    headers.Authorization = `Bearer ${token}`
  }
  return headers
}

async function readErrorText(resp: Response): Promise<string> {
  try {
    return await resp.text()
  } catch {
    return resp.statusText
  }
}

export async function getAuthSession(): Promise<AuthSessionResponse> {
  const resp = await fetch('/api/auth/session', {
    method: 'GET',
    headers: createAuthHeaders(),
  })
  if (!resp.ok) {
    throw new ApiError(resp.status, `auth session failed: ${resp.status} ${await readErrorText(resp)}`)
  }
  return resp.json() as Promise<AuthSessionResponse>
}

export async function logout(): Promise<void> {
  await fetch('/api/auth/logout', {
    method: 'POST',
    headers: createAuthHeaders(),
  })
}

export async function issueSessionToken(sessionId: string): Promise<{ token: string }> {
  const resp = await fetch(`/api/sessions/${encodeURIComponent(sessionId)}/tokens`, {
    method: 'POST',
    headers: createAuthHeaders(),
  })
  if (!resp.ok) {
    throw new ApiError(resp.status, `issue session token failed: ${resp.status} ${await readErrorText(resp)}`)
  }
  return resp.json() as Promise<{ token: string }>
}

export async function listProjectFiles(prefix = '', limit = 60, signal?: AbortSignal): Promise<string[]> {
  const url = new URL('/api/files', window.location.origin)
  if (prefix) {
    url.searchParams.set('prefix', prefix)
  }
  if (limit > 0) {
    url.searchParams.set('limit', String(limit))
  }
  const resp = await fetch(url.toString(), {
    method: 'GET',
    headers: createAuthHeaders(),
    signal,
  })
  if (!resp.ok) {
    throw new ApiError(resp.status, `list files failed: ${resp.status} ${await readErrorText(resp)}`)
  }
  const payload = (await resp.json()) as { files?: string[] }
  return Array.isArray(payload.files) ? payload.files : []
}

export type APITokenRecord = {
  id: string
  name: string
  owner?: string
  scope: string
  prefix: string
  createdAt: string
  lastUsedAt?: string
  revoked: boolean
}

export async function listAPITokens(): Promise<{ tokens: APITokenRecord[] }> {
  const resp = await fetch('/api/config/api-tokens', {
    method: 'GET',
    headers: createAuthHeaders(),
  })
  if (!resp.ok) {
    throw new ApiError(resp.status, `list api tokens failed: ${resp.status} ${await readErrorText(resp)}`)
  }
  return resp.json() as Promise<{ tokens: APITokenRecord[] }>
}

export async function createAPIToken(request: {
  name: string
  owner: string
  scope: string
}): Promise<{ token: string; record: APITokenRecord }> {
  const resp = await fetch('/api/config/api-tokens', {
    method: 'POST',
    headers: createAuthHeaders({ 'Content-Type': 'application/json' }),
    body: JSON.stringify(request),
  })
  if (!resp.ok) {
    throw new ApiError(resp.status, `create api token failed: ${resp.status} ${await readErrorText(resp)}`)
  }
  return resp.json() as Promise<{ token: string; record: APITokenRecord }>
}

export async function revokeAPIToken(tokenId: string): Promise<void> {
  const resp = await fetch(`/api/config/api-tokens/${encodeURIComponent(tokenId)}`, {
    method: 'DELETE',
    headers: createAuthHeaders(),
  })
  if (!resp.ok) {
    throw new ApiError(resp.status, `revoke api token failed: ${resp.status} ${await readErrorText(resp)}`)
  }
}

export async function listSettings(): Promise<{ settings: Record<string, string> }> {
  const resp = await fetch('/api/config/settings', {
    method: 'GET',
    headers: createAuthHeaders(),
  })
  if (!resp.ok) {
    throw new ApiError(resp.status, `list settings failed: ${resp.status} ${await readErrorText(resp)}`)
  }
  return resp.json() as Promise<{ settings: Record<string, string> }>
}

export async function updateSetting(key: string, value: string): Promise<void> {
  const resp = await fetch(`/api/config/settings/${encodeURIComponent(key)}`, {
    method: 'PUT',
    headers: createAuthHeaders({ 'Content-Type': 'application/json' }),
    body: JSON.stringify({ value }),
  })
  if (!resp.ok) {
    throw new ApiError(resp.status, `update setting failed: ${resp.status} ${await readErrorText(resp)}`)
  }
}

export type AuditEntry = {
  actor: string
  scope: string
  action: string
  payload?: unknown
  createdAt: string
}

export async function listAuditLogs(limit = 100): Promise<{ audit: AuditEntry[] }> {
  const url = new URL('/api/config/audit-logs', window.location.origin)
  url.searchParams.set('limit', String(limit))
  const resp = await fetch(url.toString(), {
    method: 'GET',
    headers: createAuthHeaders(),
  })
  if (!resp.ok) {
    throw new ApiError(resp.status, `list audit logs failed: ${resp.status} ${await readErrorText(resp)}`)
  }
  return resp.json() as Promise<{ audit: AuditEntry[] }>
}
