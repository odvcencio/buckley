import { Suspense, lazy, useMemo, useState, type ReactNode } from 'react'
import {
  ChevronDown,
  ChevronRight,
  LayoutGrid,
  Pause,
  Play,
  ListChecks,
  ShieldAlert,
  Activity,
  Terminal as TerminalIcon,
  Cpu,
  DollarSign,
  X,
  Wrench,
  FileText,
  FileCode,
  Loader2,
  FlaskConical,
} from 'lucide-react'

import type { DisplaySession, PendingApproval, ViewLineRange, ViewSessionState } from '../types'
import type { ActivityEvent } from '../hooks/useBuckleyState'
import { useOverlayControls } from '../hooks/useOverlayControls'
import { ApprovalModal } from './ApprovalModal'

const TerminalPane = lazy(async () => {
  const mod = await import('./TerminalPane')
  return { default: mod.TerminalPane }
})

interface Props {
  session: DisplaySession | null
  view: ViewSessionState | null
  approvals: PendingApproval[]
  activity: ActivityEvent[]
  terminalSessionToken?: string
  terminalCanConnect?: boolean
  onPause?: () => void
  onResume?: () => void
  onApprove?: (approvalId: string) => void
  onReject?: (approvalId: string) => void
  onRefreshApprovals?: () => void
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}

interface ExperimentVariantState {
  id: string
  name?: string
  modelId?: string
  status?: string
  durationMs?: number
  totalCost?: number
  promptTokens?: number
  completionTokens?: number
}

interface ExperimentState {
  id?: string
  name?: string
  status?: string
  variants: Record<string, ExperimentVariantState>
}

interface RLMScratchpadEntry {
  key?: string
  type?: string
  summary?: string
}

interface RLMState {
  iteration?: number
  maxIterations?: number
  ready?: boolean
  tokensUsed?: number
  summary?: string
  scratchpad: RLMScratchpadEntry[]
}

function getTelemetryData(event: ActivityEvent): Record<string, unknown> | null {
  if (!event.payload || typeof event.payload !== 'object' || Array.isArray(event.payload)) {
    return null
  }
  const payload = event.payload as { data?: unknown }
  if (!payload.data || typeof payload.data !== 'object' || Array.isArray(payload.data)) {
    return null
  }
  return payload.data as Record<string, unknown>
}

function parseExperimentState(activity: ActivityEvent[]): ExperimentState | null {
  const events = activity.filter((e) => e.type.startsWith('telemetry.experiment.'))
  if (events.length === 0) return null

  const state: ExperimentState = { variants: {} }
  const ordered = [...events].reverse()
  for (const event of ordered) {
    const data = getTelemetryData(event)
    if (!data) continue
    switch (event.type) {
      case 'telemetry.experiment.started': {
        const experimentId = typeof data.experiment_id === 'string' ? data.experiment_id : undefined
        const name = typeof data.name === 'string' ? data.name : undefined
        if (experimentId) state.id = experimentId
        if (name) state.name = name
        state.status = typeof data.status === 'string' ? data.status : 'running'
        if (Array.isArray(data.variants)) {
          for (const entry of data.variants) {
            if (!isRecord(entry)) continue
            const id = typeof entry.id === 'string' ? entry.id : undefined
            if (!id) continue
            state.variants[id] = {
              id,
              name: typeof entry.name === 'string' ? entry.name : undefined,
              modelId: typeof entry.model === 'string' ? entry.model : undefined,
              status: 'pending',
            }
          }
        }
        break
      }
      case 'telemetry.experiment.completed':
      case 'telemetry.experiment.failed': {
        state.status = typeof data.status === 'string' ? data.status : event.type.endsWith('failed') ? 'failed' : 'completed'
        break
      }
      case 'telemetry.experiment.variant.started':
      case 'telemetry.experiment.variant.completed':
      case 'telemetry.experiment.variant.failed': {
        const variantId = typeof data.variant_id === 'string' ? data.variant_id : undefined
        if (!variantId) break
        const existing = state.variants[variantId] ?? { id: variantId }
        existing.name = existing.name ?? (typeof data.variant === 'string' ? data.variant : undefined)
        existing.modelId = existing.modelId ?? (typeof data.model_id === 'string' ? data.model_id : undefined)
        const fallbackStatus = event.type.endsWith('started')
          ? 'running'
          : event.type.endsWith('failed')
            ? 'failed'
            : 'completed'
        existing.status = typeof data.status === 'string' ? data.status : fallbackStatus
        if (typeof data.duration_ms === 'number') existing.durationMs = data.duration_ms
        if (typeof data.total_cost === 'number') existing.totalCost = data.total_cost
        if (typeof data.prompt_tokens === 'number') existing.promptTokens = data.prompt_tokens
        if (typeof data.completion_tokens === 'number') existing.completionTokens = data.completion_tokens
        state.variants[variantId] = existing
        break
      }
      default:
        break
    }
  }

  return state
}

