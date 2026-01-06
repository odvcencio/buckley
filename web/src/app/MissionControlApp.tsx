import { useCallback, useEffect, useMemo, useState } from 'react'

import type { AuthPrincipal } from '../auth/types'
import { hasScope } from '../auth/scopes'
import { issueSessionToken } from '../lib/api'
import { client } from '../lib/grpc'
import { useGrpcStream, eventToWSEvent } from '../hooks/useGrpcStream'
import { useBuckleyState } from '../hooks/useBuckleyState'
import { toDisplaySession, toPendingApproval } from '../ipc/normalize'
import type { DisplayMessage, ToolCall } from '../types'

import { OperatorConsole } from './OperatorConsole'
import { ApprovalBanner } from '../components/ApprovalBanner'
import { CommandPalette } from '../components/CommandPalette'
import { ConversationSearch } from '../components/ConversationSearch'
import { ConversationView } from '../components/ConversationView'
import { MessageInput } from '../components/MessageInput'
import { PortholesDrawer, PortholesPanel } from '../components/PortholesPanel'
import { SessionDrawer } from '../components/SessionDrawer'
import { SessionHeader } from '../components/SessionHeader'
import { SessionSidebar } from '../components/SessionSidebar'

export function MissionControlApp({
  principal,
  onSignOut,
}: {
  principal: AuthPrincipal
  onSignOut?: () => void | Promise<void>
}) {
  const canWrite = hasScope(principal, 'member')
  const canOperate = hasScope(principal, 'operator')

  const {
    sessions,
    currentSessionId,
    currentSession,
    currentView,
    approvals,
    activity,
    setCurrentSessionId,
    setApprovals,
    handleEvent,
  } = useBuckleyState()

  const [drawerOpen, setDrawerOpen] = useState(false)
  const [portholesOpen, setPortholesOpen] = useState(false)
  const [operatorOpen, setOperatorOpen] = useState(false)
  const [commandPaletteOpen, setCommandPaletteOpen] = useState(false)
  const [searchOpen, setSearchOpen] = useState(false)
  const [terminalToken, setTerminalToken] = useState<string | undefined>(undefined)
  const [terminalTokenSessionId, setTerminalTokenSessionId] = useState<string | null>(null)
  const terminalSessionToken =
    canWrite && currentSessionId && terminalTokenSessionId === currentSessionId ? terminalToken : undefined

  const onGrpcEvent = useCallback(
    (event: Parameters<typeof eventToWSEvent>[0]) => {
      handleEvent(eventToWSEvent(event))
    },
    [handleEvent]
  )

  const stream = useGrpcStream({
    sessionId: currentSessionId ?? undefined,
    onEvent: onGrpcEvent,
  })

  const refreshApprovals = useCallback(async () => {
    try {
      const resp = await client.listPendingApprovals({ sessionId: currentSessionId ?? '' })
      const raw = Array.isArray(resp.approvals) ? resp.approvals : []
      const list = raw.flatMap((item) => {
        const parsed = toPendingApproval(item)
        return parsed ? [parsed] : []
      })
      setApprovals(list)
    } catch (err) {
      console.error('Failed to refresh pending approvals:', err)
      setApprovals([])
    }
  }, [currentSessionId, setApprovals])

  useEffect(() => {
    refreshApprovals()
  }, [refreshApprovals])

  useEffect(() => {
    if (currentSessionId || sessions.length === 0) return
    const preferred = sessions.find((s) => s.status === 'active') ?? sessions[0]
    setCurrentSessionId(preferred.id)
  }, [currentSessionId, sessions, setCurrentSessionId])

  useEffect(() => {
    if (!currentSessionId) return
    if (!canWrite) return

    let cancelled = false
    issueSessionToken(currentSessionId)
      .then((resp) => {
        if (cancelled) return
        setTerminalTokenSessionId(currentSessionId)
        setTerminalToken(resp.token)
      })
      .catch((err) => {
        if (cancelled) return
        console.error('Failed to issue session token:', err)
        setTerminalTokenSessionId(currentSessionId)
        setTerminalToken(undefined)
      })
    return () => {
      cancelled = true
    }
  }, [canWrite, currentSessionId])

  useEffect(() => {
    const handler = (event: KeyboardEvent) => {
      if (event.defaultPrevented) return
      const key = event.key.toLowerCase()
      const commandKey = event.metaKey || event.ctrlKey

      if (commandKey && (key === 'p' || key === 'k')) {
        event.preventDefault()
        setCommandPaletteOpen(true)
        return
      }

      if (commandKey && key === 'f') {
        event.preventDefault()
        setSearchOpen(true)
      }
    }

    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [])

  const handleSendMessage = useCallback(
    async (content: string) => {
      if (!canWrite) return
      if (!currentSessionId) return
      if (!terminalSessionToken) return
      try {
        await client.sendCommand({ sessionId: currentSessionId, type: 'input', content, sessionToken: terminalSessionToken })
      } catch (err) {
        console.error('Failed to send message:', err)
      }
    },
    [canWrite, currentSessionId, terminalSessionToken]
  )

  const handleCommand = useCallback(
    async (command: string) => {
      if (!canWrite) return
      if (!currentSessionId) return
      if (!terminalSessionToken) return
      try {
        await client.sendCommand({ sessionId: currentSessionId, type: 'slash', content: command, sessionToken: terminalSessionToken })
      } catch (err) {
        console.error('Failed to send command:', err)
      }
    },
    [canWrite, currentSessionId, terminalSessionToken]
  )

  const handleRunCommand = useCallback(
    (command: string) => {
      void handleCommand(command)
    },
    [handleCommand]
  )

  const handlePause = useCallback(async () => {
    if (!canWrite) return
    if (!currentSessionId) return
    if (!terminalSessionToken) return
    try {
      await client.workflowAction(
        { sessionId: currentSessionId, action: 'pause', note: 'Paused via web UI' },
        { sessionToken: terminalSessionToken }
      )
    } catch (err) {
      console.error('Failed to pause workflow:', err)
    }
  }, [canWrite, currentSessionId, terminalSessionToken])

  const handleResume = useCallback(async () => {
    if (!canWrite) return
    if (!currentSessionId) return
    if (!terminalSessionToken) return
    try {
      await client.workflowAction(
        { sessionId: currentSessionId, action: 'resume', note: 'Resumed via web UI' },
        { sessionToken: terminalSessionToken }
      )
    } catch (err) {
      console.error('Failed to resume workflow:', err)
    }
  }, [canWrite, currentSessionId, terminalSessionToken])

  const handleApprove = useCallback(
    async (approvalId: string) => {
      if (!canWrite) return
      try {
        await client.approveToolCall({ approvalId, note: 'Approved via web UI' })
        await refreshApprovals()
      } catch (err) {
        console.error('Failed to approve tool call:', err)
      }
    },
    [canWrite, refreshApprovals]
  )

  const handleReject = useCallback(
    async (approvalId: string) => {
      if (!canWrite) return
      try {
        await client.rejectToolCall({ approvalId, reason: 'Rejected via web UI' })
        await refreshApprovals()
      } catch (err) {
        console.error('Failed to reject tool call:', err)
      }
    },
    [canWrite, refreshApprovals]
  )

  const handleSelectSession = useCallback(
    (sessionId: string) => {
      setCurrentSessionId(sessionId)
      setDrawerOpen(false)
      setPortholesOpen(false)
    },
    [setCurrentSessionId]
  )

  const displaySession = useMemo(() => {
    return currentSession ? toDisplaySession(currentSession) : null
  }, [currentSession])

  const displaySessions = useMemo(() => {
    return sessions.map(toDisplaySession)
  }, [sessions])

  const isPaused = !!(currentView?.workflow?.paused || currentView?.status?.paused)

  const messages = useMemo((): DisplayMessage[] => {
    const viewMessages = currentView?.transcript?.messages
    if (!Array.isArray(viewMessages) || viewMessages.length === 0) return []
    return viewMessages.map((msg) => ({
      id: msg.id,
      role:
        msg.role === 'user'
          ? 'user'
          : msg.role === 'assistant'
            ? 'assistant'
            : msg.role === 'tool'
              ? 'tool'
              : 'system',
      content: msg.content,
      timestamp: msg.timestamp,
    }))
  }, [currentView])

  const toolCalls = useMemo(() => {
    const map = new Map<string, ToolCall>()
    if (!currentView?.activeToolCalls) return map
    for (const tool of currentView.activeToolCalls) {
      const status =
        tool.status === 'running' || tool.status === 'completed' || tool.status === 'failed' ? tool.status : 'running'
      map.set(tool.id, {
        id: tool.id,
        name: tool.name,
        arguments: tool.command ? { command: tool.command } : {},
        status,
        requiresApproval: false,
      })
    }
    return map
  }, [currentView?.activeToolCalls])

  const handleSelectMessage = useCallback((messageId: string) => {
    const target = document.getElementById(`message-${messageId}`)
    if (target) {
      target.scrollIntoView({ behavior: 'smooth', block: 'center' })
    }
  }, [])

  const placeholder = !currentSessionId
    ? 'Select a session to start…'
    : !canWrite
      ? 'Read-only mode — connect with a member token to send messages.'
      : !terminalSessionToken
        ? 'Loading session token…'
      : isPaused
        ? 'Session paused. Click Resume to continue.'
        : 'Send a message…'

  return (
    <div className="h-full flex flex-col bg-[var(--color-void)]">
      <SessionHeader
        session={displaySession}
        connectionState={stream.state}
        principalScope={principal.scope}
        onSessionSelect={() => setDrawerOpen(true)}
        onPortholes={() => setPortholesOpen(true)}
        onOperator={canOperate ? () => setOperatorOpen(true) : undefined}
        onSignOut={() => void onSignOut?.()}
        onReconnect={stream.reconnect}
      />

      <div className="flex-1 flex overflow-hidden">
        <SessionSidebar
          sessions={displaySessions}
          currentSessionId={currentSessionId}
          onSelectSession={handleSelectSession}
        />

        <main className="flex-1 flex flex-col overflow-hidden">
          <ConversationView messages={messages} toolCalls={toolCalls} isStreaming={currentView?.isStreaming ?? false} />
          <MessageInput
            onSend={(msg) => void handleSendMessage(msg)}
            onCommand={(cmd) => void handleCommand(cmd)}
            onPause={() => void handlePause()}
            onResume={() => void handleResume()}
            onOpenCommandPalette={() => setCommandPaletteOpen(true)}
            disabled={!currentSessionId || !canWrite || !terminalSessionToken}
            isPaused={isPaused}
            isStreaming={currentView?.isStreaming ?? false}
            placeholder={placeholder}
          />
        </main>

        <PortholesPanel
          session={displaySession}
          view={currentView}
          approvals={approvals}
          activity={activity}
          terminalSessionToken={terminalSessionToken}
          terminalCanConnect={canWrite}
          onPause={canWrite ? () => void handlePause() : undefined}
          onResume={canWrite ? () => void handleResume() : undefined}
          onApprove={canWrite ? (id) => void handleApprove(id) : undefined}
          onReject={canWrite ? (id) => void handleReject(id) : undefined}
          onRefreshApprovals={() => void refreshApprovals()}
        />
      </div>

      <SessionDrawer
        isOpen={drawerOpen}
        sessions={displaySessions}
        currentSessionId={currentSessionId}
        onClose={() => setDrawerOpen(false)}
        onSelectSession={handleSelectSession}
      />

      <div className="lg:hidden">
        <PortholesDrawer
          isOpen={portholesOpen}
          onClose={() => setPortholesOpen(false)}
          session={displaySession}
          view={currentView}
          approvals={approvals}
          activity={activity}
          terminalSessionToken={terminalSessionToken}
          terminalCanConnect={canWrite}
          onPause={canWrite ? () => void handlePause() : undefined}
          onResume={canWrite ? () => void handleResume() : undefined}
          onApprove={canWrite ? (id) => void handleApprove(id) : undefined}
          onReject={canWrite ? (id) => void handleReject(id) : undefined}
          onRefreshApprovals={() => void refreshApprovals()}
        />
      </div>

      <div className="lg:hidden">
        {approvals.slice(0, 1).map((approval) => (
          <ApprovalBanner
            key={approval.id}
            approval={approval}
            onApprove={(id) => void handleApprove(id)}
            onReject={(id) => void handleReject(id)}
          />
        ))}
      </div>

      <CommandPalette
        isOpen={commandPaletteOpen}
        onClose={() => setCommandPaletteOpen(false)}
        onRunCommand={handleRunCommand}
      />

      <ConversationSearch
        isOpen={searchOpen}
        messages={messages}
        onClose={() => setSearchOpen(false)}
        onSelectMessage={handleSelectMessage}
      />

      <OperatorConsole isOpen={operatorOpen} onClose={() => setOperatorOpen(false)} />
    </div>
  )
}
