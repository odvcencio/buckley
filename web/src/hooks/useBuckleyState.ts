import { useCallback, useMemo, useReducer } from 'react'
import type { PendingApproval, Session, ViewSessionState, WSEvent } from '../types'

export interface ActivityEvent {
  type: string
  timestamp?: string
  sessionId?: string
  payload?: unknown
  eventId?: string
}

interface BuckleyState {
  sessions: Session[]
  currentSessionId: string | null
  views: Record<string, ViewSessionState | undefined>
  approvals: PendingApproval[]
  activity: ActivityEvent[]
}

type Action =
  | { type: 'SESSIONS_SET'; sessions: Session[] }
  | { type: 'SESSION_UPSERT'; session: Session }
  | { type: 'SESSION_PATCH'; id: string; patch: Partial<Session> }
  | { type: 'SESSION_REMOVE'; id: string }
  | { type: 'CURRENT_SESSION_SET'; id: string | null }
  | { type: 'VIEW_SET'; sessionId: string; view: ViewSessionState }
  | { type: 'APPROVALS_SET'; approvals: PendingApproval[] }
  | { type: 'ACTIVITY_ADD'; event: ActivityEvent }
  | { type: 'CLEAR_SESSION_STATE' }

function upsertSession(list: Session[], session: Session): Session[] {
  const idx = list.findIndex((s) => s.id === session.id)
  if (idx === -1) return [session, ...list]
  const next = list.slice()
  next[idx] = { ...next[idx], ...session }
  return next
}

function patchSession(list: Session[], id: string, patch: Partial<Session>): Session[] {
  const idx = list.findIndex((s) => s.id === id)
  if (idx === -1) return list
  const next = list.slice()
  next[idx] = { ...next[idx], ...patch }
  return next
}

function removeSession(list: Session[], id: string): Session[] {
  return list.filter((s) => s.id !== id)
}

function reducer(state: BuckleyState, action: Action): BuckleyState {
  switch (action.type) {
    case 'SESSIONS_SET':
      return { ...state, sessions: action.sessions }
    case 'SESSION_UPSERT':
      return { ...state, sessions: upsertSession(state.sessions, action.session) }
    case 'SESSION_PATCH':
      return { ...state, sessions: patchSession(state.sessions, action.id, action.patch) }
    case 'SESSION_REMOVE': {
      const sessions = removeSession(state.sessions, action.id)
      const views = { ...state.views }
      delete views[action.id]
      const currentSessionId = state.currentSessionId === action.id ? null : state.currentSessionId
      return { ...state, sessions, views, currentSessionId }
    }
    case 'CURRENT_SESSION_SET':
      return { ...state, currentSessionId: action.id }
    case 'VIEW_SET':
      return { ...state, views: { ...state.views, [action.sessionId]: action.view } }
    case 'APPROVALS_SET':
      return { ...state, approvals: action.approvals }
    case 'ACTIVITY_ADD': {
      const next = [action.event, ...state.activity]
      return { ...state, activity: next.slice(0, 250) }
    }
    case 'CLEAR_SESSION_STATE':
      return { ...state, views: {}, approvals: [], activity: [] }
    default:
      return state
  }
}

const initialState: BuckleyState = {
  sessions: [],
  currentSessionId: null,
  views: {},
  approvals: [],
  activity: [],
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}

export function useBuckleyState() {
  const [state, dispatch] = useReducer(reducer, initialState)

  const currentSession = useMemo(() => {
    if (!state.currentSessionId) return null
    return state.sessions.find((s) => s.id === state.currentSessionId) ?? null
  }, [state.currentSessionId, state.sessions])

  const currentView = useMemo(() => {
    if (!state.currentSessionId) return null
    return state.views[state.currentSessionId] ?? null
  }, [state.currentSessionId, state.views])

  const setSessions = useCallback((sessions: Session[]) => {
    dispatch({ type: 'SESSIONS_SET', sessions })
  }, [])

  const setCurrentSessionId = useCallback((id: string | null) => {
    dispatch({ type: 'CURRENT_SESSION_SET', id })
  }, [])

  const setApprovals = useCallback((approvals: PendingApproval[]) => {
    dispatch({ type: 'APPROVALS_SET', approvals })
  }, [])

  const handleEvent = useCallback((event: WSEvent) => {
    const payload = event.payload

    if (event.type === 'sessions.snapshot' && isRecord(payload) && Array.isArray(payload.sessions)) {
      dispatch({ type: 'SESSIONS_SET', sessions: payload.sessions as Session[] })
      return
    }

    if (event.type === 'session.created' && isRecord(payload) && typeof payload.id === 'string') {
      dispatch({ type: 'SESSION_UPSERT', session: payload as unknown as Session })
      return
    }

    if (event.type === 'session.updated' && event.sessionId && isRecord(payload)) {
      dispatch({ type: 'SESSION_PATCH', id: event.sessionId, patch: payload as Partial<Session> })
      return
    }

    if (event.type === 'session.deleted' && event.sessionId) {
      dispatch({ type: 'SESSION_REMOVE', id: event.sessionId })
      return
    }

    if (event.type === 'view.patch' && isRecord(payload) && isRecord(payload.session)) {
      const session = payload.session as unknown as ViewSessionState
      if (typeof session.id === 'string') {
        dispatch({ type: 'VIEW_SET', sessionId: session.id, view: session })
      }
      return
    }

    if (event.type.startsWith('telemetry.')) {
      dispatch({
        type: 'ACTIVITY_ADD',
        event: {
          type: event.type,
          timestamp: event.timestamp,
          sessionId: event.sessionId,
          payload,
          eventId: event.eventId,
        },
      })
      return
    }

    if (event.type.startsWith('approval.')) {
      dispatch({
        type: 'ACTIVITY_ADD',
        event: {
          type: event.type,
          timestamp: event.timestamp,
          sessionId: event.sessionId,
          payload,
          eventId: event.eventId,
        },
      })
      return
    }
  }, [])

  return {
    ...state,
    currentSession,
    currentView,
    setSessions,
    setCurrentSessionId,
    setApprovals,
    handleEvent,
    clearSessionState: () => dispatch({ type: 'CLEAR_SESSION_STATE' }),
  }
}

