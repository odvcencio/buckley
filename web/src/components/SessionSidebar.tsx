import { useMemo, useState } from 'react'
import { GitBranch, Clock, Search, CheckCircle2, PauseCircle, Circle } from 'lucide-react'
import type { DisplaySession } from '../types'

interface Props {
  sessions: DisplaySession[]
  currentSessionId: string | null
  onSelectSession: (sessionId: string) => void
}

function formatTime(dateStr: string) {
  const date = new Date(dateStr)
  const now = new Date()
  const diffMs = now.getTime() - date.getTime()
  const diffMins = Math.floor(diffMs / 60000)

  if (Number.isNaN(diffMins)) return ''
  if (diffMins < 1) return 'Just now'
  if (diffMins < 60) return `${diffMins}m`
  if (diffMins < 1440) return `${Math.floor(diffMins / 60)}h`
  return date.toLocaleDateString()
}

function statusIcon(status: DisplaySession['status']) {
  switch (status) {
    case 'active':
      return CheckCircle2
    case 'paused':
      return PauseCircle
    case 'completed':
      return Circle
    default:
      return Circle
  }
}

export function SessionSidebar({ sessions, currentSessionId, onSelectSession }: Props) {
  const [query, setQuery] = useState('')

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    const list = [...sessions].sort((a, b) => new Date(b.lastActive).getTime() - new Date(a.lastActive).getTime())
    if (!q) return list
    return list.filter((s) => {
      const hay = `${s.project} ${s.branch} ${s.gitRepo ?? ''} ${s.projectPath ?? ''}`.toLowerCase()
      return hay.includes(q)
    })
  }, [sessions, query])

  return (
    <aside className="hidden lg:flex flex-col border-r border-[var(--color-border)] bg-[var(--color-abyss)]/95 backdrop-blur-xl">
      <div className="p-4 border-b border-[var(--color-border)]">
        <div className="relative">
          <Search className="w-4 h-4 text-[var(--color-text-muted)] absolute left-3 top-1/2 -translate-y-1/2" />
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search sessions…"
            className="
              w-full bg-[var(--color-surface)] border border-[var(--color-border-subtle)]
              rounded-xl pl-10 pr-3 py-2 text-sm
              text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]
              focus:outline-none focus:ring-2 focus:ring-[var(--color-accent)]/30
            "
          />
        </div>
      </div>

      <div className="flex-1 overflow-y-auto scrollbar-thin">
        {filtered.length === 0 ? (
          <div className="px-4 py-8 text-center text-sm text-[var(--color-text-muted)]">
            No sessions
          </div>
        ) : (
          filtered.map((session) => {
            const selected = session.id === currentSessionId
            const StatusIcon = statusIcon(session.status)
            return (
              <button
                key={session.id}
                onClick={() => onSelectSession(session.id)}
                className={`
                  w-full px-4 py-3 text-left transition-colors
                  hover:bg-[var(--color-surface)]
                  ${selected ? 'bg-[var(--color-surface)]' : ''}
                `}
              >
                <div className="flex items-start gap-3">
                  <StatusIcon className="w-4 h-4 mt-0.5 text-[var(--color-text-muted)]" />
                  <div className="flex-1 min-w-0">
                    <div className="text-sm font-semibold text-[var(--color-text)] truncate">
                      {session.project}
                    </div>
                    <div className="flex items-center gap-3 mt-1 text-xs text-[var(--color-text-muted)]">
                      <span className="flex items-center gap-1">
                        <GitBranch className="w-3 h-3" />
                        <span className="font-mono">{session.branch}</span>
                      </span>
                      <span className="flex items-center gap-1">
                        <Clock className="w-3 h-3" />
                        <span className="tabular-nums">{formatTime(session.lastActive)}</span>
                      </span>
                    </div>
                  </div>
                </div>
              </button>
            )
          })
        )}
      </div>
    </aside>
  )
}

