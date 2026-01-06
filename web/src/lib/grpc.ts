/**
 * gRPC/Connect client utilities for making unary RPC calls
 */

import type {
  CommandRequest,
  CommandResponse,
  ListSessionsRequest,
  ListSessionsResponse,
  GetSessionRequest,
  SessionDetail,
  WorkflowActionRequest,
  WorkflowActionResponse,
  ListPendingApprovalsRequest,
  PendingApprovalsList,
  ApproveToolCallRequest,
  ApproveToolCallResponse,
  RejectToolCallRequest,
  RejectToolCallResponse,
  ApprovalPolicy,
  GetAuditLogRequest,
  AuditLogResponse,
  PushSubscriptionRequest,
  PushSubscriptionResponse,
  UnsubscribePushRequest,
  VAPIDPublicKeyResponse,
} from '../gen/ipc_pb'
import { getAuthToken } from '../auth/token'

// Service name from the proto definition
const SERVICE_NAME = 'buckley.ipc.v1.BuckleyIPC'

// Get the base URL for API calls
function getBaseUrl(): string {
  return window.location.origin
}

// Create headers with auth
function createHeaders(extra?: Record<string, string>): Record<string, string> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    'Connect-Protocol-Version': '1',
  }
  const token = getAuthToken()
  if (token) {
    headers['Authorization'] = `Bearer ${token}`
  }
  if (extra) {
    Object.assign(headers, extra)
  }
  return headers
}

// Helper to make unary RPC calls using the Connect protocol
async function callUnary<I, O>(
  methodName: string,
  request: I,
  extraHeaders?: Record<string, string>
): Promise<O> {
  const url = `${getBaseUrl()}/${SERVICE_NAME}/${methodName}`
  const response = await fetch(url, {
    method: 'POST',
    headers: createHeaders(extraHeaders),
    body: JSON.stringify(request),
  })

  if (!response.ok) {
    const error = await response.text()
    throw new Error(`${methodName} failed: ${response.status} ${error}`)
  }

  return response.json()
}

// Typed client methods
export const client = {
  async sendCommand(request: Partial<CommandRequest>): Promise<CommandResponse> {
    return callUnary('SendCommand', request) as Promise<CommandResponse>
  },

  async listSessions(request: Partial<ListSessionsRequest>): Promise<ListSessionsResponse> {
    return callUnary('ListSessions', request) as Promise<ListSessionsResponse>
  },

  async getSession(request: Partial<GetSessionRequest>): Promise<SessionDetail> {
    return callUnary('GetSession', request) as Promise<SessionDetail>
  },

  async workflowAction(
    request: Partial<WorkflowActionRequest>,
    opts?: { sessionToken?: string }
  ): Promise<WorkflowActionResponse> {
    const token = typeof opts?.sessionToken === 'string' ? opts.sessionToken.trim() : ''
    const headers = token ? { 'X-Buckley-Session-Token': token } : undefined
    return callUnary('WorkflowAction', request, headers) as Promise<WorkflowActionResponse>
  },

  // Approval management
  async listPendingApprovals(request: Partial<ListPendingApprovalsRequest>): Promise<PendingApprovalsList> {
    return callUnary('ListPendingApprovals', request) as Promise<PendingApprovalsList>
  },

  async approveToolCall(request: Partial<ApproveToolCallRequest>): Promise<ApproveToolCallResponse> {
    return callUnary('ApproveToolCall', request) as Promise<ApproveToolCallResponse>
  },

  async rejectToolCall(request: Partial<RejectToolCallRequest>): Promise<RejectToolCallResponse> {
    return callUnary('RejectToolCall', request) as Promise<RejectToolCallResponse>
  },

  async getApprovalPolicy(): Promise<ApprovalPolicy> {
    return callUnary('GetApprovalPolicy', {}) as Promise<ApprovalPolicy>
  },

  async getAuditLog(request: Partial<GetAuditLogRequest>): Promise<AuditLogResponse> {
    return callUnary('GetAuditLog', request) as Promise<AuditLogResponse>
  },

  // Push notifications
  async subscribePush(request: Partial<PushSubscriptionRequest>): Promise<PushSubscriptionResponse> {
    return callUnary('SubscribePush', request) as Promise<PushSubscriptionResponse>
  },

  async unsubscribePush(request: Partial<UnsubscribePushRequest>): Promise<void> {
    return callUnary('UnsubscribePush', request) as Promise<void>
  },

  async getVAPIDPublicKey(): Promise<VAPIDPublicKeyResponse> {
    return callUnary('GetVAPIDPublicKey', {}) as Promise<VAPIDPublicKeyResponse>
  },
}
