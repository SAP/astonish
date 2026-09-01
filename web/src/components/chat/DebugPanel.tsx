import { useState } from 'react'

import CacheDiagnosticsPanel from './CacheDiagnosticsPanel'
import KnowledgeDebugPanel from './KnowledgeDebugPanel'
import { cn } from '@/lib/utils'

type DebugTab = 'memory' | 'cache'

const TABS: { key: DebugTab; label: string }[] = [
  { key: 'memory', label: 'Memory' },
  { key: 'cache', label: 'Cache' },
]

export default function DebugPanel({ sessionId, invocationId }: { sessionId: string; invocationId: string }) {
  const [activeTab, setActiveTab] = useState<DebugTab>('memory')

  return (
    <div className="mt-2 min-w-0" data-testid="debug-panel">
      {/* Tab bar */}
      <div className="flex gap-0 border-b border-border mb-0">
        {TABS.map(tab => (
          <button
            key={tab.key}
            type="button"
            onClick={() => setActiveTab(tab.key)}
            className={cn(
              'px-3 py-1.5 text-xs font-medium transition-colors relative',
              activeTab === tab.key
                ? 'text-foreground'
                : 'text-muted-foreground hover:text-foreground',
            )}
          >
            {tab.label}
            {activeTab === tab.key && (
              <span className="absolute bottom-0 left-0 right-0 h-0.5 bg-primary rounded-full" />
            )}
          </button>
        ))}
      </div>

      {/* Tab content */}
      {activeTab === 'memory' && (
        <KnowledgeDebugPanel sessionId={sessionId} invocationId={invocationId} />
      )}
      {activeTab === 'cache' && (
        <CacheDiagnosticsPanel sessionId={sessionId} invocationId={invocationId} />
      )}
    </div>
  )
}
