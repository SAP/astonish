import { useState, useEffect, useRef } from 'react'
import { X, Plus, Sparkles } from 'lucide-react'

interface CreateAgentData {
  id: string
  name: string
  description: string
}

interface CreateAgentModalProps {
  isOpen: boolean
  onClose: () => void
  onCreate: (data: CreateAgentData) => void
}

/**
 * Modal for creating a new agent with name and description
 */
export default function CreateAgentModal({ isOpen, onClose, onCreate }: CreateAgentModalProps) {
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [error, setError] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)

  // Focus input when modal opens
  useEffect(() => {
    if (isOpen && inputRef.current) {
      inputRef.current.focus()
    }
  }, [isOpen])

  // Reset form when modal opens
  useEffect(() => {
    if (isOpen) {
      setName('')
      setDescription('')
      setError('')
    }
  }, [isOpen])

  const handleSubmit = (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    
    if (!name.trim()) {
      setError('Please enter a name for your agent')
      return
    }
    
    // Convert to snake_case: "Github Clone Agent" -> "github_clone_agent"
    const agentId = name
      .trim()
      .toLowerCase()
      .replace(/[^a-z0-9\s]/g, '') // Remove special characters
      .replace(/\s+/g, '_') // Replace spaces with underscores
    
    if (!agentId) {
      setError('Invalid agent name. Please use letters and numbers.')
      return
    }
    
    onCreate({
      id: agentId,
      name: name.trim(),
      description: description.trim()
    })
  }

  const handleKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'Escape') {
      onClose()
    }
  }

  if (!isOpen) return null

  return (
    <div 
      className="fixed inset-0 z-50 flex items-center justify-center"
      onKeyDown={handleKeyDown}
    >
      {/* Backdrop */}
      <div 
        className="absolute inset-0 bg-black/60 backdrop-blur-sm"
        onClick={onClose}
      />
      
      {/* Modal */}
      <div 
        className="relative w-full max-w-md mx-4 rounded-2xl shadow-2xl overflow-hidden"
        style={{ background: 'var(--bg-secondary)' }}
      >
        {/* Header with gradient */}
        <div 
          className="px-6 py-5"
          style={{ 
            background: 'linear-gradient(135deg, var(--brand) 0%, var(--brand-strong) 50%, var(--accent3) 100%)'
          }}
        >
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-3">
              <div className="w-10 h-10 rounded-xl bg-white/20 flex items-center justify-center">
                <Sparkles size={20} className="text-white" />
              </div>
              <div>
                <h2 className="text-lg font-semibold text-white">Create New Agent</h2>
                <p className="text-sm text-white/70">Design your AI workflow</p>
              </div>
            </div>
            <button
              onClick={onClose}
              className="p-2 rounded-lg hover:bg-white/10 transition-colors"
            >
              <X size={20} className="text-white" />
            </button>
          </div>
        </div>

        {/* Form */}
        <form onSubmit={handleSubmit} className="p-6 space-y-5">
          {/* Name Input */}
          <div>
            <label 
              className="block text-sm font-medium mb-2"
              style={{ color: 'var(--text-secondary)' }}
            >
              Agent Name <span className="text-red-400">*</span>
            </label>
            <input
              ref={inputRef}
              type="text"
              value={name}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => {
                setName(e.target.value)
                setError('')
              }}
              placeholder="e.g. GitHub PR Reviewer"
              className="w-full px-4 py-3 rounded-xl border text-base transition-all focus:outline-none focus:ring-2 focus:ring-primary"
              style={{ 
                background: 'var(--bg-primary)', 
                borderColor: error ? '#EF4444' : 'var(--border-color)',
                color: 'var(--text-primary)'
              }}
            />
            {name && (
              <p className="text-xs mt-1.5" style={{ color: 'var(--text-muted)' }}>
                Will be saved as: <code className="px-1 py-0.5 rounded bg-primary/20 text-primary">
                  {name.trim().toLowerCase().replace(/[^a-z0-9\s]/g, '').replace(/\s+/g, '_') || '...'}.yaml
                </code>
              </p>
            )}
            {error && (
              <p className="text-xs mt-1.5 text-red-400">{error}</p>
            )}
          </div>

          {/* Description Input */}
          <div>
            <label 
              className="block text-sm font-medium mb-2"
              style={{ color: 'var(--text-secondary)' }}
            >
              Description <span style={{ color: 'var(--text-muted)' }}>(optional)</span>
            </label>
            <textarea
              value={description}
              onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setDescription(e.target.value)}
              placeholder="What does this agent do?"
              rows={3}
              className="w-full px-4 py-3 rounded-xl border text-base resize-none transition-all focus:outline-none focus:ring-2 focus:ring-primary"
              style={{ 
                background: 'var(--bg-primary)', 
                borderColor: 'var(--border-color)',
                color: 'var(--text-primary)'
              }}
            />
          </div>

          {/* Actions */}
          <div className="flex gap-3 pt-2">
            <button
              type="button"
              onClick={onClose}
              className="flex-1 px-4 py-3 rounded-xl text-sm font-medium transition-colors"
              style={{ 
                background: 'var(--bg-tertiary)', 
                color: 'var(--text-secondary)' 
              }}
            >
              Cancel
            </button>
            <button
              type="submit"
              className="flex-1 flex items-center justify-center gap-2 px-4 py-3 rounded-xl text-sm font-medium text-white transition-colors"
              style={{ 
                background: 'linear-gradient(135deg, var(--brand) 0%, var(--brand-strong) 100%)'
              }}
            >
              <Plus size={18} />
              Create Agent
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
