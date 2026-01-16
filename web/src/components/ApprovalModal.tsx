import { AlertTriangle, FileText, Terminal, X } from 'lucide-react'

import type { DiffLine, PendingApproval } from '../types'
import { useOverlayControls } from '../hooks/useOverlayControls'

interface Props {
  approval: PendingApproval
  onClose: () => void
  onApprove?: (id: string) => void
  onReject?: (id: string) => void
}

function summaryText(approval: PendingApproval) {
  if (approval.description) return approval.description
  if (approval.riskReasons && approval.riskReasons.length > 0) return approval.riskReasons[0]
  return 'A tool call is waiting for approval.'
}

function diffBadge(line: DiffLine) {
  switch (line.type) {
    case 'add':
      return { prefix: '+', className: 'text-[var(--color-success)]' }
    case 'remove':
      return { prefix: '-', className: 'text-[var(--color-error)]' }
    default:
      return { prefix: ' ', className: 'text-[var(--color-text-muted)]' }
  }
}

export function ApprovalModal({ approval, onClose, onApprove, onReject }: Props) {
  useOverlayControls(true, onClose)

  const summary = summaryText(approval)
  const diffLines = approval.diffLines ?? []
  const added = typeof approval.addedLines === 'number' ? approval.addedLines : null
  const removed = typeof approval.removedLines === 'number' ? approval.removedLines : null

  return (
    <>
      <div className="fixed inset-0 bg-black/60 z-40" onClick={onClose} aria-hidden="true" />
      <div
        className="fixed inset-0 z-50 flex items-center justify-center p-4"
        role="dialog"
        aria-modal="true"
        aria-label="Approval details"
      >
        <div className="w-full max-w-3xl max-h-[90vh] rounded-3xl border border-[var(--color-border)] bg-[var(--color-abyss)]/95 backdrop-blur-xl shadow-2xl shadow-black/50 overflow-hidden flex flex-col">
          <div className="px-6 py-5 border-b border-[var(--color-border)] flex items-center justify-between gap-4">
            <div className="flex items-center gap-3 min-w-0">
              <div className="w-10 h-10 rounded-2xl bg-[var(--color-surface)] border border-[var(--color-border-subtle)] flex items-center justify-center">
                <AlertTriangle className="w-5 h-5 text-[var(--color-warning)]" />
              </div>
              <div className="min-w-0">
                <div className="text-lg font-display font-bold text-[var(--color-text)] truncate">{approval.toolName}</div>
                <div className="text-sm text-[var(--color-text-muted)] truncate">{summary}</div>
              </div>
            </div>
            <button
              onClick={onClose}
              className="p-2 rounded-xl hover:bg-[var(--color-surface)] transition-colors"
              aria-label="Close approval details"
            >
              <X className="w-5 h-5 text-[var(--color-text-secondary)]" />
            </button>
          </div>

          <div className="flex-1 overflow-y-auto p-6 space-y-6 scrollbar-thin">
            <div className="grid md:grid-cols-2 gap-4">
              <div className="rounded-2xl border border-[var(--color-border-subtle)] bg-[var(--color-surface)]/60 p-4">
                <div className="flex items-center gap-2 text-xs text-[var(--color-text-muted)]">
                  <Terminal className="w-4 h-4" />
                  Operation
                </div>
                <div className="mt-2 text-sm font-mono text-[var(--color-text)]">
                  {approval.operationType || 'unknown'}
                </div>
                {typeof approval.riskScore === 'number' && (
                  <div className="mt-2 text-xs text-[var(--color-text-muted)]">
                    Risk score: <span className="font-mono">{approval.riskScore}</span>
                  </div>
                )}
              </div>

              <div className="rounded-2xl border border-[var(--color-border-subtle)] bg-[var(--color-surface)]/60 p-4">
                <div className="flex items-center gap-2 text-xs text-[var(--color-text-muted)]">
                  <FileText className="w-4 h-4" />
                  Target
                </div>
                <div className="mt-2 text-sm font-mono text-[var(--color-text)] truncate">
                  {approval.filePath || 'n/a'}
                </div>
                {added != null || removed != null ? (
                  <div className="mt-2 text-xs text-[var(--color-text-muted)]">
                    {added != null ? `+${added}` : ''} {removed != null ? `-${removed}` : ''}
                  </div>
                ) : null}
              </div>
            </div>

            {approval.command && (
              <div className="rounded-2xl border border-[var(--color-border-subtle)] bg-[var(--color-surface)]/60 p-4">
                <div className="text-xs font-semibold text-[var(--color-text-muted)] uppercase tracking-wide">
                  Command
                </div>
                <pre className="mt-3 text-xs bg-[var(--color-abyss)] rounded-lg p-3 overflow-x-auto font-mono text-[var(--color-text-secondary)] border border-[var(--color-border-subtle)]">
                  {approval.command}
                </pre>
              </div>
            )}

            {diffLines.length > 0 && (
              <div className="rounded-2xl border border-[var(--color-border-subtle)] bg-[var(--color-surface)]/60 p-4">
                <div className="text-xs font-semibold text-[var(--color-text-muted)] uppercase tracking-wide">
                  Diff Preview
                </div>
                <div className="mt-3 bg-[var(--color-abyss)] border border-[var(--color-border-subtle)] rounded-lg p-3 max-h-72 overflow-auto">
                  {diffLines.map((line, idx) => {
                    const badge = diffBadge(line)
                    return (
                      <div key={`${line.type}-${idx}`} className={`font-mono text-xs whitespace-pre ${badge.className}`}>
                        <span className="mr-2">{badge.prefix}</span>
                        <span>{line.content}</span>
                      </div>
                    )
                  })}
                </div>
              </div>
            )}
          </div>

          <div className="px-6 py-4 border-t border-[var(--color-border)] flex items-center justify-end gap-2">
            <button
              onClick={() => onReject?.(approval.id)}
              className="px-4 py-2 rounded-xl bg-[var(--color-surface)] border border-[var(--color-border)] text-sm font-semibold text-[var(--color-text)] hover:bg-[var(--color-elevated)] transition-colors"
            >
              Reject
            </button>
            <button
              onClick={() => onApprove?.(approval.id)}
              className="px-4 py-2 rounded-xl bg-[var(--color-success)] text-white text-sm font-semibold hover:shadow-lg hover:shadow-[var(--color-success)]/20 transition-all"
            >
              Approve
            </button>
          </div>
        </div>
      </div>
    </>
  )
}
