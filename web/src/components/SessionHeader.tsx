import { GitBranch, Cpu, ChevronDown, Wifi, WifiOff, RefreshCw, Zap, LayoutGrid, LogOut, Settings2 } from 'lucide-react'
import type { DisplaySession } from '../types'
import type { ConnectionState } from '../hooks/useGrpcStream'

interface Props {
  session: DisplaySession | null
  connectionState: ConnectionState
  principalScope?: string
  onSessionSelect?: () => void
  onPortholes?: () => void
  onOperator?: () => void
  onSignOut?: () => void
  onReconnect?: () => void
}

function scopePill(scope?: string) {
  const normalized = (scope || '').toLowerCase()
  const label = normalized || 'viewer'
  switch (label) {
    case 'operator':
      return {
        label,
        className:
          'bg-[var(--color-warning-subtle)] text-[var(--color-warning)] border-[var(--color-warning)]/20',
      }
    case 'member':
      return {
        label,
        className:
          'bg-[var(--color-success-subtle)] text-[var(--color-success)] border-[var(--color-success)]/20',
      }
    default:
      return {
        label,
        className:
          'bg-[var(--color-depth)] text-[var(--color-text-muted)] border-[var(--color-border-subtle)]',
      }
  }
}

export function SessionHeader({
  session,
  connectionState,
  principalScope,
  onSessionSelect,
  onPortholes,
  onOperator,
  onSignOut,
  onReconnect,
}: Props) {
  const connectionStatus = {
    connecting: { icon: RefreshCw, text: 'Connecting', color: 'text-[var(--color-warning)]', bgColor: 'bg-[var(--color-warning-subtle)]', animate: true },
    connected: { icon: Wifi, text: 'Connected', color: 'text-[var(--color-success)]', bgColor: 'bg-[var(--color-success-subtle)]', animate: false },
    disconnected: { icon: WifiOff, text: 'Disconnected', color: 'text-[var(--color-error)]', bgColor: 'bg-[var(--color-error-subtle)]', animate: false },
    reconnecting: { icon: RefreshCw, text: 'Reconnecting', color: 'text-[var(--color-warning)]', bgColor: 'bg-[var(--color-warning-subtle)]', animate: true },
  }

  const status = connectionStatus[connectionState]
  const StatusIcon = status.icon
  const scope = scopePill(principalScope)

  return (
    <header className="relative border-b border-[var(--color-border)] bg-[var(--color-abyss)]/95 backdrop-blur-xl px-4 py-3 safe-area-inset-top">
      {/* Subtle gradient overlay */}
      <div className="absolute inset-0 bg-gradient-to-r from-[var(--color-accent)]/[0.02] via-transparent to-[var(--color-accent)]/[0.02] pointer-events-none" />

      <div className="relative w-full flex items-center gap-4">
        {/* Logo with glow effect */}
        <div className="flex items-center gap-2.5">
          <div className="relative">
            <div className="w-9 h-9 rounded-xl bg-gradient-to-br from-[var(--color-accent)] to-[var(--color-accent-active)] flex items-center justify-center shadow-lg shadow-[var(--color-accent)]/20">
              <Zap className="w-4 h-4 text-[var(--color-text-inverse)]" />
            </div>
            {/* Ambient glow */}
            <div className="absolute inset-0 rounded-xl bg-[var(--color-accent)]/30 blur-lg -z-10" />
          </div>
          <span className="font-display font-bold text-lg text-[var(--color-text)] hidden sm:block tracking-tight">
            Buckley
          </span>
        </div>

        {/* Session selector */}
        <button
          onClick={onSessionSelect}
          className="
            lg:hidden
            group flex-1 flex items-center gap-3 px-4 py-2.5
            bg-[var(--color-surface)] rounded-xl
            border border-[var(--color-border-subtle)]
            hover:border-[var(--color-border)] hover:shadow-sm
            transition-all duration-200 text-left
          "
        >
          {session ? (
            <>
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-semibold text-[var(--color-text)] truncate">
                    {session.project}
                  </span>
                  <div
                    className={`
                      status-dot
                      ${session.status === 'active' ? 'active' : ''}
                      ${session.status === 'paused' ? 'pending' : ''}
                    `}
                  />
                </div>
                <div className="flex items-center gap-3 text-xs text-[var(--color-text-muted)] mt-0.5">
                  <span className="flex items-center gap-1">
                    <GitBranch className="w-3 h-3" />
                    <span className="font-mono">{session.branch}</span>
                  </span>
                  <span className="flex items-center gap-1">
                    <Cpu className="w-3 h-3" />
                    <span className="font-mono">{(session.model || 'default').split('/').pop()}</span>
                  </span>
                </div>
              </div>
              <ChevronDown className="w-4 h-4 text-[var(--color-text-muted)] transition-transform group-hover:translate-y-0.5" />
            </>
          ) : (
            <span className="text-sm text-[var(--color-text-muted)]">
              No session selected
            </span>
          )}
        </button>

        <div className="hidden lg:flex flex-1 items-center min-w-0">
          {session ? (
            <div className="min-w-0">
              <div className="flex items-center gap-2">
                <span className="text-sm font-semibold text-[var(--color-text)] truncate">
                  {session.project}
                </span>
                <div
                  className={`
                    status-dot
                    ${session.status === 'active' ? 'active' : ''}
                    ${session.status === 'paused' ? 'pending' : ''}
                  `}
                />
              </div>
              <div className="flex items-center gap-3 text-xs text-[var(--color-text-muted)] mt-0.5">
                <span className="flex items-center gap-1">
                  <GitBranch className="w-3 h-3" />
                  <span className="font-mono">{session.branch}</span>
                </span>
                <span className="flex items-center gap-1">
                  <Cpu className="w-3 h-3" />
                  <span className="font-mono">{(session.model || 'default').split('/').pop()}</span>
                </span>
              </div>
            </div>
          ) : (
            <span className="text-sm text-[var(--color-text-muted)]">No session selected</span>
          )}
        </div>

        <button
          onClick={onPortholes}
          className="lg:hidden p-2 rounded-xl hover:bg-[var(--color-surface)] transition-colors"
          title="Open portholes"
          aria-label="Open portholes"
        >
          <LayoutGrid className="w-4 h-4 text-[var(--color-text-secondary)]" />
        </button>

        {onOperator && (
          <button
            onClick={onOperator}
            className="p-2 rounded-xl hover:bg-[var(--color-surface)] transition-colors"
            title="Operator console"
            aria-label="Operator console"
          >
            <Settings2 className="w-4 h-4 text-[var(--color-text-secondary)]" />
          </button>
        )}

        <span
          className={`hidden sm:inline-flex items-center px-2.5 py-2 rounded-xl text-xs font-semibold border ${scope.className}`}
          title={`Auth scope: ${scope.label}`}
        >
          {scope.label}
        </span>

        {/* Connection status pill */}
        <button
          onClick={connectionState === 'disconnected' ? onReconnect : undefined}
          className={`
            group flex items-center gap-2 px-3 py-2
            rounded-xl text-xs font-medium
            ${status.color} ${status.bgColor}
            border border-current/20
            ${connectionState === 'disconnected' ? 'hover:border-current/40 cursor-pointer' : 'cursor-default'}
            transition-all duration-200
          `}
          title={connectionState === 'disconnected' ? 'Click to reconnect' : status.text}
        >
          <StatusIcon className={`w-3.5 h-3.5 ${status.animate ? 'animate-spin' : ''}`} />
          <span className="hidden sm:block">{status.text}</span>
        </button>

        <button
          onClick={onSignOut}
          disabled={!onSignOut}
          className={`p-2 rounded-xl transition-colors ${!onSignOut ? 'opacity-50 cursor-not-allowed' : 'hover:bg-[var(--color-surface)]'}`}
          title="Sign out"
          aria-label="Sign out"
        >
          <LogOut className="w-4 h-4 text-[var(--color-text-secondary)]" />
        </button>
      </div>
    </header>
  )
}
