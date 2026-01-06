import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Terminal as XTerm } from 'xterm'
import { FitAddon } from 'xterm-addon-fit'
import 'xterm/css/xterm.css'

import { RefreshCw, WifiOff, Terminal as TerminalIcon } from 'lucide-react'

const textEncoder = new TextEncoder()
const textDecoder = new TextDecoder()

function encodeBase64(value: string): string {
  const bytes = textEncoder.encode(value)
  let binary = ''
  bytes.forEach((byte) => {
    binary += String.fromCharCode(byte)
  })
  return window.btoa(binary)
}

function decodeBase64(value: string): string {
  const binary = window.atob(value)
  const bytes = Uint8Array.from(binary, (char) => char.charCodeAt(0))
  return textDecoder.decode(bytes)
}

function resolvePTYUrl(): string {
  const base = window.location.origin.replace(/^http/, 'ws')
  return `${base}/ws/pty`
}

type TerminalStatus = 'disconnected' | 'connecting' | 'connected'

interface Props {
  sessionId: string | null
  cwd?: string
  sessionToken?: string
  canConnect?: boolean
  className?: string
}

export function TerminalPane({ sessionId, cwd, sessionToken, canConnect = true, className }: Props) {
  const containerRef = useRef<HTMLDivElement | null>(null)
  const termRef = useRef<XTerm | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const socketRef = useRef<WebSocket | null>(null)
  const connectRef = useRef<() => void>(() => {})
  const reconnectTimerRef = useRef<number | undefined>(undefined)
  const chunkQueueRef = useRef<string[]>([])
  const queueBytesRef = useRef(0)
  const flushHandleRef = useRef<number | undefined>(undefined)
  const processQueueRef = useRef<() => void>(() => {})

  const [status, setStatus] = useState<TerminalStatus>('disconnected')
  const [statusDetail, setStatusDetail] = useState<string | undefined>(undefined)
  const [bufferNotice, setBufferNotice] = useState<string | undefined>(undefined)

  const processQueue = useCallback(() => {
    const term = termRef.current
    if (!term) {
      chunkQueueRef.current = []
      queueBytesRef.current = 0
      flushHandleRef.current = undefined
      return
    }
    let iterations = 0
    while (chunkQueueRef.current.length > 0 && iterations < 64) {
      const chunk = chunkQueueRef.current.shift()!
      queueBytesRef.current -= chunk.length
      term.write(chunk)
      iterations++
    }
    if (chunkQueueRef.current.length > 0) {
      flushHandleRef.current = window.requestAnimationFrame(() => processQueueRef.current())
    } else {
      flushHandleRef.current = undefined
      setBufferNotice(undefined)
    }
  }, [])

  useEffect(() => {
    processQueueRef.current = processQueue
  }, [processQueue])

  const scheduleFlush = useCallback(() => {
    if (flushHandleRef.current != null) return
    flushHandleRef.current = window.requestAnimationFrame(() => {
      flushHandleRef.current = undefined
      processQueue()
    })
  }, [processQueue])

  const enqueueChunk = useCallback(
    (chunk: string) => {
      if (!chunk) return
      chunkQueueRef.current.push(chunk)
      queueBytesRef.current += chunk.length

      if (queueBytesRef.current > 500_000) {
        let dropped = false
        while (queueBytesRef.current > 500_000 && chunkQueueRef.current.length > 0) {
          const removed = chunkQueueRef.current.shift()!
          queueBytesRef.current -= removed.length
          dropped = true
        }
        if (dropped) {
          setBufferNotice('High output detected — truncating terminal output…')
          termRef.current?.writeln('\r\n[terminal output truncated due to buffer limits]\r\n')
        }
      } else if (chunkQueueRef.current.length > 128) {
        setBufferNotice('High output detected — buffering terminal…')
      }

      scheduleFlush()
    },
    [scheduleFlush]
  )

  const connectUrl = useMemo(() => {
    if (!sessionId) return null
    if (!canConnect) return null
    if (!sessionToken) return null
    const url = new URL(resolvePTYUrl())
    if (cwd) url.searchParams.set('cwd', cwd)
    url.searchParams.set('sessionId', sessionId)
    return url
  }, [sessionId, cwd, sessionToken, canConnect])

  const teardown = useCallback(() => {
    if (reconnectTimerRef.current) {
      window.clearTimeout(reconnectTimerRef.current)
      reconnectTimerRef.current = undefined
    }
    if (flushHandleRef.current != null) {
      window.cancelAnimationFrame(flushHandleRef.current)
      flushHandleRef.current = undefined
    }
    chunkQueueRef.current = []
    queueBytesRef.current = 0
    setBufferNotice(undefined)
    const socket = socketRef.current
    socketRef.current = null
    if (socket && socket.readyState === WebSocket.OPEN) {
      try {
        socket.send(JSON.stringify({ type: 'close' }))
      } catch {
        // ignore
      }
    }
    socket?.close()
  }, [])

  const scheduleReconnect = useCallback(
    (delayMs: number) => {
      if (reconnectTimerRef.current) {
        window.clearTimeout(reconnectTimerRef.current)
      }
      reconnectTimerRef.current = window.setTimeout(() => {
        connectRef.current?.()
      }, delayMs)
    },
    []
  )

  const connect = useCallback(() => {
    const term = termRef.current
    const fit = fitRef.current
    if (!term || !fit) return
    if (!sessionId) return
    if (!canConnect) {
      setStatus('disconnected')
      setStatusDetail('Terminal requires a member token.')
      return
    }
    if (!sessionToken) {
      setStatus('disconnected')
      setStatusDetail('Requesting terminal token…')
      return
    }
    if (!connectUrl) return

    teardown()

    setStatus('connecting')
    setStatusDetail('Connecting…')
    setBufferNotice(undefined)

    fit.fit()
    connectUrl.searchParams.set('rows', term.rows.toString())
    connectUrl.searchParams.set('cols', term.cols.toString())

    const socket = new WebSocket(connectUrl.toString())
    socketRef.current = socket

    socket.onopen = () => {
      setStatus('connected')
      setStatusDetail(undefined)
      term.focus()
      try {
        socket.send(JSON.stringify({ type: 'auth', data: sessionToken }))
        socket.send(JSON.stringify({ type: 'resize', rows: term.rows, cols: term.cols }))
      } catch {
        // ignore
      }
    }

    socket.onclose = (event) => {
      setStatus('disconnected')
      const reason = event.reason || (event.wasClean ? 'Connection closed' : 'Connection lost')
      setStatusDetail(reason)
      socketRef.current = null
      scheduleReconnect(1500)
    }

    socket.onerror = () => {
      setStatus('disconnected')
      setStatusDetail('Terminal connection error')
      socket.close()
    }

    socket.onmessage = (event) => {
      try {
        const packet = JSON.parse(event.data as string) as { type: string; data?: string }
        if (packet.type === 'data' && packet.data) {
          enqueueChunk(decodeBase64(packet.data))
          return
        }
        if (packet.type === 'exit') {
          term.writeln(`\r\n[process exited with code ${packet.data ?? '0'}]`)
          return
        }
        if (packet.type === 'error') {
          term.writeln(`\r\n[pty error] ${packet.data ?? 'unknown error'}`)
          return
        }
      } catch {
        // ignore malformed frames
      }
    }
  }, [canConnect, connectUrl, enqueueChunk, scheduleReconnect, sessionId, sessionToken, teardown])

  useEffect(() => {
    connectRef.current = connect
  }, [connect])

  useEffect(() => {
    const term = new XTerm({
      fontFamily: 'var(--font-mono)',
      fontSize: 13,
      cursorBlink: true,
      allowTransparency: true,
      theme: { background: '#05070f' },
      scrollback: 4000,
      convertEol: true,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    termRef.current = term
    fitRef.current = fit

    if (containerRef.current) {
      term.open(containerRef.current)
      fit.fit()
    }

    const handleWindowResize = () => {
      fit.fit()
      const socket = socketRef.current
      if (socket && socket.readyState === WebSocket.OPEN) {
        socket.send(JSON.stringify({ type: 'resize', rows: term.rows, cols: term.cols }))
      }
    }

    const dataSub = term.onData((data) => {
      const socket = socketRef.current
      if (!socket || socket.readyState !== WebSocket.OPEN) return
      socket.send(JSON.stringify({ type: 'input', data: encodeBase64(data) }))
    })

    const resizeSub = term.onResize(({ cols, rows }) => {
      const socket = socketRef.current
      if (!socket || socket.readyState !== WebSocket.OPEN) return
      socket.send(JSON.stringify({ type: 'resize', rows, cols }))
    })

    window.addEventListener('resize', handleWindowResize)

    return () => {
      window.removeEventListener('resize', handleWindowResize)
      dataSub.dispose()
      resizeSub.dispose()
      teardown()
      term.dispose()
      termRef.current = null
      fitRef.current = null
    }
  }, [teardown])

  useEffect(() => {
    const term = termRef.current
    if (!term) return
    chunkQueueRef.current = []
    queueBytesRef.current = 0
    Promise.resolve().then(() => setBufferNotice(undefined))
    term.reset()
    term.writeln('\x1b[1mBuckley Terminal\x1b[0m')
    if (!sessionId) {
      term.writeln('Select a session to connect a terminal.')
      return
    }
    if (!canConnect) {
      term.writeln('Terminal requires a member token.')
      return
    }
    if (!sessionToken) {
      term.writeln('Requesting terminal token…')
      return
    }
    if (!connectUrl) return
    Promise.resolve().then(() => connect())
  }, [canConnect, connect, connectUrl, sessionId, sessionToken])

  const statusPill = (() => {
    switch (status) {
      case 'connected':
        return (
          <span className="inline-flex items-center gap-1.5 text-xs text-[var(--color-success)] bg-[var(--color-success-subtle)] border border-[var(--color-success)]/20 px-2 py-1 rounded-lg">
            <TerminalIcon className="w-3.5 h-3.5" />
            Connected
          </span>
        )
      case 'connecting':
        return (
          <span className="inline-flex items-center gap-1.5 text-xs text-[var(--color-warning)] bg-[var(--color-warning-subtle)] border border-[var(--color-warning)]/20 px-2 py-1 rounded-lg">
            <RefreshCw className="w-3.5 h-3.5 animate-spin" />
            Connecting
          </span>
        )
      default:
        return (
          <span className="inline-flex items-center gap-1.5 text-xs text-[var(--color-error)] bg-[var(--color-error-subtle)] border border-[var(--color-error)]/20 px-2 py-1 rounded-lg">
            <WifiOff className="w-3.5 h-3.5" />
            Disconnected
          </span>
        )
    }
  })()

  return (
    <div className={className}>
      <div className="flex items-center justify-between mb-2">
        <div className="flex items-center gap-2">
          <TerminalIcon className="w-4 h-4 text-[var(--color-text-secondary)]" />
          <span className="text-sm font-semibold text-[var(--color-text)]">Terminal</span>
        </div>
        <div className="flex items-center gap-2">
          {statusPill}
          <button
            onClick={() => connect()}
            className="p-2 rounded-lg hover:bg-[var(--color-surface)] transition-colors"
            title="Reconnect"
          >
            <RefreshCw className="w-4 h-4 text-[var(--color-text-muted)]" />
          </button>
        </div>
      </div>

      {(statusDetail || bufferNotice) && (
        <div className="space-y-1 mb-2">
          {statusDetail && <div className="text-xs text-[var(--color-text-muted)]">{statusDetail}</div>}
          {bufferNotice && <div className="text-xs text-[var(--color-warning)]">{bufferNotice}</div>}
        </div>
      )}

      <div className="rounded-xl border border-[var(--color-border-subtle)] bg-[var(--color-void)] overflow-hidden">
        <div ref={containerRef} className="h-72" />
      </div>
    </div>
  )
}