function toNumber(value: unknown): number | undefined {
  if (typeof value === 'number' && !Number.isNaN(value)) return value
  if (typeof value === 'string') {
    const parsed = Number(value)
    if (!Number.isNaN(parsed)) return parsed
  }
  return undefined
}

function parseRLMScratchpad(raw: unknown): RLMScratchpadEntry[] {
  if (!Array.isArray(raw)) return []
  return raw
    .filter(isRecord)
    .map((entry) => ({
      key: typeof entry.key === 'string' ? entry.key : undefined,
      type: typeof entry.type === 'string' ? entry.type : undefined,
      summary: typeof entry.summary === 'string' ? entry.summary : undefined,
    }))
    .filter((entry) => entry.key || entry.type || entry.summary)
}

function parseRLMState(activity: ActivityEvent[], sessionId?: string | null): RLMState | null {
  const events = activity
    .filter((e) => e.type === 'telemetry.rlm.iteration')
    .filter((e) => !sessionId || !e.sessionId || e.sessionId === sessionId)
  if (events.length === 0) return null
  const latest = events[0]
  const data = getTelemetryData(latest)
  if (!data) return null

  return {
    iteration: toNumber(data.iteration),
    maxIterations: toNumber(data.max_iterations),
    ready: typeof data.ready === 'boolean' ? data.ready : undefined,
    tokensUsed: toNumber(data.tokens_used),
    summary: typeof data.summary === 'string' ? data.summary : undefined,
    scratchpad: parseRLMScratchpad(data.scratchpad),
  }
}

function experimentStatusBadge(status?: string) {
  switch (status) {
    case 'running':
      return 'bg-[var(--color-warning-subtle)] text-[var(--color-warning)] border-[var(--color-warning)]/20'
    case 'completed':
      return 'bg-[var(--color-success-subtle)] text-[var(--color-success)] border-[var(--color-success)]/20'
    case 'failed':
      return 'bg-[var(--color-error-subtle)] text-[var(--color-error)] border-[var(--color-error)]/20'
    default:
      return 'bg-[var(--color-depth)] text-[var(--color-text-muted)] border-[var(--color-border-subtle)]'
  }
}

function rlmStatusBadge(ready?: boolean) {
  if (ready === true) {
    return 'bg-[var(--color-success-subtle)] text-[var(--color-success)] border-[var(--color-success)]/20'
  }
  if (ready === false) {
    return 'bg-[var(--color-warning-subtle)] text-[var(--color-warning)] border-[var(--color-warning)]/20'
  }
  return 'bg-[var(--color-depth)] text-[var(--color-text-muted)] border-[var(--color-border-subtle)]'
}

function formatDurationMs(ms?: number) {
  if (!ms || ms <= 0) return '—'
  if (ms < 1000) return `${ms}ms`
  const seconds = Math.round(ms / 1000)
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.round(seconds / 60)
  return `${minutes}m`
}

function Section({
  title,
  icon: Icon,
  defaultOpen,
  children,
}: {
  title: string
  icon: typeof Activity
  defaultOpen?: boolean
  children: ReactNode
}) {
  const [open, setOpen] = useState(defaultOpen ?? true)
  return (
    <div className="rounded-2xl border border-[var(--color-border-subtle)] bg-[var(--color-surface)]/70 backdrop-blur-xl overflow-hidden">
      <button
        onClick={() => setOpen((v) => !v)}
        className="w-full px-4 py-3 flex items-center justify-between text-left hover:bg-[var(--color-elevated)] transition-colors"
      >
        <div className="flex items-center gap-2">
          <Icon className="w-4 h-4 text-[var(--color-text-secondary)]" />
          <span className="text-sm font-semibold text-[var(--color-text)]">{title}</span>
        </div>
        {open ? (
          <ChevronDown className="w-4 h-4 text-[var(--color-text-muted)]" />
        ) : (
          <ChevronRight className="w-4 h-4 text-[var(--color-text-muted)]" />
        )}
      </button>
      {open && <div className="px-4 py-4">{children}</div>}
    </div>
  )
}

