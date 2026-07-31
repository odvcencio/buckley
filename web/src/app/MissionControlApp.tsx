import { useCallback, useEffect, useMemo, useState } from 'react'

import type { AuthPrincipal } from '../auth/types'
import { hasScope } from '../auth/scopes'
import { issueSessionToken, listModels, type ModelOption } from '../lib/api'
import { useGrpcStream, eventToWSEvent } from '../hooks/useGrpcStream'
import { useBuckleyState } from '../hooks/useBuckleyState'
import { useSessionActions } from '../hooks/useSessionActions'
import { toDisplaySession } from '../ipc/normalize'
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
  const [commandStatus, setCommandStatus] = useState<string | undefined>(undefined)
	const [models, setModels] = useState<ModelOption[]>([])
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

	const isStreaming = currentView?.isStreaming ?? false
	const {
		refreshApprovals,
		sendMessage,
		queueMessage,
		interrupt,
		runCommand,
		pause,
		resume,
		approve,
		reject,
	} = useSessionActions({
		canWrite,
		sessionId: currentSessionId,
		sessionToken: terminalSessionToken,
		isStreaming,
		setCommandStatus,
		setApprovals,
	})

  useEffect(() => {
    refreshApprovals()
  }, [refreshApprovals])

	useEffect(() => {
		const controller = new AbortController()
		listModels(true, controller.signal)
			.then((result) => {
				setModels(result.models)
				if (result.warning) setCommandStatus(`catalog · cached · ${result.warning}`)
			})
			.catch((err) => {
				if (!controller.signal.aborted) console.error('Failed to load model catalog:', err)
			})
		return () => controller.abort()
	}, [])

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
		reasoning: msg.reasoning,
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
  }, [currentView])

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
          <ConversationView messages={messages} toolCalls={toolCalls} isStreaming={isStreaming} />
          <MessageInput
            onSend={(msg) => void sendMessage(msg)}
            onQueue={(msg) => void queueMessage(msg)}
            onCommand={(cmd) => void runCommand(cmd)}
            onInterrupt={() => void interrupt()}
            onPause={() => void pause()}
            onResume={() => void resume()}
            onOpenCommandPalette={() => setCommandPaletteOpen(true)}
            disabled={!currentSessionId || !canWrite || !terminalSessionToken}
            isPaused={isPaused}
            isStreaming={isStreaming}
            placeholder={placeholder}
            commandStatus={commandStatus}
			models={models}
			currentModel={displaySession?.model}
          />
        </main>

        <PortholesPanel
          session={displaySession}
          view={currentView}
          approvals={approvals}
          activity={activity}
          terminalSessionToken={terminalSessionToken}
          terminalCanConnect={canWrite}
          onPause={canWrite ? () => void pause() : undefined}
          onResume={canWrite ? () => void resume() : undefined}
          onApprove={canWrite ? (id) => void approve(id) : undefined}
          onReject={canWrite ? (id) => void reject(id) : undefined}
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
          onPause={canWrite ? () => void pause() : undefined}
          onResume={canWrite ? () => void resume() : undefined}
          onApprove={canWrite ? (id) => void approve(id) : undefined}
          onReject={canWrite ? (id) => void reject(id) : undefined}
          onRefreshApprovals={() => void refreshApprovals()}
        />
      </div>

      <div className="lg:hidden">
        {approvals.slice(0, 1).map((approval) => (
          <ApprovalBanner
            key={approval.id}
            approval={approval}
            onApprove={(id) => void approve(id)}
            onReject={(id) => void reject(id)}
          />
        ))}
      </div>

      {commandPaletteOpen && (
        <CommandPalette
          isOpen
          onClose={() => setCommandPaletteOpen(false)}
          onRunCommand={(command) => void runCommand(command)}
        />
      )}

      {searchOpen && (
        <ConversationSearch
          isOpen
          messages={messages}
          onClose={() => setSearchOpen(false)}
          onSelectMessage={handleSelectMessage}
        />
      )}

      <OperatorConsole isOpen={operatorOpen} onClose={() => setOperatorOpen(false)} />
    </div>
  )
}
