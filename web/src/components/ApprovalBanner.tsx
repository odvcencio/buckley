import { useEffect, useState } from 'react'
import { AlertTriangle, Check, X, Clock } from 'lucide-react'
import type { PendingApproval } from '../types'

interface Props {
  approval: PendingApproval
  onApprove: (id: string) => void
  onReject: (id: string) => void
}

export function ApprovalBanner({ approval, onApprove, onReject }: Props) {
  const [timeLeft, setTimeLeft] = useState<number | null>(null)

  const resolveExpiry = (value: unknown): Date | null => {
    if (!value) return null
    if (value instanceof Date) return value
    if (typeof value === 'string') {
      const d = new Date(value)
      return Number.isNaN(d.getTime()) ? null : d
    }
    if (typeof value === 'object') {
      const v = value as { seconds?: bigint | number | string; nanos?: number | string }
      if (v.seconds == null) return null
      const seconds = typeof v.seconds === 'bigint' ? Number(v.seconds) : Number(v.seconds)
      const nanos = v.nanos == null ? 0 : Number(v.nanos)
      const d = new Date(seconds * 1000 + Math.floor(nanos / 1_000_000))
      return Number.isNaN(d.getTime()) ? null : d
    }
    return null
  }

  useEffect(() => {
    const expiresAt = resolveExpiry((approval as unknown as { expiresAt?: unknown }).expiresAt)
    if (!expiresAt) {
      Promise.resolve().then(() => setTimeLeft(null))
      return
    }
    const updateTimer = () => {
      const now = new Date()
      const diff = expiresAt.getTime() - now.getTime()
      setTimeLeft(Math.max(0, Math.floor(diff / 1000)))
    }

    updateTimer()
    const interval = setInterval(updateTimer, 1000)
    return () => clearInterval(interval)
  }, [approval])

  const formatTime = (seconds: number) => {
    const mins = Math.floor(seconds / 60)
    const secs = seconds % 60
    return `${mins}:${secs.toString().padStart(2, '0')}`
  }

  const urgency = timeLeft != null && timeLeft < 30 ? 'urgent' : timeLeft != null && timeLeft < 60 ? 'warning' : 'normal'
  const summary =
    (approval.riskReasons && approval.riskReasons.length > 0
      ? approval.riskReasons[0]
      : undefined) ?? 'A tool call is waiting for approval.'

  return (
    <div
      className={`
        fixed bottom-20 left-4 right-4 md:left-auto md:right-4 md:w-96
        rounded-xl border shadow-lg backdrop-blur-sm
        animate-in slide-in-from-bottom-4
        ${urgency === 'urgent'
          ? 'bg-[var(--color-error)]/95 border-[var(--color-error)]'
          : urgency === 'warning'
            ? 'bg-[var(--color-warning)]/95 border-[var(--color-warning)]'
            : 'bg-[var(--color-surface)]/95 border-[var(--color-border)]'
        }
      `}
    >
      <div className="p-4">
        {/* Header */}
        <div className="flex items-start gap-3 mb-3">
          <div
            className={`
              p-2 rounded-lg
              ${urgency === 'urgent' || urgency === 'warning'
                ? 'bg-white/20'
                : 'bg-[var(--color-warning)]/20'
              }
            `}
          >
            <AlertTriangle
              className={`
                w-5 h-5
                ${urgency === 'urgent' || urgency === 'warning'
                  ? 'text-white'
                  : 'text-[var(--color-warning)]'
                }
              `}
            />
          </div>
          <div className="flex-1 min-w-0">
            <h3
              className={`
                font-semibold text-sm
                ${urgency === 'urgent' || urgency === 'warning'
                  ? 'text-white'
                  : 'text-[var(--color-text)]'
                }
              `}
            >
              Approval Required
            </h3>
            <p
              className={`
                text-sm mt-0.5
                ${urgency === 'urgent' || urgency === 'warning'
                  ? 'text-white/80'
                  : 'text-[var(--color-text-secondary)]'
                }
              `}
            >
              {summary}
            </p>
          </div>
        </div>

        {/* Tool info */}
        <div
          className={`
            font-mono text-xs px-2 py-1.5 rounded-md mb-3
            ${urgency === 'urgent' || urgency === 'warning'
              ? 'bg-white/10 text-white'
              : 'bg-[var(--color-abyss)] text-[var(--color-text-secondary)]'
            }
          `}
        >
          {approval.toolName}
        </div>

        {/* Timer */}
        {timeLeft != null && (
          <div
            className={`
              flex items-center gap-2 text-xs mb-3
              ${urgency === 'urgent'
                ? 'text-white'
                : urgency === 'warning'
                  ? 'text-white/90'
                  : 'text-[var(--color-text-muted)]'
              }
            `}
          >
            <Clock className="w-3.5 h-3.5" />
            <span>Expires in {formatTime(timeLeft)}</span>
            {urgency === 'urgent' && <span className="pulse">(urgent)</span>}
          </div>
        )}

        {/* Actions */}
        <div className="flex gap-2">
          <button
            onClick={() => onApprove(approval.id)}
            className={`
              flex-1 flex items-center justify-center gap-2 px-4 py-2.5
              rounded-lg font-medium text-sm transition-all
              ${urgency === 'urgent' || urgency === 'warning'
                ? 'bg-white text-[var(--color-void)] hover:bg-white/90'
                : 'bg-[var(--color-success)] text-white hover:opacity-90'
              }
            `}
          >
            <Check className="w-4 h-4" />
            Approve
          </button>
          <button
            onClick={() => onReject(approval.id)}
            className={`
              flex-1 flex items-center justify-center gap-2 px-4 py-2.5
              rounded-lg font-medium text-sm transition-all
              ${urgency === 'urgent' || urgency === 'warning'
                ? 'bg-white/20 text-white hover:bg-white/30'
                : 'bg-[var(--color-surface)] border border-[var(--color-border)] text-[var(--color-text)] hover:bg-[var(--color-elevated)]'
              }
            `}
          >
            <X className="w-4 h-4" />
            Reject
          </button>
        </div>
      </div>
    </div>
  )
}