function todoBadge(status: string) {
  switch (status) {
    case 'completed':
      return 'bg-[var(--color-success-subtle)] text-[var(--color-success)] border-[var(--color-success)]/20'
    case 'in_progress':
      return 'bg-[var(--color-warning-subtle)] text-[var(--color-warning)] border-[var(--color-warning)]/20'
    case 'failed':
      return 'bg-[var(--color-error-subtle)] text-[var(--color-error)] border-[var(--color-error)]/20'
    default:
      return 'bg-[var(--color-depth)] text-[var(--color-text-muted)] border-[var(--color-border-subtle)]'
  }
}

function formatEventTitle(type: string) {
  return type.replace(/^telemetry\./, '').replace(/^approval\./, 'approval ')
}

function touchBadgeClass(operation?: string) {
  const op = (operation || '').toLowerCase()
  if (op.includes('write') || op.includes('delete')) {
    return 'bg-[var(--color-warning-subtle)] text-[var(--color-warning)] border-[var(--color-warning)]/20'
  }
  return 'bg-[var(--color-depth)] text-[var(--color-text-muted)] border-[var(--color-border-subtle)]'
}

function touchBadgeLabel(operation?: string) {
  const op = (operation || '').toLowerCase()
  if (op.includes('write') || op.includes('delete')) {
    return 'W'
  }
  if (op.includes('read')) {
    return 'R'
  }
  return '-'
}

function formatTouchRange(ranges?: ViewLineRange[]) {
  if (!ranges || ranges.length === 0) return ''
  const [first] = ranges
  if (!first) return ''
  const base = first.start === first.end ? `L${first.start}` : `L${first.start}-${first.end}`
  if (ranges.length > 1) {
    return `${base} +${ranges.length - 1}`
  }
  return base
}

