import { useCallback, useEffect, useMemo, useState } from 'react'
import { Copy, KeyRound, RefreshCw, Settings2, Trash2, X } from 'lucide-react'

import type { APITokenRecord, AuditEntry } from '../lib/api'
import { createAPIToken, listAPITokens, listAuditLogs, listSettings, revokeAPIToken, updateSetting } from '../lib/api'
import { useOverlayControls } from '../hooks/useOverlayControls'

type Tab = 'tokens' | 'settings' | 'audit'

function formatTime(value?: string) {
  if (!value) return '—'
  const date = new Date(value)
  const now = new Date()
  const diffMs = now.getTime() - date.getTime()
  const diffMins = Math.floor(diffMs / 60000)
  if (Number.isNaN(diffMins)) return '—'
  if (diffMins < 1) return 'Just now'
  if (diffMins < 60) return `${diffMins}m`
  if (diffMins < 1440) return `${Math.floor(diffMins / 60)}h`
  return date.toLocaleDateString()
}

function scopePill(scope: string) {
  const normalized = (scope || '').toLowerCase()
  switch (normalized) {
    case 'operator':
      return 'bg-[var(--color-warning-subtle)] text-[var(--color-warning)] border-[var(--color-warning)]/20'
    case 'viewer':
      return 'bg-[var(--color-depth)] text-[var(--color-text-muted)] border-[var(--color-border-subtle)]'
    default:
      return 'bg-[var(--color-success-subtle)] text-[var(--color-success)] border-[var(--color-success)]/20'
  }
}

