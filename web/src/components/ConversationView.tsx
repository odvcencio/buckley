import { useEffect, useRef } from 'react'
import { MessageBubble } from './MessageBubble'
import { ToolCallCard } from './ToolCallCard'
import { Zap, Terminal, FileCode, GitBranch } from 'lucide-react'
import type { DisplayMessage, ToolCall } from '../types'

interface Props {
  messages: DisplayMessage[]
  toolCalls: Map<string, ToolCall>
  isStreaming: boolean
  onApproveToolCall?: (id: string) => void
  onRejectToolCall?: (id: string) => void
}

export function ConversationView({
  messages,
  toolCalls,
  isStreaming,
  onApproveToolCall,
  onRejectToolCall,
}: Props) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const bottomRef = useRef<HTMLDivElement>(null)

  // Auto-scroll to bottom on new messages
  useEffect(() => {
    if (bottomRef.current) {
      bottomRef.current.scrollIntoView({ behavior: 'smooth' })
    }
  }, [messages, isStreaming])

  // Build interleaved view of messages and tool calls
  const items: Array<{ type: 'message' | 'tool'; data: DisplayMessage | ToolCall; key: string }> = []

  messages.forEach((msg) => {
    items.push({ type: 'message', data: msg, key: `msg-${msg.id}` })
  })

  // Insert tool calls after their initiating assistant messages
  toolCalls.forEach((tc, id) => {
    items.push({ type: 'tool', data: tc, key: `tool-${id}` })
  })

  // Sort by timestamp (messages have timestamp, tool calls we'll place at end for now)
  items.sort((a, b) => {
    const aTime = a.type === 'message' ? new Date((a.data as DisplayMessage).timestamp).getTime() : Infinity
    const bTime = b.type === 'message' ? new Date((b.data as DisplayMessage).timestamp).getTime() : Infinity
    return aTime - bTime
  })

  return (
    <div
      ref={scrollRef}
      className="flex-1 overflow-y-auto scrollbar-thin px-4 py-6"
    >
      {messages.length === 0 && !isStreaming ? (
        <div className="h-full flex flex-col items-center justify-center text-center px-6">
          {/* Hero icon with glow */}
          <div className="relative mb-8">
            <div className="w-20 h-20 rounded-2xl bg-gradient-to-br from-[var(--color-accent)] to-[var(--color-accent-active)] flex items-center justify-center shadow-xl shadow-[var(--color-accent)]/25">
              <Zap className="w-10 h-10 text-[var(--color-text-inverse)]" />
            </div>
            {/* Ambient glow */}
            <div className="absolute inset-0 rounded-2xl bg-[var(--color-accent)]/40 blur-2xl -z-10" />
          </div>

          <h2 className="text-2xl font-display font-bold text-[var(--color-text)] mb-3 tracking-tight">
            Ready to assist
          </h2>
          <p className="text-sm text-[var(--color-text-secondary)] max-w-sm mb-8 leading-relaxed">
            Start a conversation or connect to an active session to see messages here.
          </p>

          {/* Capability hints */}
          <div className="grid grid-cols-3 gap-4 max-w-md stagger-children">
            <div className="group flex flex-col items-center gap-2 p-4 rounded-xl bg-[var(--color-surface)] border border-[var(--color-border-subtle)] hover:border-[var(--color-border)] transition-all duration-200">
              <div className="p-2 rounded-lg bg-[var(--color-depth)] text-[var(--color-accent)] group-hover:scale-110 transition-transform">
                <Terminal className="w-4 h-4" />
              </div>
              <span className="text-xs text-[var(--color-text-muted)]">Shell</span>
            </div>
            <div className="group flex flex-col items-center gap-2 p-4 rounded-xl bg-[var(--color-surface)] border border-[var(--color-border-subtle)] hover:border-[var(--color-border)] transition-all duration-200">
              <div className="p-2 rounded-lg bg-[var(--color-depth)] text-[var(--color-accent)] group-hover:scale-110 transition-transform">
                <FileCode className="w-4 h-4" />
              </div>
              <span className="text-xs text-[var(--color-text-muted)]">Code</span>
            </div>
            <div className="group flex flex-col items-center gap-2 p-4 rounded-xl bg-[var(--color-surface)] border border-[var(--color-border-subtle)] hover:border-[var(--color-border)] transition-all duration-200">
              <div className="p-2 rounded-lg bg-[var(--color-depth)] text-[var(--color-accent)] group-hover:scale-110 transition-transform">
                <GitBranch className="w-4 h-4" />
              </div>
              <span className="text-xs text-[var(--color-text-muted)]">Git</span>
            </div>
          </div>
        </div>
      ) : (
        <div className="max-w-3xl mx-auto space-y-4">
          {items.map((item, index) => {
            if (item.type === 'message') {
              return (
                <div
                  key={item.key}
                  id={`message-${(item.data as DisplayMessage).id}`}
                  className="reveal"
                  style={{ animationDelay: `${Math.min(index * 50, 200)}ms` }}
                >
                  <MessageBubble message={item.data as DisplayMessage} />
                </div>
              )
            } else {
              const tc = item.data as ToolCall
              return (
                <div
                  key={item.key}
                  className="ml-12 reveal"
                  style={{ animationDelay: `${Math.min(index * 50, 200)}ms` }}
                >
                  <ToolCallCard
                    toolCall={tc}
                    onApprove={() => onApproveToolCall?.(tc.id)}
                    onReject={() => onRejectToolCall?.(tc.id)}
                  />
                </div>
              )
            }
          })}

          {/* Typing indicator when streaming with no content yet */}
          {isStreaming && messages.every((m) => !m.streaming) && (
            <div className="flex gap-3 reveal">
              <div className="relative w-9 h-9 rounded-xl bg-[var(--color-surface)] border border-[var(--color-streaming)]/30 flex items-center justify-center">
                <span className="inline-flex gap-1">
                  <span className="typing-dot w-1.5 h-1.5 rounded-full bg-[var(--color-streaming)]" />
                  <span className="typing-dot w-1.5 h-1.5 rounded-full bg-[var(--color-streaming)]" />
                  <span className="typing-dot w-1.5 h-1.5 rounded-full bg-[var(--color-streaming)]" />
                </span>
                {/* Glow effect */}
                <div className="absolute inset-0 rounded-xl bg-[var(--color-streaming)]/20 blur-md animate-pulse" />
              </div>
            </div>
          )}

          <div ref={bottomRef} />
        </div>
      )}
    </div>
  )
}
