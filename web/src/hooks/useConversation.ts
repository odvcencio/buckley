import { useCallback, useReducer } from 'react'
import type {
  DisplayMessage,
  ToolCall,
  PendingApproval,
  WSEvent,
  StorageMessage,
  Session,
} from '../types'

interface ConversationState {
  messages: DisplayMessage[]
  toolCalls: Map<string, ToolCall>
  pendingApprovals: PendingApproval[]
  isStreaming: boolean
  streamingMessageId: string | null
  currentSession: Session | null
  sessions: Session[]
}

type Action =
  | { type: 'SET_SESSIONS'; sessions: Session[] }
  | { type: 'SET_CURRENT_SESSION'; session: Session | null }
  | { type: 'SET_MESSAGES'; messages: DisplayMessage[] }
  | { type: 'ADD_MESSAGE'; message: DisplayMessage }
  | { type: 'UPDATE_MESSAGE'; id: string; content: string; done?: boolean }
  | { type: 'ADD_TOOL_CALL'; toolCall: ToolCall }
  | { type: 'UPDATE_TOOL_CALL'; id: string; updates: Partial<ToolCall> }
  | { type: 'ADD_APPROVAL'; approval: PendingApproval }
  | { type: 'REMOVE_APPROVAL'; id: string }
  | { type: 'SET_STREAMING'; isStreaming: boolean; messageId?: string }
  | { type: 'CLEAR' }

function reducer(state: ConversationState, action: Action): ConversationState {
  switch (action.type) {
    case 'SET_SESSIONS':
      return { ...state, sessions: action.sessions }

    case 'SET_CURRENT_SESSION':
      return { ...state, currentSession: action.session }

    case 'SET_MESSAGES':
      return { ...state, messages: action.messages }

    case 'ADD_MESSAGE':
      return {
        ...state,
        messages: [...state.messages, action.message],
      }

    case 'UPDATE_MESSAGE': {
      const messages = state.messages.map((msg) =>
        msg.id === action.id
          ? { ...msg, content: msg.content + action.content, streaming: !action.done }
          : msg
      )
      return {
        ...state,
        messages,
        isStreaming: !action.done,
        streamingMessageId: action.done ? null : action.id,
      }
    }

    case 'ADD_TOOL_CALL': {
      const newToolCalls = new Map(state.toolCalls)
      newToolCalls.set(action.toolCall.id, action.toolCall)
      return { ...state, toolCalls: newToolCalls }
    }

    case 'UPDATE_TOOL_CALL': {
      const newToolCalls = new Map(state.toolCalls)
      const existing = newToolCalls.get(action.id)
      if (existing) {
        newToolCalls.set(action.id, { ...existing, ...action.updates })
      }
      return { ...state, toolCalls: newToolCalls }
    }

    case 'ADD_APPROVAL':
      return {
        ...state,
        pendingApprovals: [...state.pendingApprovals, action.approval],
      }

    case 'REMOVE_APPROVAL':
      return {
        ...state,
        pendingApprovals: state.pendingApprovals.filter((a) => a.id !== action.id),
      }

    case 'SET_STREAMING':
      return {
        ...state,
        isStreaming: action.isStreaming,
        streamingMessageId: action.messageId ?? null,
      }

    case 'CLEAR':
      return {
        ...state,
        messages: [],
        toolCalls: new Map(),
        pendingApprovals: [],
        isStreaming: false,
        streamingMessageId: null,
      }

    default:
      return state
  }
}

const initialState: ConversationState = {
  messages: [],
  toolCalls: new Map(),
  pendingApprovals: [],
  isStreaming: false,
  streamingMessageId: null,
  currentSession: null,
  sessions: [],
}

// Convert storage message to display message
function storageToDisplay(msg: StorageMessage): DisplayMessage {
  const role: DisplayMessage['role'] =
    msg.role === 'user' ? 'user' : msg.role === 'assistant' ? 'assistant' : msg.role === 'tool' ? 'tool' : 'system'
  return {
    id: msg.id,
    role,
    content: msg.content,
    timestamp: msg.createdAt,
  }
}

export function useConversation() {
  const [state, dispatch] = useReducer(reducer, initialState)

  const handleWSEvent = useCallback((event: WSEvent) => {
    switch (event.type) {
      case 'sessions.snapshot': {
        const sessions = event.payload as Session[]
        dispatch({ type: 'SET_SESSIONS', sessions })
        break
      }

      case 'view.state': {
        // Full view state update
        const payload = event.payload as { sessions?: Session[] }
        if (payload.sessions) {
          dispatch({ type: 'SET_SESSIONS', sessions: payload.sessions })
        }
        break
      }

      case 'view.patch': {
        // Partial view state update for a session
        const payload = event.payload as {
          session?: { session?: Session; recentMessages?: StorageMessage[] }
        }
        if (payload.session?.recentMessages) {
          const messages = payload.session.recentMessages.map(storageToDisplay)
          dispatch({ type: 'SET_MESSAGES', messages })
        }
        break
      }

      case 'message.created':
      case 'message.updated': {
        const msg = event.payload as StorageMessage
        if (msg) {
          dispatch({
            type: 'ADD_MESSAGE',
            message: storageToDisplay(msg),
          })
        }
        break
      }

      default:
        // Log unknown events for debugging
        if (event.type.startsWith('telemetry.')) {
          // Ignore telemetry events in conversation handler
          return
        }
        console.log('Unhandled WS event:', event.type)
    }
  }, [])

  const setSessions = useCallback((sessions: Session[]) => {
    dispatch({ type: 'SET_SESSIONS', sessions })
  }, [])

  const setCurrentSession = useCallback((session: Session | null) => {
    dispatch({ type: 'SET_CURRENT_SESSION', session })
  }, [])

  const setMessages = useCallback((messages: DisplayMessage[]) => {
    dispatch({ type: 'SET_MESSAGES', messages })
  }, [])

  const clear = useCallback(() => {
    dispatch({ type: 'CLEAR' })
  }, [])

  const addPendingApproval = useCallback((approval: PendingApproval) => {
    dispatch({ type: 'ADD_APPROVAL', approval })
  }, [])

  const removePendingApproval = useCallback((id: string) => {
    dispatch({ type: 'REMOVE_APPROVAL', id })
  }, [])

  return {
    ...state,
    handleWSEvent,
    setSessions,
    setCurrentSession,
    setMessages,
    clear,
    addPendingApproval,
    removePendingApproval,
  }
}
