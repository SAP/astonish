import { useState, useEffect, useRef, useCallback } from 'react'
import { X, Brain, Trash2, Edit3, Check, RotateCcw, Sparkles, Loader } from 'lucide-react'
import { listSessionMemories, extractSessionMemories, updateMemory, deleteTeamMemory, deletePersonalMemory } from '../api/platform'
import type { ExtractionEntry } from '../api/platform'

interface MemoryEntry {
  id: string
  snippet: string
  category: string
  scope: string
  session_id?: string
  created_at?: string
  created_by?: string
}

interface SessionMemoryPanelProps {
  sessionId: string
  isConsolidating?: boolean
  onClose: () => void
}

export default function SessionMemoryPanel({ sessionId, isConsolidating = false, onClose }: SessionMemoryPanelProps) {
  const [memories, setMemories] = useState<MemoryEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [editingId, setEditingId] = useState<string | null>(null)
  const [editContent, setEditContent] = useState('')
  const [editCategory, setEditCategory] = useState('')
  const [extracting, setExtracting] = useState(false)
  const [extractionPreview, setExtractionPreview] = useState<ExtractionEntry[] | null>(null)
  const [applyingExtraction, setApplyingExtraction] = useState(false)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const loadMemories = useCallback(async () => {
    setLoading(true)
    try {
      const res = await listSessionMemories(sessionId)
      setMemories(res.memories || [])
    } catch (err) {
      console.error('Failed to load session memories:', err)
    } finally {
      setLoading(false)
    }
  }, [sessionId])

  useEffect(() => {
    loadMemories()
  }, [sessionId, loadMemories])

  // Poll while consolidating — auto-refresh every 3s until done
  useEffect(() => {
    if (isConsolidating) {
      pollRef.current = setInterval(() => {
        loadMemories()
      }, 3000)
    } else {
      // Consolidation just finished — do one final reload
      if (pollRef.current) {
        clearInterval(pollRef.current)
        pollRef.current = null
        loadMemories()
      }
    }
    return () => {
      if (pollRef.current) {
        clearInterval(pollRef.current)
        pollRef.current = null
      }
    }
  }, [isConsolidating, loadMemories])

  async function handleDelete(memory: MemoryEntry) {
    try {
      if (memory.scope === 'personal') {
        await deletePersonalMemory(memory.id)
      } else {
        await deleteTeamMemory(memory.id)
      }
      setMemories(prev => prev.filter(m => m.id !== memory.id))
    } catch (err) {
      console.error('Failed to delete memory:', err)
    }
  }

  function startEdit(memory: MemoryEntry) {
    setEditingId(memory.id)
    setEditContent(memory.snippet)
    setEditCategory(memory.category)
  }

  async function saveEdit(memory: MemoryEntry) {
    try {
      await updateMemory(memory.scope || 'team', memory.id, editContent, editCategory)
      setMemories(prev => prev.map(m =>
        m.id === memory.id ? { ...m, snippet: editContent, category: editCategory } : m
      ))
      setEditingId(null)
    } catch (err) {
      console.error('Failed to update memory:', err)
    }
  }

  async function handleExtract() {
    setExtracting(true)
    try {
      const res = await extractSessionMemories(sessionId, true) // dry_run = true
      setExtractionPreview(res.entries)
    } catch (err) {
      console.error('Failed to extract memories:', err)
    } finally {
      setExtracting(false)
    }
  }

  async function applyExtraction() {
    setApplyingExtraction(true)
    try {
      await extractSessionMemories(sessionId, false) // dry_run = false (apply)
      setExtractionPreview(null)
      await loadMemories() // refresh
    } catch (err) {
      console.error('Failed to apply extraction:', err)
    } finally {
      setApplyingExtraction(false)
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center"
      style={{ background: 'rgba(0,0,0,0.5)' }}
      onClick={onClose}
    >
      <div
        className="w-full max-w-2xl max-h-[80vh] rounded-xl shadow-2xl overflow-hidden flex flex-col"
        style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)' }}
        onClick={e => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-4" style={{ borderBottom: '1px solid var(--border-color)' }}>
          <div className="flex items-center gap-2">
            <Brain size={18} className="text-primary" />
            <h2 className="text-lg font-semibold" style={{ color: 'var(--text-primary)' }}>
              Session Memories
            </h2>
            <span className="text-xs px-2 py-0.5 rounded-full" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}>
              {memories.length}
            </span>
          </div>
          <div className="flex items-center gap-2">
            {memories.length >= 2 && !extractionPreview && !isConsolidating && (
              <button
                onClick={handleExtract}
                disabled={extracting}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm transition-colors hover:opacity-80"
                style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
                title="Consolidate memories using AI"
              >
                {extracting ? <Loader size={14} className="animate-spin" /> : <Sparkles size={14} className="text-yellow-400" />}
                <span>Extract</span>
              </button>
            )}
            <button onClick={onClose} className="p-1 rounded hover:bg-white/10 transition-colors">
              <X size={18} style={{ color: 'var(--text-muted)' }} />
            </button>
          </div>
        </div>

        {/* Content */}
        <div className="flex-1 overflow-y-auto p-5 space-y-3">
          {/* Consolidation banner */}
          {isConsolidating && (
            <div
              className="flex items-center gap-2 px-4 py-3 rounded-lg"
              style={{ background: 'rgba(128, 90, 213, 0.1)', border: '1px solid rgba(128, 90, 213, 0.3)' }}
            >
              <Loader size={14} className="animate-spin text-primary" />
              <span className="text-sm text-primary">Organizing memories...</span>
            </div>
          )}

          {loading && !isConsolidating && (
            <div className="flex items-center justify-center py-8">
              <Loader size={20} className="animate-spin text-primary" />
            </div>
          )}

          {!loading && memories.length === 0 && !isConsolidating && (
            <div className="text-center py-8" style={{ color: 'var(--text-muted)' }}>
              <Brain size={32} className="mx-auto mb-2 opacity-40" />
              <p className="text-sm">No memories saved during this session yet.</p>
            </div>
          )}

          {/* Extraction Preview */}
          {extractionPreview && (
            <div className="rounded-lg p-4 space-y-3" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--brand)' }}>
              <div className="flex items-center justify-between">
                <span className="text-sm font-medium text-primary">Extraction Preview</span>
                <div className="flex items-center gap-2">
                  <button
                    onClick={() => setExtractionPreview(null)}
                    className="text-xs px-2 py-1 rounded hover:bg-white/10 transition-colors"
                    style={{ color: 'var(--text-muted)' }}
                  >
                    Cancel
                  </button>
                  <button
                    onClick={applyExtraction}
                    disabled={applyingExtraction}
                    className="text-xs px-3 py-1 rounded-lg bg-primary hover:bg-primary/90 text-white transition-colors disabled:opacity-50"
                  >
                    {applyingExtraction ? 'Applying...' : 'Apply'}
                  </button>
                </div>
              </div>
              {extractionPreview.map((entry, i) => (
                <div key={i} className="rounded-md p-3" style={{ background: 'var(--bg-tertiary)' }}>
                  <div className="text-xs font-medium text-primary mb-1">{entry.category}</div>
                  <pre className="text-xs whitespace-pre-wrap" style={{ color: 'var(--text-primary)' }}>{entry.content}</pre>
                </div>
              ))}
            </div>
          )}

          {/* Memory List */}
          {!extractionPreview && memories.map(memory => (
            <div
              key={memory.id}
              className="rounded-lg p-3 group"
              style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}
            >
              {editingId === memory.id ? (
                <div className="space-y-2">
                  <input
                    value={editCategory}
                    onChange={e => setEditCategory(e.target.value)}
                    className="w-full px-2 py-1 rounded text-xs"
                    style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
                    placeholder="Category"
                  />
                  <textarea
                    value={editContent}
                    onChange={e => setEditContent(e.target.value)}
                    className="w-full px-2 py-1.5 rounded text-xs resize-none"
                    style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
                    rows={4}
                  />
                  <div className="flex items-center gap-2">
                    <button
                      onClick={() => saveEdit(memory)}
                      className="text-xs px-2 py-1 rounded bg-green-600 hover:bg-green-700 text-white transition-colors"
                    >
                      <Check size={12} />
                    </button>
                    <button
                      onClick={() => setEditingId(null)}
                      className="text-xs px-2 py-1 rounded hover:bg-white/10 transition-colors"
                      style={{ color: 'var(--text-muted)' }}
                    >
                      <RotateCcw size={12} />
                    </button>
                  </div>
                </div>
              ) : (
                <>
                  <div className="flex items-center justify-between mb-1">
                    <div className="flex items-center gap-2">
                      <span className="text-xs font-medium text-primary">{memory.category}</span>
                      <span className="text-xs px-1.5 py-0.5 rounded" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}>
                        {memory.scope}
                      </span>
                    </div>
                    <div className="flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
                      <button
                        onClick={() => startEdit(memory)}
                        className="p-1 rounded hover:bg-white/10 transition-colors"
                        title="Edit"
                      >
                        <Edit3 size={12} style={{ color: 'var(--text-muted)' }} />
                      </button>
                      <button
                        onClick={() => handleDelete(memory)}
                        className="p-1 rounded hover:bg-red-500/20 transition-colors"
                        title="Delete"
                      >
                        <Trash2 size={12} className="text-red-400" />
                      </button>
                    </div>
                  </div>
                  <pre className="text-xs whitespace-pre-wrap" style={{ color: 'var(--text-primary)' }}>
                    {memory.snippet}
                  </pre>
                </>
              )}
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
