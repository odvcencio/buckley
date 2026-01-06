import { Loader2 } from 'lucide-react'

import { LoginScreen } from './auth/LoginScreen'
import { useAuth } from './auth/useAuth'
import { MissionControlApp } from './app/MissionControlApp'

export default function App() {
  const auth = useAuth()

  if (auth.state.status === 'checking') {
    return (
      <div className="min-h-screen bg-[var(--color-void)] flex items-center justify-center">
        <div className="flex items-center gap-3 text-[var(--color-text-secondary)]">
          <Loader2 className="w-5 h-5 animate-spin" />
          <span className="text-sm">Connecting…</span>
        </div>
      </div>
    )
  }

  if (auth.state.status === 'needs_token') {
    return (
      <LoginScreen
        initialToken={auth.savedToken}
        message={auth.state.message}
        onSubmit={auth.login}
        onClear={auth.clearToken}
      />
    )
  }

  return <MissionControlApp principal={auth.state.principal} onSignOut={auth.clearToken} />
}

