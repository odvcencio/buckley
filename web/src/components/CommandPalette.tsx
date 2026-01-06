import { useEffect, useMemo, useRef, useState } from 'react'
import { Command, Search, X } from 'lucide-react'

import { useOverlayControls } from '../hooks/useOverlayControls'

type PaletteCommand = {
  title: string
  command: string
  description?: string
  category?: string
}

const COMMANDS: PaletteCommand[] = [
  { title: 'New Conversation', command: '/new', description: 'Start fresh', category: 'Session' },
  { title: 'Clear Messages', command: '/clear', description: 'Clear current transcript', category: 'Session' },
  { title: 'View History', command: '/history', description: 'Show recent messages', category: 'Session' },
  { title: 'Export Conversation', command: '/export', description: 'Export transcript', category: 'Session' },
  { title: 'List Models', command: '/models', description: 'Show available models', category: 'Model' },
  { title: 'Usage Stats', command: '/usage', description: 'Token/cost summary', category: 'Model' },
  { title: 'List Plans', command: '/plans', description: 'Show plans', category: 'Plan' },
  { title: 'Plan Status', command: '/status', description: 'Show plan status', category: 'Plan' },
  { title: 'Show Tools', command: '/tools', description: 'List available tools', category: 'System' },
  { title: 'Show Config', command: '/config', description: 'View config', category: 'System' },
  { title: 'Show Agents', command: '/agents', description: 'List loaded agents', category: 'System' },
  { title: 'Show Help', command: '/help', description: 'Show command help', category: 'System' },
]

function normalizeQuery(value: string) {
  return value.trim().toLowerCase()
}

function matchesCommand(item: PaletteCommand, query: string) {
  if (!query) return true
  const haystack = [item.title, item.command, item.description, item.category].filter(Boolean).join(' ').toLowerCase()
  return haystack.includes(query)
}

interface Props {
  isOpen: boolean
  onClose: () => void
  onRunCommand: (command: string) => void
}

export function CommandPalette({ isOpen, onClose, onRunCommand }: Props) {
  useOverlayControls(isOpen, onClose)

  const [query, setQuery] = useState('')
  const [activeIndex, setActiveIndex] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)

  const normalized = normalizeQuery(query)
  const filtered = useMemo(() => {
    return COMMANDS.filter((item) => matchesCommand(item, normalized))
  }, [normalized])

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

  const runCommand = (command: string) => {
    const trimmed = command.trim()
    if (!trimmed) return
    onRunCommand(trimmed.startsWith('/') ? trimmed : `/${trimmed}`)
    onClose()
  }

  const handleKeyDown = (event: React.KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      if (filtered.length === 0) return
      setActiveIndex((idx) => Math.min(filtered.length - 1, idx + 1))
      return
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault()
      if (filtered.length === 0) return
      setActiveIndex((idx) => Math.max(0, idx - 1))
      return
    }
    if (event.key === 'Enter') {
      event.preventDefault()
      const selected = filtered[activeIndex]
      if (selected) {
        runCommand(selected.command)
      } else if (query.trim()) {
        runCommand(query)
      }
      return
    }
  }

  return (
    <>
      <div className="fixed inset-0 bg-black/60 z-40" onClick={onClose} aria-hidden="true" />
      <div
        className="fixed inset-0 z-50 flex items-center justify-center p-4"
        role="dialog"
        aria-modal="true"
        aria-label="Command palette"
      >
        <div className="w-full max-w-2xl max-h-[80vh] rounded-3xl border border-[var(--color-border)] bg-[var(--color-abyss)]/95 backdrop-blur-xl shadow-2xl shadow-black/50 overflow-hidden flex flex-col">
          <div className="px-6 py-5 border-b border-[var(--color-border)] flex items-center justify-between gap-4">
            <div className="flex items-center gap-3 min-w-0">
              <div className="w-10 h-10 rounded-2xl bg-[var(--color-surface)] border border-[var(--color-border-subtle)] flex items-center justify-center">
                <Command className="w-5 h-5 text-[var(--color-accent)]" />
              </div>
              <div className="min-w-0">
                <div className="text-lg font-display font-bold text-[var(--color-text)] truncate">Command Palette</div>
                <div className="text-sm text-[var(--color-text-muted)] truncate">Run a slash command quickly.</div>
              </div>
            </div>
            <button
              onClick={onClose}
              className="p-2 rounded-xl hover:bg-[var(--color-surface)] transition-colors"
              aria-label="Close command palette"
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
                placeholder="Type a command or filter list..."
                className="w-full rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] pl-10 pr-4 py-2.5 text-sm text-[var(--color-text)] focus:outline-none focus:ring-2 focus:ring-[var(--color-accent)]/30"
              />
            </div>
          </div>

          <div className="flex-1 overflow-y-auto p-4 space-y-2 scrollbar-thin">
            {filtered.length === 0 ? (
              <div className="rounded-2xl border border-[var(--color-border-subtle)] bg-[var(--color-surface)]/40 p-4 text-sm text-[var(--color-text-muted)]">
                No matching commands. Press Enter to run what you typed.
              </div>
            ) : (
              filtered.map((item, index) => (
                <button
                  key={item.command}
                  onClick={() => runCommand(item.command)}
                  onMouseEnter={() => setActiveIndex(index)}
                  className={`w-full text-left rounded-2xl border transition-colors px-4 py-3 ${
                    index === activeIndex
                      ? 'bg-[var(--color-surface)] border-[var(--color-border)]'
                      : 'bg-[var(--color-depth)] border-[var(--color-border-subtle)] hover:bg-[var(--color-surface)]'
                  }`}
                >
                  <div className="flex items-center justify-between gap-3">
                    <div className="min-w-0">
                      <div className="text-sm font-semibold text-[var(--color-text)] truncate">{item.title}</div>
                      {item.description && (
                        <div className="text-xs text-[var(--color-text-muted)] truncate mt-0.5">{item.description}</div>
                      )}
                    </div>
                    <div className="text-xs font-mono text-[var(--color-text-secondary)] bg-[var(--color-abyss)] px-2 py-1 rounded-lg border border-[var(--color-border-subtle)]">
                      {item.command}
                    </div>
                  </div>
                  {item.category && (
                    <div className="mt-2 text-[10px] text-[var(--color-text-muted)] uppercase tracking-wide">
                      {item.category}
                    </div>
                  )}
                </button>
              ))
            )}
          </div>
        </div>
      </div>
    </>
  )
}
