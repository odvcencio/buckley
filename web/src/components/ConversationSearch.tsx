import { useEffect, useMemo, useRef, useState } from 'react'
import { Search, X } from 'lucide-react'

import type { DisplayMessage } from '../types'
import { useOverlayControls } from '../hooks/useOverlayControls'

interface Props {
  isOpen: boolean
  messages: DisplayMessage[]
  onClose: () => void
  onSelectMessage: (id: string) => void
}

function normalizeQuery(value: string) {
  return value.trim().toLowerCase()
}

function truncate(value: string, max: number) {
  if (value.length <= max) return value
  return value.slice(0, max - 3) + '...'
}

export function ConversationSearch({ isOpen, messages, onClose, onSelectMessage }: Props) {
  useOverlayControls(isOpen, onClose)

  const [query, setQuery] = useState('')
  const [activeIndex, setActiveIndex] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)

  const normalized = normalizeQuery(query)
  const results = useMemo(() => {
    if (!normalized) return []
    const matches = messages.filter((msg) => msg.content.toLowerCase().includes(normalized))
    return matches.slice(0, 20)
  }, [messages, normalized])

  useEffect(() => {
    if (!isOpen) return
    setQuery('')
    setActiveIndex(0)
    const handle = window.setTimeout(() => {
      inputRef.current?.focus()
    }, 0)
    return () => window.clearTimeout(handle)
  }, [isOpen])

  useEffect(() => {
    setActiveIndex(0)
  }, [normalized])

  if (!isOpen) return null

  const handleSelect = (id: string) => {
    onSelectMessage(id)
    onClose()
  }

  const handleKeyDown = (event: React.KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      if (results.length === 0) return
      setActiveIndex((idx) => Math.min(results.length - 1, idx + 1))
      return
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault()
      if (results.length === 0) return
      setActiveIndex((idx) => Math.max(0, idx - 1))
      return
    }
    if (event.key === 'Enter') {
      event.preventDefault()
      const selected = results[activeIndex]
      if (selected) {
        handleSelect(selected.id)
      }
    }
  }

  return (
    <>
      <div className="fixed inset-0 bg-black/60 z-40" onClick={onClose} aria-hidden="true" />
      <div
        className="fixed inset-0 z-50 flex items-center justify-center p-4"
        role="dialog"
        aria-modal="true"
        aria-label="Search conversation"
      >
        <div className="w-full max-w-2xl max-h-[80vh] rounded-3xl border border-[var(--color-border)] bg-[var(--color-abyss)]/95 backdrop-blur-xl shadow-2xl shadow-black/50 overflow-hidden flex flex-col">
          <div className="px-6 py-5 border-b border-[var(--color-border)] flex items-center justify-between gap-4">
            <div className="flex items-center gap-3 min-w-0">
              <div className="w-10 h-10 rounded-2xl bg-[var(--color-surface)] border border-[var(--color-border-subtle)] flex items-center justify-center">
                <Search className="w-5 h-5 text-[var(--color-accent)]" />
              </div>
              <div className="min-w-0">
                <div className="text-lg font-display font-bold text-[var(--color-text)] truncate">Search Transcript</div>
                <div className="text-sm text-[var(--color-text-muted)] truncate">Find messages in this session.</div>
              </div>
            </div>
            <button
              onClick={onClose}
              className="p-2 rounded-xl hover:bg-[var(--color-surface)] transition-colors"
              aria-label="Close search"
            >
              <X className="w-5 h-5 text-[var(--color-text-secondary)]" />
            </button>
          </div>

          <div className="px-6 py-4 border-b border-[var(--color-border)]">
            <div className="relative">
              <Search className="w-4 h-4 text-[var(--color-text-muted)] absolute left-3 top-1/2 -translate-y-1/2" />
              <input
                ref={inputRef}
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                onKeyDown={handleKeyDown}
                placeholder="Type to search messages..."
                className="w-full rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] pl-10 pr-4 py-2.5 text-sm text-[var(--color-text)] focus:outline-none focus:ring-2 focus:ring-[var(--color-accent)]/30"
              />
            </div>
          </div>

          <div className="flex-1 overflow-y-auto p-4 space-y-2 scrollbar-thin">
            {normalized === '' ? (
              <div className="rounded-2xl border border-[var(--color-border-subtle)] bg-[var(--color-surface)]/40 p-4 text-sm text-[var(--color-text-muted)]">
                Type a keyword to search this transcript.
              </div>
            ) : results.length === 0 ? (
              <div className="rounded-2xl border border-[var(--color-border-subtle)] bg-[var(--color-surface)]/40 p-4 text-sm text-[var(--color-text-muted)]">
                No matches found.
              </div>
            ) : (
              results.map((msg, index) => (
                <button
                  key={msg.id}
                  onClick={() => handleSelect(msg.id)}
                  onMouseEnter={() => setActiveIndex(index)}
                  className={`w-full text-left rounded-2xl border transition-colors px-4 py-3 ${
                    index === activeIndex
                      ? 'bg-[var(--color-surface)] border-[var(--color-border)]'
                      : 'bg-[var(--color-depth)] border-[var(--color-border-subtle)] hover:bg-[var(--color-surface)]'
                  }`}
                >
                  <div className="flex items-center justify-between gap-3">
                    <div className="text-xs font-mono text-[var(--color-text-muted)] uppercase">
                      {msg.role}
                    </div>
                    <div className="text-[10px] text-[var(--color-text-subtle)]">
                      {new Date(msg.timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
                    </div>
                  </div>
                  <div className="mt-2 text-sm text-[var(--color-text)]">
                    {truncate(msg.content.replace(/\s+/g, ' '), 160)}
                  </div>
                </button>
              ))
            )}
          </div>
        </div>
      </div>
    </>
  )
}
