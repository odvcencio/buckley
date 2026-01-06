import { User, Bot, AlertCircle, Sparkles, Terminal } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkBreaks from 'remark-breaks'
import remarkGfm from 'remark-gfm'
import rehypeHighlight from 'rehype-highlight'
import type { Components } from 'react-markdown'

import type { DisplayMessage } from '../types'

interface Props {
  message: DisplayMessage
}

const markdownComponents: Components = {
  a({ href, children, ...props }) {
    const isExternal = href && /^https?:\/\//i.test(href)
    return (
      <a
        href={href}
        target={isExternal ? '_blank' : undefined}
        rel={isExternal ? 'noreferrer' : undefined}
        {...props}
      >
        {children}
      </a>
    )
  },
  pre({ children, ...props }) {
    return (
      <pre {...props} className="message-markdown__pre">
        {children}
      </pre>
    )
  },
  code({ inline, className, children, ...props }) {
    if (inline) {
      return (
        <code className="message-markdown__inline-code" {...props}>
          {children}
        </code>
      )
    }
    const mergedClass = className ? `message-markdown__code ${className}` : 'message-markdown__code'
    return (
      <code className={mergedClass} {...props}>
        {children}
      </code>
    )
  },
  table({ children, ...props }) {
    return (
      <div className="message-markdown__table">
        <table {...props}>{children}</table>
      </div>
    )
  },
  input({ type, checked, ...props }) {
    if (type === 'checkbox') {
      return (
        <input
          type="checkbox"
          checked={checked}
          readOnly
          disabled
          className="message-markdown__checkbox"
          {...props}
        />
      )
    }
    return <input type={type} {...props} />
  },
}

export function MessageBubble({ message }: Props) {
  const isUser = message.role === 'user'
  const isSystem = message.role === 'system'
  const isTool = message.role === 'tool'
  const isAssistant = message.role === 'assistant'
  const isStreaming = !!message.streaming && isAssistant

  return (
    <div
      className={`
        group flex gap-3 ${isUser ? 'flex-row-reverse' : ''}
        reveal
      `}
      style={{ animationDelay: '0ms' }}
    >
      {/* Avatar with subtle glow on AI messages */}
      <div
        className={`
          relative flex-shrink-0 w-9 h-9 rounded-xl flex items-center justify-center
          transition-all duration-300 ease-out-expo
          ${isUser
            ? 'bg-[var(--color-accent)] text-[var(--color-text-inverse)] shadow-md'
            : isSystem
              ? 'bg-[var(--color-error-subtle)] text-[var(--color-error)] border border-[var(--color-error)]/20'
              : isTool
                ? 'bg-[var(--color-depth)] text-[var(--color-text-secondary)] border border-[var(--color-border-subtle)]'
              : 'bg-[var(--color-surface)] text-[var(--color-text-secondary)] border border-[var(--color-border-subtle)]'
          }
          ${isStreaming ? 'ring-2 ring-[var(--color-streaming)]/30 ring-offset-2 ring-offset-[var(--color-void)]' : ''}
        `}
      >
        {isUser ? (
          <User className="w-4 h-4" />
        ) : isSystem ? (
          <AlertCircle className="w-4 h-4" />
        ) : isTool ? (
          <Terminal className="w-4 h-4" />
        ) : isStreaming ? (
          <Sparkles className="w-4 h-4 text-[var(--color-streaming)] animate-pulse" />
        ) : (
          <Bot className="w-4 h-4" />
        )}

        {/* Subtle glow effect for AI avatar during streaming */}
        {isStreaming && (
          <div className="absolute inset-0 rounded-xl bg-[var(--color-streaming)]/20 blur-md animate-pulse" />
        )}
      </div>

      {/* Content */}
      <div
        className={`
          flex-1 max-w-[85%] md:max-w-[75%]
          ${isUser ? 'text-right' : ''}
        `}
      >
        <div
          className={`
            relative inline-block px-4 py-3 rounded-2xl
            transition-all duration-200
            ${isUser
              ? 'bg-gradient-to-br from-[var(--color-accent)] to-[var(--color-accent-active)] text-[var(--color-text-inverse)] rounded-tr-md shadow-lg shadow-[var(--color-accent)]/20'
              : isSystem
                ? 'bg-[var(--color-error-subtle)] text-[var(--color-error)] border border-[var(--color-error)]/20 rounded-tl-md'
                : isTool
                  ? 'bg-[var(--color-depth)] text-[var(--color-text-secondary)] border border-[var(--color-border-subtle)] rounded-tl-md'
                : 'bg-[var(--color-surface)] text-[var(--color-text)] rounded-tl-md border border-[var(--color-border-subtle)] shadow-sm'
            }
            ${isStreaming ? 'border-[var(--color-streaming)]/30' : ''}
          `}
        >
          {/* Content with proper prose styling */}
          <div className="message-markdown">
            <ReactMarkdown
              remarkPlugins={[remarkGfm, remarkBreaks]}
              rehypePlugins={[rehypeHighlight]}
              components={markdownComponents}
              skipHtml
            >
              {message.content}
            </ReactMarkdown>
            {isStreaming && (
              <span className="inline-flex gap-1 ml-2 align-middle">
                <span className="typing-dot w-1.5 h-1.5 rounded-full bg-current opacity-70" />
                <span className="typing-dot w-1.5 h-1.5 rounded-full bg-current opacity-70" />
                <span className="typing-dot w-1.5 h-1.5 rounded-full bg-current opacity-70" />
              </span>
            )}
          </div>

          {/* Subtle gradient overlay for AI messages */}
          {isAssistant && (
            <div
              className="
                absolute inset-0 rounded-2xl rounded-tl-md pointer-events-none
                bg-gradient-to-br from-[var(--color-accent)]/[0.02] to-transparent
                opacity-0 group-hover:opacity-100 transition-opacity duration-300
              "
            />
          )}
        </div>

        {/* Timestamp with fade-in on hover */}
        <div
          className={`
            text-[10px] text-[var(--color-text-subtle)] mt-1.5
            opacity-0 group-hover:opacity-100 transition-opacity duration-200
            ${isUser ? 'text-right pr-1' : 'text-left pl-1'}
          `}
        >
          {new Date(message.timestamp).toLocaleTimeString([], {
            hour: '2-digit',
            minute: '2-digit',
          })}
        </div>
      </div>
    </div>
  )
}
