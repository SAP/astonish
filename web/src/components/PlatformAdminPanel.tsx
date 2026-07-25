import { useState, lazy, Suspense } from 'react'
import { Building2, Users, Crown, Shield, Globe, Box, Loader2 } from 'lucide-react'
import { gradientAmber } from './platformAdmin/sharedStyles'

const OrgsTab = lazy(() => import('./platformAdmin/OrgsTab'))
const UsersTab = lazy(() => import('./platformAdmin/UsersTab'))
const AuthTab = lazy(() => import('./platformAdmin/AuthTab'))
const ChannelsTab = lazy(() => import('./platformAdmin/ChannelsTab'))
const SandboxBaseTab = lazy(() => import('./platformAdmin/SandboxBaseTab'))

function TabFallback() {
  return (
    <div className="flex items-center justify-center py-12">
      <Loader2 size={24} className="animate-spin" style={{ color: 'var(--text-muted)' }} />
    </div>
  )
}

interface PlatformAdminPanelProps {
  theme?: string
  activeTab?: string
  onTabChange?: (tab: string) => void
}

export default function PlatformAdminPanel({ theme, activeTab: externalTab, onTabChange: externalOnTabChange }: PlatformAdminPanelProps) {
  const [internalTab, setInternalTab] = useState<string>('orgs')
  const activeTab = externalTab || internalTab
  const onTabChange = externalOnTabChange || setInternalTab

  return (
    <div className="flex flex-col h-full" style={{ background: 'var(--bg-primary)', color: 'var(--text-primary)' }}>
      {/* Header */}
      <div className="flex items-center justify-between px-6 py-4 border-b" style={{ borderColor: 'var(--border-color)' }}>
        <div className="flex items-center gap-3">
          <div className="p-2 rounded-xl" style={gradientAmber}>
            <Crown size={20} className="text-white" />
          </div>
          <div>
            <h1 className="text-lg font-semibold" style={{ color: 'var(--text-primary)' }}>Platform Administration</h1>
            <p className="text-xs" style={{ color: 'var(--text-muted)' }}>Manage organizations and users across the platform</p>
          </div>
        </div>

        {/* Tabs in header area */}
        <div className="flex items-center gap-1">
          <button
            onClick={() => onTabChange('orgs')}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-colors"
            style={{
              background: activeTab === 'orgs' ? 'var(--accent-soft)' : 'transparent',
              color: activeTab === 'orgs' ? 'var(--brand)' : 'var(--text-muted)',
            }}
          >
            <Building2 size={13} /> Organizations
          </button>
          <button
            onClick={() => onTabChange('users')}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-colors"
            style={{
              background: activeTab === 'users' ? 'var(--accent-soft)' : 'transparent',
              color: activeTab === 'users' ? 'var(--brand)' : 'var(--text-muted)',
            }}
          >
            <Users size={13} /> Users
          </button>
          <button
            onClick={() => onTabChange('auth')}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-colors"
            style={{
              background: activeTab === 'auth' ? 'var(--accent-soft)' : 'transparent',
              color: activeTab === 'auth' ? 'var(--brand)' : 'var(--text-muted)',
            }}
          >
            <Shield size={13} /> Authentication
          </button>
          <button
            onClick={() => onTabChange('channels')}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-colors"
            style={{
              background: activeTab === 'channels' ? 'var(--accent-soft)' : 'transparent',
              color: activeTab === 'channels' ? 'var(--brand)' : 'var(--text-muted)',
            }}
          >
            <Globe size={13} /> Channels
          </button>
          <button
            onClick={() => onTabChange('sandbox')}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-colors"
            style={{
              background: activeTab === 'sandbox' ? 'var(--accent-soft)' : 'transparent',
              color: activeTab === 'sandbox' ? 'var(--brand)' : 'var(--text-muted)',
            }}
          >
            <Box size={13} /> Base Sandbox
          </button>
        </div>
      </div>

      {/* Content */}
      <Suspense fallback={<TabFallback />}>
        {activeTab === 'orgs' && <OrgsTab />}
        {activeTab === 'users' && <UsersTab />}
        {activeTab === 'auth' && <AuthTab />}
        {activeTab === 'channels' && <ChannelsTab />}
        {activeTab === 'sandbox' && <SandboxBaseTab />}
      </Suspense>
    </div>
  )
}
