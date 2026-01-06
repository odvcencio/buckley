import type { PendingApproval, Session, DisplaySession, DiffLine } from '../types'

export function timestampToISO(value: unknown): string {
  if (!value) return new Date().toISOString()
  if (typeof value === 'string') return value
  if (typeof value === 'object') {
    const v = value as { seconds?: bigint | number | string; nanos?: number | string }
    if (v.seconds != null) {
      const seconds = typeof v.seconds === 'bigint' ? Number(v.seconds) : Number(v.seconds)
      const nanos = v.nanos == null ? 0 : Number(v.nanos)
      const d = new Date(seconds * 1000 + Math.floor(nanos / 1_000_000))
      if (!Number.isNaN(d.getTime())) return d.toISOString()
    }
  }
  return new Date().toISOString()
}

function emptyToUndefined(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const trimmed = value.trim()
  return trimmed === '' ? undefined : trimmed
}

export function toSession(raw: unknown): Session | null {
  if (!raw || typeof raw !== 'object') return null
  const s = raw as Record<string, unknown>
  const id = typeof s.id === 'string' ? s.id : ''
  if (!id) return null
  const status = typeof s.status === 'string' ? s.status : 'active'
  return {
    id,
    projectPath: emptyToUndefined(s.projectPath),
    gitRepo: emptyToUndefined(s.gitRepo),
    gitBranch: emptyToUndefined(s.gitBranch),
    status,
    createdAt: timestampToISO(s.createdAt),
    lastActive: timestampToISO(s.lastActive),
    messageCount: typeof s.messageCount === 'number' ? s.messageCount : undefined,
    todoCount: typeof s.todoCount === 'number' ? s.todoCount : undefined,
    agentId: emptyToUndefined(s.agentId),
  }
}

export function toDisplaySession(sess: Session): DisplaySession {
  const project = sess.gitRepo ? sess.gitRepo.split('/').pop() || 'Unknown' : sess.projectPath?.split('/').pop() || 'Unknown'
  const branch = sess.gitBranch || 'main'
  return { ...sess, project, branch }
}

function normalizeToolInput(value: unknown): Record<string, unknown> | undefined {
  if (!value) return undefined
  if (typeof value === 'object' && !Array.isArray(value)) {
    const v = value as { fields?: Record<string, unknown> }
    if (!v.fields) return value as Record<string, unknown>
    const out: Record<string, unknown> = {}
    for (const [key, field] of Object.entries(v.fields)) {
      const kind = (field as { kind?: { case?: string; value?: unknown } })?.kind
      if (!kind) continue
      switch (kind.case) {
        case 'nullValue':
          out[key] = null
          break
        case 'stringValue':
        case 'numberValue':
        case 'boolValue':
          out[key] = kind.value
          break
        case 'structValue':
          out[key] = normalizeToolInput(kind.value)
          break
        case 'listValue': {
          const list = kind.value as { values?: unknown[] } | undefined
          const values = Array.isArray(list?.values) ? list?.values : []
          out[key] = values.map((entry) => {
            const entryKind = (entry as { kind?: { case?: string; value?: unknown } })?.kind
            if (!entryKind) return undefined
            switch (entryKind.case) {
              case 'nullValue':
                return null
              case 'stringValue':
              case 'numberValue':
              case 'boolValue':
                return entryKind.value
              case 'structValue':
                return normalizeToolInput(entryKind.value)
              default:
                return undefined
            }
          })
          break
        }
        default:
          break
      }
    }
    return out
  }
  return undefined
}

function normalizeDiffLines(value: unknown): DiffLine[] | undefined {
  if (!Array.isArray(value)) return undefined
  const lines: DiffLine[] = []
  for (const entry of value) {
    if (!entry || typeof entry !== 'object') continue
    const line = entry as { type?: unknown; content?: unknown }
    if (typeof line.type !== 'string' || typeof line.content !== 'string') continue
    if (line.type !== 'add' && line.type !== 'remove' && line.type !== 'context') continue
    lines.push({ type: line.type, content: line.content })
  }
  return lines.length > 0 ? lines : undefined
}

export function toPendingApproval(raw: unknown): PendingApproval | null {
  if (!raw || typeof raw !== 'object') return null
  const a = raw as Record<string, unknown>
  const id = typeof a.id === 'string' ? a.id : ''
  const sessionId = typeof a.sessionId === 'string' ? a.sessionId : ''
  const toolName = typeof a.toolName === 'string' ? a.toolName : ''
  if (!id || !sessionId || !toolName) return null
  const createdAt = a.createdAt == null ? undefined : typeof a.createdAt === 'string' ? a.createdAt : timestampToISO(a.createdAt)
  const expiresAt = a.expiresAt == null ? undefined : typeof a.expiresAt === 'string' ? a.expiresAt : timestampToISO(a.expiresAt)
  return {
    id,
    sessionId,
    toolName,
    toolInput: normalizeToolInput(a.toolInput),
    riskScore: typeof a.riskScore === 'number' ? a.riskScore : undefined,
    riskReasons: Array.isArray(a.riskReasons) ? a.riskReasons.filter((r): r is string => typeof r === 'string') : undefined,
    status: typeof a.status === 'string' ? a.status : undefined,
    createdAt,
    expiresAt,
    operationType: typeof a.operationType === 'string' ? a.operationType : undefined,
    description: typeof a.description === 'string' ? a.description : undefined,
    command: typeof a.command === 'string' ? a.command : undefined,
    filePath: typeof a.filePath === 'string' ? a.filePath : undefined,
    diffLines: normalizeDiffLines(a.diffLines),
    addedLines: typeof a.addedLines === 'number' ? a.addedLines : undefined,
    removedLines: typeof a.removedLines === 'number' ? a.removedLines : undefined,
  }
}