function PortholesBody({
  session,
  view,
  approvals,
  activity,
  terminalSessionToken,
  terminalCanConnect,
  onPause,
  onResume,
  onApprove,
  onReject,
  onRefreshApprovals,
}: Props) {
  const workflow = view?.workflow
  const paused = !!workflow?.paused
  const [selectedApproval, setSelectedApproval] = useState<PendingApproval | null>(null)

  const sessionId = session?.id
  const shellEvents = useMemo(() => {
    return activity
      .filter((e) => e.type.startsWith('telemetry.shell.'))
      .filter((e) => !sessionId || !e.sessionId || e.sessionId === sessionId)
      .slice(0, 20)
  }, [activity, sessionId])

  const experimentState = useMemo(() => parseExperimentState(activity), [activity])
  const rlmState = useMemo(() => parseRLMState(activity, sessionId), [activity, sessionId])

  return (
    <>
      <Section title="Workflow" icon={Cpu} defaultOpen>
        {session ? (
          <div className="space-y-3">
            <div className="flex items-center justify-between">
              <div className="text-sm text-[var(--color-text-secondary)]">
                <span className="font-semibold text-[var(--color-text)]">{session.project}</span>
                {workflow?.phase ? (
                  <span className="ml-2 text-xs text-[var(--color-text-muted)]">
                    Phase: <span className="font-mono">{workflow.phase}</span>
                  </span>
                ) : null}
              </div>
              <div className="flex items-center gap-2">
                {paused ? (
                  <button
                    onClick={onResume}
                    disabled={!onResume}
                    className={`inline-flex items-center gap-1.5 px-3 py-2 rounded-xl text-xs font-semibold bg-[var(--color-success)] text-white transition-all ${!onResume ? 'opacity-60 cursor-not-allowed' : 'hover:shadow-lg hover:shadow-[var(--color-success)]/20'}`}
                  >
                    <Play className="w-3.5 h-3.5" />
                    Resume
                  </button>
                ) : (
                  <button
                    onClick={onPause}
                    disabled={!onPause}
                    className={`inline-flex items-center gap-1.5 px-3 py-2 rounded-xl text-xs font-semibold bg-[var(--color-surface)] text-[var(--color-text)] border border-[var(--color-border)] transition-colors ${!onPause ? 'opacity-60 cursor-not-allowed' : 'hover:bg-[var(--color-elevated)]'}`}
                  >
                    <Pause className="w-3.5 h-3.5" />
                    Pause
                  </button>
                )}
              </div>
            </div>

            <div className="grid grid-cols-2 gap-2">
              <div className="rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] p-3">
                <div className="flex items-center gap-2 text-xs text-[var(--color-text-muted)]">
                  <Cpu className="w-3.5 h-3.5" />
                  Active Agent
                </div>
                <div className="mt-1 text-sm font-mono text-[var(--color-text)] truncate">
                  {workflow?.activeAgent || '—'}
                </div>
              </div>

              <div className="rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] p-3">
                <div className="flex items-center gap-2 text-xs text-[var(--color-text-muted)]">
                  <DollarSign className="w-3.5 h-3.5" />
                  Session Cost
                </div>
                <div className="mt-1 text-sm font-mono text-[var(--color-text)]">
                  {view?.metrics?.totalCost != null ? `$${view.metrics.totalCost.toFixed(4)}` : '—'}
                </div>
              </div>

              <div className="rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] p-3">
                <div className="flex items-center gap-2 text-xs text-[var(--color-text-muted)]">
                  <Cpu className="w-3.5 h-3.5" />
                  Total Tokens
                </div>
                <div className="mt-1 text-sm font-mono text-[var(--color-text)]">
                  {view?.metrics?.totalTokens != null ? view.metrics.totalTokens.toLocaleString() : '—'}
                </div>
              </div>
            </div>

            {(workflow?.pauseReason || workflow?.pauseQuestion) && (
              <div className="rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] p-3">
                {workflow.pauseReason && (
                  <div className="text-xs text-[var(--color-text-muted)]">
                    {workflow.pauseReason}
                  </div>
                )}
                {workflow.pauseQuestion && (
                  <div className="mt-1 text-sm text-[var(--color-text)]">
                    {workflow.pauseQuestion}
                  </div>
                )}
              </div>
            )}
          </div>
        ) : (
          <div className="text-sm text-[var(--color-text-muted)]">Select a session to see workflow status.</div>
        )}
      </Section>

      {/* Running Tools - shows active tool calls from telemetry */}
      {view?.activeToolCalls && view.activeToolCalls.length > 0 && (
        <Section title="Running Tools" icon={Wrench} defaultOpen>
          <div className="space-y-2">
            {view.activeToolCalls.map((tool) => (
              <div
                key={tool.id}
                className="flex items-center gap-3 rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] p-3"
              >
                <Loader2 className="w-4 h-4 text-[var(--color-accent)] animate-spin flex-shrink-0" />
                <div className="min-w-0 flex-1">
                  <div className="text-sm font-mono text-[var(--color-text)] truncate">
                    {tool.name}
                  </div>
                  {tool.command && (
                    <div className="text-xs text-[var(--color-text-muted)] truncate mt-0.5">
                      {tool.command}
                    </div>
                  )}
                </div>
              </div>
            ))}
          </div>
        </Section>
      )}

      {rlmState ? (
        <Section title="RLM Loop" icon={Cpu} defaultOpen>
          <div className="space-y-3">
            <div className="rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] p-3">
              <div className="flex items-center justify-between">
                <div className="text-xs text-[var(--color-text-muted)]">Iteration</div>
                <div className="text-sm font-mono text-[var(--color-text)]">
                  {`${rlmState.iteration ?? '—'}${typeof rlmState.maxIterations === 'number' ? `/${rlmState.maxIterations}` : ''}`}
                </div>
              </div>
              <div className="mt-2 flex items-center justify-between text-xs text-[var(--color-text-muted)]">
                <span className={`px-2 py-0.5 rounded-full border text-[10px] ${rlmStatusBadge(rlmState.ready)}`}>
                  {rlmState.ready === true ? 'ready' : rlmState.ready === false ? 'running' : 'unknown'}
                </span>
                <span className="font-mono">
                  {typeof rlmState.tokensUsed === 'number'
                    ? `${rlmState.tokensUsed.toLocaleString()} tokens`
                    : '— tokens'}
                </span>
              </div>
            </div>

            {rlmState.summary ? (
              <div className="rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] p-3">
                <div className="text-xs text-[var(--color-text-muted)]">Coordinator Summary</div>
                <div className="mt-1 text-sm text-[var(--color-text)] leading-relaxed line-clamp-4">
                  {rlmState.summary}
                </div>
              </div>
            ) : null}

            <div className="space-y-2">
              {rlmState.scratchpad.length > 0 ? (
                rlmState.scratchpad.slice(0, 4).map((entry, idx) => (
                  <div
                    key={entry.key ?? entry.summary ?? `scratch-${idx}`}
                    className="flex items-start gap-3 rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] p-3"
                  >
                    <span className="text-[10px] px-2 py-0.5 rounded-full border bg-[var(--color-depth)] text-[var(--color-text-muted)]">
                      {entry.type ?? 'entry'}
                    </span>
                    <div className="min-w-0 flex-1">
                      <div className="text-sm text-[var(--color-text)] truncate">
                        {entry.summary ?? entry.key ?? '—'}
                      </div>
                      {entry.summary && entry.key ? (
                        <div className="text-[10px] text-[var(--color-text-muted)] font-mono truncate mt-0.5">
                          {entry.key}
                        </div>
                      ) : null}
                    </div>
                  </div>
                ))
              ) : (
                <div className="text-sm text-[var(--color-text-muted)]">No scratchpad entries yet.</div>
              )}
            </div>
          </div>
        </Section>
      ) : null}

      {experimentState ? (
        <Section title="Experiments" icon={FlaskConical} defaultOpen={false}>
          <div className="space-y-3">
            <div className="rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] p-3">
              <div className="text-sm font-semibold text-[var(--color-text)]">
                {experimentState.name ?? 'Unnamed experiment'}
              </div>
              <div className="mt-1 flex items-center gap-2 text-xs text-[var(--color-text-muted)]">
                <span className={`px-2 py-0.5 rounded-full border text-[10px] ${experimentStatusBadge(experimentState.status)}`}>
                  {experimentState.status ?? 'pending'}
                </span>
                <span className="font-mono text-[11px]">
                  {experimentState.id ? experimentState.id.slice(0, 8) : '—'}
                </span>
              </div>
            </div>

            <div className="space-y-2">
              {Object.values(experimentState.variants).map((variant) => (
                <div
                  key={variant.id}
                  className="flex items-center justify-between gap-3 rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] p-3"
                >
                  <div className="min-w-0">
                    <div className="text-sm font-mono text-[var(--color-text)] truncate">
                      {variant.name ?? variant.modelId ?? variant.id}
                    </div>
                    <div className="mt-1 flex items-center gap-2 text-[10px] text-[var(--color-text-muted)]">
                      <span className={`px-2 py-0.5 rounded-full border ${experimentStatusBadge(variant.status)}`}>
                        {variant.status ?? 'pending'}
                      </span>
                      <span className="font-mono">
                        {formatDurationMs(variant.durationMs)}
                      </span>
                      {typeof variant.totalCost === 'number' ? (
                        <span className="font-mono">${variant.totalCost.toFixed(4)}</span>
                      ) : null}
                    </div>
                  </div>
                  <div className="text-right text-[10px] text-[var(--color-text-muted)] font-mono">
                    {typeof variant.promptTokens === 'number' ? (
                      <div>{variant.promptTokens} prompt</div>
                    ) : null}
                    {typeof variant.completionTokens === 'number' ? (
                      <div>{variant.completionTokens} completion</div>
                    ) : null}
                  </div>
                </div>
              ))}
            </div>
          </div>
        </Section>
      ) : null}

      {view?.activeTouches && view.activeTouches.length > 0 && (
        <Section title="Active Touches" icon={FileCode} defaultOpen>
          <div className="space-y-2">
            {view.activeTouches.map((touch) => {
              const rangeLabel = formatTouchRange(touch.ranges)
              return (
                <div
                  key={touch.id}
                  className="flex items-start gap-3 rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] p-3"
                >
                  <span
                    className={`text-[10px] px-1.5 py-0.5 rounded font-mono border ${touchBadgeClass(touch.operation)}`}
                  >
                    {touchBadgeLabel(touch.operation)}
                  </span>
                  <div className="min-w-0 flex-1">
                    <div className="text-sm font-mono text-[var(--color-text)] truncate">
                      {touch.filePath}
                    </div>
                    {rangeLabel ? (
                      <div className="text-xs text-[var(--color-text-muted)] font-mono mt-0.5">
                        {rangeLabel}
                      </div>
                    ) : null}
                  </div>
                </div>
              )
            })}
          </div>
        </Section>
      )}

      {/* Recent Files - shows recently accessed files from telemetry */}
      {view?.recentFiles && view.recentFiles.length > 0 && (
        <Section title="Recent Files" icon={FileText} defaultOpen>
          <div className="space-y-1.5">
            {view.recentFiles.map((file) => (
              <div
                key={file.path}
                className="flex items-center gap-2 rounded-lg bg-[var(--color-depth)] border border-[var(--color-border-subtle)] px-3 py-2"
              >
                <span
                  className={`text-[10px] px-1.5 py-0.5 rounded font-mono ${
                    file.operation === 'write'
                      ? 'bg-[var(--color-warning-subtle)] text-[var(--color-warning)]'
                      : 'bg-[var(--color-depth)] text-[var(--color-text-muted)]'
                  }`}
                >
                  {file.operation === 'write' ? 'W' : 'R'}
                </span>
                <span className="text-sm font-mono text-[var(--color-text)] truncate">
                  {file.path.split('/').pop()}
                </span>
              </div>
            ))}
          </div>
        </Section>
      )}

      <Section title="Plan" icon={LayoutGrid} defaultOpen={false}>
        {view?.plan ? (
          <div className="space-y-3">
            <div>
              <div className="text-sm font-semibold text-[var(--color-text)]">
                {view.plan.featureName}
              </div>
              {view.plan.description ? (
                <div className="mt-1 text-xs text-[var(--color-text-muted)] leading-relaxed">
                  {view.plan.description}
                </div>
              ) : null}
            </div>

            {view.plan.progress ? (
              <div className="grid grid-cols-4 gap-2">
                <div className="rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] p-3">
                  <div className="text-[10px] text-[var(--color-text-muted)]">Done</div>
                  <div className="mt-1 text-sm font-mono text-[var(--color-text)] tabular-nums">
                    {view.plan.progress.completed}
                  </div>
                </div>
                <div className="rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] p-3">
                  <div className="text-[10px] text-[var(--color-text-muted)]">Pending</div>
                  <div className="mt-1 text-sm font-mono text-[var(--color-text)] tabular-nums">
                    {view.plan.progress.pending}
                  </div>
                </div>
                <div className="rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] p-3">
                  <div className="text-[10px] text-[var(--color-text-muted)]">Failed</div>
                  <div className="mt-1 text-sm font-mono text-[var(--color-text)] tabular-nums">
                    {view.plan.progress.failed}
                  </div>
                </div>
                <div className="rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] p-3">
                  <div className="text-[10px] text-[var(--color-text-muted)]">Total</div>
                  <div className="mt-1 text-sm font-mono text-[var(--color-text)] tabular-nums">
                    {view.plan.progress.total}
                  </div>
                </div>
              </div>
            ) : null}

            {Array.isArray(view.plan.tasks) && view.plan.tasks.length > 0 ? (
              <div className="space-y-2">
                {view.plan.tasks.slice(0, 30).map((task) => (
                  <div key={task.id} className="flex items-start gap-2">
                    <span
                      className={`mt-0.5 text-[10px] px-2 py-0.5 rounded-full border ${todoBadge(task.status)}`}
                    >
                      {task.status}
                    </span>
                    <div className="text-sm text-[var(--color-text)] leading-snug">
                      {task.title}
                    </div>
                  </div>
                ))}
                {view.plan.tasks.length > 30 ? (
                  <div className="text-xs text-[var(--color-text-muted)]">
                    Showing first 30 tasks.
                  </div>
                ) : null}
              </div>
            ) : (
              <div className="text-sm text-[var(--color-text-muted)]">No plan tasks recorded yet.</div>
            )}
          </div>
        ) : (
          <div className="text-sm text-[var(--color-text-muted)]">No plan attached to this session.</div>
        )}
      </Section>

      <Section title="Terminal" icon={TerminalIcon} defaultOpen>
        <Suspense
          fallback={
            <div className="rounded-xl border border-[var(--color-border-subtle)] bg-[var(--color-void)] h-72 flex items-center justify-center text-sm text-[var(--color-text-muted)]">
              Loading terminal…
            </div>
          }
        >
          <TerminalPane
            sessionId={session?.id ?? null}
            cwd={session?.projectPath}
            sessionToken={terminalSessionToken}
            canConnect={terminalCanConnect}
          />
        </Suspense>
      </Section>

      <Section title="Todos" icon={ListChecks} defaultOpen={false}>
        {view?.todos && view.todos.length > 0 ? (
          <div className="space-y-2">
            {view.todos.slice(0, 40).map((todo) => (
              <div key={todo.id} className="flex items-start gap-2">
                <span className={`mt-0.5 text-[10px] px-2 py-0.5 rounded-full border ${todoBadge(todo.status)}`}>
                  {todo.status}
                </span>
                <div className="text-sm text-[var(--color-text)] leading-snug">
                  {todo.content}
                  {todo.error ? (
                    <div className="mt-1 text-xs text-[var(--color-error)]">
                      {todo.error}
                    </div>
                  ) : null}
                </div>
              </div>
            ))}
          </div>
        ) : (
          <div className="text-sm text-[var(--color-text-muted)]">No todos yet.</div>
        )}
      </Section>

      <Section title="Approvals" icon={ShieldAlert} defaultOpen={false}>
        <div className="flex items-center justify-between mb-3">
          <div className="text-xs text-[var(--color-text-muted)]">
            {approvals.length} pending
          </div>
          <button
            onClick={onRefreshApprovals}
            disabled={!onRefreshApprovals}
            className={`text-xs px-2 py-1 rounded-lg bg-[var(--color-depth)] border border-[var(--color-border-subtle)] text-[var(--color-text-secondary)] transition-colors ${!onRefreshApprovals ? 'opacity-60 cursor-not-allowed' : 'hover:bg-[var(--color-elevated)]'}`}
          >
            Refresh
          </button>
        </div>

        {approvals.length === 0 ? (
          <div className="text-sm text-[var(--color-text-muted)]">No pending approvals.</div>
        ) : (
          <div className="space-y-3">
            {approvals.slice(0, 20).map((approval) => (
              <div key={approval.id} className="rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] p-3">
                <div className="flex items-center justify-between gap-2">
                  <div className="min-w-0">
                    <div className="text-sm font-mono text-[var(--color-text)] truncate">
                      {approval.toolName}
                    </div>
                    {typeof approval.riskScore === 'number' && (
                      <div className="text-xs text-[var(--color-text-muted)] mt-0.5">
                        Risk score: <span className="font-mono">{approval.riskScore}</span>
                      </div>
                    )}
                    {approval.description && (
                      <div className="text-xs text-[var(--color-text-muted)] mt-1 line-clamp-2">
                        {approval.description}
                      </div>
                    )}
                    {approval.filePath && !approval.description && (
                      <div className="text-xs text-[var(--color-text-muted)] mt-1 font-mono truncate">
                        {approval.filePath}
                      </div>
                    )}
                  </div>
                  <div className="flex gap-2">
                    <button
                      onClick={() => setSelectedApproval(approval)}
                      className="px-3 py-1.5 rounded-lg text-xs font-semibold bg-[var(--color-depth)] border border-[var(--color-border-subtle)] text-[var(--color-text-secondary)] hover:bg-[var(--color-elevated)] transition-colors"
                    >
                      Details
                    </button>
                    <button
                      onClick={() => onApprove?.(approval.id)}
                      disabled={!onApprove}
                      className={`px-3 py-1.5 rounded-lg text-xs font-semibold bg-[var(--color-success)] text-white transition-all ${!onApprove ? 'opacity-60 cursor-not-allowed' : 'hover:shadow-md hover:shadow-[var(--color-success)]/20'}`}
                    >
                      Approve
                    </button>
                    <button
                      onClick={() => onReject?.(approval.id)}
                      disabled={!onReject}
                      className={`px-3 py-1.5 rounded-lg text-xs font-semibold bg-[var(--color-surface)] text-[var(--color-text)] border border-[var(--color-border)] transition-colors ${!onReject ? 'opacity-60 cursor-not-allowed' : 'hover:bg-[var(--color-elevated)]'}`}
                    >
                      Reject
                    </button>
                  </div>
                </div>

                {approval.toolInput && (
                  <pre className="mt-2 text-xs bg-[var(--color-abyss)] rounded-lg p-2 overflow-x-auto font-mono text-[var(--color-text-secondary)] border border-[var(--color-border-subtle)]">
                    {JSON.stringify(approval.toolInput, null, 2)}
                  </pre>
                )}
              </div>
            ))}
          </div>
        )}
      </Section>

      <Section title="Activity" icon={Activity} defaultOpen={false}>
        {shellEvents.length === 0 ? (
          <div className="text-sm text-[var(--color-text-muted)]">No recent shell telemetry.</div>
        ) : (
          <div className="space-y-2">
            {shellEvents.map((evt) => {
              const payload = isRecord(evt.payload) ? evt.payload : undefined
              const data = payload && isRecord(payload.data) ? payload.data : undefined
              const command = typeof data?.command === 'string' ? data.command : undefined
              const duration = typeof data?.duration_ms === 'number' ? data.duration_ms : undefined
              const exitCode = typeof data?.exit_code === 'number' ? data.exit_code : undefined
              return (
                <div key={evt.eventId ?? `${evt.type}:${evt.timestamp}`} className="rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] p-3">
                  <div className="flex items-center justify-between gap-2">
                    <div className="text-xs font-semibold text-[var(--color-text-secondary)]">
                      {formatEventTitle(evt.type)}
                    </div>
                    {typeof duration === 'number' && (
                      <div className="text-[10px] text-[var(--color-text-muted)] tabular-nums">
                        {duration}ms
                      </div>
                    )}
                  </div>
                  {command && (
                    <div className="mt-1 text-xs font-mono text-[var(--color-text)] whitespace-pre-wrap break-words">
                      {command}
                    </div>
                  )}
                  {typeof exitCode === 'number' && (
                    <div className="mt-1 text-[10px] text-[var(--color-text-muted)]">
                      exit {exitCode}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        )}
      </Section>

      {selectedApproval && (
        <ApprovalModal
          approval={selectedApproval}
          onClose={() => setSelectedApproval(null)}
          onApprove={(id) => {
            onApprove?.(id)
            setSelectedApproval(null)
          }}
          onReject={(id) => {
            onReject?.(id)
            setSelectedApproval(null)
          }}
        />
      )}
    </>
  )
}

export function PortholesPanel(props: Props) {
  return (
    <aside className="hidden lg:flex flex-col gap-4 p-4 border-l border-[var(--color-border)] bg-[var(--color-abyss)]/95 backdrop-blur-xl overflow-y-auto scrollbar-thin">
      <PortholesBody {...props} />
    </aside>
  )
}

export function PortholesDrawer({
  isOpen,
  onClose,
  ...props
}: Props & { isOpen: boolean; onClose: () => void }) {
  useOverlayControls(isOpen, onClose)
  if (!isOpen) return null

  const title = props.session?.project ? `Portholes · ${props.session.project}` : 'Portholes'

  return (
    <>
      <div className="fixed inset-0 bg-black/50 z-40" onClick={onClose} aria-hidden="true" />

      <div
        className="fixed inset-y-0 right-0 w-[26rem] max-w-[92vw] bg-[var(--color-abyss)] border-l border-[var(--color-border)] z-50 flex flex-col animate-in slide-in-from-right"
        role="dialog"
        aria-modal="true"
        aria-label={title}
      >
        <div className="flex items-center justify-between px-4 py-4 border-b border-[var(--color-border)] safe-area-inset-top">
          <div className="flex items-center gap-2 min-w-0">
            <LayoutGrid className="w-4 h-4 text-[var(--color-text-secondary)]" />
            <h2 className="font-display font-semibold text-[var(--color-text)] truncate">{title}</h2>
          </div>
          <button
            onClick={onClose}
            className="p-2 rounded-lg hover:bg-[var(--color-surface)] transition-colors"
            aria-label="Close portholes"
          >
            <X className="w-5 h-5 text-[var(--color-text-secondary)]" />
          </button>
        </div>

        <div className="flex-1 overflow-y-auto p-4 space-y-4 safe-area-inset-bottom">
          <PortholesBody {...props} />
        </div>
      </div>
    </>
  )
}