export function OperatorConsole({ isOpen, onClose }: { isOpen: boolean; onClose: () => void }) {
  useOverlayControls(isOpen, onClose)

  const [tab, setTab] = useState<Tab>('tokens')
  const [error, setError] = useState<string | null>(null)

  const [tokens, setTokens] = useState<APITokenRecord[] | null>(null)
  const [settings, setSettings] = useState<Record<string, string> | null>(null)
  const [audit, setAudit] = useState<AuditEntry[] | null>(null)

  const [newTokenName, setNewTokenName] = useState('')
  const [newTokenOwner, setNewTokenOwner] = useState('')
  const [newTokenScope, setNewTokenScope] = useState<'member' | 'viewer' | 'operator'>('member')
  const [issuedToken, setIssuedToken] = useState<{ secret: string; record: APITokenRecord } | null>(null)

  const [editingSettings, setEditingSettings] = useState<Record<string, string>>({})

  const loadAll = useCallback(async () => {
    setError(null)
    try {
      const [tokResp, settingsResp, auditResp] = await Promise.all([
        listAPITokens(),
        listSettings(),
        listAuditLogs(200),
      ])
      setTokens(tokResp.tokens || [])
      setSettings(settingsResp.settings || {})
      setEditingSettings(settingsResp.settings || {})
      setAudit(auditResp.audit || [])
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load operator data.')
    }
  }, [])

  useEffect(() => {
    if (!isOpen) return
    Promise.resolve().then(() => loadAll())
  }, [isOpen, loadAll])

  const activeTabClass =
    'bg-[var(--color-surface)] border-[var(--color-border)] text-[var(--color-text)]'
  const inactiveTabClass =
    'bg-transparent border-[var(--color-border-subtle)] text-[var(--color-text-muted)] hover:bg-[var(--color-surface)]/50 hover:text-[var(--color-text-secondary)]'

  const tokenCount = tokens?.length ?? 0
  const auditCount = audit?.length ?? 0

  const handleCopy = useCallback(async (value: string) => {
    try {
      await navigator.clipboard.writeText(value)
    } catch {
      // ignore
    }
  }, [])

  const handleCreateToken = useCallback(async () => {
    setError(null)
    try {
      const resp = await createAPIToken({
        name: newTokenName.trim(),
        owner: newTokenOwner.trim(),
        scope: newTokenScope,
      })
      setIssuedToken({ secret: resp.token, record: resp.record })
      const list = await listAPITokens()
      setTokens(list.tokens || [])
      setNewTokenName('')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create token.')
    }
  }, [newTokenName, newTokenOwner, newTokenScope])

  const handleRevokeToken = useCallback(async (tok: APITokenRecord) => {
    setError(null)
    try {
      if (tok.revoked) return
      const label = tok.prefix ? `${tok.name} (${tok.prefix})` : tok.name
      if (!window.confirm(`Revoke token "${label}"?`)) return
      await revokeAPIToken(tok.id)
      const list = await listAPITokens()
      setTokens(list.tokens || [])
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to revoke token.')
    }
  }, [])

  const sortedSettings = useMemo(() => {
    const entries = Object.entries(editingSettings)
    entries.sort((a, b) => a[0].localeCompare(b[0]))
    return entries
  }, [editingSettings])

  const handleSaveSetting = useCallback(async (key: string) => {
    setError(null)
    try {
      await updateSetting(key, editingSettings[key] ?? '')
      const fresh = await listSettings()
      setSettings(fresh.settings || {})
      setEditingSettings(fresh.settings || {})
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update setting.')
    }
  }, [editingSettings])

  if (!isOpen) return null

  return (
    <>
      <div className="fixed inset-0 bg-black/60 z-40" onClick={onClose} aria-hidden="true" />

      <div className="fixed inset-0 z-50 flex items-center justify-center p-4">
        <div
          className="w-full max-w-5xl max-h-[90vh] rounded-3xl border border-[var(--color-border)] bg-[var(--color-abyss)]/95 backdrop-blur-xl shadow-2xl shadow-black/50 overflow-hidden flex flex-col"
          role="dialog"
          aria-modal="true"
          aria-label="Operator Console"
        >
          <div className="px-6 py-5 border-b border-[var(--color-border)] flex items-center justify-between gap-4">
            <div className="flex items-center gap-3 min-w-0">
              <div className="w-10 h-10 rounded-2xl bg-[var(--color-surface)] border border-[var(--color-border-subtle)] flex items-center justify-center">
                <Settings2 className="w-5 h-5 text-[var(--color-text-secondary)]" />
              </div>
              <div className="min-w-0">
                <div className="text-lg font-display font-bold text-[var(--color-text)] truncate">
                  Operator Console
                </div>
                <div className="text-sm text-[var(--color-text-muted)] truncate">
                  Manage API tokens, settings, and audit history.
                </div>
              </div>
            </div>

            <div className="flex items-center gap-2">
              <button
                onClick={() => void loadAll()}
                className="p-2 rounded-xl hover:bg-[var(--color-surface)] transition-colors"
                title="Refresh"
                aria-label="Refresh"
              >
                <RefreshCw className="w-4 h-4 text-[var(--color-text-secondary)]" />
              </button>
              <button
                onClick={onClose}
                className="p-2 rounded-xl hover:bg-[var(--color-surface)] transition-colors"
                title="Close"
                aria-label="Close"
              >
                <X className="w-5 h-5 text-[var(--color-text-secondary)]" />
              </button>
            </div>
          </div>

          <div className="px-6 py-3 border-b border-[var(--color-border)] flex items-center gap-2">
            <button
              onClick={() => setTab('tokens')}
              className={`px-4 py-2 rounded-xl border text-sm font-semibold transition-colors ${tab === 'tokens' ? activeTabClass : inactiveTabClass}`}
            >
              Tokens <span className="ml-1 text-xs text-[var(--color-text-muted)]">({tokenCount})</span>
            </button>
            <button
              onClick={() => setTab('settings')}
              className={`px-4 py-2 rounded-xl border text-sm font-semibold transition-colors ${tab === 'settings' ? activeTabClass : inactiveTabClass}`}
            >
              Settings
            </button>
            <button
              onClick={() => setTab('audit')}
              className={`px-4 py-2 rounded-xl border text-sm font-semibold transition-colors ${tab === 'audit' ? activeTabClass : inactiveTabClass}`}
            >
              Audit <span className="ml-1 text-xs text-[var(--color-text-muted)]">({auditCount})</span>
            </button>

            <div className="flex-1" />

            {error && (
              <div className="text-sm text-[var(--color-error)] truncate max-w-[50%]">{error}</div>
            )}
          </div>

          <div className="flex-1 overflow-y-auto p-6 space-y-6 scrollbar-thin">
            {tab === 'tokens' && (
              <div className="space-y-6">
                <div className="rounded-2xl border border-[var(--color-border-subtle)] bg-[var(--color-surface)]/70 backdrop-blur-xl p-5">
                  <div className="flex items-center gap-2 mb-4">
                    <KeyRound className="w-4 h-4 text-[var(--color-text-secondary)]" />
                    <div className="text-sm font-semibold text-[var(--color-text)]">Create token</div>
                  </div>

                  <div className="grid md:grid-cols-3 gap-3">
                    <div>
                      <label className="block text-xs font-semibold text-[var(--color-text-muted)] mb-2">Name</label>
                      <input
                        value={newTokenName}
                        onChange={(e) => setNewTokenName(e.target.value)}
                        placeholder="mission-control"
                        className="w-full rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] px-3 py-2 text-sm text-[var(--color-text)] focus:outline-none focus:ring-2 focus:ring-[var(--color-accent)]/30"
                      />
                    </div>

                    <div>
                      <label className="block text-xs font-semibold text-[var(--color-text-muted)] mb-2">Owner</label>
                      <input
                        value={newTokenOwner}
                        onChange={(e) => setNewTokenOwner(e.target.value)}
                        placeholder="team@yourcompany.com"
                        className="w-full rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] px-3 py-2 text-sm text-[var(--color-text)] focus:outline-none focus:ring-2 focus:ring-[var(--color-accent)]/30"
                      />
                    </div>

                    <div>
                      <label className="block text-xs font-semibold text-[var(--color-text-muted)] mb-2">Scope</label>
                      <select
                        value={newTokenScope}
                        onChange={(e) => setNewTokenScope(e.target.value as 'member' | 'viewer' | 'operator')}
                        className="w-full rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] px-3 py-2 text-sm text-[var(--color-text)] focus:outline-none focus:ring-2 focus:ring-[var(--color-accent)]/30"
                      >
                        <option value="viewer">viewer (read-only)</option>
                        <option value="member">member (commands + approvals)</option>
                        <option value="operator">operator (admin)</option>
                      </select>
                    </div>
                  </div>

                  <div className="mt-4 flex items-center justify-between gap-3">
                    <div className="text-xs text-[var(--color-text-muted)]">
                      Secrets are shown once. Store them in a password manager or secret store.
                    </div>
                    <button
                      onClick={() => void handleCreateToken()}
                      className="px-4 py-2 rounded-xl bg-[var(--color-accent)] text-[var(--color-text-inverse)] text-sm font-semibold hover:bg-[var(--color-accent-hover)] transition-colors"
                    >
                      Create
                    </button>
                  </div>
                </div>

                {issuedToken && (
                  <div className="rounded-2xl border border-[var(--color-border-subtle)] bg-[var(--color-abyss)]/60 p-5">
                    <div className="flex items-center justify-between gap-3">
                      <div className="text-sm font-semibold text-[var(--color-text)]">Token created</div>
                      <button
                        onClick={() => setIssuedToken(null)}
                        className="p-2 rounded-xl hover:bg-[var(--color-surface)] transition-colors"
                        aria-label="Dismiss token"
                      >
                        <X className="w-4 h-4 text-[var(--color-text-secondary)]" />
                      </button>
                    </div>
                    <div className="mt-3 grid md:grid-cols-2 gap-3">
                      <div className="rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] p-3">
                        <div className="text-xs text-[var(--color-text-muted)] mb-1">Record</div>
                        <div className="text-sm text-[var(--color-text)]">
                          <span className="font-semibold">{issuedToken.record.name}</span>{' '}
                          <span className={`ml-2 text-[10px] px-2 py-0.5 rounded-full border ${scopePill(issuedToken.record.scope)}`}>
                            {issuedToken.record.scope}
                          </span>
                        </div>
                        <div className="mt-1 text-xs text-[var(--color-text-muted)]">
                          id <span className="font-mono">{issuedToken.record.id}</span> · prefix{' '}
                          <span className="font-mono">{issuedToken.record.prefix}</span>
                        </div>
                      </div>
                      <div className="rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] p-3">
                        <div className="flex items-center justify-between gap-2">
                          <div className="text-xs text-[var(--color-text-muted)]">Secret</div>
                          <button
                            onClick={() => void handleCopy(issuedToken.secret)}
                            className="p-1.5 rounded-lg hover:bg-[var(--color-surface)] transition-colors"
                            title="Copy"
                          >
                            <Copy className="w-4 h-4 text-[var(--color-text-secondary)]" />
                          </button>
                        </div>
                        <div className="mt-1 font-mono text-xs text-[var(--color-text)] break-all">
                          {issuedToken.secret}
                        </div>
                      </div>
                    </div>
                  </div>
                )}

                <div className="rounded-2xl border border-[var(--color-border-subtle)] bg-[var(--color-surface)]/40 backdrop-blur-xl overflow-hidden">
                  <div className="px-5 py-4 border-b border-[var(--color-border-subtle)] flex items-center justify-between">
                    <div className="text-sm font-semibold text-[var(--color-text)]">API tokens</div>
                    <div className="text-xs text-[var(--color-text-muted)]">
                      {tokenCount} total
                    </div>
                  </div>
                  <div className="divide-y divide-[var(--color-border-subtle)]">
                    {tokens == null ? (
                      <div className="px-5 py-6 text-sm text-[var(--color-text-muted)]">Loading…</div>
                    ) : tokens.length === 0 ? (
                      <div className="px-5 py-6 text-sm text-[var(--color-text-muted)]">No tokens yet.</div>
                    ) : (
                      tokens.map((tok) => (
                        <div key={tok.id} className="px-5 py-4 flex items-start justify-between gap-4">
                          <div className="min-w-0">
                            <div className="flex items-center gap-2">
                              <div className="text-sm font-semibold text-[var(--color-text)] truncate">{tok.name}</div>
                              <span className={`text-[10px] px-2 py-0.5 rounded-full border ${scopePill(tok.scope)}`}>
                                {tok.scope}
                              </span>
                              {tok.revoked && (
                                <span className="text-[10px] px-2 py-0.5 rounded-full border bg-[var(--color-error-subtle)] text-[var(--color-error)] border-[var(--color-error)]/20">
                                  revoked
                                </span>
                              )}
                            </div>
                            <div className="mt-1 text-xs text-[var(--color-text-muted)]">
                              prefix <span className="font-mono">{tok.prefix}</span> · created {formatTime(tok.createdAt)} · last used{' '}
                              {formatTime(tok.lastUsedAt)}
                            </div>
                            {tok.owner ? (
                              <div className="mt-1 text-xs text-[var(--color-text-muted)]">
                                owner <span className="font-mono">{tok.owner}</span>
                              </div>
                            ) : null}
                          </div>

                          <div className="flex items-center gap-2">
                            <button
                              onClick={() => void handleCopy(tok.id)}
                              className="p-2 rounded-xl hover:bg-[var(--color-surface)] transition-colors"
                              title="Copy token id"
                            >
                              <Copy className="w-4 h-4 text-[var(--color-text-secondary)]" />
                            </button>
                            <button
                              onClick={() => void handleRevokeToken(tok)}
                              disabled={tok.revoked}
                              className={`p-2 rounded-xl transition-colors ${tok.revoked ? 'opacity-50 cursor-not-allowed' : 'hover:bg-[var(--color-surface)]'}`}
                              title="Revoke token"
                            >
                              <Trash2 className="w-4 h-4 text-[var(--color-text-secondary)]" />
                            </button>
                          </div>
                        </div>
                      ))
                    )}
                  </div>
                </div>
              </div>
            )}

            {tab === 'settings' && (
              <div className="rounded-2xl border border-[var(--color-border-subtle)] bg-[var(--color-surface)]/40 backdrop-blur-xl overflow-hidden">
                <div className="px-5 py-4 border-b border-[var(--color-border-subtle)]">
                  <div className="text-sm font-semibold text-[var(--color-text)]">Settings</div>
                  <div className="text-xs text-[var(--color-text-muted)] mt-1">
                    These values affect hosted/remote flows. Empty values reset to defaults.
                  </div>
                </div>

                <div className="divide-y divide-[var(--color-border-subtle)]">
                  {settings == null ? (
                    <div className="px-5 py-6 text-sm text-[var(--color-text-muted)]">Loading…</div>
                  ) : sortedSettings.length === 0 ? (
                    <div className="px-5 py-6 text-sm text-[var(--color-text-muted)]">No editable settings.</div>
                  ) : (
                    sortedSettings.map(([key, value]) => (
                      <div key={key} className="px-5 py-4">
                        <div className="flex items-center justify-between gap-3">
                          <div className="min-w-0">
                            <div className="text-sm font-semibold text-[var(--color-text)] font-mono truncate">{key}</div>
                            <div className="mt-1 text-xs text-[var(--color-text-muted)]">
                              current: <span className="font-mono">{value || '—'}</span>
                            </div>
                          </div>
                          <button
                            onClick={() => void handleSaveSetting(key)}
                            className="px-3 py-2 rounded-xl bg-[var(--color-surface)] border border-[var(--color-border)] text-xs font-semibold text-[var(--color-text)] hover:bg-[var(--color-elevated)] transition-colors"
                          >
                            Save
                          </button>
                        </div>
                        <input
                          value={editingSettings[key] ?? ''}
                          onChange={(e) => setEditingSettings((prev) => ({ ...prev, [key]: e.target.value }))}
                          placeholder={key === 'remote.base_url' ? 'https://buckley.yourdomain.com' : 'Optional note shown to users'}
                          className="mt-3 w-full rounded-xl bg-[var(--color-depth)] border border-[var(--color-border-subtle)] px-3 py-2 text-sm text-[var(--color-text)] focus:outline-none focus:ring-2 focus:ring-[var(--color-accent)]/30"
                        />
                      </div>
                    ))
                  )}
                </div>
              </div>
            )}

            {tab === 'audit' && (
              <div className="rounded-2xl border border-[var(--color-border-subtle)] bg-[var(--color-surface)]/40 backdrop-blur-xl overflow-hidden">
                <div className="px-5 py-4 border-b border-[var(--color-border-subtle)] flex items-center justify-between gap-3">
                  <div>
                    <div className="text-sm font-semibold text-[var(--color-text)]">Audit log</div>
                    <div className="text-xs text-[var(--color-text-muted)] mt-1">Recent operator actions.</div>
                  </div>
                  <div className="text-xs text-[var(--color-text-muted)]">{auditCount} shown</div>
                </div>
                <div className="divide-y divide-[var(--color-border-subtle)]">
                  {audit == null ? (
                    <div className="px-5 py-6 text-sm text-[var(--color-text-muted)]">Loading…</div>
                  ) : audit.length === 0 ? (
                    <div className="px-5 py-6 text-sm text-[var(--color-text-muted)]">No audit entries yet.</div>
                  ) : (
                    audit.map((entry, idx) => (
                      <div key={`${entry.createdAt}:${idx}`} className="px-5 py-4">
                        <div className="flex items-center justify-between gap-2">
                          <div className="text-sm font-semibold text-[var(--color-text)]">
                            {entry.action}
                          </div>
                          <div className="text-xs text-[var(--color-text-muted)] tabular-nums">
                            {formatTime(entry.createdAt)}
                          </div>
                        </div>
                        <div className="mt-1 text-xs text-[var(--color-text-muted)]">
                          actor <span className="font-mono">{entry.actor}</span> · scope{' '}
                          <span className="font-mono">{entry.scope}</span>
                        </div>
                        {entry.payload != null && (
                          <pre className="mt-2 text-xs bg-[var(--color-abyss)] rounded-lg p-3 overflow-x-auto font-mono text-[var(--color-text-secondary)] border border-[var(--color-border-subtle)]">
                            {JSON.stringify(entry.payload, null, 2)}
                          </pre>
                        )}
                      </div>
                    ))
                  )}
                </div>
              </div>
            )}
          </div>

          <div className="px-6 py-4 border-t border-[var(--color-border)] flex items-center justify-end gap-2">
            <div className="text-xs text-[var(--color-text-muted)] flex-1">
              Operator actions are logged. Tokens follow the principle of least privilege.
            </div>
            <button
              onClick={onClose}
              className="px-4 py-2 rounded-xl bg-[var(--color-surface)] border border-[var(--color-border)] text-sm font-semibold text-[var(--color-text)] hover:bg-[var(--color-elevated)] transition-colors"
            >
              Close
            </button>
          </div>
        </div>
      </div>
    </>
  )
}
