import { useState, useRef, useEffect } from 'react'
import { Send, Command, Pause, Play, Square, ListPlus, FileCode } from 'lucide-react'

import { listProjectFiles, type ModelOption } from '../lib/api'

interface Props {
  onSend: (message: string) => void
  onQueue?: (message: string) => void
  onCommand?: (command: string) => void
  onInterrupt?: () => void
  onPause?: () => void
  onResume?: () => void
  onOpenCommandPalette?: () => void
  disabled?: boolean
  isPaused?: boolean
  isStreaming?: boolean
  placeholder?: string
  commandStatus?: string
  models?: ModelOption[]
  currentModel?: string
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

function ModelPicker({ open, models, results, activeIndex, currentModel, onSelect, onActiveIndex }: {
  open: boolean
  models: ModelOption[]
  results: ModelOption[]
  activeIndex: number
  currentModel?: string
  onSelect: (modelID: string) => void
  onActiveIndex: (index: number) => void
}) {
  if (!open) return null
  return (
    <div className="absolute left-0 right-0 bottom-full mb-2 z-30">
      <div className="rounded-xl border border-[var(--color-border)] bg-[var(--color-abyss)]/95 shadow-xl overflow-hidden">
        <div className="px-4 py-3 border-b border-[var(--color-border)] flex items-center justify-between">
          <div className="text-xs font-semibold text-[var(--color-text)]">OpenRouter models</div>
          <div className="text-[10px] text-[var(--color-text-muted)] font-mono">{models.length.toLocaleString()} available</div>
        </div>
        <div className="max-h-72 overflow-y-auto scrollbar-thin">
          {results.length === 0 ? (
            <div className="px-4 py-3 text-sm text-[var(--color-text-muted)]">No model matches.</div>
          ) : results.map((item, index) => (
            <button
              key={item.id}
              type="button"
              onClick={() => onSelect(item.id)}
              onMouseEnter={() => onActiveIndex(index)}
              className={`w-full text-left px-4 py-2.5 border-b border-[var(--color-border-subtle)] last:border-0 ${index === activeIndex ? 'bg-[var(--color-surface)]' : ''}`}
            >
              <div className="flex items-center gap-2">
                <span className="text-sm font-mono text-[var(--color-text)]">{item.id}</span>
                {item.id === currentModel ? <span className="text-[10px] text-[var(--color-accent)]">current</span> : null}
              </div>
              <div className="mt-0.5 text-xs text-[var(--color-text-muted)] truncate">
                {item.name}{item.contextLength ? ` · ${item.contextLength.toLocaleString()} context` : ''}
              </div>
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}

function FilePicker({ open, query, loading, error, results, activeIndex, onSelect, onActiveIndex }: {
  open: boolean
  query: string
  loading: boolean
  error: string | null
  results: string[]
  activeIndex: number
  onSelect: (path: string) => void
  onActiveIndex: (index: number) => void
}) {
  if (!open) return null
  let body: React.ReactNode
  if (loading) body = <div className="px-4 py-3 text-sm text-[var(--color-text-muted)]">Loading files...</div>
  else if (error) body = <div className="px-4 py-3 text-sm text-[var(--color-error)]">{error}</div>
  else if (results.length === 0) body = <div className="px-4 py-3 text-sm text-[var(--color-text-muted)]">No matches.</div>
  else body = results.map((path, index) => (
    <button
      key={path}
      type="button"
      onClick={() => onSelect(path)}
      onMouseEnter={() => onActiveIndex(index)}
      className={`w-full text-left px-4 py-2.5 text-sm font-mono transition-colors ${index === activeIndex ? 'bg-[var(--color-surface)] text-[var(--color-text)]' : 'text-[var(--color-text-secondary)] hover:bg-[var(--color-surface)]'}`}
    >
      {path}
    </button>
  ))
  return (
    <div className="absolute left-0 right-0 bottom-full mb-2 z-30">
      <div className="rounded-2xl border border-[var(--color-border)] bg-[var(--color-abyss)]/95 backdrop-blur-xl shadow-xl shadow-black/40 overflow-hidden">
        <div className="px-4 py-3 border-b border-[var(--color-border)] flex items-center justify-between">
          <div className="text-xs font-semibold text-[var(--color-text)]">File Picker</div>
          <div className="text-[10px] text-[var(--color-text-muted)] font-mono">@{query || '...'}</div>
        </div>
        <div className="max-h-56 overflow-y-auto scrollbar-thin">{body}</div>
      </div>
    </div>
  )
}

function ComposerActions({ valueLength, canQueue, isPaused, isStreaming, onCommands, onFiles, onQueue, onInterrupt, onPause, onResume }: {
  valueLength: number
  canQueue: boolean
  isPaused: boolean
  isStreaming: boolean
  onCommands: () => void
  onFiles: () => void
  onQueue: () => void
  onInterrupt?: () => void
  onPause?: () => void
  onResume?: () => void
}) {
  return (
    <div className="flex items-center gap-2 mt-3 px-1">
      <button onClick={onCommands} className="group flex items-center gap-1.5 px-3 py-1.5 text-xs text-[var(--color-text-muted)] rounded-lg hover:text-[var(--color-text-secondary)] hover:bg-[var(--color-surface)]/50 transition-all duration-200">
        <Command className="w-3 h-3 transition-transform group-hover:scale-110" />
        <span>Commands</span>
        <kbd className="hidden sm:inline-block ml-1 px-1.5 py-0.5 text-[10px] font-mono bg-[var(--color-surface)] rounded border border-[var(--color-border-subtle)]">Ctrl+P</kbd>
      </button>
      <button onClick={onFiles} className="group flex items-center gap-1.5 px-3 py-1.5 text-xs text-[var(--color-text-muted)] rounded-lg hover:text-[var(--color-text-secondary)] hover:bg-[var(--color-surface)]/50 transition-all duration-200">
        <FileCode className="w-3 h-3 transition-transform group-hover:scale-110" />
        <span>Files</span>
        <kbd className="hidden sm:inline-block ml-1 px-1.5 py-0.5 text-[10px] font-mono bg-[var(--color-surface)] rounded border border-[var(--color-border-subtle)]">@</kbd>
      </button>
      <div className="flex-1" />
      {isStreaming && canQueue ? (
        <button onClick={onQueue} className="group flex items-center gap-1.5 px-3 py-1.5 text-xs text-[var(--color-text-muted)] rounded-lg hover:text-[var(--color-text-secondary)] hover:bg-[var(--color-surface)]/50 transition-all duration-200">
          <ListPlus className="w-3 h-3 transition-transform group-hover:scale-110" /><span>Queue</span>
        </button>
      ) : null}
      {isPaused ? (
        <button onClick={onResume} className="group flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-[var(--color-success)] bg-[var(--color-success-subtle)] rounded-lg border border-[var(--color-success)]/20 hover:border-[var(--color-success)]/40 transition-all duration-200">
          <Play className="w-3 h-3" /><span>Resume</span>
        </button>
      ) : isStreaming ? (
        <>
          <button onClick={onInterrupt} className="group flex items-center gap-1.5 px-3 py-1.5 text-xs text-[var(--color-text-muted)] rounded-lg hover:text-[var(--color-error)] hover:bg-[var(--color-error-subtle)] transition-all duration-200"><Square className="w-3 h-3" /><span>Stop</span></button>
          <button onClick={onPause} className="group flex items-center gap-1.5 px-3 py-1.5 text-xs text-[var(--color-text-muted)] rounded-lg hover:text-[var(--color-warning)] hover:bg-[var(--color-warning-subtle)] transition-all duration-200"><Pause className="w-3 h-3" /><span>Pause</span></button>
        </>
      ) : null}
      {valueLength > 0 ? <span className="text-[10px] text-[var(--color-text-subtle)] tabular-nums">{valueLength.toLocaleString()}</span> : null}
    </div>
  )
}

function handleComposerKeyDown(e: React.KeyboardEvent, options: {
  modelPickerOpen: boolean
  modelResults: ModelOption[]
  modelActiveIndex: number
  setModelActiveIndex: React.Dispatch<React.SetStateAction<number>>
  selectModel: (modelID: string) => void
  filePickerOpen: boolean
  fileResults: string[]
  fileActiveIndex: number
  setFileActiveIndex: React.Dispatch<React.SetStateAction<number>>
  disableFilePicker: () => void
  insertFile: (path: string) => void
  submit: () => void
}) {
  if (options.modelPickerOpen) {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      const delta = e.key === 'ArrowDown' ? 1 : -1
      options.setModelActiveIndex((index) => Math.max(0, Math.min(options.modelResults.length - 1, index + delta)))
      return
    }
    if ((e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey)) && options.modelResults.length > 0) {
      e.preventDefault()
      const selected = options.modelResults[options.modelActiveIndex]
      if (selected) options.selectModel(selected.id)
      return
    }
  }
  if (options.filePickerOpen) {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      const delta = e.key === 'ArrowDown' ? 1 : -1
      options.setFileActiveIndex((index) => Math.max(0, Math.min(options.fileResults.length - 1, index + delta)))
      return
    }
    if (e.key === 'Escape') {
      e.preventDefault()
      options.disableFilePicker()
      return
    }
    if (e.key === 'Enter' && !e.shiftKey && options.fileResults.length > 0) {
      e.preventDefault()
      const selected = options.fileResults[options.fileActiveIndex]
      if (selected) options.insertFile(selected)
      return
    }
  }
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    options.submit()
  }
}

export function MessageInput({
  onSend,
  onQueue,
  onCommand,
  onInterrupt,
  onPause,
  onResume,
  onOpenCommandPalette,
  disabled = false,
  isPaused = false,
  isStreaming = false,
  placeholder = 'Send a message...',
  commandStatus,
  models = [],
  currentModel,
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
	const modelMatch = value.slice(0, cursorPos).match(/^\/model(?:\s+([^\s]*))?$/i)
	const modelQuery = (modelMatch?.[1] ?? '').toLowerCase()
	const modelResults = modelMatch
		? models.filter((item) => `${item.id} ${item.name}`.toLowerCase().includes(modelQuery)).slice(0, 10)
		: []
	const modelPickerOpen = !disabled && !!modelMatch
	const [modelActiveIndex, setModelActiveIndex] = useState(0)

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

	const selectModel = (modelID: string) => {
		const next = `/model ${modelID}`
		setValue(next)
		setModelActiveIndex(0)
		window.requestAnimationFrame(() => {
			const el = textareaRef.current
			if (!el) return
			el.focus()
			el.setSelectionRange(next.length, next.length)
			setCursorPos(next.length)
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

  const handleQueue = () => {
    const trimmed = value.trim()
    if (!trimmed || disabled || !onQueue) return
    onQueue(trimmed)
    setValue('')
    setCursorPos(0)
  }

  const handleKeyDown = (e: React.KeyboardEvent) => handleComposerKeyDown(e, {
    modelPickerOpen,
    modelResults,
    modelActiveIndex,
    setModelActiveIndex,
    selectModel,
    filePickerOpen,
    fileResults,
    fileActiveIndex,
    setFileActiveIndex,
    disableFilePicker: () => setFilePickerEnabled(false),
    insertFile,
    submit: handleSubmit,
  })

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
            <ModelPicker open={modelPickerOpen} models={models} results={modelResults} activeIndex={modelActiveIndex} currentModel={currentModel} onSelect={selectModel} onActiveIndex={setModelActiveIndex} />
            <FilePicker open={filePickerOpen} query={fileQuery} loading={fileLoading} error={fileError} results={fileResults} activeIndex={fileActiveIndex} onSelect={insertFile} onActiveIndex={setFileActiveIndex} />
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
              aria-label={isStreaming ? 'Steer current turn' : 'Send message'}
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
              <Send className="w-4 h-4" />
            </button>
          </div>
        </div>

        <ComposerActions
          valueLength={value.length}
          canQueue={canSubmit && !!onQueue}
          isPaused={isPaused}
          isStreaming={isStreaming}
          onCommands={() => onOpenCommandPalette ? onOpenCommandPalette() : insertTextAtCursor('/')}
          onFiles={() => insertTextAtCursor('@')}
          onQueue={handleQueue}
          onInterrupt={onInterrupt}
          onPause={onPause}
          onResume={onResume}
        />
        {commandStatus ? (
          <div className="mt-2 px-1 text-[10px] font-mono text-[var(--color-text-muted)]" aria-live="polite">
            {commandStatus}
          </div>
        ) : null}
      </div>
    </div>
  )
}
