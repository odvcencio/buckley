// Event envelope (Connect stream / hub events).
export interface WSEvent {
  type: string
  sessionId?: string
  payload?: unknown
  timestamp?: string
  eventId?: string
}

// Session summary (from storage snapshots and IPC APIs).
export interface Session {
  id: string
  projectPath?: string
  gitRepo?: string
  gitBranch?: string
  status: 'active' | 'paused' | 'completed' | string
  createdAt: string
  lastActive: string
  messageCount?: number
  todoCount?: number
  totalTokens?: number
  totalCost?: number
  completedAt?: string | null
  agentId?: string
  model?: string
}

// Render-friendly view model (from `view.patch` events).
export interface ViewSessionState {
  id: string
  title?: string
  status: ViewSessionStatus
  workflow: ViewWorkflowStatus
  transcript: ViewTranscriptPage
  todos?: ViewTodo[]
  plan?: ViewPlanSnapshot
  metrics: ViewMetrics
  activity?: string[]

  // Runtime state (live telemetry-derived)
  isStreaming?: boolean
  activeToolCalls?: ViewToolCall[]
  recentFiles?: ViewFileTouch[]
  activeTouches?: ViewCodeTouch[]
}

// Runtime tool call state (from telemetry).
export interface ViewToolCall {
  id: string
  name: string
  status: 'running' | 'completed' | 'failed'
  command?: string
  startedAt: string
}

// Recently accessed file (from telemetry).
export interface ViewFileTouch {
  path: string
  operation: 'read' | 'write' | 'create'
  touchedAt: string
}

// Active code touch state (from telemetry).
export interface ViewCodeTouch {
  id: string
  toolName?: string
  operation: string
  filePath: string
  ranges?: ViewLineRange[]
  startedAt: string
  expiresAt?: string
}

export interface ViewLineRange {
  start: number
  end: number
}

export interface ViewSessionStatus {
  state: string
  paused?: boolean
  awaitingUser?: boolean
  reason?: string
  question?: string
  lastUpdated?: string
}

export interface ViewWorkflowStatus {
  phase?: string
  activeAgent?: string
  paused?: boolean
  awaitingUser?: boolean
  pauseReason?: string
  pauseQuestion?: string
  pauseAt?: string
}

export interface ViewTranscriptPage {
  messages: ViewMessage[]
  hasMore: boolean
  nextOffset: number
}

export interface ViewMessage {
  id: string
  role: string
  content: string
  contentType?: string
  reasoning?: string
  tokens?: number
  timestamp: string
  isSummary?: boolean
}

export interface ViewTodo {
  id: number
  content: string
  activeForm?: string
  status: string
  completedAt?: string
  error?: string
}

export interface ViewPlanSnapshot {
  id: string
  featureName: string
  description?: string
  tasks?: ViewPlanTask[]
  progress?: ViewTaskSummary
}

export interface ViewPlanTask {
  id: string
  title: string
  status: string
  type?: string
}

export interface ViewTaskSummary {
  completed: number
  failed: number
  pending: number
  total: number
}

export interface ViewMetrics {
  totalTokens: number
  totalCost: number
}

// Frontend-specific types for conversation display
export interface DisplayMessage {
  id: string
  role: 'user' | 'assistant' | 'system' | 'tool'
  content: string
  timestamp: string
  streaming?: boolean
}

// Storage-layer message shape (used by older handlers and some RPC payloads).
export interface StorageMessage {
  id: string
  sessionId?: string
  role: string
  content: string
  createdAt: string
  timestamp?: string
  toolName?: string
  toolCallId?: string
}

export interface ToolCall {
  id: string
  name: string
  arguments: Record<string, unknown>
  status: 'pending' | 'running' | 'completed' | 'failed'
  result?: ToolResult
  requiresApproval: boolean
}

export interface ToolResult {
  success: boolean
  output?: string
  error?: string
  abridged?: boolean
}

export interface PendingApproval {
  id: string
  sessionId: string
  toolName: string
  toolInput?: Record<string, unknown>
  riskScore?: number
  riskReasons?: string[]
  status?: string
  createdAt?: string
  expiresAt?: string
  operationType?: string
  description?: string
  command?: string
  filePath?: string
  diffLines?: DiffLine[]
  addedLines?: number
  removedLines?: number
}

export interface DiffLine {
  type: 'add' | 'remove' | 'context'
  content: string
}

// Conversation state
export interface ConversationState {
  messages: DisplayMessage[]
  toolCalls: Map<string, ToolCall>
  pendingApprovals: PendingApproval[]
  isStreaming: boolean
}

// Command types for sending to backend
export interface SessionCommand {
  sessionId: string
  type: 'input' | 'slash' | 'approval'
  content: string
}

// Display-friendly session type with derived fields
export interface DisplaySession extends Session {
  project: string
  branch: string
}
