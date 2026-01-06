import { useCallback, useEffect, useRef, useState } from 'react'
import { create, fromJson, type JsonObject } from '@bufbuild/protobuf'
import { SubscribeRequestSchema, EventSchema, type Event as IpcEvent } from '../gen/ipc_pb'
import type { WSEvent } from '../types'
import { getAuthToken } from '../auth/token'

export type ConnectionState = 'connecting' | 'connected' | 'disconnected' | 'reconnecting'

const textEncoder = new TextEncoder()
const textDecoder = new TextDecoder()

const connectFrameHeaderBytes = 5
const maxConnectFrameBytes = 4 * 1024 * 1024

interface UseGrpcStreamOptions {
  baseUrl?: string
  sessionId?: string
  eventTypes?: string[]
  onEvent?: (event: IpcEvent) => void
  onConnect?: () => void
  onDisconnect?: () => void
  reconnectAttempts?: number
  reconnectInterval?: number
}

interface UseGrpcStreamReturn {
  state: ConnectionState
  reconnect: () => void
  lastEventId: string | null
}

function protoValueToJS(value: unknown): unknown {
  if (!value || typeof value !== 'object') return undefined
  const v = value as { kind?: { case?: string; value?: unknown } }
  if (!v.kind) return undefined

  switch (v.kind.case) {
    case 'nullValue':
      return null
    case 'stringValue':
    case 'numberValue':
    case 'boolValue':
      return v.kind.value
    case 'structValue':
      return protoStructToJS(v.kind.value)
    case 'listValue': {
      const list = v.kind.value as { values?: unknown[] } | undefined
      const values = Array.isArray(list?.values) ? list?.values : []
      return values.map(protoValueToJS)
    }
    default:
      return undefined
  }
}

function protoStructToJS(struct: unknown): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  if (!struct || typeof struct !== 'object') return out
  const s = struct as { fields?: Record<string, unknown> }
  if (!s.fields) return out
  for (const [key, value] of Object.entries(s.fields)) {
    out[key] = protoValueToJS(value)
  }
  return out
}

class ByteQueue {
  private chunks: Uint8Array[] = []
  private headOffset = 0
  private total = 0

  get length(): number {
    return this.total
  }

  push(chunk: Uint8Array) {
    if (chunk.byteLength === 0) return
    this.chunks.push(chunk)
    this.total += chunk.byteLength
  }

  peek(n: number): Uint8Array | null {
    if (this.total < n) return null
    const out = new Uint8Array(n)
    let written = 0
    let chunkIndex = 0
    let offset = this.headOffset
    while (written < n) {
      const chunk = this.chunks[chunkIndex]
      const start = chunkIndex === 0 ? offset : 0
      const take = Math.min(n - written, chunk.byteLength - start)
      out.set(chunk.subarray(start, start + take), written)
      written += take
      chunkIndex++
      offset = 0
    }
    return out
  }

  read(n: number): Uint8Array | null {
    if (this.total < n) return null
    const out = new Uint8Array(n)
    let written = 0
    while (written < n) {
      const head = this.chunks[0]
      const available = head.byteLength - this.headOffset
      const take = Math.min(n - written, available)
      out.set(head.subarray(this.headOffset, this.headOffset + take), written)
      this.headOffset += take
      written += take
      this.total -= take
      if (this.headOffset >= head.byteLength) {
        this.chunks.shift()
        this.headOffset = 0
      }
    }
    return out
  }
}

function encodeConnectFrame(payload: Uint8Array<ArrayBuffer>): Uint8Array<ArrayBuffer> {
  const frame = new Uint8Array(connectFrameHeaderBytes + payload.byteLength)
  frame[0] = 0
  const len = payload.byteLength >>> 0
  frame[1] = (len >>> 24) & 0xff
  frame[2] = (len >>> 16) & 0xff
  frame[3] = (len >>> 8) & 0xff
  frame[4] = len & 0xff
  frame.set(payload, connectFrameHeaderBytes)
  return frame
}

function decodeUint32BE(bytes: Uint8Array, offset: number): number {
  return (
    ((bytes[offset] << 24) |
      (bytes[offset + 1] << 16) |
      (bytes[offset + 2] << 8) |
      bytes[offset + 3]) >>>
    0
  )
}

function parseConnectEnvelope(payload: Uint8Array): IpcEvent | null {
  const text = textDecoder.decode(payload).trim()
  if (!text) return null
  const parsed = JSON.parse(text) as { result?: unknown; error?: { code?: string; message?: string } }
  if (parsed && typeof parsed === 'object' && parsed.error) {
    const message = parsed.error.message || 'Stream error'
    const code = typeof parsed.error.code === 'string' ? parsed.error.code.trim() : ''
    throw new Error(code ? `${code}: ${message}` : message)
  }
  if (parsed && typeof parsed === 'object' && parsed.result) {
    return fromJson(EventSchema, parsed.result as JsonObject)
  }
  return fromJson(EventSchema, parsed as unknown as JsonObject)
}

