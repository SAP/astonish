import { lazy, Suspense } from 'react'
import { Loader2, KeyRound } from 'lucide-react'

const CredentialsSettings = lazy(() => import('./settings/CredentialsSettings'))

/**
 * Top-level Credentials view — rendered at #/credentials.
 * Provides centered layout with header, wrapping the CredentialsSettings panel.
 */
export default function CredentialsView({ isPlatform }: { isPlatform?: boolean }) {
  return (
    <div className="flex-1 overflow-auto p-6" style={{ background: 'var(--bg-primary)' }}>
      <div className="max-w-4xl mx-auto">
        {/* Header */}
        <div className="flex items-center gap-3 mb-6">
          <KeyRound size={20} style={{ color: 'var(--brand)' }} />
          <h1 className="text-lg font-semibold" style={{ color: 'var(--text-primary)' }}>Credentials</h1>
        </div>

        {/* Content */}
        <Suspense fallback={
          <div className="flex items-center justify-center py-12">
            <Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} />
          </div>
        }>
          <CredentialsSettings isPlatform={isPlatform} />
        </Suspense>
      </div>
    </div>
  )
}
