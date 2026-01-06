import { useCallback, useEffect, useState } from 'react'
import { KeyRound, Loader2, ShieldCheck, XCircle } from 'lucide-react'

export function LoginScreen({
  initialToken,
  message,
  onSubmit,
  onClear,
}: {
  initialToken: string
  message?: string
  onSubmit: (token: string) => Promise<void>
  onClear: () => Promise<void>
}) {
  const [token, setToken] = useState(initialToken)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | undefined>(undefined)

  useEffect(() => {
    setToken(initialToken)
  }, [initialToken])

  const handleSubmit = useCallback(async () => {
    setBusy(true)
    setError(undefined)
    try {
      await onSubmit(token)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Login failed.')
    } finally {
      setBusy(false)
    }
  }, [onSubmit, token])

  return (
    <div className="min-h-screen bg-[var(--color-void)] flex items-center justify-center px-4 py-10">
      <div className="w-full max-w-md rounded-3xl border border-[var(--color-border)] bg-[var(--color-abyss)]/90 backdrop-blur-xl shadow-2xl shadow-black/40 overflow-hidden">
        <div className="p-6 border-b border-[var(--color-border)]">
          <div className="flex items-center gap-3">
            <div className="relative">
              <div className="w-11 h-11 rounded-2xl bg-gradient-to-br from-[var(--color-accent)] to-[var(--color-accent-active)] flex items-center justify-center shadow-lg shadow-[var(--color-accent)]/20">
                <KeyRound className="w-5 h-5 text-[var(--color-text-inverse)]" />
              </div>
              <div className="absolute inset-0 rounded-2xl bg-[var(--color-accent)]/25 blur-xl -z-10" />
            </div>
            <div>
              <div className="text-lg font-display font-bold text-[var(--color-text)]">Connect to Buckley</div>
              <div className="text-sm text-[var(--color-text-secondary)]">Enter an IPC token to unlock Mission Control.</div>
            </div>
          </div>
        </div>

        <div className="p-6 space-y-4">
          {(message || error) && (
            <div className="rounded-2xl border border-[var(--color-border-subtle)] bg-[var(--color-surface)] p-4 text-sm text-[var(--color-text-secondary)]">
              <div className="flex items-start gap-3">
                {error ? (
                  <XCircle className="w-5 h-5 text-[var(--color-error)] mt-0.5" />
                ) : (
                  <ShieldCheck className="w-5 h-5 text-[var(--color-accent)] mt-0.5" />
                )}
                <div className="flex-1 min-w-0">
                  <div className="text-[var(--color-text)] font-semibold">{error ? 'Authentication failed' : 'Authentication required'}</div>
                  <div className="mt-1 text-[var(--color-text-secondary)] break-words">{error ?? message}</div>
                </div>
              </div>
            </div>
          )}

          <div>
            <label className="block text-xs font-semibold text-[var(--color-text-muted)] mb-2">Token</label>
            <input
              value={token}
              onChange={(e) => setToken(e.target.value)}
              placeholder="Paste your IPC token"
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              className="
                w-full rounded-2xl bg-[var(--color-surface)] border border-[var(--color-border-subtle)]
                px-4 py-3 text-sm text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]
                focus:outline-none focus:ring-2 focus:ring-[var(--color-accent)]/30
              "
            />
            <div className="mt-2 text-xs text-[var(--color-text-muted)]">
              Tip: you can also open <span className="font-mono">/?token=…</span> once to save it locally.
            </div>
          </div>

          <div className="flex gap-3">
            <button
              type="button"
              onClick={handleSubmit}
              disabled={busy}
              className="
                flex-1 inline-flex items-center justify-center gap-2
                px-4 py-3 rounded-2xl
                bg-[var(--color-accent)] text-[var(--color-text-inverse)]
                font-semibold text-sm
                hover:bg-[var(--color-accent-hover)]
                disabled:opacity-60 disabled:cursor-not-allowed
                transition-colors
              "
            >
              {busy ? <Loader2 className="w-4 h-4 animate-spin" /> : null}
              {busy ? 'Connecting…' : 'Connect'}
            </button>
            <button
              type="button"
              onClick={onClear}
              className="
                px-4 py-3 rounded-2xl
                bg-[var(--color-surface)] border border-[var(--color-border)]
                text-[var(--color-text)]
                font-semibold text-sm
                hover:bg-[var(--color-elevated)]
                transition-colors
              "
            >
              Clear
            </button>
          </div>

          <details className="rounded-2xl border border-[var(--color-border-subtle)] bg-[var(--color-abyss)]/60 p-4">
            <summary className="cursor-pointer text-sm font-semibold text-[var(--color-text)]">Where do I get a token?</summary>
            <div className="mt-3 text-sm text-[var(--color-text-secondary)] space-y-2">
              <div>
                <span className="font-semibold text-[var(--color-text)]">Local:</span> set <span className="font-mono">BUCKLEY_IPC_TOKEN</span> (or pass{' '}
                <span className="font-mono">--auth-token</span>) when starting <span className="font-mono">buckley serve</span>.
              </div>
              <div>
                <span className="font-semibold text-[var(--color-text)]">Hosted:</span> ask an operator for a token, or generate one with{' '}
                <span className="font-mono">buckley remote tokens create</span>.
              </div>
            </div>
          </details>
        </div>
      </div>
    </div>
  )
}

