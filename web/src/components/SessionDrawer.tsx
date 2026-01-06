import { X, GitBranch, Clock, Plus } from 'lucide-react'
import type { DisplaySession } from '../types'
import { useOverlayControls } from '../hooks/useOverlayControls'

interface Props {
  isOpen: boolean
  sessions: DisplaySession[]
  currentSessionId: string | null
  onClose: () => void
  onSelectSession: (sessionId: string) => void
  onNewSession?: () => void
}

export function SessionDrawer({
  isOpen,
  sessions,
  currentSessionId,
  onClose,
  onSelectSession,
  onNewSession,
}: Props) {
  useOverlayControls(isOpen, onClose)
  if (!isOpen) return null

  const formatTime = (dateStr: string) => {
    const date = new Date(dateStr)
    const now = new Date()
    const diffMs = now.getTime() - date.getTime()
    const diffMins = Math.floor(diffMs / 60000)

    if (diffMins < 1) return 'Just now'
    if (diffMins < 60) return `${diffMins}m ago`
    if (diffMins < 1440) return `${Math.floor(diffMins / 60)}h ago`
    return date.toLocaleDateString()
  }

  const statusDot = (status: DisplaySession['status']) => {
    switch (status) {
      case 'active':
        return 'active'
      case 'paused':
        return 'pending'
      default:
        return 'idle'
    }
  }

  return (
    <>
      {/* Backdrop */}
      <div
        className="fixed inset-0 bg-black/50 z-40"
        onClick={onClose}
        aria-hidden="true"
      />

      {/* Drawer */}
      <div
        className="fixed inset-y-0 left-0 w-80 max-w-[85vw] bg-[var(--color-abyss)] border-r border-[var(--color-border)] z-50 flex flex-col animate-in slide-in-from-left"
        role="dialog"
        aria-modal="true"
        aria-label="Sessions"
      >
        {/* Header */}
        <div className="flex items-center justify-between px-4 py-4 border-b border-[var(--color-border)]">
          <h2 className="font-display font-semibold text-[var(--color-text)]">Sessions</h2>
          <button
            onClick={onClose}
            className="p-2 rounded-lg hover:bg-[var(--color-surface)] transition-colors"
            aria-label="Close sessions"
          >
            <X className="w-5 h-5 text-[var(--color-text-secondary)]" />
          </button>
        </div>

        {/* Session list */}
        <div className="flex-1 overflow-y-auto py-2">
          {sessions.length === 0 ? (
            <div className="px-4 py-8 text-center text-sm text-[var(--color-text-muted)]">
              No active sessions
            </div>
          ) : (
            sessions.map((session) => (
              <button
                key={session.id}
                onClick={() => {
                  onSelectSession(session.id)
                  onClose()
                }}
                className={`
                  w-full px-4 py-3 text-left
                  hover:bg-[var(--color-surface)]
                  transition-colors
                  ${currentSessionId === session.id ? 'bg-[var(--color-surface)]' : ''}
                `}
              >
                <div className="flex items-center gap-3">
                  <div className={`status-dot ${statusDot(session.status)}`} />
                  <div className="flex-1 min-w-0">
                    <div className="text-sm font-medium text-[var(--color-text)] truncate">
                      {session.project}
                    </div>
                    <div className="flex items-center gap-3 mt-1 text-xs text-[var(--color-text-muted)]">
                      <span className="flex items-center gap-1">
                        <GitBranch className="w-3 h-3" />
                        {session.branch}
                      </span>
                      <span className="flex items-center gap-1">
                        <Clock className="w-3 h-3" />
                        {formatTime(session.lastActive)}
                      </span>
                    </div>
                  </div>
                </div>
              </button>
            ))
          )}
        </div>

        {/* Footer */}
        {onNewSession && (
          <div className="p-4 border-t border-[var(--color-border)]">
            <button
              onClick={onNewSession}
              className="
                w-full flex items-center justify-center gap-2
                px-4 py-3 rounded-lg
                bg-[var(--color-accent)] text-[var(--color-void)]
                font-medium text-sm
                hover:bg-[var(--color-accent-hover)]
                transition-colors
              "
            >
              <Plus className="w-4 h-4" />
              New Session
            </button>
          </div>
        )}
      </div>
    </>
  )
}
