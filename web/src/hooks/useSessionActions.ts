import { useCallback } from 'react'

import { client } from '../lib/grpc'
import { toPendingApproval } from '../ipc/normalize'
import type { PendingApproval } from '../types'

type Options = {
  canWrite: boolean
  sessionId: string | null
  sessionToken?: string
  isStreaming: boolean
  setCommandStatus: (status: string) => void
  setApprovals: (approvals: PendingApproval[]) => void
}

export function useSessionActions({ canWrite, sessionId, sessionToken, isStreaming, setCommandStatus, setApprovals }: Options) {
  const refreshApprovals = useCallback(async () => {
    try {
      const resp = await client.listPendingApprovals({ sessionId: sessionId ?? '' })
      const raw = Array.isArray(resp.approvals) ? resp.approvals : []
      setApprovals(raw.flatMap((item) => {
        const parsed = toPendingApproval(item)
        return parsed ? [parsed] : []
      }))
    } catch (err) {
      console.error('Failed to refresh pending approvals:', err)
      setApprovals([])
    }
  }, [sessionId, setApprovals])

  const sendCommand = useCallback(async (type: string, content = '') => {
    if (!canWrite || !sessionId || !sessionToken) return
    try {
      const ack = await client.sendCommand({ sessionId, type, content, sessionToken })
      setCommandStatus(`${type} · ${ack.status} · ${ack.commandId.slice(0, 10)}`)
    } catch (err) {
      console.error(`Failed to send ${type} command:`, err)
      setCommandStatus(`${type} · failed`)
    }
  }, [canWrite, sessionId, sessionToken, setCommandStatus])

  const sendMessage = useCallback((content: string) => {
    return sendCommand(isStreaming ? 'steer' : 'input', content)
  }, [isStreaming, sendCommand])
  const queueMessage = useCallback((content: string) => sendCommand('queue', content), [sendCommand])
  const interrupt = useCallback(() => sendCommand('interrupt'), [sendCommand])
  const runCommand = useCallback((command: string) => {
    const modelMatch = command.match(/^\/model\s+(\S+)\s*$/i)
    return sendCommand(modelMatch ? 'model' : 'slash', modelMatch ? modelMatch[1] : command)
  }, [sendCommand])

  const workflow = useCallback(async (action: 'pause' | 'resume') => {
    if (!canWrite || !sessionId || !sessionToken) return
    try {
      await client.workflowAction(
        { sessionId, action, note: `${action === 'pause' ? 'Paused' : 'Resumed'} via web UI` },
        { sessionToken }
      )
    } catch (err) {
      console.error(`Failed to ${action} workflow:`, err)
    }
  }, [canWrite, sessionId, sessionToken])

  const decideApproval = useCallback(async (approvalId: string, approved: boolean) => {
    if (!canWrite) return
    try {
      if (approved) await client.approveToolCall({ approvalId, note: 'Approved via web UI' })
      else await client.rejectToolCall({ approvalId, reason: 'Rejected via web UI' })
      await refreshApprovals()
    } catch (err) {
      console.error(`Failed to ${approved ? 'approve' : 'reject'} tool call:`, err)
    }
  }, [canWrite, refreshApprovals])

  return {
    refreshApprovals,
    sendMessage,
    queueMessage,
    interrupt,
    runCommand,
    pause: () => workflow('pause'),
    resume: () => workflow('resume'),
    approve: (approvalId: string) => decideApproval(approvalId, true),
    reject: (approvalId: string) => decideApproval(approvalId, false),
  }
}
