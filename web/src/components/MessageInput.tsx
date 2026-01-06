import { useState, useRef, useEffect } from 'react'
import { Send, Command, Pause, Play, Loader2, FileCode } from 'lucide-react'

import { listProjectFiles } from '../lib/api'

interface Props {
  onSend: (message: string) => void
  onCommand?: (command: string) => void
  onPause?: () => void
  onResume?: () => void
  onOpenCommandPalette?: () => void
  disabled?: boolean
  isPaused?: boolean
  isStreaming?: boolean
  placeholder?: string
}

type FileTrigger = {
  start: number
  query: string
}

function findFileTrigger(value: string, cursor: number): FileTrigger | null {
  if (cursor < 0 || cursor > value.length) return null
  const prefix = value.slice(0, cursor)
  const atIndex = prefix.lastIndexOf('@')
  if (atIndex === -1) return null
  if (atIndex > 0 && !/\s/.test(prefix[atIndex - 1])) return null
  const query = prefix.slice(atIndex + 1)
  if (query.includes(' ') || query.includes('\n') || query.includes('\t')) return null
  return { start: atIndex, query }
}

export function MessageInput({
  onSend,
  onCommand,
  onPause,
  onResume,
  onOpenCommandPalette,
  disabled = false,
  isPaused = false,
  isStreaming = false,
  placeholder = 'Send a message...',
}: Props) {
  const [value, setValue] = useState('')
  const [isFocused, setIsFocused] = useState(false)
  const [cursorPos, setCursorPos] = useState(0)
  const [fileResults, setFileResults] = useState<string[]>([])
  const [fileLoading, setFileLoading] = useState(false)
  const [fileError, setFileError] = useState<string | null>(null)
  const [fileActiveIndex, setFileActiveIndex] = useState(0)
  const [filePickerEnabled, setFilePickerEnabled] = useState(true)
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  // Auto-resize textarea
  useEffect(() => {
    const textarea = textareaRef.current
    if (textarea) {
      textarea.style.height = 'auto'
      textarea.style.height = `${Math.min(textarea.scrollHeight, 200)}px`
    }
  }, [value])

  const fileTrigger = findFileTrigger(value, cursorPos)
  const filePickerOpen = !disabled && filePickerEnabled && !!fileTrigger
  const fileQuery = fileTrigger?.query ?? ''

  useEffect(() => {
    if (fileTrigger) return
    setFilePickerEnabled(true)
  }, [fileTrigger])

  useEffect(() => {
    if (!filePickerOpen) {
      setFileLoading(false)
      setFileError(null)
      setFileResults([])
      return
    }

    const controller = new AbortController()
    setFileLoading(true)
    setFileError(null)

    const handle = window.setTimeout(async () => {
      try {
        const results = await listProjectFiles(fileQuery, 80, controller.signal)
        setFileResults(results)
        setFileActiveIndex(0)
      } catch (err) {
        if (controller.signal.aborted) return
        setFileError(err instanceof Error ? err.message : 'Failed to load files.')
        setFileResults([])
      } finally {
        if (!controller.signal.aborted) {
          setFileLoading(false)
        }
      }
    }, 150)

    return () => {
      controller.abort()
      window.clearTimeout(handle)
    }
  }, [filePickerOpen, fileQuery])

  const syncCursor = (target?: HTMLTextAreaElement | null) => {
    const el = target ?? textareaRef.current
    if (!el) return
    setCursorPos(el.selectionStart ?? el.value.length)
  }

  const insertTextAtCursor = (text: string) => {
    if (disabled) return
    const el = textareaRef.current
    const start = el?.selectionStart ?? value.length
    const end = el?.selectionEnd ?? value.length
    const next = value.slice(0, start) + text + value.slice(end)
    setValue(next)
    setFilePickerEnabled(true)
    window.requestAnimationFrame(() => {
      if (!el) return
      const pos = start + text.length
      el.focus()
      el.setSelectionRange(pos, pos)
      setCursorPos(pos)
    })
  }

  const insertFile = (path: string) => {
    if (disabled) return
    if (!fileTrigger) return
    const before = value.slice(0, fileTrigger.start)
    const after = value.slice(fileTrigger.start + fileTrigger.query.length + 1)
    const next = `${before}@${path} ${after}`
    setValue(next)
    setFilePickerEnabled(true)
    setFileResults([])
    window.requestAnimationFrame(() => {
      const el = textareaRef.current
      if (!el) return
      const pos = before.length + path.length + 2
      el.focus()
      el.setSelectionRange(pos, pos)
      setCursorPos(pos)
    })
  }

  const handleSubmit = () => {
    const trimmed = value.trim()
    if (!trimmed || disabled) return

    if (trimmed.startsWith('/')) {
      onCommand?.(trimmed)
    } else {
      onSend(trimmed)
    }
    setValue('')
    setCursorPos(0)
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (filePickerOpen) {
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        if (fileResults.length === 0) return
        setFileActiveIndex((idx) => Math.min(fileResults.length - 1, idx + 1))
        return
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault()
        if (fileResults.length === 0) return
        setFileActiveIndex((idx) => Math.max(0, idx - 1))
        return
      }
      if (e.key === 'Escape') {
        e.preventDefault()
        setFilePickerEnabled(false)
        return
      }
      if (e.key === 'Enter' && !e.shiftKey && fileResults.length > 0) {
        e.preventDefault()
        const selected = fileResults[fileActiveIndex]
        if (selected) {
          insertFile(selected)
        }
        return
      }
    }

    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      handleSubmit()
    }
  }

  const canSubmit = value.trim().length > 0 && !disabled

  return (
    <div className="border-t border-[var(--color-border)] bg-[var(--color-abyss)]/95 backdrop-blur-xl px-4 py-4 safe-area-inset-bottom">
      <div className="max-w-3xl mx-auto">
        {/* Main input container with glow effect */}
        <div
          className={`
            relative rounded-2xl transition-all duration-300
            ${isFocused
              ? 'shadow-[0_0_0_1px_var(--color-accent),0_0_20px_var(--color-accent-glow)]'
              : 'shadow-[0_0_0_1px_var(--color-border-subtle)]'
            }
          `}
        >
          {/* Gradient border effect */}
          <div
            className={`
              absolute -inset-px rounded-2xl pointer-events-none
              bg-gradient-to-r from-[var(--color-accent)]/50 via-[var(--color-accent)]/20 to-[var(--color-accent)]/50
              opacity-0 transition-opacity duration-300
              ${isFocused ? 'opacity-100' : ''}
            `}
          />

          <div className="relative bg-[var(--color-surface)] rounded-2xl">
            {filePickerOpen && (
              <div className="absolute left-0 right-0 bottom-full mb-2 z-30">
                <div className="rounded-2xl border border-[var(--color-border)] bg-[var(--color-abyss)]/95 backdrop-blur-xl shadow-xl shadow-black/40 overflow-hidden">
                  <div className="px-4 py-3 border-b border-[var(--color-border)] flex items-center justify-between">
                    <div className="text-xs font-semibold text-[var(--color-text)]">File Picker</div>
                    <div className="text-[10px] text-[var(--color-text-muted)] font-mono">
                      @{fileQuery || '...'}
                    </div>
                  </div>
                  <div className="max-h-56 overflow-y-auto scrollbar-thin">
                    {fileLoading ? (
                      <div className="px-4 py-3 text-sm text-[var(--color-text-muted)]">Loading files...</div>
                    ) : fileError ? (
                      <div className="px-4 py-3 text-sm text-[var(--color-error)]">{fileError}</div>
                    ) : fileResults.length === 0 ? (
                      <div className="px-4 py-3 text-sm text-[var(--color-text-muted)]">No matches.</div>
                    ) : (
                      fileResults.map((path, index) => (
                        <button
                          key={path}
                          onClick={() => insertFile(path)}
                          onMouseEnter={() => setFileActiveIndex(index)}
                          className={`w-full text-left px-4 py-2.5 text-sm font-mono transition-colors ${
                            index === fileActiveIndex
                              ? 'bg-[var(--color-surface)] text-[var(--color-text)]'
                              : 'text-[var(--color-text-secondary)] hover:bg-[var(--color-surface)]'
                          }`}
                        >
                          {path}
                        </button>
                      ))
                    )}
                  </div>
                </div>
              </div>
            )}
            <textarea
              ref={textareaRef}
              value={value}
              onChange={(e) => {
                setValue(e.target.value)
                syncCursor(e.target)
              }}
              onKeyDown={handleKeyDown}
              onKeyUp={() => syncCursor()}
              onClick={() => syncCursor()}
              onFocus={() => setIsFocused(true)}
              onBlur={() => setIsFocused(false)}
              placeholder={placeholder}
              disabled={disabled}
              rows={1}
              className="
                w-full resize-none bg-transparent rounded-2xl
                px-5 py-4 pr-14
                text-[var(--text-sm)] text-[var(--color-text)]
                placeholder:text-[var(--color-text-muted)]
                focus:outline-none
                disabled:opacity-50 disabled:cursor-not-allowed
              "
            />

            {/* Send button with magnetic hover effect */}
            <button
              onClick={handleSubmit}
              disabled={!canSubmit}
              className={`
                absolute right-3 bottom-3
                p-2.5 rounded-xl
                transition-all duration-200 ease-out-expo
                ${canSubmit
                  ? 'bg-[var(--color-accent)] text-[var(--color-text-inverse)] hover:scale-105 hover:shadow-lg hover:shadow-[var(--color-accent)]/30 active:scale-95'
                  : 'bg-[var(--color-elevated)] text-[var(--color-text-muted)] cursor-not-allowed'
                }
              `}
            >
              {isStreaming ? (
                <Loader2 className="w-4 h-4 animate-spin" />
              ) : (
                <Send className="w-4 h-4" />
              )}
            </button>
          </div>
        </div>

        {/* Quick actions row with subtle styling */}
        <div className="flex items-center gap-2 mt-3 px-1">
          <button
            onClick={() => {
              if (onOpenCommandPalette) {
                onOpenCommandPalette()
              } else {
                insertTextAtCursor('/')
              }
            }}
            className="
              group flex items-center gap-1.5 px-3 py-1.5
              text-xs text-[var(--color-text-muted)]
              rounded-lg
              hover:text-[var(--color-text-secondary)] hover:bg-[var(--color-surface)]/50
              transition-all duration-200
            "
          >
            <Command className="w-3 h-3 transition-transform group-hover:scale-110" />
            <span>Commands</span>
            <kbd className="hidden sm:inline-block ml-1 px-1.5 py-0.5 text-[10px] font-mono bg-[var(--color-surface)] rounded border border-[var(--color-border-subtle)]">
              Ctrl+P
            </kbd>
          </button>

          <button
            onClick={() => insertTextAtCursor('@')}
            className="
              group flex items-center gap-1.5 px-3 py-1.5
              text-xs text-[var(--color-text-muted)]
              rounded-lg
              hover:text-[var(--color-text-secondary)] hover:bg-[var(--color-surface)]/50
              transition-all duration-200
            "
          >
            <FileCode className="w-3 h-3 transition-transform group-hover:scale-110" />
            <span>Files</span>
            <kbd className="hidden sm:inline-block ml-1 px-1.5 py-0.5 text-[10px] font-mono bg-[var(--color-surface)] rounded border border-[var(--color-border-subtle)]">
              @
            </kbd>
          </button>

          <div className="flex-1" />

          {isPaused ? (
            <button
              onClick={onResume}
              className="
                group flex items-center gap-1.5 px-3 py-1.5
                text-xs font-medium
                text-[var(--color-success)] bg-[var(--color-success-subtle)]
                rounded-lg border border-[var(--color-success)]/20
                hover:border-[var(--color-success)]/40 hover:shadow-sm hover:shadow-[var(--color-success)]/10
                transition-all duration-200
              "
            >
              <Play className="w-3 h-3 transition-transform group-hover:scale-110" />
              <span>Resume</span>
            </button>
          ) : isStreaming ? (
            <button
              onClick={onPause}
              className="
                group flex items-center gap-1.5 px-3 py-1.5
                text-xs text-[var(--color-text-muted)]
                rounded-lg
                hover:text-[var(--color-warning)] hover:bg-[var(--color-warning-subtle)]
                transition-all duration-200
              "
            >
              <Pause className="w-3 h-3 transition-transform group-hover:scale-110" />
              <span>Pause</span>
            </button>
          ) : null}

          {/* Character count indicator */}
          {value.length > 0 && (
            <span className="text-[10px] text-[var(--color-text-subtle)] tabular-nums">
              {value.length.toLocaleString()}
            </span>
          )}
        </div>
      </div>
    </div>
  )
}
