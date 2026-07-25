import { useState, useEffect, useRef } from 'react'
import { Send, Brain, Wrench, Loader, RotateCcw, Square, Code, Copy, Check } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { markdownComponents } from './chat/markdownComponents'

interface ChatMessage {
  type: string
  content?: string
  nodeName?: string
  options?: string[]
  preserveWhitespace?: boolean
  attempt?: number
  maxRetries?: number
  reason?: string
  toolName?: string
  title?: string
  suggestion?: string
  originalError?: string
}

interface ChatPanelProps {
  messages: ChatMessage[]
  onSendMessage: (message: string) => void
  onStartRun?: () => void
  onStop: () => void
  theme: string
  isWaitingForInput: boolean
  hasActiveSession: boolean
  autoApprove: boolean
  onToggleAutoApprove: (checked: boolean) => void
}

export default function ChatPanel({ messages, onSendMessage, onStartRun, onStop, theme, isWaitingForInput, hasActiveSession, autoApprove, onToggleAutoApprove }: ChatPanelProps) {
  const [input, setInput] = useState('')
  const [rawViewIndices, setRawViewIndices] = useState(new Set<number>()) // Track which messages show raw
  const [copiedIndex, setCopiedIndex] = useState<number | null>(null) // Track which message was just copied
  const scrollRef = useRef<HTMLDivElement | null>(null)
  const inputRef = useRef<HTMLInputElement | null>(null)

  const toggleRawView = (index: number) => {
    setRawViewIndices(prev => {
      const next = new Set(prev)
      if (next.has(index)) {
        next.delete(index)
      } else {
        next.add(index)
      }
      return next
    })
  }

  const copyToClipboard = async (content: string, index: number) => {
    await navigator.clipboard.writeText(content)
    setCopiedIndex(index)
    setTimeout(() => setCopiedIndex(null), 2000)
  }

  // Auto-scroll to bottom directly, without smooth behavior for instant feedback
  useEffect(() => {
    if (scrollRef.current) {
       scrollRef.current.scrollTop = scrollRef.current.scrollHeight
    }
  }, [messages])

  // Auto-focus input when waiting for free-text input
  useEffect(() => {
    if (isWaitingForInput && inputRef.current) {
      // Check if there are selection options (buttons) - if so, don't focus
      const lastMessage = messages[messages.length - 1]
      const hasSelectionOptions = lastMessage?.type === 'input_request' && (lastMessage?.options?.length ?? 0) > 0
      if (!hasSelectionOptions) {
        inputRef.current.focus()
      }
    }
  }, [isWaitingForInput, messages])

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (input.trim()) {
      onSendMessage(input.trim())
      setInput('')
    }
  }

  return (
    <div className="flex flex-col h-full" style={{ background: 'var(--bg-secondary)' }}>
      {/* Header */}
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-[var(--border-color)] bg-[var(--bg-tertiary)]">
        <div className="flex items-center gap-2">
           <Brain size={18} style={{ color: 'var(--brand)' }} />
           <span className="font-semibold text-[var(--text-primary)]">Agent Chat</span>
        </div>
        <label className={`flex items-center gap-2 px-2 py-1 rounded transition-colors select-none ${hasActiveSession ? 'opacity-50 cursor-not-allowed' : 'cursor-pointer hover:bg-[var(--bg-secondary)]'}`}>
             <input 
               type="checkbox" 
               checked={autoApprove} 
               onChange={(e) => onToggleAutoApprove(e.target.checked)}
               disabled={hasActiveSession}
               className="w-3.5 h-3.5 rounded border-gray-600 text-primary focus:ring-primary bg-gray-700 disabled:opacity-50"
             />
             <span className="text-xs font-medium text-[var(--text-secondary)]">Auto-Approve</span>
           </label>
      </div>

      {/* Messages */}
      <div ref={scrollRef} className="flex-1 overflow-y-auto p-4 space-y-4">
        {messages.length === 0 && onStartRun && (
          <div className="h-full flex flex-col items-center justify-center gap-4 text-center">
            <div className="p-4 rounded-full bg-primary/10 mb-2">
              <Brain size={32} className="text-primary" />
            </div>
            <h3 className="text-lg font-medium" style={{ color: 'var(--text-primary)' }}>
              Ready to Run
            </h3>
            <p className="text-sm max-w-xs mb-4" style={{ color: 'var(--text-muted)' }}>
              Start the agent execution to see the flow in action.
            </p>
            <button
              onClick={onStartRun}
              className="px-6 py-3 bg-gradient-to-r from-primary to-[var(--brand-strong)] hover:opacity-90 text-primary-foreground font-medium rounded-xl shadow-lg transition-all hover:scale-105 flex items-center gap-2"
            >
              <Send size={18} />
              Start Execution
            </button>
          </div>
        )}
        {messages.map((message, index) => (
          <div key={index}>
            {message.type === 'agent' && (
              <div className="space-y-2">
                <div className="flex items-center justify-between">
                  <div className="text-xs font-medium" style={{ color: 'var(--text-muted)' }}>Agent</div>
                  <div className="flex gap-1">
                    <button
                      onClick={() => toggleRawView(index)}
                      className="p-1 rounded hover:bg-white/10 transition-colors"
                      title={rawViewIndices.has(index) ? "Show formatted" : "Show raw markdown"}
                    >
                      <Code size={14} className={rawViewIndices.has(index) ? "text-primary" : "text-gray-500"} />
                    </button>
                    <button
                      onClick={() => copyToClipboard(message.content ?? '', index)}
                      className="p-1 rounded hover:bg-white/10 transition-colors"
                      title="Copy markdown"
                    >
                      {copiedIndex === index ? (
                        <Check size={14} className="text-green-400" />
                      ) : (
                        <Copy size={14} className="text-gray-500" />
                      )}
                    </button>
                  </div>
                </div>
                <div 
                  className="p-4 rounded-lg max-w-[90%]"
                  style={{ 
                    background: theme === 'dark' ? 'rgba(255,255,255,0.08)' : 'white',
                    border: `1px solid var(--border-color)` 
                  }}
                >
                  {rawViewIndices.has(index) ? (
                    <pre 
                      className="text-sm whitespace-pre-wrap break-words font-mono"
                      style={{ color: 'var(--text-primary)' }}
                    >
                      {message.content}
                    </pre>
                  ) : (
                    <div style={{ color: 'var(--text-primary)' }} className="markdown-body text-sm">
                      <ReactMarkdown 
                        remarkPlugins={[remarkGfm]} 
                        components={message.preserveWhitespace ? {
                          ...markdownComponents,
                          p: ({node, ...props}) => <p style={{whiteSpace: 'pre-wrap'}} {...props} />
                        } : markdownComponents}
                      >
                        {message.content}
                      </ReactMarkdown>
                    </div>
                  )}
                </div>
              </div>
            )}
            {message.type === 'user' && (
              <div className="flex justify-end">
                <div className="space-y-2">
                  <div className="text-xs font-medium text-right" style={{ color: 'var(--text-muted)' }}>User</div>
                  <div className="chat-bubble-user p-4 rounded-lg max-w-[90%]">
                    <p>{message.content}</p>
                  </div>
                </div>
              </div>
            )}
            {message.type === 'node' && (
              <div className="flex items-center justify-center my-2">
                <div className="px-3 py-1 rounded-full text-xs font-medium bg-primary/10 text-primary border border-primary/20">
                  ⚡ Executing Node: {message.nodeName}
                </div>
              </div>
            )}
            {message.type === 'system' && (
              <div className="text-xs text-center my-1 italic" style={{ color: 'var(--text-muted)' }}>
                {message.content}
              </div>
            )}
            {message.type === 'error' && (
              <div className="p-3 rounded-lg bg-red-500/10 border border-red-500/20 text-red-400 text-sm">
                Error: {message.content}
              </div>
            )}
            {message.type === 'retry' && (
              <div className="flex items-center gap-2 px-3 py-2 my-1 text-sm">
                <span className="text-orange-400 font-medium">⟳ Retry {message.attempt}/{message.maxRetries}:</span>
                <span className="text-gray-400">{message.reason}</span>
              </div>
            )}
            {message.type === 'tool_auto_approved' && (
              <div className="flex items-center gap-2 px-3 py-2 my-1 text-sm">
                <span className="flex items-center gap-1.5 px-2 py-1 rounded bg-green-500/10 border border-green-500/20 text-green-400">
                  <span>✓</span> Auto-approved: <code className="bg-green-500/20 px-1.5 py-0.5 rounded font-mono text-xs">{message.toolName}</code>
                </span>
              </div>
            )}
            {message.type === 'error_info' && (
              <div className="my-3 p-4 rounded-lg bg-red-500/5 border border-red-500/20 space-y-3">
                <div className="flex items-center gap-2 text-red-400 font-medium">
                  <span>✕</span> {message.title}
                </div>
                {message.reason && (
                  <p className="text-sm text-gray-300 pl-4">{message.reason}</p>
                )}
                {message.suggestion && (
                  <div className="pl-4">
                    <p className="text-sm font-medium text-yellow-400">Suggestion:</p>
                    <p className="text-sm text-yellow-300/80">{message.suggestion}</p>
                  </div>
                )}
                {message.originalError && (
                  <details className="pl-4">
                    <summary className="text-xs text-gray-500 cursor-pointer hover:text-gray-400">Raw Error</summary>
                    <pre className="text-xs text-gray-500 mt-1 whitespace-pre-wrap break-words max-h-32 overflow-y-auto">
                      {message.originalError}
                    </pre>
                  </details>
                )}
              </div>
            )}
            {message.type === 'thinking' && (
              <div className="thinking-indicator flex items-center gap-2 px-3 py-2 text-sm w-fit">
                <Brain size={16} style={{ color: 'var(--brand)' }} />
                <span style={{ color: 'var(--brand)' }}>Thinking...</span>
              </div>
            )}
            {message.type === 'input_request' && message.options && (
               <div className="grid grid-cols-2 gap-2 mt-2">
                 {message.options.map((opt, i) => (
                   <button 
                     key={i}
                     onClick={() => onSendMessage(opt)}
                     disabled={!isWaitingForInput || index !== messages.length - 1}
                     className="px-3 py-2 text-sm bg-primary/20 hover:bg-primary/30 text-primary border border-primary/30 rounded transition-colors text-left truncate disabled:opacity-50 disabled:cursor-not-allowed disabled:hover:bg-primary/20"
                   >
                     {opt}
                   </button>
                 ))}
               </div>
            )}
          </div>
        ))}
        
        {/* Thinking Indicator - shows when agent is running but not waiting for input */}
        {hasActiveSession && !isWaitingForInput && messages.length > 0 && messages[messages.length - 1].type !== 'flow_complete' && (
          <div className="thinking-indicator flex items-center gap-3 px-4 py-3 animate-pulse">
            <Loader size={18} className="animate-spin" style={{ color: 'var(--brand)' }} />
            <span className="text-sm" style={{ color: 'var(--brand)' }}>Thinking...</span>
          </div>
        )}
        
        {/* Restart Flow Button */}
        {messages.length > 0 && messages[messages.length - 1].type === 'flow_complete' && (
          <div className="flex justify-center mt-6 mb-4">
            <button
              onClick={onStartRun}
              className="flex items-center gap-2 px-6 py-3 bg-gradient-to-r from-primary to-[var(--brand-strong)] hover:opacity-90 text-primary-foreground font-medium rounded-xl shadow-lg transition-all hover:scale-105"
            >
              <RotateCcw size={18} />
              Start Again
            </button>
          </div>
        )}
      </div>

      {/* Input */}
      <form onSubmit={handleSubmit} className="p-4" style={{ borderTop: '1px solid var(--border-color)' }}>
        {(() => {
          // Check if last message has selection options (must click buttons, not type)
          const lastMessage = messages[messages.length - 1]
          const hasSelectionOptions = lastMessage?.type === 'input_request' && (lastMessage?.options?.length ?? 0) > 0
          const canType = isWaitingForInput && !hasSelectionOptions
          
          return (
            <div className="flex gap-3">
              {/* Stop Button - only when session is active */}
              {hasActiveSession && (
                <button
                  type="button"
                  onClick={onStop}
                  className="px-4 py-3 bg-red-500 hover:bg-red-600 text-white rounded-lg transition-colors flex items-center gap-2"
                  title="Stop Execution"
                >
                  <Square size={18} />
                </button>
              )}
              <div className="relative flex-1">
                <input
                  ref={inputRef}
                  type="text"
                  value={input}
                  onChange={(e) => setInput(e.target.value)}
                  disabled={!canType}
                  placeholder={hasSelectionOptions ? "Click an option above" : (isWaitingForInput ? "Type your response..." : "Agent is thinking...")}
                  className="w-full px-4 py-3 rounded-lg focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-70 disabled:cursor-not-allowed transition-all"
                  style={{ 
                    background: 'var(--bg-tertiary)', 
                    color: 'var(--text-primary)',
                    border: '1px solid var(--border-color)'
                  }}
                />
              </div>
              <button
                type="submit"
                disabled={!canType || !input.trim()}
                className="px-4 py-3 bg-primary hover:bg-primary/90 text-primary-foreground rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
              >
                <Send size={20} />
              </button>
            </div>
          )
        })()}
      </form>
    </div>
  )
}