// Connect protocol streaming implementation (framed JSON)
async function* streamSubscribe(
  baseUrl: string,
  request: Parameters<typeof create<typeof SubscribeRequestSchema>>[1],
  signal: AbortSignal,
  authToken?: string | null
): AsyncGenerator<IpcEvent> {
  const subscribeReq = create(SubscribeRequestSchema, request)

  const requestJSON = JSON.stringify({
    sessionId: subscribeReq.sessionId,
    eventTypes: subscribeReq.eventTypes,
    lastEventId: subscribeReq.lastEventId,
    includeAgentEvents: subscribeReq.includeAgentEvents,
  })

  const requestBody = encodeConnectFrame(textEncoder.encode(requestJSON))

  const headers: HeadersInit = {
    'Content-Type': 'application/connect+json',
    Accept: 'application/connect+json',
    'Connect-Protocol-Version': '1',
    'Connect-Accept-Encoding': 'identity',
    'Cache-Control': 'no-cache',
    Pragma: 'no-cache',
  }

  if (authToken) {
    headers['Authorization'] = `Bearer ${authToken}`
  }

  const response = await fetch(
    `${baseUrl}/buckley.ipc.v1.BuckleyIPC/Subscribe`,
    {
      method: 'POST',
      headers,
      body: requestBody,
      signal,
    }
  )

  if (!response.ok) {
    throw new Error(`Subscribe failed: ${response.status} ${response.statusText}`)
  }

  const reader = response.body?.getReader()
  if (!reader) {
    throw new Error('No response body')
  }

  const queue = new ByteQueue()

  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break

      if (value) queue.push(value)

      while (true) {
        const header = queue.peek(connectFrameHeaderBytes)
        if (!header) break
        const flags = header[0]
        const length = decodeUint32BE(header, 1)
        if (length > maxConnectFrameBytes) {
          throw new Error(`Connect frame too large: ${length} bytes`)
        }
        if (queue.length < connectFrameHeaderBytes + length) break

        queue.read(connectFrameHeaderBytes)
        if (length === 0) continue
        const payload = queue.read(length)
        if (!payload) break

        if ((flags & 1) === 1) {
          throw new Error('Compressed Connect frames are not supported (expected identity encoding).')
        }

        const event = parseConnectEnvelope(payload)
        if (event) yield event
      }
    }
  } finally {
    reader.releaseLock()
  }
}

export function useGrpcStream({
  baseUrl,
  sessionId,
  eventTypes,
  onEvent,
  onConnect,
  onDisconnect,
  reconnectAttempts = 10,
  reconnectInterval = 2000,
}: UseGrpcStreamOptions): UseGrpcStreamReturn {
  const [state, setState] = useState<ConnectionState>('disconnected')
  const reconnectCountRef = useRef(0)
  const reconnectTimeoutRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const lastEventIdRef = useRef<string | null>(null)
  const abortControllerRef = useRef<AbortController | null>(null)

  const connect = useCallback(async () => {
    // Cancel any existing connection
    if (abortControllerRef.current) {
      abortControllerRef.current.abort()
    }

    const effectiveBaseUrl = baseUrl || window.location.origin
    const token = getAuthToken()
    abortControllerRef.current = new AbortController()

    setState('connecting')

    try {
      const stream = streamSubscribe(
        effectiveBaseUrl,
        {
          sessionId: sessionId || '',
          eventTypes: eventTypes || [],
          lastEventId: lastEventIdRef.current || '',
        },
        abortControllerRef.current.signal,
        token
      )

      setState('connected')
      reconnectCountRef.current = 0
      onConnect?.()

      // Process events from the stream
      for await (const event of stream) {
        if (event.eventId) {
          lastEventIdRef.current = event.eventId
        }
        onEvent?.(event)
      }

      // Stream ended normally
      setState('disconnected')
      onDisconnect?.()
    } catch (error) {
      // Check if it was an intentional abort
      if (error instanceof Error && error.name === 'AbortError') {
        return
      }

      console.error('gRPC stream error:', error)
      onDisconnect?.()

      // Attempt reconnection
      if (reconnectCountRef.current < reconnectAttempts) {
        setState('reconnecting')
        reconnectCountRef.current++

        const delay = Math.min(
          reconnectInterval * Math.pow(1.5, reconnectCountRef.current - 1),
          30000
        )

        reconnectTimeoutRef.current = setTimeout(() => {
          connect()
        }, delay)
      } else {
        setState('disconnected')
      }
    }
  }, [
    baseUrl,
    sessionId,
    eventTypes,
    onEvent,
    onConnect,
    onDisconnect,
    reconnectAttempts,
    reconnectInterval,
  ])

  const reconnect = useCallback(() => {
    if (reconnectTimeoutRef.current) {
      clearTimeout(reconnectTimeoutRef.current)
    }
    reconnectCountRef.current = 0

    if (abortControllerRef.current) {
      abortControllerRef.current.abort()
    }

    connect()
  }, [connect])

  // Connect on mount
  useEffect(() => {
    connect()

    return () => {
      if (reconnectTimeoutRef.current) {
        clearTimeout(reconnectTimeoutRef.current)
      }
      if (abortControllerRef.current) {
        abortControllerRef.current.abort()
      }
    }
  }, [connect])

  return {
    state,
    reconnect,
    lastEventId: lastEventIdRef.current,
  }
}

export function eventToWSEvent(event: IpcEvent): WSEvent {
  // Convert payload struct to plain object
  const payload = event.payload?.fields ? protoStructToJS(event.payload) : undefined

  return {
    type: event.type,
    sessionId: event.sessionId,
    payload,
    timestamp: event.timestamp
      ? new Date(Number(event.timestamp.seconds) * 1000 + Number(event.timestamp.nanos) / 1000000).toISOString()
      : undefined,
    eventId: event.eventId,
  }
}

// Re-export the Event type with a better name to avoid DOM Event conflict
export type { IpcEvent }
