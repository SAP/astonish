import { useState } from 'react'
import { X, Maximize2, Minimize2, Save } from 'lucide-react'
import CodeMirror from '@uiw/react-codemirror'
import { yaml } from '@codemirror/lang-yaml'
import { search, searchKeymap, highlightSelectionMatches } from '@codemirror/search'
import { keymap, EditorView } from '@codemirror/view'

interface YamlDrawerProps {
  content: string
  onChange: (value: string) => void
  onClose: () => void
  theme: 'dark' | 'light'
  subtitle?: string
  onSave?: () => void
  isSaving?: boolean
  saveStatus?: 'saved' | 'error' | null
}

export default function YamlDrawer({ content, onChange, onClose, theme, subtitle, onSave, isSaving, saveStatus }: YamlDrawerProps) {
  const [isFullscreen, setIsFullscreen] = useState(false)

  return (
    <div 
      className={`flex flex-col ${isFullscreen ? 'fixed inset-0 z-50' : 'h-full'}`}
      style={{ background: 'var(--bg-secondary)' }}
    >
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3" style={{ borderBottom: '1px solid var(--border-color)' }}>
        <div>
          <h2 className="font-semibold text-sm" style={{ color: 'var(--text-primary)' }}>Source (YAML)</h2>
          <p className="text-xs" style={{ color: 'var(--text-muted)' }}>{subtitle || 'Live synchronized with flow'}</p>
        </div>
        <div className="flex items-center gap-2">
          {onSave && (
            <>
              {saveStatus === 'saved' && <span className="text-xs text-green-400">Saved</span>}
              {saveStatus === 'error' && <span className="text-xs text-red-400">Save failed</span>}
              <button
                onClick={onSave}
                disabled={isSaving}
                className="flex items-center gap-1.5 rounded-lg bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground transition-colors hover:bg-primary/90 disabled:opacity-50"
              >
                <Save size={12} /> {isSaving ? 'Saving...' : 'Save'}
              </button>
            </>
          )}
          <button
            onClick={() => setIsFullscreen(!isFullscreen)}
            className="rounded-lg p-2 transition-colors hover:bg-[color:var(--item-active)]"
            title={isFullscreen ? 'Exit fullscreen' : 'Fullscreen'}
          >
            {isFullscreen ? (
              <Minimize2 size={18} style={{ color: 'var(--text-muted)' }} />
            ) : (
              <Maximize2 size={18} style={{ color: 'var(--text-muted)' }} />
            )}
          </button>
          <button
            onClick={onClose}
            className="rounded-lg p-2 transition-colors hover:bg-[color:var(--item-active)]"
          >
            <X size={20} style={{ color: 'var(--text-muted)' }} />
          </button>
        </div>
      </div>

      {/* Code Editor */}
      <div className="flex-1 overflow-hidden">
        <CodeMirror
          value={content}
          onChange={onChange}
          height="100%"
          extensions={[
            yaml(),
            search({
              scrollToMatch: (range) => EditorView.scrollIntoView(range, { y: 'center', yMargin: 100 })
            }),
            highlightSelectionMatches(),
            keymap.of(searchKeymap),
          ]}
          theme={theme === 'dark' ? 'dark' : 'light'}
          className="h-full text-sm"
          basicSetup={{
            lineNumbers: true,
            highlightActiveLineGutter: true,
            highlightActiveLine: true,
            foldGutter: true,
          }}
        />
      </div>
    </div>
  )
}
