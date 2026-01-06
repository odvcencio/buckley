import { useState } from 'react'
import { ChevronDown, ChevronUp, Check, X, Loader2, Clock, Terminal, FileCode, Search, GitBranch, Globe, Edit3 } from 'lucide-react'
import type { ToolCall } from '../types'

interface Props {
  toolCall: ToolCall
  onApprove?: () => void
  onReject?: () => void
}

// Tool name to icon mapping for visual interest
const toolIcons: Record<string, typeof Terminal> = {
  read_file: FileCode,
  Read: FileCode,
  write_file: Edit3,
  Write: Edit3,
  Edit: Edit3,
  edit_file: Edit3,
  Bash: Terminal,
  bash: Terminal,
  shell: Terminal,
  run_shell: Terminal,
  execute_command: Terminal,
  Grep: Search,
  grep: Search,
  search: Search,
  Glob: Search,
  glob: Search,
  list_files: Search,
  git: GitBranch,
  WebFetch: Globe,
  web_fetch: Globe,
  WebSearch: Globe,
  web_search: Globe,
}

export function ToolCallCard({ toolCall, onApprove, onReject }: Props) {
  const [expanded, setExpanded] = useState(false)

  const statusConfig = {
    pending: {
      icon: Clock,
      color: 'text-[var(--color-warning)]',
      bgColor: 'bg-[var(--color-warning-subtle)]',
      borderColor: 'border-[var(--color-warning)]/20',
      label: 'Pending',
    },
    running: {
      icon: Loader2,
      color: 'text-[var(--color-streaming)]',
      bgColor: 'bg-[var(--color-streaming)]/10',
      borderColor: 'border-[var(--color-streaming)]/20',
      label: 'Running',
    },
    completed: {
      icon: Check,
      color: 'text-[var(--color-success)]',
      bgColor: 'bg-[var(--color-success-subtle)]',
      borderColor: 'border-[var(--color-success)]/20',
      label: 'Completed',
    },
    failed: {
      icon: X,
      color: 'text-[var(--color-error)]',
      bgColor: 'bg-[var(--color-error-subtle)]',
      borderColor: 'border-[var(--color-error)]/20',
      label: 'Failed',
    },
  }

  const status = statusConfig[toolCall.status]
  const StatusIcon = status.icon
  const ToolIcon = toolIcons[toolCall.name] || Terminal

  return (
    <div
      className={`
        group rounded-xl border transition-all duration-200
        ${status.borderColor} ${status.bgColor}
        hover:shadow-sm
      `}
    >
      {/* Header */}
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full flex items-center gap-3 px-4 py-3 text-left"
      >
        {/* Tool icon */}
        <div className={`p-1.5 rounded-lg ${status.bgColor} ${status.color}`}>
          <ToolIcon className="w-3.5 h-3.5" />
        </div>

        {/* Tool name */}
        <span className="flex-1 font-mono text-sm font-medium text-[var(--color-text)]">
          {toolCall.name}
        </span>

        {/* Status badge */}
        <span className={`flex items-center gap-1.5 text-xs font-medium ${status.color}`}>
          <StatusIcon className={`w-3.5 h-3.5 ${toolCall.status === 'running' ? 'animate-spin' : ''}`} />
          <span className="hidden sm:inline">{status.label}</span>
        </span>

        {/* Expand icon */}
        <div className={`p-1 rounded-md transition-colors ${expanded ? 'bg-[var(--color-surface)]' : ''}`}>
          {expanded ? (
            <ChevronUp className="w-4 h-4 text-[var(--color-text-muted)]" />
          ) : (
            <ChevronDown className="w-4 h-4 text-[var(--color-text-muted)]" />
          )}
        </div>
      </button>

      {/* Expanded content */}
      <div
        className={`
          grid transition-all duration-200
          ${expanded ? 'grid-rows-[1fr] opacity-100' : 'grid-rows-[0fr] opacity-0'}
        `}
      >
        <div className="overflow-hidden">
          <div className="px-4 pb-4 space-y-3">
            {/* Arguments */}
            {Object.keys(toolCall.arguments).length > 0 && (
              <div>
                <div className="text-[10px] font-semibold uppercase tracking-wider text-[var(--color-text-muted)] mb-1.5">
                  Arguments
                </div>
                <pre className="text-xs bg-[var(--color-abyss)] rounded-lg p-3 overflow-x-auto font-mono text-[var(--color-text-secondary)] border border-[var(--color-border-subtle)]">
                  {JSON.stringify(toolCall.arguments, null, 2)}
                </pre>
              </div>
            )}

            {/* Result */}
            {toolCall.result && (
              <div>
                <div className={`text-[10px] font-semibold uppercase tracking-wider mb-1.5 ${toolCall.result.success ? 'text-[var(--color-text-muted)]' : 'text-[var(--color-error)]'}`}>
                  {toolCall.result.success ? 'Result' : 'Error'}
                </div>
                <pre
                  className={`
                    text-xs rounded-lg p-3 overflow-x-auto font-mono max-h-48 overflow-y-auto
                    border scrollbar-thin
                    ${toolCall.result.success
                      ? 'bg-[var(--color-abyss)] text-[var(--color-text-secondary)] border-[var(--color-border-subtle)]'
                      : 'bg-[var(--color-error-subtle)] text-[var(--color-error)] border-[var(--color-error)]/20'
                    }
                  `}
                >
                  {toolCall.result.output || toolCall.result.error}
                  {toolCall.result.abridged && (
                    <span className="text-[var(--color-text-subtle)] italic block mt-2 pt-2 border-t border-current/10">
                      (output truncated)
                    </span>
                  )}
                </pre>
              </div>
            )}

            {/* Approval buttons */}
            {toolCall.requiresApproval && toolCall.status === 'pending' && (
              <div className="flex gap-2 pt-2">
                <button
                  onClick={onApprove}
                  className="
                    flex-1 flex items-center justify-center gap-2 px-4 py-2.5
                    bg-[var(--color-success)] text-white rounded-xl
                    font-semibold text-sm
                    hover:shadow-lg hover:shadow-[var(--color-success)]/20
                    active:scale-[0.98]
                    transition-all duration-200
                  "
                >
                  <Check className="w-4 h-4" />
                  Approve
                </button>
                <button
                  onClick={onReject}
                  className="
                    flex-1 flex items-center justify-center gap-2 px-4 py-2.5
                    bg-[var(--color-surface)] text-[var(--color-text)] rounded-xl
                    font-semibold text-sm border border-[var(--color-border)]
                    hover:bg-[var(--color-elevated)] hover:border-[var(--color-border-strong)]
                    active:scale-[0.98]
                    transition-all duration-200
                  "
                >
                  <X className="w-4 h-4" />
                  Reject
                </button>
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
