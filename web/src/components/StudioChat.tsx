import { useState, useEffect, useRef, useCallback, useMemo } from 'react'
import { Send, Plus, Trash2, MessageSquare, ChevronRight, ChevronDown, Loader, Square, Copy, Check, Code, RotateCcw, Clock, Search, Users, Info, FileText, Globe, ListChecks, AppWindow, Brain, Paperclip, X } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'

import { markdownComponents } from './chat/markdownComponents'
import { fetchSessions, fetchSessionHistory, deleteSession, connectChat, stopChat, fetchSessionStatus, connectChatStream, fetchNetworkDenials, fetchSessionModelStatus, patchSessionModel, fetchAvailableProviders } from '../api/studioChat'
import type { ChatSession, AttachmentPayload, SessionModelStatus } from '../api/studioChat'
import { startFleetSession, connectFleetStream, sendFleetMessage, stopFleetSession, fetchFleetSessions } from '../api/fleetChat'
import type { FleetSession } from '../api/fleetChat'
import HomePage from './HomePage'
import type { FleetMessageItem, ChatMsg, FleetInfo, FleetStateInfo, DeferredPrompt, FleetExecutionMessage, FleetEvent, AgentMessage, ToolResultMessage, BrowserHandoffMessage, SubTaskExecutionMessage, SubTaskEvent, SubTaskInfo, PlanMessage, PlanStepInfo, SessionArtifact, ArtifactMessage, AppPreviewMessage, AppSavedMessage, DistillPreviewMessage, DistillSavedMessage, TutorialBlueprintPreviewMessage, TutorialBlueprintApprovedMessage, TutorialSceneSlideshowMessage, UserMessage, AttachmentInfo, NetworkDenialMessage, ImageMessage } from './chat/chatTypes'
import { buildActivityRenderIndex, deriveLiveStreamStatus } from './chat/toolActivity'
import ToolActivityBlock from './chat/ToolActivityBlock'
import { getAgentColor } from './chat/chatTypes'
import FleetStartDialog from './chat/FleetStartDialog'
import FleetTemplatePicker from './chat/FleetTemplatePicker'
import FleetExecutionPanel from './chat/FleetExecutionPanel'
import TaskPlanPanel from './chat/TaskPlanPanel'
import PlanPanel from './chat/PlanPanel'
import FilePanel from './chat/FilePanel'
import TodoPanel from './chat/TodoPanel'
import AppsPanel from './chat/AppsPanel'
import HarnessPanel from './chat/HarnessPanel'
import HarnessPlaceholder from './chat/HarnessPlaceholder'
import {
  deriveLatestHarness,
  harnessFocusEquals,
  resolveHarnessFocus,
  SIDEBAR_COLLAPSED_WIDTH,
  SIDEBAR_EXPANDED_WIDTH,
  type HarnessFocus,
} from './chat/chatHarness'
import SessionMemoryPanel from './SessionMemoryPanel'
import SessionSidebar from './chat/SessionSidebar'
import { listSessionMemories } from '../api/platform'
import UsagePopover from './chat/UsagePopover'
import type { TokenUsage } from './chat/UsagePopover'
import ArtifactCard from './chat/ArtifactCard'
import AppCodeIndicator from './chat/AppCodeIndicator'
import NetworkDenialPrompt from './chat/NetworkDenialPrompt'
import ModelCredentialBanner from './chat/ModelCredentialBanner'
import SessionModelPicker from './chat/SessionModelPicker'
import PreChatModelPicker from './chat/PreChatModelPicker'
import { fileTypeFromFileName } from '../utils/artifactMedia'

function normalizeBlueprintScenes(raw: unknown): TutorialBlueprintPreviewMessage['scenes'] {
  if (!Array.isArray(raw)) return []
  return raw.map((item: any) => ({
    id: item?.id || '',
    title: item?.title || '',
    voiceover: item?.voiceover || '',
    visual_kind: item?.visual_kind || item?.visual?.kind || '',
    visual_description: item?.visual_description || item?.visual?.description || '',
    duration_hint_s: item?.duration_hint_s || item?.durationHintS || undefined,
  }))
}

function normalizeTutorialScenes(raw: unknown): TutorialSceneSlideshowMessage['scenes'] {
  if (!Array.isArray(raw)) return []
  return raw.map((item: any) => ({
    id: item?.id || '',
    voiceover: item?.voiceover || '',
    narration: item?.narration || '',
    visual_kind: item?.visual_kind || 'screen',
    visual_description: item?.visual_description || '',
    hold_ms: item?.hold_ms || undefined,
    path: item?.path || '',
    duration_seconds: item?.duration_seconds || undefined,
  }))
}

function slideshowVideoPaths(messages: ChatMsg[]): Set<string> {
  const paths = new Set<string>()
  for (const m of messages) {
    if (m.type !== 'tutorial_scene_slideshow') continue
    for (const sc of (m as TutorialSceneSlideshowMessage).scenes || []) {
      if (sc.path) paths.add(sc.path)
    }
  }
  return paths
}

// Extended ChatSession with optional fleet fields coming from the sidebar
interface SidebarSession extends ChatSession {
  fleetKey?: string
  fleetName?: string
}

// Web search tool name patterns (matches tool results from search MCP servers)
const WEB_SEARCH_PATTERNS = ['search', 'web_search', 'web-search', 'web_fetch', 'scrape', 'crawl', 'extract', 'browse']

// Extract URLs from a tool result value (deeply searches objects/arrays/strings)
function extractUrlsFromResult(result: unknown): string[] {
  const urls: string[] = []
  const urlRegex = /https?:\/\/[^\s"',\]}>]+/g

  function walk(val: unknown) {
    if (typeof val === 'string') {
      const matches = val.match(urlRegex)
      if (matches) urls.push(...matches)
    } else if (Array.isArray(val)) {
      val.forEach(walk)
    } else if (val && typeof val === 'object') {
      Object.values(val as Record<string, unknown>).forEach(walk)
    }
  }
  walk(result)

  // Deduplicate and filter out API/internal URLs
  return [...new Set(urls)].filter(u =>
    !u.includes('api.tavily') &&
    !u.includes('api.firecrawl') &&
    !u.includes('localhost') &&
    !u.includes('127.0.0.1')
  )
}

// Collect web source URLs from tool_result messages preceding an agent message
function collectSourceUrls(messages: ChatMsg[], agentIndex: number): string[] {
  const urls: string[] = []
  // Walk backwards from the agent message, collecting URLs from web search tool results
  for (let i = agentIndex - 1; i >= 0; i--) {
    const m = messages[i]
    // Stop at user messages or other agent messages (only look at the current "turn")
    if (m.type === 'user' || m.type === 'agent') break

    if (m.type === 'tool_result') {
      const tr = m as ToolResultMessage
      const toolName = String(tr.toolName || '').toLowerCase()
      const isWebTool = WEB_SEARCH_PATTERNS.some(p => toolName.includes(p))
      if (isWebTool && tr.toolResult) {
        urls.push(...extractUrlsFromResult(tr.toolResult))
      }
    }
  }
  return [...new Set(urls)]
}

// Inline citation pill component
function SourceCitations({ urls }: { urls: string[] }) {
  const [expanded, setExpanded] = useState(false)

  if (urls.length === 0) return null

  // Extract domain for display
  const getDomain = (url: string) => {
    try { return new URL(url).hostname.replace('www.', '') }
    catch { return url }
  }

  // Favicon URL via Google's favicon service
  const getFavicon = (url: string) => {
    try {
      const hostname = new URL(url).hostname
      return `https://www.google.com/s2/favicons?domain=${hostname}&sz=16`
    } catch { return '' }
  }

  return (
    <div className="mt-2">
      <button
        onClick={() => setExpanded(!expanded)}
        className="source-citation-pill flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs cursor-pointer transition-colors hover:opacity-80"
        style={{
          background: 'var(--bg-tertiary)',
          border: '1px solid var(--border-color)',
          color: 'var(--text-secondary)',
        }}
      >
        {/* Stacked favicons (show up to 3) */}
        <span className="flex items-center -space-x-1">
          {urls.slice(0, 3).map((url, i) => (
            <img
              key={i}
              src={getFavicon(url)}
              alt=""
              className="w-4 h-4 rounded-full border"
              style={{ borderColor: 'var(--bg-tertiary)' }}
              onError={(e) => { (e.target as HTMLImageElement).style.display = 'none' }}
            />
          ))}
        </span>
        <Globe size={12} style={{ color: 'var(--text-muted)' }} />
        <span>{urls.length} source{urls.length !== 1 ? 's' : ''}</span>
      </button>

      {expanded && (
        <div
          className="mt-1.5 p-2 rounded-lg space-y-1 text-xs"
          style={{
            background: 'var(--bg-tertiary)',
            border: '1px solid var(--border-color)',
          }}
        >
          {urls.map((url, i) => (
            <div key={i} className="flex items-center gap-2">
              <img
                src={getFavicon(url)}
                alt=""
                className="w-3.5 h-3.5 rounded shrink-0"
                onError={(e) => { (e.target as HTMLImageElement).style.display = 'none' }}
              />
              <a
                href={url}
                target="_blank"
                rel="noopener noreferrer"
                className="truncate hover:underline"
                style={{ color: 'var(--brand)' }}
              >
                {getDomain(url)}
              </a>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// --- Attachment constants (outside component to avoid ESLint deps warnings) ---
const MAX_ATTACHMENTS = 10
const MAX_FILE_SIZE = 10 * 1024 * 1024 // 10 MB
const ACCEPTED_MIME_TYPES = [
  'text/plain', 'text/html', 'text/css', 'text/csv', 'text/xml', 'text/markdown',
  'application/json', 'application/xml', 'application/yaml', 'application/pdf',
  'application/x-yaml',
  'image/png', 'image/jpeg', 'image/gif', 'image/webp',
]
const ACCEPTED_EXTENSIONS = '.txt,.html,.htm,.css,.csv,.xml,.md,.json,.yaml,.yml,.log,.pdf,.png,.jpg,.jpeg,.gif,.webp'

interface PendingAttachment {
  id: string
  file: File
  preview: string | null  // data URL for image previews
  mimeType: string
  name: string
}

export default function StudioChat({ theme, initialSessionId, pendingChatMessage, onPendingChatMessageConsumed, onSessionChange, userDisplayName }: { theme: string; initialSessionId?: string | null; pendingChatMessage?: { message: string; systemContext?: string } | null; onPendingChatMessageConsumed?: () => void; onSessionChange?: (sessionId: string | null) => void; /** Shown on empty-chat hero as "Hello, {firstName}" */ userDisplayName?: string | null }) {
  // Session state
  const [sessions, setSessions] = useState<SidebarSession[]>([])
  const [activeSessionId, setActiveSessionId] = useState<string | null>(initialSessionId || null)
  const [isLoadingSessions, setIsLoadingSessions] = useState(true)
  const [isLoadingHistory, setIsLoadingHistory] = useState(false)
  const [sessionFilter, setSessionFilter] = useState('')

  // Model picker state
  const [modelStatus, setModelStatus] = useState<SessionModelStatus | null>(null)
  const [modelPickerOpenSignal, setModelPickerOpenSignal] = useState(0)
  const [preChatProvider, setPreChatProvider] = useState('')
  const [preChatModel, setPreChatModel] = useState('')
  const [preChatProviders, setPreChatProviders] = useState<string[]>([])

  // Chat state
  const [messages, setMessages] = useState<ChatMsg[]>([])
  const [input, setInput] = useState('')
  const [isStreaming, setIsStreaming] = useState(false)
  const [activeAppId, setActiveAppId] = useState<string | null>(null) // ID of app currently being refined

  // Fleet state
  const [isFleetMode, setIsFleetMode] = useState(false)
  const [fleetSessionId, setFleetSessionId] = useState<string | null>(null)
  const [fleetInfo, setFleetInfo] = useState<FleetInfo | null>(null) // { fleet_key, fleet_name, agents }
  const [fleetState, setFleetState] = useState<FleetStateInfo | null>(null) // { state, active_agent }
  const [showFleetDialog, setShowFleetDialog] = useState(false)
  const [fleetDialogMessage, setFleetDialogMessage] = useState('') // pre-populated from /fleet command
  const [showTemplatePicker, setShowTemplatePicker] = useState(false) // /fleet-plan without template key
  const [pendingFleetPlanPrompt, setPendingFleetPlanPrompt] = useState<DeferredPrompt | null>(null) // deferred plan creation message
  const [pendingDrillPrompt, setPendingDrillPrompt] = useState<DeferredPrompt | null>(null) // deferred drill creation message
  const [activeWizardContext, setActiveWizardContext] = useState<string | null>(null) // persisted wizard system prompt for multi-turn sessions
  const [activePinnedToolGroups, setActivePinnedToolGroups] = useState<string[] | null>(null) // persisted pinned tool groups for wizard sessions

  // Slash command popup
  const [showSlashPopup, setShowSlashPopup] = useState(false)
  const [slashFilter, setSlashFilter] = useState('')
  const [slashIndex, setSlashIndex] = useState(0)

  // UI state
  const [copiedIndex, setCopiedIndex] = useState<number | null>(null)
  const [rawViewIndices, setRawViewIndices] = useState<Set<number>>(new Set())
  const [expandedCodeIndices, setExpandedCodeIndices] = useState<Set<number>>(new Set())
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)

  // File panel state
  const [filePanelOpen, setFilePanelOpen] = useState(false)
  const [sessionArtifacts, setSessionArtifacts] = useState<SessionArtifact[]>([])
  const [filePanelInitialPath, setFilePanelInitialPath] = useState<string | null>(null)

  // Todo panel state
  const [todoPanelOpen, setTodoPanelOpen] = useState(false)

  // Apps panel state
  const [appsPanelOpen, setAppsPanelOpen] = useState(false)

  // Right-hand harness panel (Apps / Reports / Flow draft / Tutorial blueprint)
  const [harnessOpen, setHarnessOpen] = useState(false)
  const [manualHarnessFocus, setManualHarnessFocus] = useState<HarnessFocus | null>(null)
  const prevHarnessIndexRef = useRef(-1)
  const prevSidebarCollapsedRef = useRef(false)
  const harnessAutoCollapsedRef = useRef(false)
  const chatLayoutRef = useRef<HTMLDivElement>(null)
  const [chatLayoutWidth, setChatLayoutWidth] = useState(0)

  // Token usage state (from API-reported UsageMetadata)
  const [tokenUsage, setTokenUsage] = useState<TokenUsage>({ inputTokens: 0, outputTokens: 0, totalTokens: 0 })
  const [sessionStartTime, setSessionStartTime] = useState<number | null>(null)
  const [hasSessionMemories, setHasSessionMemories] = useState(false) // whether session has any memories
  const [showMemoryPanel, setShowMemoryPanel] = useState(false)
  const [isConsolidatingMemories, setIsConsolidatingMemories] = useState(false) // true while reflector+extraction runs after done

  // Refs
  const scrollRef = useRef<HTMLDivElement | null>(null)
  const inputRef = useRef<HTMLTextAreaElement | null>(null)
  const abortRef = useRef<AbortController | null>(null)
  const streamingTextRef = useRef('')
  const isNearBottomRef = useRef(true)
  const [showScrollButton, setShowScrollButton] = useState(false)
  const fileInputRef = useRef<HTMLInputElement | null>(null)

  // Attachment state
  const [attachments, setAttachments] = useState<PendingAttachment[]>([])
  const [isDragOver, setIsDragOver] = useState(false)

  const slashCommands = useMemo(() => [
    { cmd: '/help', desc: 'Show available commands' },
    { cmd: '/status', desc: 'Show provider, model, and tools info' },
    { cmd: '/new', desc: 'Start a fresh conversation' },
    { cmd: '/compact', desc: 'Show context window usage' },
    { cmd: '/distill', desc: 'Distill last task into a flow' },
    { cmd: '/fleet', desc: 'Start a fleet-based task with specialized agents' },
    { cmd: '/fleet-plan', desc: 'Create a reusable fleet plan' },
    { cmd: '/drill', desc: 'Create a drill suite with guided wizard' },
    { cmd: '/drill-add', desc: 'Add new drills to an existing suite' },
    { cmd: '/tutorial-drill', desc: 'Create a tutorial (narrated) drill with guided wizard' },
    { cmd: '/tutorial-drill-add', desc: 'Add tutorial drills to an existing tutorial suite' },
  ], [])

  // Pre-compute which artifact paths should be rendered as embedded file viewers
  // inside the last agent message bubble instead of as standalone inline ArtifactCards.
  //
  // Markdown reports use a narrow three-signal gate (do NOT widen — see AGENTS.md):
  //   1. produced in the *last turn*, AND
  //   2. fileType === 'Markdown', AND
  //   3. isReport === true (matching ```astonish-report``` fence).
  // Last-turn Video artifacts (e.g. browser_stop_recording) also embed so the
  // user gets an inline player — separate from the markdown report contract.
  const embeddedArtifactPaths = useMemo(() => {
    const paths = new Set<string>()
    if (isStreaming || sessionArtifacts.length === 0) return paths
    const slideshowPaths = slideshowVideoPaths(messages)
    // Find the last user message index — artifacts before it belong to earlier turns
    let lastUserIdx = -1
    for (let i = messages.length - 1; i >= 0; i--) {
      if (messages[i].type === 'user') { lastUserIdx = i; break }
    }
    // Collect artifact paths that appear after the last user message (current turn)
    const lastTurnArtifactPaths = new Set<string>()
    for (let i = Math.max(lastUserIdx + 1, 0); i < messages.length; i++) {
      if (messages[i].type === 'artifact') {
        lastTurnArtifactPaths.add((messages[i] as ArtifactMessage).path)
      }
    }
    // Only embed artifacts that belong to the last turn
    if (lastTurnArtifactPaths.size === 0) return paths
    for (let i = messages.length - 1; i >= 0; i--) {
      if (messages[i].type === 'agent' && !(messages[i] as AgentMessage)._streaming) {
        for (const a of sessionArtifacts) {
          if (!lastTurnArtifactPaths.has(a.path)) continue
          if (slideshowPaths.has(a.path)) continue
          if (a.fileType === 'Video') {
            paths.add(a.path)
            continue
          }
          if (a.fileType !== 'Markdown') continue
          if (!a.isReport) continue
          paths.add(a.path)
        }
        break
      }
    }
    return paths
  }, [messages, isStreaming, sessionArtifacts])

  // Markdown reports that belong in the harness panel (same three-signal gate).
  const reportHarnessPaths = useMemo(() => {
    const paths = new Set<string>()
    for (const p of embeddedArtifactPaths) {
      const a = sessionArtifacts.find(sa => sa.path === p)
      if (a && a.fileType === 'Markdown' && a.isReport) paths.add(p)
    }
    return paths
  }, [embeddedArtifactPaths, sessionArtifacts])

  // Last-turn videos for the harness (embeddedArtifactPaths already excludes slideshow-owned).
  const videoHarnessPaths = useMemo(() => {
    const paths = new Set<string>()
    for (const p of embeddedArtifactPaths) {
      const a = sessionArtifacts.find(sa => sa.path === p)
      if (a && a.fileType === 'Video') paths.add(p)
    }
    return paths
  }, [embeddedArtifactPaths, sessionArtifacts])

  const latestHarness = useMemo(
    () => deriveLatestHarness(messages, reportHarnessPaths, videoHarnessPaths),
    [messages, reportHarnessPaths, videoHarnessPaths],
  )

  const effectiveHarnessFocus = useMemo(
    () => resolveHarnessFocus(latestHarness, manualHarnessFocus),
    [latestHarness, manualHarnessFocus],
  )

  const openHarness = useCallback((focus: HarnessFocus) => {
    setManualHarnessFocus(focus)
    setHarnessOpen(true)
    setFilePanelOpen(false)
    setTodoPanelOpen(false)
    setAppsPanelOpen(false)
    if (!harnessAutoCollapsedRef.current) {
      prevSidebarCollapsedRef.current = sidebarCollapsed
      harnessAutoCollapsedRef.current = true
      setSidebarCollapsed(true)
    }
  }, [sidebarCollapsed])

  const closeHarness = useCallback(() => {
    setHarnessOpen(false)
    setManualHarnessFocus(null)
    if (harnessAutoCollapsedRef.current) {
      setSidebarCollapsed(prevSidebarCollapsedRef.current)
      harnessAutoCollapsedRef.current = false
    }
  }, [])

  // Auto-open harness when a newer harness item is emitted; collapse session sidebar.
  useEffect(() => {
    if (!latestHarness) return
    if (latestHarness.messageIndex <= prevHarnessIndexRef.current) return
    prevHarnessIndexRef.current = latestHarness.messageIndex
    setManualHarnessFocus(null)
    setHarnessOpen(true)
    setFilePanelOpen(false)
    setTodoPanelOpen(false)
    setAppsPanelOpen(false)
    if (!harnessAutoCollapsedRef.current) {
      prevSidebarCollapsedRef.current = sidebarCollapsed
      harnessAutoCollapsedRef.current = true
      setSidebarCollapsed(true)
    }
  // Intentionally omit sidebarCollapsed from deps — we only snapshot it when first opening.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [latestHarness])

  // Reset harness tracking when the session changes.
  useEffect(() => {
    prevHarnessIndexRef.current = -1
    setHarnessOpen(false)
    setManualHarnessFocus(null)
    harnessAutoCollapsedRef.current = false
  }, [activeSessionId])

  // Measure the StudioChat flex row for harness width clamping.
  useEffect(() => {
    const el = chatLayoutRef.current
    if (!el) return
    const update = () => setChatLayoutWidth(el.clientWidth)
    update()
    if (typeof ResizeObserver !== 'undefined') {
      const ro = new ResizeObserver(update)
      ro.observe(el)
      return () => ro.disconnect()
    }
    window.addEventListener('resize', update)
    return () => window.removeEventListener('resize', update)
  }, [])

  // Latest plan message that actually has steps. The Todo button/panel are
  // shown only when this is non-null. A plan may be persisted without steps
  // (e.g. an announce_plan with empty/omitted steps), in which case there is
  // nothing to show — and `steps` may even be undefined on loaded sessions.
  const latestPlanWithSteps = useMemo(() => {
    for (let i = messages.length - 1; i >= 0; i--) {
      const m = messages[i]
      if (m.type === 'plan') {
        const plan = m as PlanMessage
        return (plan.steps?.length ?? 0) > 0 ? plan : null
      }
    }
    return null
  }, [messages])

  // Wrapper to keep URL in sync with active session
  const changeSession = useCallback((sessionId: string | null, { userInitiated = false, skipModelFetch = false } = {}) => {
    setActiveSessionId(sessionId)
    setActiveAppId(null)
    setAppsPanelOpen(false)
    setHasSessionMemories(false)
    if (userInitiated) {
      setActiveWizardContext(null)
    }
    if (onSessionChange) onSessionChange(sessionId)
    if (sessionId) {
      if (!skipModelFetch) {
        fetchSessionModelStatus(sessionId).then(setModelStatus).catch(() => setModelStatus(null))
      }
    } else {
      setModelStatus(null)
    }
  }, [onSessionChange])

  const connectToFleetStream = useCallback((sessionId: string) => {
    const controller = connectFleetStream({
      sessionId,
      onEvent: (eventType, data) => {
        switch (eventType) {
          case 'fleet_session':
            setFleetInfo({ fleet_key: data.fleet_key as string, fleet_name: data.fleet_name as string, agents: data.agents })
            break

          case 'fleet_message':
            setMessages((prev: ChatMsg[]) => {
              // Deduplicate by message ID
              if (data.id && prev.some(m => (m as FleetMessageItem).id === data.id)) {
                return prev
              }
              // Skip human messages from the stream since we add them optimistically.
              // Match by sender + text to detect the duplicate.
              if (data.sender === 'customer' && prev.some(m => (m as FleetMessageItem).sender === 'customer' && (m as FleetMessageItem).text === data.text && !(m as FleetMessageItem).id)) {
                // Replace the optimistic message (no id) with the server version (has id)
                return prev.map(m =>
                  (m as FleetMessageItem).sender === 'customer' && (m as FleetMessageItem).text === data.text && !(m as FleetMessageItem).id
                    ? { ...m, id: data.id, timestamp: (data.timestamp as number) || (m as FleetMessageItem).timestamp } as ChatMsg
                    : m
                )
              }
              return [...prev, { type: 'fleet_message', ...data, timestamp: (data.timestamp as number) || Date.now() } as ChatMsg]
            })
            break

          case 'fleet_state':
            setFleetState({ state: data.state as string, active_agent: data.active_agent as string })
            break

          case 'fleet_agent_started':
          case 'fleet_agent_finished':
          case 'fleet_task_posted':
          case 'fleet_task_claimed':
          case 'fleet_task_completed':
          case 'fleet_mailbox_delivered':
            setMessages((prev: ChatMsg[]) => {
              const event: FleetEvent = {
                ...data,
                type: eventType,
                timestamp: Date.now(),
              }
              const existing = prev.find(m => m.type === 'fleet_execution') as FleetExecutionMessage | undefined
              if (existing) {
                const maxParallelAgents = Number(data.max_parallel_agents || data.maxParallelAgents || existing.maxParallelAgents || 1)
                return prev.map(m => m.type === 'fleet_execution'
                  ? { ...existing, events: [...existing.events, event], maxParallelAgents }
                  : m)
              }
              return [...prev, {
                type: 'fleet_execution',
                events: [event],
                currentPhase: (data.phase as string) || null,
                currentAgent: (data.agent as string) || null,
                status: 'running',
                maxParallelAgents: Number(data.max_parallel_agents || data.maxParallelAgents || 1),
              } as FleetExecutionMessage]
            })
            break

          case 'fleet_done':
            setIsStreaming(false)
            break

          case 'error':
            setMessages((prev: ChatMsg[]) => [...prev, { type: 'error', content: (data.error as string) || 'Unknown error' }])
            break

          default:
            break
        }
      },
      onError: (err) => {
        console.error('Fleet stream error:', err)
        setMessages((prev: ChatMsg[]) => [...prev, { type: 'error', content: err.message }])
        setIsStreaming(false)
      },
      onDone: () => {
        setIsStreaming(false)
      },
    })

    abortRef.current = controller
    return controller
  }, [])

  // Load sessions on mount (and initial session if URL specifies one)
  // Also check for active fleet sessions that we should reconnect to.
  // Load provider names once for pre-chat picker and as a fallback for the
  // in-session picker when model-status returns an empty list.
  useEffect(() => {
    fetchAvailableProviders().then(setPreChatProviders).catch(() => setPreChatProviders([]))
  }, [])

  useEffect(() => {
    loadSessions()

    const init = async () => {
      if (initialSessionId) {
        // Check if this is an active fleet session
        try {
          const data = await fetchFleetSessions()
          const activeFleet = (data.sessions || []).find((s: FleetSession) => s.id === initialSessionId)
          if (activeFleet) {
            setIsFleetMode(true)
            setFleetSessionId(initialSessionId)
            setFleetState({ state: activeFleet.state, active_agent: activeFleet.active_agent })
            setMessages([])
            setIsStreaming(true)
            changeSession(initialSessionId)
            connectToFleetStream(initialSessionId)
            return
          }
        } catch {
          // fetchFleetSessions may fail if fleet system not initialized; that's ok
        }
        // Check if this session has an active background chat runner
        try {
          const status = await fetchSessionStatus(initialSessionId)
          if (status.running) {
            changeSession(initialSessionId)
            reconnectToChatRunner(initialSessionId)
            return
          }
        } catch {
          // Status endpoint may fail; fall through to history
        }
        // Not a fleet session or active runner, load as regular history
        loadSessionHistory(initialSessionId)
      }
    }
    init()
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // React to URL-driven session changes after mount (e.g., user edits URL,
  // navigates to a deleted session, or session is removed from sidebar).
  const mountedRef = useRef(false)
  useEffect(() => {
    if (!mountedRef.current) {
      mountedRef.current = true
      return // Skip on mount — handled by the init effect above
    }
    const sid = initialSessionId || null
    if (sid === activeSessionId) return // No change

    if (!sid) {
      // URL changed to /#/chat (no session) — reset to new chat
      changeSession(null)
      setMessages([])
      return
    }

    // URL changed to a different session — try to load it
    loadSessionHistory(sid)
  }, [initialSessionId]) // eslint-disable-line react-hooks/exhaustive-deps

  // Auto-scroll on new messages — only if user is near the bottom
  useEffect(() => {
    if (scrollRef.current && isNearBottomRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight
    }
    // Show scroll button when not at bottom and there are messages
    if (!isNearBottomRef.current && messages.length > 0) {
      setShowScrollButton(true)
    }
  }, [messages])

  // Track scroll position to detect if user scrolled up
  useEffect(() => {
    const el = scrollRef.current
    if (!el) return
    const handleScroll = () => {
      const threshold = 80
      const atBottom = el.scrollTop + el.clientHeight >= el.scrollHeight - threshold
      isNearBottomRef.current = atBottom
      if (atBottom) {
        setShowScrollButton(false)
      }
    }
    el.addEventListener('scroll', handleScroll, { passive: true })
    return () => el.removeEventListener('scroll', handleScroll)
  }, []) // Re-attaches if scrollRef changes — but ref is stable

  const scrollToBottom = useCallback(() => {
    if (scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight
      isNearBottomRef.current = true
      setShowScrollButton(false)
    }
  }, [])

  // Focus input when not streaming
  useEffect(() => {
    if (!isStreaming && inputRef.current) {
      inputRef.current.focus()
    }
  }, [isStreaming, activeSessionId])

  const loadSessions = async () => {
    try {
      setIsLoadingSessions(true)
      const data = await fetchSessions()
      setSessions(Array.isArray(data) ? data : [])
    } catch (err: any) {
      console.error('Failed to load sessions:', err)
      setSessions([])
    } finally {
      setIsLoadingSessions(false)
    }
  }

  const loadSessionHistory = useCallback(async (sessionId: string) => {
    // Ensure the toolbar model picker has status whenever history is loaded
    // (including hard refresh on /#/chat/:id, which never goes through changeSession).
    fetchSessionModelStatus(sessionId).then(setModelStatus).catch(() => setModelStatus(null))
    try {
      setIsLoadingHistory(true)
      const data = await fetchSessionHistory(sessionId)
      const dataAny = data as Record<string, any>

      // Extract artifacts from the session detail response
      if (dataAny.artifacts && Array.isArray(dataAny.artifacts)) {
        setSessionArtifacts(dataAny.artifacts as SessionArtifact[])
      } else {
        setSessionArtifacts([])
      }

      // Restore cumulative token usage from the persisted session data.
      // The backend sums UsageMetadata across all LLM responses in the transcript.
      if (dataAny.totalUsage) {
        setTokenUsage({
          inputTokens: dataAny.totalUsage.inputTokens || 0,
          outputTokens: dataAny.totalUsage.outputTokens || 0,
          totalTokens: dataAny.totalUsage.totalTokens || 0,
        })
      } else {
        setTokenUsage({ inputTokens: 0, outputTokens: 0, totalTokens: 0 })
      }

      // If the response includes fleet messages, convert them to the fleet_message format
      if (dataAny.fleetMessages && dataAny.fleetMessages.length > 0) {
        const fleetMsgs: ChatMsg[] = dataAny.fleetMessages.map((m: any) => ({
          type: 'fleet_message' as const,
          id: m.id,
          sender: m.sender,
          text: m.text,
          mentions: m.mentions,
          timestamp: m.timestamp ? new Date(m.timestamp).getTime() : Date.now(),
          metadata: m.metadata,
        }))
        setMessages(fleetMsgs)
      } else {
        // Map API messages to frontend ChatMsg types.
        // The API uses StudioMessage with generic fields (content, toolName);
        // some message types need field remapping for the frontend types.
        const apiMessages = (data.messages || []) as Array<Record<string, any>>
        const mapped: ChatMsg[] = apiMessages.map(m => {
          if (m.type === 'artifact' && m.content) {
            return { type: 'artifact', path: m.content, toolName: m.toolName || 'write_file' } as ArtifactMessage
          }
          if (m.type === 'distill_preview') {
            return {
              type: 'distill_preview',
              yaml: m.yaml || '',
              flowName: m.flowName || '',
              description: m.description || '',
              tags: m.tags || [],
              explanation: m.explanation || '',
            } as DistillPreviewMessage
          }
          if (m.type === 'distill_saved') {
            return {
              type: 'distill_saved',
              filePath: m.filePath || '',
              runCommand: m.runCommand || '',
            } as DistillSavedMessage
          }
          if (m.type === 'tutorial_blueprint_preview') {
            return {
              type: 'tutorial_blueprint_preview',
              title: m.blueprintTitle || m.title || '',
              suite: m.blueprintSuite || m.suite || '',
              blueprintYaml: m.blueprintYaml || '',
              scenes: normalizeBlueprintScenes(m.blueprintScenes || m.scenes),
            } as TutorialBlueprintPreviewMessage
          }
          if (m.type === 'tutorial_blueprint_approved') {
            return {
              type: 'tutorial_blueprint_approved',
              title: m.blueprintTitle || m.title || '',
              suite: m.blueprintSuite || m.suite || '',
              blueprintYaml: m.blueprintYaml || '',
              drillYaml: m.drillYaml || '',
              drillName: m.drillName || '',
              content: m.content || '',
            } as TutorialBlueprintApprovedMessage
          }
          if (m.type === 'tutorial_scene_slideshow') {
            return {
              type: 'tutorial_scene_slideshow',
              title: m.tutorialTitle || m.title || '',
              suite: m.tutorialSuite || m.suite || '',
              drill: m.tutorialDrill || m.drill || '',
              manifestPath: m.manifestPath || '',
              scenes: normalizeTutorialScenes(m.tutorialScenes || m.scenes),
            } as TutorialSceneSlideshowMessage
          }
          if (m.type === 'app_preview') {
            return {
              type: 'app_preview',
              code: m.code || '',
              title: m.title || 'App Preview',
              description: m.description || '',
              version: m.version || 1,
              appId: m.appId || undefined,
            } as AppPreviewMessage
          }
          if (m.type === 'flow_output') {
            return { type: 'agent', content: m.content || '' } as AgentMessage
          }
          if (m.type === 'image' && m.data && m.mimeType) {
            return { type: 'image', data: m.data, mimeType: m.mimeType } as ImageMessage
          }
          if (m.type === 'plan') {
            return { type: 'plan', goal: m.goal || '', steps: m.steps ?? [] } as PlanMessage
          }
          return m as unknown as ChatMsg
        })
        setMessages(mapped)
        // Restore active app state from session history
        const appPreviews = mapped.filter((m): m is AppPreviewMessage => m.type === 'app_preview')
        if (appPreviews.length > 0) {
          const lastApp = appPreviews[appPreviews.length - 1]
          if (lastApp.appId) setActiveAppId(lastApp.appId)
        }
      }
    } catch (err: any) {
      console.error('Failed to load session history:', err)
      setMessages([])
      setSessionArtifacts([])
      // Redirect to clean chat when session is invalid or deleted
      changeSession(null)
    } finally {
      setIsLoadingHistory(false)
    }

    // Check if this session has any memories (async, non-blocking)
    listSessionMemories(sessionId)
      .then(res => {
        if (res.memories && res.memories.length > 0) {
          setHasSessionMemories(true)
        }
      })
      .catch(() => { /* ignore */ })
  }, [changeSession])

  // Reconnect to an active background chat runner. The runner streams all
  // buffered events (catch-up) followed by live events. This uses the same
  // event handler as sendMessage so all event types are handled identically.
  const reconnectToChatRunner = useCallback((sessionId: string) => {
    setMessages([])
    setSessionArtifacts([])
    setIsStreaming(true)
    setSessionStartTime(Date.now())
    streamingTextRef.current = ''

    const controller = connectChatStream({
      sessionId,
      onEvent: (eventType, data) => {
        // Reuse the exact same event handling as sendMessage's onEvent
        switch (eventType) {
          case 'session':
            if (data.sessionId) {
              changeSession(data.sessionId as string)
            }
            break

          case 'text':
            if (data.text) {
              streamingTextRef.current += data.text
              const currentText = streamingTextRef.current
              setMessages((prev: ChatMsg[]) => {
                const last = prev[prev.length - 1]
                if (last && last.type === 'agent' && (last as AgentMessage)._streaming) {
                  return [...prev.slice(0, -1), { type: 'agent', content: currentText, _streaming: true }]
                }
                return [...prev, { type: 'agent', content: currentText, _streaming: true }]
              })
            }
            break

          case 'tool_call':
            if (streamingTextRef.current) {
              const finalText = streamingTextRef.current
              streamingTextRef.current = ''
              setMessages((prev: ChatMsg[]) => {
                const last = prev[prev.length - 1]
                if (last && last.type === 'agent' && (last as AgentMessage)._streaming) {
                  return [...prev.slice(0, -1), { type: 'agent', content: finalText }]
                }
                return prev
              })
            }
            setMessages((prev: ChatMsg[]) => [...prev, { type: 'tool_call', toolName: data.name, toolArgs: data.args }])
            break

          case 'tool_result':
            if (data.name === 'browser_request_human' && data.result && typeof data.result === 'object' && (data.result as Record<string, unknown>).vnc_proxy_url) {
              const result = data.result as Record<string, unknown>
              setMessages((prev: ChatMsg[]) => [...prev, {
                type: 'browser_handoff',
                vncProxyUrl: String(result.vnc_proxy_url || ''),
                pageUrl: String(result.page_url || ''),
                pageTitle: String(result.page_title || ''),
                reason: String(result.message || 'Human assistance needed'),
              } as BrowserHandoffMessage])
            } else {
              setMessages((prev: ChatMsg[]) => [...prev, { type: 'tool_result', toolName: data.name, toolResult: data.result }])
            }
            break

          case 'image':
            if (data.data && data.mimeType) {
              setMessages((prev: ChatMsg[]) => [...prev, { type: 'image', data: data.data, mimeType: data.mimeType }])
            }
            break

          case 'memory_saved':
            setHasSessionMemories(true)
            break

          case 'network_denial_hint':
            // Backend detected a proxy denial and extracted host:port from output.
            // Use inline denial data if available; fall back to API poll otherwise.
            if (data.denials && (data.denials as unknown[]).length > 0) {
              setMessages((prev: ChatMsg[]) => [...prev, {
                type: 'network_denial',
                denials: data.denials as NetworkDenialMessage['denials'],
                sandbox_name: (data.sandbox_name as string) || '',
              }])
            } else if (data.session_id) {
              fetchNetworkDenials(String(data.session_id)).then(resp => {
                if (resp.denials && resp.denials.length > 0) {
                  setMessages((prev: ChatMsg[]) => [...prev, {
                    type: 'network_denial',
                    denials: resp.denials,
                    sandbox_name: resp.sandbox_name,
                  }])
                }
              }).catch(() => { /* non-fatal */ })
            }
            break

          case 'artifact':
            if (data.path) {
              const fileName = (data.path as string).split('/').pop() || 'file'
              const fileType = fileTypeFromFileName(fileName)
              setSessionArtifacts(prev => {
                if (prev.some(a => a.path === data.path)) return prev
                return [...prev, { path: data.path as string, fileName, fileType, toolName: (data.tool_name as string) || 'write_file' }]
              })
              // Also add inline artifact message to the chat thread
              setMessages((prev: ChatMsg[]) => [...prev, {
                type: 'artifact',
                path: data.path as string,
                toolName: (data.tool_name as string) || 'write_file',
              } as ArtifactMessage])
            }
            break

          case 'report_marker':
            // The agent emitted an ```astonish-report``` fence whose `path`
            // matched a same-turn write_file/edit_file artifact. Flip the
            // matching artifact's isReport flag so the embed gate
            // (embeddedArtifactPaths useMemo) admits it. The marker carries
            // no on-screen content of its own — only metadata.
            if (data.path) {
              const reportPath = data.path as string
              const reportTitle = (data.title as string) || ''
              setSessionArtifacts(prev => prev.map(a =>
                a.path === reportPath
                  ? { ...a, isReport: true, reportTitle }
                  : a,
              ))
              setMessages((prev: ChatMsg[]) => prev.map(m =>
                m.type === 'artifact' && (m as ArtifactMessage).path === reportPath
                  ? { ...(m as ArtifactMessage), isReport: true, reportTitle }
                  : m,
              ))
            }
            break

          case 'flow_output':
            if (streamingTextRef.current) {
              const finalText = streamingTextRef.current
              streamingTextRef.current = ''
              setMessages((prev: ChatMsg[]) => {
                const last = prev[prev.length - 1]
                if (last && last.type === 'agent' && (last as AgentMessage)._streaming) {
                  return [...prev.slice(0, -1), { type: 'agent', content: finalText }]
                }
                return prev
              })
            }
            if (data.content) {
              setMessages((prev: ChatMsg[]) => [...prev, { type: 'agent', content: data.content as string }])
            }
            break

          case 'approval':
            setMessages((prev: ChatMsg[]) => [...prev, { type: 'approval', toolName: data.tool, options: data.options }])
            break

          case 'auto_approved':
            setMessages((prev: ChatMsg[]) => [...prev, { type: 'auto_approved', toolName: data.tool }])
            break

          case 'thinking':
            if (data.text) {
              setMessages((prev: ChatMsg[]) => [...prev, { type: 'thinking', content: data.text }])
            }
            break

          case 'retry':
            setMessages((prev: ChatMsg[]) => [...prev, { type: 'retry', attempt: data.attempt, maxRetries: data.maxRetries, reason: data.reason }])
            break

          case 'model_changed':
            setModelStatus(prev => prev ? {
              ...prev,
              effectiveProvider: (data.effectiveProvider as string) || (data.provider as string) || prev.effectiveProvider,
              effectiveModel: (data.effectiveModel as string) || (data.model as string) || prev.effectiveModel,
              pinnedProvider: (data.pinnedProvider as string) ?? (data.provider as string) ?? prev.pinnedProvider,
              pinnedModel: (data.pinnedModel as string) ?? (data.model as string) ?? prev.pinnedModel,
              credentialsAvailable: data.credentialsAvailable !== undefined ? !!data.credentialsAvailable : prev.credentialsAvailable,
            } : prev)
            break

          case 'error':
            setMessages((prev: ChatMsg[]) => [...prev, { type: 'error', content: (data.error as string) || (data.message as string) || 'Unknown error' }])
            break

          case 'error_info':
            setMessages((prev: ChatMsg[]) => [...prev, { type: 'error_info', title: data.title, reason: data.reason, suggestion: data.suggestion, originalError: data.originalError }])
            break

          case 'distill_preview':
            // Finalize any streaming text first
            if (streamingTextRef.current) {
              const finalText = streamingTextRef.current
              streamingTextRef.current = ''
              setMessages((prev: ChatMsg[]) => {
                const last = prev[prev.length - 1]
                if (last && last.type === 'agent' && (last as AgentMessage)._streaming) {
                  return [...prev.slice(0, -1), { type: 'agent', content: finalText }]
                }
                return prev
              })
            }
            setMessages((prev: ChatMsg[]) => [...prev, {
              type: 'distill_preview',
              yaml: data.yaml as string,
              flowName: data.flowName as string,
              description: data.description as string,
              tags: (data.tags as string[]) || [],
              explanation: data.explanation as string || '',
            } as DistillPreviewMessage])
            break

          case 'tutorial_blueprint_preview':
            if (streamingTextRef.current) {
              const finalText = streamingTextRef.current
              streamingTextRef.current = ''
              setMessages((prev: ChatMsg[]) => {
                const last = prev[prev.length - 1]
                if (last && last.type === 'agent' && (last as AgentMessage)._streaming) {
                  return [...prev.slice(0, -1), { type: 'agent', content: finalText }]
                }
                return prev
              })
            }
            setMessages((prev: ChatMsg[]) => [...prev, {
              type: 'tutorial_blueprint_preview',
              title: (data.title as string) || '',
              suite: (data.suite as string) || '',
              blueprintYaml: (data.blueprint_yaml as string) || '',
              scenes: normalizeBlueprintScenes(data.scenes),
            } as TutorialBlueprintPreviewMessage])
            break

          case 'tutorial_blueprint_approved':
            setMessages((prev: ChatMsg[]) => [...prev, {
              type: 'tutorial_blueprint_approved',
              title: (data.title as string) || '',
              suite: (data.suite as string) || '',
              blueprintYaml: (data.blueprint_yaml as string) || '',
              drillYaml: (data.drill_yaml as string) || '',
              drillName: (data.drill_name as string) || '',
              content: (data.message as string) || '',
            } as TutorialBlueprintApprovedMessage])
            break

          case 'tutorial_scene_slideshow':
            setMessages((prev: ChatMsg[]) => [...prev, {
              type: 'tutorial_scene_slideshow',
              title: (data.title as string) || '',
              suite: (data.suite as string) || '',
              drill: (data.drill as string) || '',
              manifestPath: (data.manifest_path as string) || '',
              scenes: normalizeTutorialScenes(data.scenes),
            } as TutorialSceneSlideshowMessage])
            break

          case 'app_preview':
            // Finalize any streaming text first
            if (streamingTextRef.current) {
              const finalText = streamingTextRef.current
              streamingTextRef.current = ''
              setMessages((prev: ChatMsg[]) => {
                const last = prev[prev.length - 1]
                if (last && last.type === 'agent' && (last as AgentMessage)._streaming) {
                  return [...prev.slice(0, -1), { type: 'agent', content: finalText }]
                }
                return prev
              })
            }
            setMessages((prev: ChatMsg[]) => [...prev, {
              type: 'app_preview',
              code: data.code as string,
              title: data.title as string || 'App Preview',
              description: data.description as string || '',
              version: (data.version as number) || 1,
              appId: data.appId as string || undefined,
            } as AppPreviewMessage])
            if (data.appId) setActiveAppId(data.appId as string)
            break

          case 'app_done':
          case 'app_saved':
            setActiveAppId(null)
            if (data.name) {
              setMessages((prev: ChatMsg[]) => [...prev, {
                type: 'app_saved',
                name: data.name as string || '',
                path: data.path as string || '',
              } as AppSavedMessage])
            }
            window.dispatchEvent(new Event('astonish:apps-updated'))
            break

          case 'distill_saved':
            setMessages((prev: ChatMsg[]) => [...prev, {
              type: 'distill_saved',
              filePath: data.filePath as string,
              runCommand: data.runCommand as string,
            } as DistillSavedMessage])
            // Notify App to refresh the flows list
            window.dispatchEvent(new Event('astonish:flows-updated'))
            break

          case 'done':
            if (streamingTextRef.current) {
              const finalText = streamingTextRef.current
              streamingTextRef.current = ''
              setMessages((prev: ChatMsg[]) => {
                const last = prev[prev.length - 1]
                if (last && last.type === 'agent' && (last as AgentMessage)._streaming) {
                  return [...prev.slice(0, -1), { type: 'agent', content: finalText }]
                }
                return prev
              })
            }
            setIsStreaming(false)
            // After done, check for session memories (reflector may still be running)
            setIsConsolidatingMemories(true)
            setTimeout(async () => {
              try {
                const res = await listSessionMemories(sessionId)
                if (res.memories && res.memories.length > 0) {
                  setHasSessionMemories(true)
                }
              } catch { /* ignore */ }
              setIsConsolidatingMemories(false)
            }, 8000)
            break

          case 'subtask_progress': {
            const stEventType = data.event_type as string

            // ── Plan events ──
            if (stEventType === 'plan_announced') {
              const steps: PlanStepInfo[] = ((data.plan_steps as Array<{ name: string; description: string }>) || []).map(s => ({
                name: s.name,
                description: s.description,
                status: 'pending' as const,
              }))
              setMessages((prev: ChatMsg[]) => [...prev, {
                type: 'plan',
                goal: (data.plan_goal as string) || '',
                steps,
              } as PlanMessage])
              break
            }

            if (stEventType === 'plan_step_update') {
              const stepName = data.step_name as string
              const stepStatus = data.step_status as PlanStepInfo['status']
              setMessages((prev: ChatMsg[]) => {
                for (let i = prev.length - 1; i >= 0; i--) {
                  if (prev[i].type === 'plan') {
                    const plan = prev[i] as PlanMessage
                    const updatedSteps = plan.steps.map(s =>
                      s.name === stepName ? { ...s, status: stepStatus } : s
                    )
                    return prev.map((m, idx) => idx === i ? { ...plan, steps: updatedSteps } : m)
                  }
                }
                return prev
              })
              break
            }

            // ── Delegation events ──
            const stEvent: SubTaskEvent = {
              type: stEventType,
              task_name: data.task_name as string | undefined,
              status: data.status as string | undefined,
              duration: data.duration as string | undefined,
              error: data.error as string | undefined,
              tool_name: data.tool_name as string | undefined,
              tool_args: data.tool_args,
              tool_result: data.tool_result,
              text: data.text as string | undefined,
              tasks: data.tasks as SubTaskInfo[] | undefined,
              timestamp: Date.now(),
            }
            setMessages((prev: ChatMsg[]) => {
              let existingIdx = -1
              for (let i = prev.length - 1; i >= 0; i--) {
                if (prev[i].type === 'subtask_execution' && (prev[i] as SubTaskExecutionMessage).status === 'running') {
                  existingIdx = i
                  break
                }
              }
              const existing = existingIdx >= 0 ? prev[existingIdx] as SubTaskExecutionMessage : undefined
              if (existing) {
                const updated: SubTaskExecutionMessage = { ...existing, events: [...existing.events, stEvent] }
                if (stEventType === 'delegation_complete') {
                  updated.status = (data.status as string) || 'complete'
                }
                if (stEventType === 'delegation_start' && stEvent.tasks) {
                  updated.tasks = stEvent.tasks
                }
                return prev.map((m, i) => i === existingIdx ? updated : m)
              }
              const tasks: SubTaskInfo[] = (stEventType === 'delegation_start' && stEvent.tasks) ? stEvent.tasks : []
              return [...prev, {
                type: 'subtask_execution',
                events: [stEvent],
                tasks,
                status: 'running',
              } as SubTaskExecutionMessage]
            })
            break
          }

          case 'usage': {
            const input = (data.input_tokens as number) || 0
            const output = (data.output_tokens as number) || 0
            const total = (data.total_tokens as number) || 0
            setTokenUsage(prev => ({
              inputTokens: prev.inputTokens + input,
              outputTokens: prev.outputTokens + output,
              totalTokens: prev.totalTokens + total,
            }))
            break
          }

          default:
            break
        }
      },
      onError: (err) => {
        console.error('Chat reconnect stream error:', err)
        setIsStreaming(false)
      },
      onDone: () => {
        // Check for dangling partial streaming text (same as sendMessage)
        setMessages((prev: ChatMsg[]) => {
          const last = prev[prev.length - 1]
          if (last && last.type === 'agent' && (last as AgentMessage)._streaming) {
            const finalText = streamingTextRef.current || (last as AgentMessage).content
            streamingTextRef.current = ''
            const finalized = prev.map((m, i) =>
              i === prev.length - 1 ? { type: 'agent', content: finalText } as ChatMsg : m
            )
            return [...finalized, {
              type: 'error',
              content: 'The model stopped responding unexpectedly. You can send a follow-up message to continue.',
            } as ChatMsg]
          }
          return prev
        })
        setIsStreaming(false)
        setTimeout(() => loadSessions(), 1000)
      },
    })

    abortRef.current = controller
  }, [changeSession])

  const handleSelectSession = useCallback(async (sessionId: string) => {
    // Cancel any active stream
    if (abortRef.current) {
      abortRef.current.abort()
      abortRef.current = null
    }
    setIsStreaming(false)
    setFilePanelOpen(false)
    setSessionArtifacts([])
    setFilePanelInitialPath(null)
    setTodoPanelOpen(false)
    setTokenUsage({ inputTokens: 0, outputTokens: 0, totalTokens: 0 })
    setSessionStartTime(null)

    // Check if this is a fleet session (from sidebar data)
    const session = sessions.find(s => s.id === sessionId)
    if (session && session.fleetKey) {
      // Check if this fleet session is still active in the registry
      try {
        const data = await fetchFleetSessions()
        const activeFleet = (data.sessions || []).find((s: FleetSession) => s.id === sessionId)
        if (activeFleet) {
          // Reconnect to the active fleet session
          setIsFleetMode(true)
           setFleetSessionId(sessionId)
          setFleetState({ state: activeFleet.state, active_agent: activeFleet.active_agent })
          setMessages([])
          setIsStreaming(true)
          changeSession(sessionId, { userInitiated: true })
          connectToFleetStream(sessionId)
          return
        }
      } catch (err: any) {
        console.error('Failed to check fleet session status:', err)
      }
      // Fleet session is no longer active; enter fleet mode as read-only history
      setIsFleetMode(true)
      setFleetSessionId(sessionId)
      setFleetInfo({ fleet_key: session.fleetKey, fleet_name: session.fleetName || '' })
      setFleetState({ state: 'stopped', active_agent: '' })
      changeSession(sessionId, { userInitiated: true })
      await loadSessionHistory(sessionId)
      return
    } else {
      // Exit fleet mode if switching to a regular session
      if (isFleetMode) {
        setIsFleetMode(false)
        setFleetSessionId(null)
        setFleetInfo(null)
        setFleetState(null)
      }
    }

    changeSession(sessionId, { userInitiated: true })

    // Check if this session has an active background runner — if so, reconnect
    // to the live SSE stream instead of loading static history.
    try {
      const status = await fetchSessionStatus(sessionId)
      if (status.running) {
        reconnectToChatRunner(sessionId)
        return
      }
    } catch {
      // Status endpoint may fail if chat manager not initialized; fall through to history
    }

    await loadSessionHistory(sessionId)
  }, [sessions, isFleetMode, connectToFleetStream, changeSession, reconnectToChatRunner, loadSessionHistory])

  const handleNewSession = useCallback(() => {
    if (abortRef.current) {
      abortRef.current.abort()
      abortRef.current = null
    }
    // If in fleet mode, just disconnect the SSE stream (don't stop the fleet session)
    if (isFleetMode) {
      setIsFleetMode(false)
      setFleetSessionId(null)
      setFleetInfo(null)
      setFleetState(null)
    }
    setIsStreaming(false)
    changeSession(null, { userInitiated: true })
    setMessages([])
    setSessionArtifacts([])
    setFilePanelOpen(false)
    setFilePanelInitialPath(null)
    setTodoPanelOpen(false)
    setTokenUsage({ inputTokens: 0, outputTokens: 0, totalTokens: 0 })
    setSessionStartTime(null)
    if (inputRef.current) inputRef.current.focus()
  }, [isFleetMode, changeSession])

  const handleDeleteSession = useCallback(async (e: React.MouseEvent, sessionId: string) => {
    e.stopPropagation()
    try {
      // If this is an active fleet session, stop it first
      const session = sessions.find(s => s.id === sessionId)
      if (session && session.fleetKey) {
        try {
          await stopFleetSession(sessionId)
        } catch {
          // Fleet session may already be stopped
        }
      }
      await deleteSession(sessionId)
      setSessions(prev => prev.filter(s => s.id !== sessionId))
      if (activeSessionId === sessionId) {
        if (isFleetMode) {
          setIsFleetMode(false)
          setFleetSessionId(null)
          setFleetInfo(null)
          setFleetState(null)
          setIsStreaming(false)
          if (abortRef.current) {
            abortRef.current.abort()
            abortRef.current = null
          }
        }
        changeSession(null, { userInitiated: true })
        setMessages([])
      }
    } catch (err: any) {
      console.error('Failed to delete session:', err)
    }
  }, [activeSessionId, sessions, isFleetMode, changeSession])

  const handleStop = useCallback(() => {
    // Disconnect the SSE viewer
    if (abortRef.current) {
      abortRef.current.abort()
      abortRef.current = null
    }
    // Stop the background runner (this is the actual kill switch)
    if (isFleetMode && fleetSessionId) {
      stopFleetSession(fleetSessionId)
    } else if (activeSessionId) {
      stopChat(activeSessionId) // calls POST /api/studio/sessions/{id}/stop which stops the background runner
    }
    setIsStreaming(false)
  }, [activeSessionId, isFleetMode, fleetSessionId])

  // Start a fleet session
  const handleFleetStart = useCallback(async (fleetKey: string | null, initialMessage: string, planKey: string) => {
    setShowFleetDialog(false)
    setFleetDialogMessage('')
    setIsFleetMode(true)
    setMessages([])
    setIsStreaming(true)

    // Add the initial human message to the UI if provided
    if (initialMessage) {
      setMessages([{ type: 'fleet_message', sender: 'customer', text: initialMessage, timestamp: Date.now() }])
    }

    try {
      // Create the fleet session (returns JSON with session info)
      const sessionInfo = await startFleetSession({ fleetKey: fleetKey || undefined, planKey, message: initialMessage })
      setFleetSessionId(sessionInfo.session_id)
      setFleetInfo({ fleet_key: sessionInfo.fleet_key, fleet_name: sessionInfo.fleet_name, agents: sessionInfo.agents })
      changeSession(sessionInfo.session_id)

      // Refresh sidebar to show the new fleet session
      loadSessions()

      // Connect to the SSE stream for real-time events
      connectToFleetStream(sessionInfo.session_id)
    } catch (err: any) {
      console.error('Failed to start fleet session:', err)
      setMessages((prev: ChatMsg[]) => [...prev, { type: 'error', content: 'Failed to start fleet: ' + err.message }])
      setIsStreaming(false)
      setIsFleetMode(false)
    }
  }, [connectToFleetStream, changeSession])

  // Send a human message to the fleet session
  const sendFleetHumanMessage = useCallback(async (text: string) => {
    if (!text.trim() || !fleetSessionId) return
    // Reset scroll position — user expects to see the conversation flow
    isNearBottomRef.current = true
    setShowScrollButton(false)
    // Add human message to UI immediately
    setMessages((prev: ChatMsg[]) => [...prev, { type: 'fleet_message', sender: 'customer', text, timestamp: Date.now() }])
    setInput('')
    if (inputRef.current) inputRef.current.style.height = 'auto'
    try {
      await sendFleetMessage(fleetSessionId, text)
    } catch (err: any) {
      console.error('Failed to send fleet message:', err)
      setMessages((prev: ChatMsg[]) => [...prev, { type: 'error', content: 'Failed to send message: ' + err.message }])
    }
  }, [fleetSessionId])

  // Exit fleet mode
  const handleExitFleet = useCallback(() => {
    if (abortRef.current) {
      abortRef.current.abort()
      abortRef.current = null
    }
    if (fleetSessionId) {
      stopFleetSession(fleetSessionId)
    }
    setIsFleetMode(false)
    setFleetSessionId(null)
    setFleetInfo(null)
    setFleetState(null)
    setIsStreaming(false)
    changeSession(null, { userInitiated: true })
    setMessages([])
    loadSessions()
  }, [fleetSessionId, changeSession])

  // --- Attachment handlers ---

  const validateAndAddFiles = useCallback((files: FileList | File[]) => {
    const fileArray = Array.from(files)
    const errors: string[] = []

    setAttachments(prev => {
      const remaining = MAX_ATTACHMENTS - prev.length
      if (remaining <= 0) {
        errors.push(`Maximum ${MAX_ATTACHMENTS} files allowed`)
        return prev
      }

      const toAdd: typeof prev = []
      for (const file of fileArray.slice(0, remaining)) {
        if (file.size > MAX_FILE_SIZE) {
          errors.push(`${file.name} exceeds 10 MB limit`)
          continue
        }
        // Check MIME type or extension
        const isAcceptedMime = ACCEPTED_MIME_TYPES.some(t => file.type.startsWith(t.replace('*', '')))
        const ext = '.' + file.name.split('.').pop()?.toLowerCase()
        const isAcceptedExt = ACCEPTED_EXTENSIONS.split(',').includes(ext)
        if (!isAcceptedMime && !isAcceptedExt) {
          errors.push(`${file.name}: unsupported file type`)
          continue
        }

        const id = crypto.randomUUID()
        let preview: string | null = null
        if (file.type.startsWith('image/')) {
          preview = URL.createObjectURL(file)
        }
        toAdd.push({ id, file, preview, mimeType: file.type || 'application/octet-stream', name: file.name })
      }

      if (fileArray.length > remaining) {
        errors.push(`Only ${remaining} more file(s) can be added`)
      }

      return [...prev, ...toAdd]
    })

    // Show errors as console warnings (could be toast in the future)
    if (errors.length > 0) {
      console.warn('Attachment errors:', errors)
    }
  }, [])

  const removeAttachment = useCallback((id: string) => {
    setAttachments(prev => {
      const removed = prev.find(a => a.id === id)
      if (removed?.preview) URL.revokeObjectURL(removed.preview)
      return prev.filter(a => a.id !== id)
    })
  }, [])

  const clearAttachments = useCallback(() => {
    setAttachments(prev => {
      prev.forEach(a => { if (a.preview) URL.revokeObjectURL(a.preview) })
      return []
    })
  }, [])

  const handleFileSelect = useCallback((e: React.ChangeEvent<HTMLInputElement>) => {
    if (e.target.files && e.target.files.length > 0) {
      validateAndAddFiles(e.target.files)
      e.target.value = '' // Reset so same file can be re-selected
    }
  }, [validateAndAddFiles])

  const handleDragOver = useCallback((e: React.DragEvent) => {
    e.preventDefault()
    e.stopPropagation()
    setIsDragOver(true)
  }, [])

  const handleDragLeave = useCallback((e: React.DragEvent) => {
    e.preventDefault()
    e.stopPropagation()
    setIsDragOver(false)
  }, [])

  const handleDrop = useCallback((e: React.DragEvent) => {
    e.preventDefault()
    e.stopPropagation()
    setIsDragOver(false)
    if (e.dataTransfer.files && e.dataTransfer.files.length > 0) {
      validateAndAddFiles(e.dataTransfer.files)
    }
  }, [validateAndAddFiles])

  // Convert pending attachments to API payloads (base64)
  const prepareAttachmentPayloads = useCallback(async (): Promise<AttachmentPayload[]> => {
    const payloads: AttachmentPayload[] = []
    for (const att of attachments) {
      const arrayBuffer = await att.file.arrayBuffer()
      const bytes = new Uint8Array(arrayBuffer)
      let binary = ''
      for (let i = 0; i < bytes.byteLength; i++) {
        binary += String.fromCharCode(bytes[i])
      }
      const base64 = btoa(binary)
      payloads.push({
        filename: att.name,
        mimeType: att.mimeType,
        data: base64,
      })
    }
    return payloads
  }, [attachments])

  // --- End attachment handlers ---

  const sendMessage = useCallback(async (text: string, options: { systemContext?: string, pinnedToolGroups?: string[] } = {}) => {
    if (!text.trim() && attachments.length === 0) return
    const userMsg = text.trim()

    // Reset scroll position — user expects to see the conversation flow
    isNearBottomRef.current = true
    setShowScrollButton(false)

    // Prepare attachments (async base64 encoding)
    let attachmentPayloads: AttachmentPayload[] = []
    let attachmentInfos: AttachmentInfo[] = []
    if (attachments.length > 0) {
      attachmentPayloads = await prepareAttachmentPayloads()
      // For image previews in local messages, include base64 data for thumbnails
      attachmentInfos = attachments.map(a => ({
        filename: a.name,
        mimeType: a.mimeType,
        size: a.file.size,
        data: a.mimeType.startsWith('image/') ? attachmentPayloads.find(p => p.filename === a.name)?.data : undefined,
      }))
    }

    // Add user message to chat (unless it's a slash command or internal action)
    if (!userMsg.startsWith('/') && !userMsg.startsWith('__distill_') && !userMsg.startsWith('__app_') && !userMsg.startsWith('__tutorial_blueprint_')) {
      const userChatMsg: UserMessage = { type: 'user', content: userMsg }
      if (attachmentInfos.length > 0) {
        userChatMsg.attachments = attachmentInfos
      }
      setMessages((prev: ChatMsg[]) => [...prev, userChatMsg])
    }

    setInput('')
    if (inputRef.current) {
      inputRef.current.style.height = 'auto'
    }
    setIsStreaming(true)
    setSessionStartTime(Date.now())
    streamingTextRef.current = ''

    // Clear attachments after capturing payloads
    clearAttachments()

    const controller = connectChat({
      sessionId: activeSessionId || '',
      message: userMsg,
      attachments: attachmentPayloads.length > 0 ? attachmentPayloads : undefined,
      systemContext: options.systemContext || activeWizardContext || undefined,
      pinnedToolGroups: options.pinnedToolGroups || activePinnedToolGroups || undefined,
      provider: preChatProvider || undefined,
      model: preChatModel || undefined,
      onEvent: (eventType, data) => {
        switch (eventType) {
          case 'session':
            if (data.sessionId) {
              const sid = data.sessionId as string
              const shouldPin = !!(data.isNew && (preChatProvider || preChatModel))
              const pinProvider = preChatProvider
              const pinModel = preChatModel
              // Skip the immediate model-status fetch when we're about to pin —
              // otherwise it races and briefly shows the cascade default.
              changeSession(sid, { skipModelFetch: shouldPin })
              if (data.isNew) {
                setTimeout(() => loadSessions(), 500)
              }
              if (shouldPin) {
                const providersSnapshot = preChatProviders
                setPreChatProvider('')
                setPreChatModel('')
                // Show the selection immediately so the picker doesn't flash
                // "(default — cascade)" while the pin round-trip completes.
                setModelStatus((prev) => ({
                  pinnedProvider: pinProvider,
                  pinnedModel: pinModel,
                  effectiveProvider: pinProvider || prev?.effectiveProvider || '',
                  effectiveModel: pinModel || prev?.effectiveModel || '',
                  credentialsAvailable: true,
                  availableProviders: prev?.availableProviders?.length
                    ? prev.availableProviders
                    : providersSnapshot,
                }))
                // Backend persists the pin on create; PATCH keeps the live runner
                // in sync. Re-fetch model-status for the authoritative pin + list.
                patchSessionModel(sid, pinProvider, pinModel)
                  .catch((err) => console.warn(err))
                  .finally(() => {
                    fetchSessionModelStatus(sid).then(setModelStatus).catch(() => setModelStatus(null))
                  })
              }
            }
            break

          case 'text':
            if (data.text) {
              streamingTextRef.current += data.text
              const currentText = streamingTextRef.current
              setMessages((prev: ChatMsg[]) => {
                const last = prev[prev.length - 1]
                if (last && last.type === 'agent' && (last as AgentMessage)._streaming) {
                  return [...prev.slice(0, -1), { type: 'agent', content: currentText, _streaming: true }]
                }
                return [...prev, { type: 'agent', content: currentText, _streaming: true }]
              })
            }
            break

          case 'tool_call':
            // Finalize any streaming text before tool call
            if (streamingTextRef.current) {
              const finalText = streamingTextRef.current
              streamingTextRef.current = ''
              setMessages((prev: ChatMsg[]) => {
                const last = prev[prev.length - 1]
                if (last && last.type === 'agent' && (last as AgentMessage)._streaming) {
                  return [...prev.slice(0, -1), { type: 'agent', content: finalText }]
                }
                return prev
              })
            }
            setMessages((prev: ChatMsg[]) => [...prev, { type: 'tool_call', toolName: data.name, toolArgs: data.args }])
            break

          case 'tool_result':
            // Detect browser handoff with VNC viewer
            if (data.name === 'browser_request_human' && data.result && typeof data.result === 'object' && (data.result as Record<string, unknown>).vnc_proxy_url) {
              const result = data.result as Record<string, unknown>
              const vncUrl = String(result.vnc_proxy_url || '')
              setMessages((prev: ChatMsg[]) => [...prev, {
                type: 'browser_handoff',
                vncProxyUrl: vncUrl,
                pageUrl: String(result.page_url || ''),
                pageTitle: String(result.page_title || ''),
                reason: String(result.message || 'Human assistance needed'),
              } as BrowserHandoffMessage])
            } else {
              setMessages((prev: ChatMsg[]) => [...prev, { type: 'tool_result', toolName: data.name, toolResult: data.result }])
            }
            // Clear wizard context once the fleet plan or drill suite has been saved
            if (data.name === 'save_fleet_plan' || data.name === 'save_drill') {
              setActiveWizardContext(null)
            }
            break

          case 'memory_saved':
            // Mark that this session has memories (optimistic)
            setHasSessionMemories(true)
            break

          case 'network_denial_hint':
            if (data.denials && (data.denials as unknown[]).length > 0) {
              setMessages((prev: ChatMsg[]) => [...prev, {
                type: 'network_denial',
                denials: data.denials as NetworkDenialMessage['denials'],
                sandbox_name: (data.sandbox_name as string) || '',
              }])
            } else if (data.session_id) {
              fetchNetworkDenials(String(data.session_id)).then(resp => {
                if (resp.denials && resp.denials.length > 0) {
                  setMessages((prev: ChatMsg[]) => [...prev, {
                    type: 'network_denial',
                    denials: resp.denials,
                    sandbox_name: resp.sandbox_name,
                  }])
                }
              }).catch(() => { /* non-fatal */ })
            }
            break

          case 'image':
            if (data.data && data.mimeType) {
              setMessages((prev: ChatMsg[]) => [...prev, { type: 'image', data: data.data, mimeType: data.mimeType }])
            }
            break

          case 'artifact':
            if (data.path) {
              const fileName = (data.path as string).split('/').pop() || 'file'
              const fileType = fileTypeFromFileName(fileName)
              setSessionArtifacts(prev => {
                if (prev.some(a => a.path === data.path)) return prev
                return [...prev, { path: data.path as string, fileName, fileType, toolName: (data.tool_name as string) || 'write_file' }]
              })
              // Also add inline artifact message to the chat thread
              setMessages((prev: ChatMsg[]) => [...prev, {
                type: 'artifact',
                path: data.path as string,
                toolName: (data.tool_name as string) || 'write_file',
              } as ArtifactMessage])
            }
            break

          case 'report_marker':
            // Mirror of the primary handler — see comments at the first
            // case 'report_marker' above. Both SSE consumers (sendMessage
            // and reconnect) must apply the same flip so a tab refresh in
            // the middle of a streaming turn produces the same result as
            // the original stream.
            if (data.path) {
              const reportPath = data.path as string
              const reportTitle = (data.title as string) || ''
              setSessionArtifacts(prev => prev.map(a =>
                a.path === reportPath
                  ? { ...a, isReport: true, reportTitle }
                  : a,
              ))
              setMessages((prev: ChatMsg[]) => prev.map(m =>
                m.type === 'artifact' && (m as ArtifactMessage).path === reportPath
                  ? { ...(m as ArtifactMessage), isReport: true, reportTitle }
                  : m,
              ))
            }
            break

          case 'flow_output':
            // Flow output delivered directly — bypass LLM, render as markdown.
            // Finalize any pending streaming text first.
            if (streamingTextRef.current) {
              const finalText = streamingTextRef.current
              streamingTextRef.current = ''
              setMessages((prev: ChatMsg[]) => {
                const last = prev[prev.length - 1]
                if (last && last.type === 'agent' && (last as AgentMessage)._streaming) {
                  return [...prev.slice(0, -1), { type: 'agent', content: finalText }]
                }
                return prev
              })
            }
            if (data.content) {
              setMessages((prev: ChatMsg[]) => [...prev, { type: 'agent', content: data.content as string }])
            }
            break

          case 'new_session':
            if (data.sessionId) {
              changeSession(data.sessionId as string)
              setMessages([])
              streamingTextRef.current = ''
              loadSessions()
            }
            break

          case 'session_title':
            // Update the session title in the sidebar.
            // Use sessionId from the event payload to avoid stale closure issues
            // (activeSessionId may still be null for newly created sessions).
            // Upsert when the session isn't in the list yet (title can arrive
            // before the deferred loadSessions after isNew).
            if (data.title && data.sessionId) {
              const sid = data.sessionId as string
              const title = data.title as string
              setSessions(prev => {
                const idx = prev.findIndex(s => s.id === sid)
                if (idx >= 0) {
                  return prev.map(s => s.id === sid ? { ...s, title } : s)
                }
                const now = new Date().toISOString()
                return [{
                  id: sid,
                  title,
                  createdAt: now,
                  updatedAt: now,
                  messageCount: 0,
                }, ...prev]
              })
            }
            break

          case 'system':
            if (data.content) {
              setMessages((prev: ChatMsg[]) => [...prev, {
                type: 'system',
                content: data.content as string,
              }])
            }
            break

          case 'approval':
            setMessages((prev: ChatMsg[]) => [...prev, {
              type: 'approval',
              toolName: data.tool,
              options: data.options,
            }])
            break

          case 'auto_approved':
            setMessages((prev: ChatMsg[]) => [...prev, {
              type: 'auto_approved',
              toolName: data.tool,
            }])
            break

          case 'thinking':
            if (data.text) {
              setMessages((prev: ChatMsg[]) => [...prev, { type: 'thinking', content: data.text }])
            }
            break

          case 'fleet_redirect':
            // /fleet [task] command opens the fleet dialog, optionally pre-populated
            setIsStreaming(false)
            setFleetDialogMessage((data.task as string) || '')
            setShowFleetDialog(true)
            break

          case 'fleet_plan_redirect':
            // /fleet-plan [hint] command: start plan creation in a fresh conversation.
            // If the backend found a plan_wizard in the template, use it as system context.
            // If no hint, show a template picker dialog so the user selects one first.
            setIsStreaming(false)
            {
              const hint = (data.hint as string) || ''
              const wizardSystemPrompt = (data.wizard_system_prompt as string) || ''
              const pinnedGroups = (data.pinned_tool_groups as string[]) || null
              const setupDraftId = (data.setup_draft_id as string) || ''
              const setupProfileKey = (data.setup_profile_key as string) || ''
              const currentStepTitle = (data.current_setup_step_title as string) || ''

              if (wizardSystemPrompt) {
                setActiveWizardContext(wizardSystemPrompt)
                if (pinnedGroups && pinnedGroups.length > 0) {
                  setActivePinnedToolGroups(pinnedGroups)
                }
                const kickoff = currentStepTitle
                  ? `Help me set up a fleet plan. Let's start with ${currentStepTitle.toLowerCase()}.`
                  : 'Help me set up a fleet plan.'
                const agentOps = setupDraftId
                  ? `\n\nSETUP SESSION:\n- draft_id: ${setupDraftId}\n- profile_key: ${setupProfileKey || hint || 'generic'}\n- Call get_setup_profile with draft_id and profile_key before asking questions.\n- Work ONLY on the current step.\n- Call update_setup_draft when the step is complete.\n- Follow the profile step order; do not skip ahead.`
                  : ''
                setPendingFleetPlanPrompt({
                  message: kickoff,
                  systemContext: wizardSystemPrompt + agentOps,
                  pinnedToolGroups: pinnedGroups || undefined,
                })
              } else if (hint) {
                // Profile resolution failed — still avoid hardcoding identity-first ordering.
                const genericSystemPrompt = `You are helping the user create a fleet plan based on the "${hint}" fleet template. The base_fleet_key is "${hint}". Call get_setup_profile to load the setup profile and current step, then follow that step order strictly. Do not ask for plan identity unless the current setup step is identity. Before saving, call validate_fleet_plan with all config including credentials. Only call save_fleet_plan after validation passes.`
                setActiveWizardContext(genericSystemPrompt)
                setPendingFleetPlanPrompt({
                  message: `Help me create a fleet plan from the "${hint}" template.`,
                  systemContext: genericSystemPrompt,
                })
              } else {
                // No hint: show template picker so user selects one, then re-issue /fleet-plan <key>
                setShowTemplatePicker(true)
              }
            }
            break

          case 'drill_redirect':
            // /drill or /tutorial-drill [hint]: start drill/tutorial suite creation wizard
            setIsStreaming(false)
            {
              const hint = (data.hint as string) || ''
              const wizardSystemPrompt = (data.wizard_system_prompt as string) || ''
              const wizardKind = (data.wizard_kind as string) || 'drill'
              const pinnedGroups = (data.pinned_tool_groups as string[]) || null

              if (wizardSystemPrompt) {
                setActiveWizardContext(wizardSystemPrompt)
                if (pinnedGroups) setActivePinnedToolGroups(pinnedGroups)
                let kickoff: string
                if (wizardKind === 'tutorial') {
                  kickoff = hint
                    ? `I'd like to create a tutorial drill. Here's the flow to teach: ${hint}`
                    : 'I\'d like to create a tutorial drill for my product UI.'
                } else {
                  kickoff = hint
                    ? `I'd like to create a drill suite. Here's what I want to test: ${hint}`
                    : 'I\'d like to create a drill suite for my project.'
                }
                setPendingDrillPrompt({
                  message: kickoff,
                  systemContext: wizardSystemPrompt,
                  pinnedToolGroups: pinnedGroups || undefined,
                })
              }
            }
            break

          case 'drill_add_redirect':
            // /drill-add or /tutorial-drill-add <suite>: add drills to an existing suite
            setIsStreaming(false)
            {
              const suiteName = (data.suite_name as string) || ''
              const wizardSystemPrompt = (data.wizard_system_prompt as string) || ''
              const wizardKind = (data.wizard_kind as string) || 'drill'
              const pinnedGroups = (data.pinned_tool_groups as string[]) || null

              if (wizardSystemPrompt) {
                setActiveWizardContext(wizardSystemPrompt)
                if (pinnedGroups) setActivePinnedToolGroups(pinnedGroups)
                const kickoff = wizardKind === 'tutorial'
                  ? `I'd like to add new tutorial drills to the "${suiteName}" suite.`
                  : `I'd like to add new drills to the "${suiteName}" suite.`
                setPendingDrillPrompt({
                  message: kickoff,
                  systemContext: wizardSystemPrompt,
                  pinnedToolGroups: pinnedGroups || undefined,
                })
              }
            }
            break

          case 'fleet_progress':
            // Accumulate fleet progress events into a structured fleet_execution message.
            // Each event is appended to the phases array; the UI renders a collapsible panel.
            setMessages((prev: ChatMsg[]) => {
              const existing = prev.find(m => m.type === 'fleet_execution') as FleetExecutionMessage | undefined
              const event: FleetEvent = {
                ...data,
                type: data.type as string,
                timestamp: Date.now(),
                // Preserve rich data fields from SSE payload
                args: data.args || null,
                result: data.result !== undefined ? data.result : null,
                text: (data.text as string) || '',
              }
              const maxParallelAgents = Number(data.max_parallel_agents || data.maxParallelAgents || existing?.maxParallelAgents || 1)

              if (existing) {
                const updated: FleetExecutionMessage = { ...existing, events: [...existing.events, event], maxParallelAgents }
                // Update current phase/status
                if (data.type === 'phase_start' || data.type === 'conversation_start') {
                  updated.currentPhase = data.phase as string
                  updated.currentAgent = data.agent as string
                } else if (data.type === 'fleet_complete') {
                  updated.status = 'complete'
                  updated.currentPhase = null
                }
                return prev.map(m => m.type === 'fleet_execution' ? updated : m)
              }

              // First event: create the fleet_execution message
              return [...prev, {
                type: 'fleet_execution',
                events: [event],
                currentPhase: (data.type === 'phase_start' || data.type === 'conversation_start') ? data.phase as string : null,
                currentAgent: (data.type === 'phase_start' || data.type === 'conversation_start') ? data.agent as string : null,
                status: 'running',
                maxParallelAgents,
              } as FleetExecutionMessage]
            })
            break

          case 'subtask_progress': {
            // Handle plan events and sub-task progress events.
            // Plan events create/update a PlanMessage; delegation events manage SubTaskExecutionMessage.
            const eventType = data.event_type as string

            // ── Plan events ──
            if (eventType === 'plan_announced') {
              const steps: PlanStepInfo[] = ((data.plan_steps as Array<{ name: string; description: string }>) || []).map(s => ({
                name: s.name,
                description: s.description,
                status: 'pending' as const,
              }))
              setMessages((prev: ChatMsg[]) => [...prev, {
                type: 'plan',
                goal: (data.plan_goal as string) || '',
                steps,
              } as PlanMessage])
              break
            }

            if (eventType === 'plan_step_update') {
              const stepName = data.step_name as string
              const stepStatus = data.step_status as PlanStepInfo['status']
              setMessages((prev: ChatMsg[]) => {
                // Find the most recent plan message
                for (let i = prev.length - 1; i >= 0; i--) {
                  if (prev[i].type === 'plan') {
                    const plan = prev[i] as PlanMessage
                    const updatedSteps = plan.steps.map(s =>
                      s.name === stepName ? { ...s, status: stepStatus } : s
                    )
                    return prev.map((m, idx) => idx === i ? { ...plan, steps: updatedSteps } : m)
                  }
                }
                return prev
              })
              break
            }

            // ── Delegation events (existing subtask_execution handling) ──
            const event: SubTaskEvent = {
              type: eventType,
              task_name: data.task_name as string | undefined,
              status: data.status as string | undefined,
              duration: data.duration as string | undefined,
              error: data.error as string | undefined,
              tool_name: data.tool_name as string | undefined,
              tool_args: data.tool_args,
              tool_result: data.tool_result,
              text: data.text as string | undefined,
              tasks: data.tasks as SubTaskInfo[] | undefined,
              timestamp: Date.now(),
            }

            setMessages((prev: ChatMsg[]) => {
              // Find the latest subtask_execution message that is still running (search from end)
              let existingIdx = -1
              for (let i = prev.length - 1; i >= 0; i--) {
                if (prev[i].type === 'subtask_execution' && (prev[i] as SubTaskExecutionMessage).status === 'running') {
                  existingIdx = i
                  break
                }
              }
              const existing = existingIdx >= 0 ? prev[existingIdx] as SubTaskExecutionMessage : undefined

              if (existing) {
                const updated: SubTaskExecutionMessage = { ...existing, events: [...existing.events, event] }
                // Update status on delegation_complete
                if (eventType === 'delegation_complete') {
                  updated.status = (data.status as string) || 'complete'
                }
                // Update tasks list on delegation_start (if somehow received after creation)
                if (eventType === 'delegation_start' && event.tasks) {
                  updated.tasks = event.tasks
                }
                return prev.map((m, i) => i === existingIdx ? updated : m)
              }

              // First event: create the subtask_execution message
              const tasks: SubTaskInfo[] = (eventType === 'delegation_start' && event.tasks) ? event.tasks : []
              return [...prev, {
                type: 'subtask_execution',
                events: [event],
                tasks,
                status: 'running',
              } as SubTaskExecutionMessage]
            })
            break
          }

          case 'retry':
            setMessages((prev: ChatMsg[]) => [...prev, {
              type: 'retry',
              attempt: data.attempt,
              maxRetries: data.maxRetries,
              reason: data.reason,
            }])
            break

          case 'model_changed':
            setModelStatus(prev => prev ? {
              ...prev,
              effectiveProvider: (data.effectiveProvider as string) || (data.provider as string) || prev.effectiveProvider,
              effectiveModel: (data.effectiveModel as string) || (data.model as string) || prev.effectiveModel,
              pinnedProvider: (data.pinnedProvider as string) ?? (data.provider as string) ?? prev.pinnedProvider,
              pinnedModel: (data.pinnedModel as string) ?? (data.model as string) ?? prev.pinnedModel,
              credentialsAvailable: data.credentialsAvailable !== undefined ? !!data.credentialsAvailable : prev.credentialsAvailable,
            } : prev)
            break

          case 'error':
            setMessages((prev: ChatMsg[]) => [...prev, { type: 'error', content: (data.error as string) || (data.message as string) || 'Unknown error' }])
            break

          case 'error_info':
            setMessages((prev: ChatMsg[]) => [...prev, {
              type: 'error_info',
              title: data.title,
              reason: data.reason,
              suggestion: data.suggestion,
              originalError: data.originalError,
            }])
            break

          case 'distill_preview':
            if (streamingTextRef.current) {
              const finalText = streamingTextRef.current
              streamingTextRef.current = ''
              setMessages((prev: ChatMsg[]) => {
                const last = prev[prev.length - 1]
                if (last && last.type === 'agent' && (last as AgentMessage)._streaming) {
                  return [...prev.slice(0, -1), { type: 'agent', content: finalText }]
                }
                return prev
              })
            }
            setMessages((prev: ChatMsg[]) => [...prev, {
              type: 'distill_preview',
              yaml: data.yaml as string,
              flowName: data.flowName as string,
              description: data.description as string,
              tags: (data.tags as string[]) || [],
              explanation: data.explanation as string || '',
            } as DistillPreviewMessage])
            break

          case 'tutorial_blueprint_preview':
            if (streamingTextRef.current) {
              const finalText = streamingTextRef.current
              streamingTextRef.current = ''
              setMessages((prev: ChatMsg[]) => {
                const last = prev[prev.length - 1]
                if (last && last.type === 'agent' && (last as AgentMessage)._streaming) {
                  return [...prev.slice(0, -1), { type: 'agent', content: finalText }]
                }
                return prev
              })
            }
            setMessages((prev: ChatMsg[]) => [...prev, {
              type: 'tutorial_blueprint_preview',
              title: (data.title as string) || '',
              suite: (data.suite as string) || '',
              blueprintYaml: (data.blueprint_yaml as string) || '',
              scenes: normalizeBlueprintScenes(data.scenes),
            } as TutorialBlueprintPreviewMessage])
            break

          case 'tutorial_blueprint_approved':
            setMessages((prev: ChatMsg[]) => [...prev, {
              type: 'tutorial_blueprint_approved',
              title: (data.title as string) || '',
              suite: (data.suite as string) || '',
              blueprintYaml: (data.blueprint_yaml as string) || '',
              drillYaml: (data.drill_yaml as string) || '',
              drillName: (data.drill_name as string) || '',
              content: (data.message as string) || '',
            } as TutorialBlueprintApprovedMessage])
            break

          case 'tutorial_scene_slideshow':
            setMessages((prev: ChatMsg[]) => [...prev, {
              type: 'tutorial_scene_slideshow',
              title: (data.title as string) || '',
              suite: (data.suite as string) || '',
              drill: (data.drill as string) || '',
              manifestPath: (data.manifest_path as string) || '',
              scenes: normalizeTutorialScenes(data.scenes),
            } as TutorialSceneSlideshowMessage])
            break

          case 'app_preview':
            // Finalize any streaming text first
            if (streamingTextRef.current) {
              const finalText = streamingTextRef.current
              streamingTextRef.current = ''
              setMessages((prev: ChatMsg[]) => {
                const last = prev[prev.length - 1]
                if (last && last.type === 'agent' && (last as AgentMessage)._streaming) {
                  return [...prev.slice(0, -1), { type: 'agent', content: finalText }]
                }
                return prev
              })
            }
            setMessages((prev: ChatMsg[]) => [...prev, {
              type: 'app_preview',
              code: data.code as string,
              title: data.title as string || 'App Preview',
              description: data.description as string || '',
              version: (data.version as number) || 1,
              appId: data.appId as string || undefined,
            } as AppPreviewMessage])
            if (data.appId) setActiveAppId(data.appId as string)
            break

          case 'app_done':
          case 'app_saved':
            setActiveAppId(null)
            if (data.name) {
              setMessages((prev: ChatMsg[]) => [...prev, {
                type: 'app_saved',
                name: data.name as string || '',
                path: data.path as string || '',
              } as AppSavedMessage])
            }
            window.dispatchEvent(new Event('astonish:apps-updated'))
            break

          case 'distill_saved':
            setMessages((prev: ChatMsg[]) => [...prev, {
              type: 'distill_saved',
              filePath: data.filePath as string,
              runCommand: data.runCommand as string,
            } as DistillSavedMessage])
            // Notify App to refresh the flows list
            window.dispatchEvent(new Event('astonish:flows-updated'))
            break

          case 'done':
            // Finalize streaming text and stop the processing indicator.
            // The SSE stream may stay open briefly (grace period for async
            // events like session_title), but the user-facing response is
            // complete — hide the spinner immediately.
            if (streamingTextRef.current) {
              const finalText = streamingTextRef.current
              streamingTextRef.current = ''
              setMessages((prev: ChatMsg[]) => {
                const last = prev[prev.length - 1]
                if (last && last.type === 'agent' && (last as AgentMessage)._streaming) {
                  return [...prev.slice(0, -1), { type: 'agent', content: finalText }]
                }
                return prev
              })
            }
            setIsStreaming(false)
            // After done, the platform reflector runs async (reflection + extraction).
            // Mark as consolidating so the memory panel shows a loading state.
            setIsConsolidatingMemories(true)
            if (activeSessionId) {
              const sid = activeSessionId
              setTimeout(async () => {
                try {
                  const res = await listSessionMemories(sid)
                  if (res.memories && res.memories.length > 0) {
                    setHasSessionMemories(true)
                  }
                } catch { /* ignore — session may have been deleted */ }
                setIsConsolidatingMemories(false)
              }, 8000) // 8s delay to give reflector + extraction time to finish
            } else {
              setIsConsolidatingMemories(false)
            }
            break

          case 'usage': {
            const input = (data.input_tokens as number) || 0
            const output = (data.output_tokens as number) || 0
            const total = (data.total_tokens as number) || 0
            setTokenUsage(prev => ({
              inputTokens: prev.inputTokens + input,
              outputTokens: prev.outputTokens + output,
              totalTokens: prev.totalTokens + total,
            }))
            break
          }

          default:
            break
        }
      },
      onError: (err) => {
        console.error('Chat stream error:', err)
        setMessages((prev: ChatMsg[]) => [...prev, { type: 'error', content: err.message }])
        setIsStreaming(false)
      },
      onDone: () => {
        // Check for dangling partial streaming text — if the stream ended
        // without a proper 'done' event (e.g., server crash, network drop),
        // finalize whatever text was received and show an error.
        setMessages((prev: ChatMsg[]) => {
          const last = prev[prev.length - 1]
          if (last && last.type === 'agent' && (last as AgentMessage)._streaming) {
            const finalText = streamingTextRef.current || (last as AgentMessage).content
            streamingTextRef.current = ''
            const finalized = prev.map((m, i) =>
              i === prev.length - 1 ? { type: 'agent', content: finalText } as ChatMsg : m
            )
            return [...finalized, {
              type: 'error',
              content: 'The model stopped responding unexpectedly. You can send a follow-up message to continue.',
            } as ChatMsg]
          }
          return prev
        })
        setIsStreaming(false)
        // Refresh sessions to pick up title updates
        setTimeout(() => loadSessions(), 1000)
      },
    })

    abortRef.current = controller
  }, [activeSessionId, activeWizardContext, activePinnedToolGroups, attachments, prepareAttachmentPayloads, clearAttachments, preChatProvider, preChatModel, preChatProviders, changeSession])

  // Process deferred fleet plan prompt (set by fleet_plan_redirect SSE event)
  useEffect(() => {
    if (pendingFleetPlanPrompt && !isStreaming) {
      const { message, systemContext, pinnedToolGroups } = pendingFleetPlanPrompt
      setPendingFleetPlanPrompt(null)
      sendMessage(message, { systemContext, pinnedToolGroups })
    }
  }, [pendingFleetPlanPrompt, isStreaming, sendMessage])

  // Process deferred drill prompt (set by drill_redirect SSE event)
  useEffect(() => {
    if (pendingDrillPrompt && !isStreaming) {
      const { message, systemContext, pinnedToolGroups } = pendingDrillPrompt
      setPendingDrillPrompt(null)
      sendMessage(message, { systemContext, pinnedToolGroups })
    }
  }, [pendingDrillPrompt, isStreaming, sendMessage])

  // Process pending chat message passed from another view (e.g., Fleet UI "Create Plan with AI Guide", Apps "Improve with AI")
  useEffect(() => {
    if (pendingChatMessage && !isStreaming) {
      // If there's an active session and the pending message needs a fresh session
      // (has systemContext, e.g. app refinement), clear the session first.
      // sendMessage with activeSessionId=null will create a new session.
      if (activeSessionId && pendingChatMessage.systemContext) {
        changeSession(null)
        return // re-runs on next render when activeSessionId is null
      }
      sendMessage(pendingChatMessage.message, { systemContext: pendingChatMessage.systemContext })
      if (onPendingChatMessageConsumed) {
        onPendingChatMessageConsumed()
      }
    }
  }, [pendingChatMessage, isStreaming, sendMessage, onPendingChatMessageConsumed, activeSessionId, changeSession])

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (isStreaming && !isFleetMode) return

    // In fleet mode, send as human message to the fleet
    if (isFleetMode && input.trim()) {
      sendFleetHumanMessage(input)
      return
    }

    // If slash popup is open, send the highlighted or only matching command
    if (showSlashPopup && filteredSlashCommands.length > 0) {
      const selected = filteredSlashCommands[slashIndex] || filteredSlashCommands[0]
      handleSlashSelect(selected.cmd)
      return
    }

    // If input starts with / but popup is closed (no matches), ignore
    if (input.startsWith('/') && !input.includes(' ')) {
      return
    }

    if (!input.trim() && attachments.length === 0) return
    sendMessage(input)
  }

  // Auto-resize textarea to fit content
  const autoResize = useCallback((el: HTMLTextAreaElement) => {
    if (!el) return
    el.style.height = 'auto'
    el.style.height = Math.min(el.scrollHeight, 200) + 'px'
  }, [])

  // Handle input changes for slash command popup
  const handleInputChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    const val = e.target.value
    setInput(val)
    autoResize(e.target)

    if (val === '/') {
      setShowSlashPopup(true)
      setSlashFilter('')
      setSlashIndex(0)
    } else if (val.startsWith('/') && !val.includes(' ')) {
      setShowSlashPopup(true)
      setSlashFilter(val.slice(1).toLowerCase())
      setSlashIndex(0)
    } else {
      setShowSlashPopup(false)
    }
  }

  const handleSlashSelect = (cmd: string) => {
    setShowSlashPopup(false)
    setSlashIndex(0)
    setInput('')
    if (inputRef.current) {
      inputRef.current.style.height = 'auto'
    }
    sendMessage(cmd)
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (!showSlashPopup) return

    if (e.key === 'Escape') {
      setShowSlashPopup(false)
      e.preventDefault()
      return
    }

    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setSlashIndex(prev => (prev + 1) % filteredSlashCommands.length)
      return
    }

    if (e.key === 'ArrowUp') {
      e.preventDefault()
      setSlashIndex(prev => (prev - 1 + filteredSlashCommands.length) % filteredSlashCommands.length)
      return
    }

    if (e.key === 'Tab') {
      e.preventDefault()
      if (filteredSlashCommands.length > 0) {
        const selected = filteredSlashCommands[slashIndex] || filteredSlashCommands[0]
        handleSlashSelect(selected.cmd)
      }
      return
    }
  }

  const filteredSlashCommands = useMemo(() => {
    if (!slashFilter) return slashCommands
    return slashCommands.filter(c => c.cmd.slice(1).startsWith(slashFilter))
  }, [slashFilter, slashCommands])

  const toggleRawView = (index: number) => {
    setRawViewIndices(prev => {
      const next = new Set(prev)
      if (next.has(index)) next.delete(index)
      else next.add(index)
      return next
    })
  }

  const copyToClipboard = async (content: string, index: number) => {
    await navigator.clipboard.writeText(content)
    setCopiedIndex(index)
    setTimeout(() => setCopiedIndex(null), 2000)
  }

  const filteredSessions = useMemo(() => {
    if (!sessionFilter) return sessions
    const q = sessionFilter.toLowerCase()
    return sessions.filter(s => (s.title || s.id).toLowerCase().includes(q))
  }, [sessions, sessionFilter])

  const { activityByStart, skipIndices: toolActivitySkipIndices, lastActivityStart } = useMemo(
    () => buildActivityRenderIndex(messages, { absorbTrailingSoft: isStreaming }),
    [messages, isStreaming],
  )

  const liveStreamStatus = useMemo(
    () => (isStreaming ? deriveLiveStreamStatus(messages) : null),
    [isStreaming, messages],
  )

  return (
    <>
    <div
      ref={chatLayoutRef}
      className="bg-grad-app flex flex-1 overflow-hidden"
    >
      {/* Session Sidebar */}
      <SessionSidebar
        sessions={filteredSessions}
        activeSessionId={activeSessionId}
        sessionFilter={sessionFilter}
        onSessionFilterChange={setSessionFilter}
        isLoadingSessions={isLoadingSessions}
        sidebarCollapsed={sidebarCollapsed}
        onToggleSidebar={() => setSidebarCollapsed(!sidebarCollapsed)}
        onSelectSession={(session) => handleSelectSession(session.id)}
        onNewSession={handleNewSession}
        onDeleteSession={handleDeleteSession}
        onStartFleet={() => { setFleetDialogMessage(''); setShowFleetDialog(true) }}
        theme={theme}
      />

      {/* Chat Area */}
      <div className="flex-1 flex flex-col overflow-hidden min-w-0">
        {/* Toolbar bar — always visible */}
        <div
          className="flex shrink-0 items-center justify-between border-b border-border bg-transparent px-3 py-1.5"
        >
          {/* Left side — pre-chat model picker */}
          <div className="flex items-center gap-1.5">
            {!activeSessionId && (
              <PreChatModelPicker availableProviders={preChatProviders} provider={preChatProvider} model={preChatModel} onChange={(p, m) => { setPreChatProvider(p); setPreChatModel(m) }} />
            )}
            {activeSessionId && modelStatus && modelStatus.effectiveProvider && (
              <SessionModelPicker
                sessionId={activeSessionId}
                modelStatus={modelStatus}
                availableProviders={preChatProviders}
                openSignal={modelPickerOpenSignal}
                onUpdate={setModelStatus}
              />
            )}
          </div>

          {/* Right side — Todo, Files, Apps, Model, Usage */}
          <div className="flex items-center gap-1.5">
          {/* Todo button — shows plan steps in side panel (only when the latest plan has steps) */}
          {latestPlanWithSteps && (
          <button
            onClick={() => {
              const next = !todoPanelOpen
              setTodoPanelOpen(next)
              if (next) {
                setFilePanelOpen(false)
                setAppsPanelOpen(false)
                setHarnessOpen(false)
                setManualHarnessFocus(null)
              }
            }}
            className="flex items-center gap-1.5 px-2 py-1 rounded text-xs transition-colors"
            style={{
              background: todoPanelOpen ? 'var(--accent-bg, rgba(59, 130, 246, 0.15))' : 'transparent',
              color: todoPanelOpen ? 'var(--accent-color, #60a5fa)' : 'var(--text-secondary)',
              border: todoPanelOpen ? '1px solid var(--accent-border, rgba(59, 130, 246, 0.3))' : '1px solid transparent',
            }}
            title="Todo / Plan"
          >
            <ListChecks size={13} />
            <span>Todo</span>
            <span className="px-1 py-0 rounded text-[10px] font-medium" style={{
              background: 'var(--accent-bg, rgba(59, 130, 246, 0.15))',
              color: 'var(--accent-color, #60a5fa)',
            }}>
              {(() => {
                const done = latestPlanWithSteps.steps.filter(s => s.status === 'complete').length
                return `${done}/${latestPlanWithSteps.steps.length}`
              })()}
            </span>
          </button>
          )}

          {/* Files button — shown when session has artifacts */}
          {sessionArtifacts.length > 0 && (
            <button
              onClick={() => {
                const next = !filePanelOpen
                setFilePanelOpen(next)
                setFilePanelInitialPath(null)
                if (next) {
                  setTodoPanelOpen(false)
                  setAppsPanelOpen(false)
                  setHarnessOpen(false)
                  setManualHarnessFocus(null)
                }
              }}
              className="flex items-center gap-1.5 px-2 py-1 rounded text-xs transition-colors"
              style={{
                background: filePanelOpen ? 'var(--accent-bg, rgba(59, 130, 246, 0.15))' : 'transparent',
                color: filePanelOpen ? 'var(--accent-color, #60a5fa)' : 'var(--text-secondary)',
                border: filePanelOpen ? '1px solid var(--accent-border, rgba(59, 130, 246, 0.3))' : '1px solid transparent',
              }}
              title={`${sessionArtifacts.length} file(s) generated`}
            >
              <FileText size={13} />
              <span>Files</span>
              <span className="px-1 py-0 rounded text-[10px] font-medium" style={{
                background: 'var(--accent-bg, rgba(59, 130, 246, 0.15))',
                color: 'var(--accent-color, #60a5fa)',
              }}>
                {sessionArtifacts.length}
              </span>
            </button>
          )}

          {/* Apps button — shown when session has app previews */}
          {messages.some(m => m.type === 'app_preview') && (
            <button
              onClick={() => {
                const next = !appsPanelOpen
                setAppsPanelOpen(next)
                if (next) {
                  setTodoPanelOpen(false)
                  setFilePanelOpen(false)
                  setHarnessOpen(false)
                  setManualHarnessFocus(null)
                }
              }}
              className="flex items-center gap-1.5 px-2 py-1 rounded text-xs transition-colors"
              style={{
                background: appsPanelOpen ? 'var(--accent-bg, rgba(59, 130, 246, 0.15))' : 'transparent',
                color: appsPanelOpen ? 'var(--accent-color, #60a5fa)' : 'var(--text-secondary)',
                border: appsPanelOpen ? '1px solid var(--accent-border, rgba(59, 130, 246, 0.3))' : '1px solid transparent',
              }}
              title="Generated apps"
            >
              <AppWindow size={13} />
              <span>Apps</span>
              <span className="px-1 py-0 rounded text-[10px] font-medium" style={{
                background: 'var(--accent-bg, rgba(59, 130, 246, 0.15))',
                color: 'var(--accent-color, #60a5fa)',
              }}>
                {(() => {
                  const appPreviews = messages.filter(m => m.type === 'app_preview') as AppPreviewMessage[]
                  const uniqueApps = new Set(appPreviews.map(a => a.appId || a.title))
                  return uniqueApps.size
                })()}
              </span>
            </button>
          )}

          {/* Usage popover — shows token counts */}
          <UsagePopover
            usage={tokenUsage}
            isStreaming={isStreaming}
            sessionStartTime={sessionStartTime}
          />
          </div>
        </div>
        {modelStatus && modelStatus.pinnedProvider && modelStatus.credentialsAvailable === false && (
          <ModelCredentialBanner
            pinnedProvider={modelStatus.pinnedProvider}
            pinnedModel={modelStatus.pinnedModel}
            effectiveProvider={modelStatus.effectiveProvider}
            effectiveModel={modelStatus.effectiveModel}
            onPickAnother={() => setModelPickerOpenSignal((n) => n + 1)}
          />
        )}
        {/* Fleet session header */}
        {isFleetMode && fleetInfo && (
          <div className="flex items-center justify-between px-4 py-2" style={{ borderBottom: '1px solid var(--border-color)', background: 'rgba(6, 182, 212, 0.05)' }}>
            <div className="flex items-center gap-3">
              <Users size={16} className="text-cyan-400" />
              <span className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>{fleetInfo.fleet_name}</span>
              {fleetState && (
                <span className="flex items-center gap-1.5 text-xs px-2 py-0.5 rounded-full" style={{
                  background: fleetState.state === 'waiting_for_customer' ? 'rgba(234, 179, 8, 0.15)' : fleetState.state === 'processing' ? 'rgba(6, 182, 212, 0.15)' : 'rgba(107, 114, 128, 0.15)',
                  color: fleetState.state === 'waiting_for_customer' ? '#facc15' : fleetState.state === 'processing' ? '#22d3ee' : '#9ca3af',
                }}>
                  {fleetState.state === 'processing' && <Loader size={10} className="animate-spin" />}
                  {fleetState.state === 'waiting_for_customer' && '? '}
                  {fleetState.active_agent ? `@${fleetState.active_agent}` : fleetState.state}
                </span>
              )}
            </div>
            <button
              onClick={handleExitFleet}
              className="text-xs px-2 py-1 rounded hover:bg-red-500/10 text-red-400 transition-colors"
            >
              Exit Fleet
            </button>
          </div>
        )}
        {/* Messages Area (with scroll-to-bottom button) */}
        <div className="flex-1 relative overflow-hidden">
        <div ref={scrollRef} data-testid="message-area" className="absolute inset-0 overflow-y-auto p-4 space-y-4">
          {isLoadingHistory ? (
            <div className="flex items-center justify-center py-16">
              <Loader size={24} className="animate-spin text-primary" />
            </div>
          ) : messages.length === 0 ? (
            <HomePage
              userDisplayName={userDisplayName || undefined}
              onSuggestionClick={(text) => { setInput(text); inputRef.current?.focus() }}
            />
          ) : (
            messages.map((msg, index) => {
              // Turn-level activity fold: render block at segment start; skip covered indices.
              const activityAt = activityByStart.get(index)
              if (activityAt) {
                // Trailing activity stays "live" for the whole open stream (including
                // provisional process text after the last tool), not only while a tool runs.
                const streamingActivity = isStreaming && activityAt.start === lastActivityStart
                return (
                  <ToolActivityBlock
                    key={`activity-${activityAt.start}`}
                    blockId={`activity-${activityAt.start}`}
                    steps={activityAt.steps}
                    notes={activityAt.notes}
                    streaming={streamingActivity}
                  />
                )
              }
              if (toolActivitySkipIndices.has(index)) return null

              if (msg.type === 'user') {
                const userMsg = msg as UserMessage
                return (
                  <div key={index} className="flex justify-end">
                    <div className="space-y-1 max-w-[80%]">
                      <div className="text-xs font-medium text-right" style={{ color: 'var(--text-muted)' }}>You</div>
                      <div className="chat-bubble-user p-3 rounded-lg">
                        {userMsg.content && <p className="text-sm whitespace-pre-wrap">{userMsg.content}</p>}
                        {userMsg.attachments && userMsg.attachments.length > 0 && (
                          <div className={`flex flex-wrap gap-2 ${userMsg.content ? 'mt-2' : ''}`}>
                            {userMsg.attachments.map((att, i) => (
                              <div key={i} className="flex items-center gap-1.5 px-2 py-1 rounded text-xs"
                                style={{ background: 'rgba(255,255,255,0.1)', border: '1px solid rgba(255,255,255,0.15)' }}>
                                {att.mimeType.startsWith('image/') && att.data ? (
                                  <img
                                    src={`data:${att.mimeType};base64,${att.data}`}
                                    alt={att.filename}
                                    className="w-10 h-10 rounded object-cover"
                                  />
                                ) : (
                                  <FileText size={14} style={{ color: 'var(--text-muted)' }} />
                                )}
                                <span className="max-w-[100px] truncate opacity-80">{att.filename}</span>
                              </div>
                            ))}
                          </div>
                        )}
                      </div>
                    </div>
                  </div>
                )
              }

              if (msg.type === 'agent') {
                // Collect web source URLs for citation pill
                const sourceUrls = !(msg as AgentMessage)._streaming ? collectSourceUrls(messages, index) : []

                // Check if this is the last agent message and should show embedded file viewers
                const isLastAgent = !isStreaming &&
                  !(msg as AgentMessage)._streaming &&
                  !messages.slice(index + 1).some(m => m.type === 'agent') &&
                  embeddedArtifactPaths.size > 0

                return (
                  <div key={index} className="space-y-1">
                    <div className="flex items-center justify-between">
                      <div className="text-xs font-medium" style={{ color: 'var(--text-muted)' }}>Agent</div>
                      <div className="flex gap-1">
                        <button
                          onClick={() => toggleRawView(index)}
                          className="p-1 rounded hover:bg-white/10 transition-colors"
                          title={rawViewIndices.has(index) ? 'Show formatted' : 'Show raw markdown'}
                        >
                          <Code size={14} className={rawViewIndices.has(index) ? 'text-primary' : 'text-gray-500'} />
                        </button>
                        <button
                          onClick={() => copyToClipboard(msg.content, index)}
                          className="p-1 rounded hover:bg-white/10 transition-colors"
                          title="Copy"
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
                        border: '1px solid var(--border-color)',
                      }}
                    >
                      {rawViewIndices.has(index) ? (
                        <pre className="text-sm whitespace-pre-wrap break-words font-mono" style={{ color: 'var(--text-primary)' }}>
                          {msg.content}
                        </pre>
                      ) : (() => {
                        // Strip ```astonish-report``` fences first — they are
                        // signal-only (their effect is the report_marker SSE
                        // event flipping the artifact gate), not display
                        // content. The matching disk file is rendered inline
                        // below as an EmbeddedFileViewer; showing the raw
                        // fence here would duplicate the path on screen.
                        // Mirrors stripReportMarkerFences in pkg/api/chat_runner.go.
                        const reportFenceRe = /```astonish-report\s*\n[\s\S]*?(?:\n```|$)/g
                        const cleanedContent = msg.content.replace(reportFenceRe, '').trim()
                        // Split out astonish-app code fences so they render as a stable
                        // AppCodeIndicator outside the ReactMarkdown tree. This avoids
                        // the jank of ReactMarkdown recreating the component on every
                        // streaming text update.
                        const appFenceRe = /```astonish-app\s*\n([\s\S]*?)(?:\n```|$)/
                        const match = cleanedContent.match(appFenceRe)
                        if (!match) {
                          return (
                            <div style={{ color: 'var(--text-primary)' }} className="markdown-body text-sm">
                              <ReactMarkdown remarkPlugins={[remarkGfm]} components={markdownComponents}>{cleanedContent}</ReactMarkdown>
                            </div>
                          )
                        }
                        const fenceStart = match.index!
                        const fenceEnd = fenceStart + match[0].length
                        const before = cleanedContent.slice(0, fenceStart).trimEnd()
                        const appCode = match[1]
                        const after = cleanedContent.slice(fenceEnd).trimStart()
                        return (
                          <>
                            {before && (
                              <div style={{ color: 'var(--text-primary)' }} className="markdown-body text-sm">
                                <ReactMarkdown remarkPlugins={[remarkGfm]} components={markdownComponents}>{before}</ReactMarkdown>
                              </div>
                            )}
                            <AppCodeIndicator
                              streaming={!!(msg as AgentMessage)._streaming}
                              code={appCode}
                              expanded={expandedCodeIndices.has(index)}
                              onToggle={() => setExpandedCodeIndices(prev => {
                                const next = new Set(prev)
                                if (next.has(index)) next.delete(index)
                                else next.add(index)
                                return next
                              })}
                            />
                            {after && (
                              <div style={{ color: 'var(--text-primary)' }} className="markdown-body text-sm">
                                <ReactMarkdown remarkPlugins={[remarkGfm]} components={markdownComponents}>{after}</ReactMarkdown>
                              </div>
                            )}
                          </>
                        )
                      })()}
                    </div>
                    {/* Last-turn report + video placeholders (full UI in harness panel) */}
                    {isLastAgent && (
                      <div className="mt-3 space-y-3">
                        {sessionArtifacts.filter(a => reportHarnessPaths.has(a.path)).map(a => {
                          const artifactMsgIdx = messages.findIndex(
                            m => m.type === 'artifact' && (m as ArtifactMessage).path === a.path,
                          )
                          const focus: HarnessFocus = {
                            kind: 'report',
                            path: a.path,
                            messageIndex: artifactMsgIdx >= 0 ? artifactMsgIdx : messages.length,
                          }
                          return (
                            <HarnessPlaceholder
                              key={a.path}
                              focus={focus}
                              title={a.reportTitle || a.fileName || a.path}
                              subtitle="Open in side panel"
                              isFocused={harnessOpen && harnessFocusEquals(effectiveHarnessFocus, focus)}
                              onOpen={openHarness}
                            />
                          )
                        })}
                        {sessionArtifacts.filter(a => videoHarnessPaths.has(a.path)).map(a => {
                          const artifactMsgIdx = messages.findIndex(
                            m => m.type === 'artifact' && (m as ArtifactMessage).path === a.path,
                          )
                          const focus: HarnessFocus = {
                            kind: 'video',
                            path: a.path,
                            messageIndex: artifactMsgIdx >= 0 ? artifactMsgIdx : messages.length,
                          }
                          return (
                            <HarnessPlaceholder
                              key={a.path}
                              focus={focus}
                              title={a.fileName || a.path}
                              subtitle="Open in side panel"
                              isFocused={harnessOpen && harnessFocusEquals(effectiveHarnessFocus, focus)}
                              onOpen={openHarness}
                            />
                          )
                        })}
                      </div>
                    )}
                    {sourceUrls.length > 0 && <SourceCitations urls={sourceUrls} />}
                  </div>
                )
              }

              if (msg.type === 'tool_call' || msg.type === 'tool_result') {
                // Covered by activity fold above; unpaired leftovers should not render.
                return null
              }

              if (msg.type === 'browser_handoff') {
                const handoff = msg as BrowserHandoffMessage
                const focus: HarnessFocus = { kind: 'browser_handoff', messageIndex: index }
                return (
                  <div key={index}>
                    <HarnessPlaceholder
                      focus={focus}
                      title={handoff.reason || handoff.pageTitle || 'Browser session'}
                      subtitle={handoff.pageUrl || 'Open in side panel'}
                      isFocused={harnessOpen && harnessFocusEquals(effectiveHarnessFocus, focus)}
                      onOpen={openHarness}
                    />
                  </div>
                )
              }

              if (msg.type === 'image') {
                return (
                  <div key={index} className="my-2">
                    <img
                      src={`data:${msg.mimeType};base64,${msg.data}`}
                      alt="Screenshot"
                      className="rounded-lg max-w-full"
                      style={{
                        maxHeight: '500px',
                        border: '1px solid var(--border-color)',
                      }}
                    />
                  </div>
                )
              }

              if (msg.type === 'error') {
                return (
                  <div key={index} className="p-3 rounded-lg bg-red-500/10 border border-red-500/20 text-red-400 text-sm">
                    Error: {msg.content}
                  </div>
                )
              }

              if (msg.type === 'error_info') {
                return (
                  <div key={index} className="my-3 p-4 rounded-lg bg-red-500/5 border border-red-500/20 space-y-3">
                    <div className="flex items-center gap-2 text-red-400 font-medium">
                      <span>&#x2715;</span> {String(msg.title)}
                    </div>
                    {!!msg.reason && <p className="text-sm text-gray-300 pl-4">{String(msg.reason)}</p>}
                    {!!msg.suggestion && (
                      <div className="pl-4">
                        <p className="text-sm font-medium text-yellow-400">Suggestion:</p>
                        <p className="text-sm text-yellow-300/80">{String(msg.suggestion)}</p>
                      </div>
                    )}
                    {!!msg.originalError && (
                      <details className="pl-4">
                        <summary className="text-xs text-gray-500 cursor-pointer hover:text-gray-400">Raw Error</summary>
                        <pre className="text-xs text-gray-500 mt-1 whitespace-pre-wrap break-words max-h-32 overflow-y-auto">
                          {String(msg.originalError)}
                        </pre>
                      </details>
                    )}
                  </div>
                )
              }

              if (msg.type === 'approval') {
                return (
                  <div key={index} className="my-2 p-3 rounded-lg bg-yellow-500/10 border border-yellow-500/20">
                    <p className="text-sm font-medium text-yellow-400 mb-2">
                      Approve tool: <code className="bg-yellow-500/20 px-1.5 rounded">{String(msg.toolName)}</code>
                    </p>
                    {!!msg.options && (msg.options as unknown[]).length > 0 && (
                      <div className="flex gap-2 flex-wrap">
                        {(msg.options as unknown[]).map((opt, i) => (
                          <button
                            key={i}
                            onClick={() => sendMessage(String(opt))}
                            className="px-3 py-1.5 text-xs bg-yellow-500/20 hover:bg-yellow-500/30 text-yellow-300 border border-yellow-500/30 rounded transition-colors"
                          >
                            {String(opt)}
                          </button>
                        ))}
                      </div>
                    )}
                  </div>
                )
              }

              if (msg.type === 'auto_approved') {
                return (
                  <div key={index} className="flex items-center gap-2 px-3 py-2 my-1 text-sm">
                    <span className="flex items-center gap-1.5 px-2 py-1 rounded bg-green-500/10 border border-green-500/20 text-green-400">
                      <span>&#10003;</span> Auto-approved: <code className="bg-green-500/20 px-1.5 py-0.5 rounded font-mono text-xs">{msg.toolName as string}</code>
                    </span>
                  </div>
                )
              }

              if (msg.type === 'network_denial') {
                const denialMsg = msg as NetworkDenialMessage
                return (
                  <NetworkDenialPrompt
                    key={index}
                    denials={denialMsg.denials}
                    sandboxName={denialMsg.sandbox_name}
                    sessionId={activeSessionId || ''}
                    onApproved={(host) => {
                      sendMessage(`I just approved network access to ${host}. Please retry the previous command that was blocked by the proxy.`)
                    }}
                  />
                )
              }

              if (msg.type === 'thinking') {
                const text = (msg.content as string) || ''
                if (!text) return null
                return (
                  <div key={index} className="thinking-note">
                    {text}
                  </div>
                )
              }

              if (msg.type === 'fleet_execution') {
                return <FleetExecutionPanel key={index} data={msg as FleetExecutionMessage} />
              }

              if (msg.type === 'plan') {
                return <PlanPanel key={index} data={msg as PlanMessage} />
              }

              if (msg.type === 'subtask_execution') {
                return <TaskPlanPanel key={index} data={msg as SubTaskExecutionMessage} sessionId={activeSessionId || undefined} />
              }

              if (msg.type === 'fleet_message') {
                const fMsg = msg as FleetMessageItem
                const isHuman = fMsg.sender === 'customer'
                const isSystem = fMsg.sender === 'system'
                const color = getAgentColor(fMsg.sender)

                if (isHuman) {
                  return (
                    <div key={index} className="flex justify-end">
                      <div className="space-y-1 max-w-[80%]">
                        <div className="text-xs font-medium text-right" style={{ color: 'var(--text-muted)' }}>You</div>
                        <div className="chat-bubble-user p-3 rounded-lg">
                          <p className="text-sm whitespace-pre-wrap">{fMsg.text}</p>
                        </div>
                      </div>
                    </div>
                  )
                }

                return (
                  <div key={index} className="space-y-1">
                    <div className="flex items-center gap-2">
                      <span
                        className="text-xs font-bold px-1.5 py-0.5 rounded"
                        style={{ background: color.bg, color: color.text, border: `1px solid ${color.border}` }}
                      >
                        @{fMsg.sender}
                      </span>
                      {isSystem && <span className="text-[10px]" style={{ color: 'var(--text-muted)' }}>system</span>}
                      {fMsg.mentions && fMsg.mentions.length > 0 && (
                        <span className="text-[10px]" style={{ color: 'var(--text-muted)' }}>
                          &rarr; {fMsg.mentions.map(m => `@${m}`).join(', ')}
                        </span>
                      )}
                    </div>
                    <div
                      className="p-4 rounded-lg max-w-[90%]"
                      style={{
                        background: color.bg,
                        border: `1px solid ${color.border}`,
                      }}
                    >
                      <div style={{ color: 'var(--text-primary)' }} className="markdown-body text-sm">
                        <ReactMarkdown remarkPlugins={[remarkGfm]} components={markdownComponents}>{fMsg.text}</ReactMarkdown>
                      </div>
                    </div>
                  </div>
                )
              }

              if (msg.type === 'system') {
                return (
                  <div key={index} className="my-2 p-4 rounded-lg" style={{
                    background: 'color-mix(in oklab, var(--brand) 8%, transparent)',
                    border: '1px solid color-mix(in oklab, var(--brand) 20%, transparent)',
                  }}>
                    <div className="flex items-center gap-2 mb-2">
                      <Info size={14} style={{ color: 'rgba(129, 140, 248, 0.9)' }} />
                      <span className="text-xs font-medium" style={{ color: 'rgba(129, 140, 248, 0.9)' }}>System</span>
                    </div>
                    <div style={{ color: 'var(--text-primary)' }} className="markdown-body text-sm">
                      <ReactMarkdown remarkPlugins={[remarkGfm]} components={markdownComponents}>{msg.content as string}</ReactMarkdown>
                    </div>
                  </div>
                )
              }

              if (msg.type === 'retry') {
                return (
                  <div key={index} className="flex items-center gap-2 px-3 py-2 my-1 text-sm">
                    <RotateCcw size={14} className="text-orange-400" />
                    <span className="text-orange-400 font-medium">Retry {msg.attempt as string}/{msg.maxRetries as string}:</span>
                    <span className="text-gray-400">{msg.reason as string}</span>
                  </div>
                )
              }

              if (msg.type === 'artifact') {
                const artifactMsg = msg as ArtifactMessage
                // Suppress inline card when this artifact is embedded in the ResultCard
                if (embeddedArtifactPaths.has(artifactMsg.path)) return null
                if (slideshowVideoPaths(messages).has(artifactMsg.path)) return null
                return (
                  <div key={index}>
                    <ArtifactCard
                      data={artifactMsg}
                      sessionId={activeSessionId}
                      onOpenInPanel={(path) => {
                        setFilePanelInitialPath(path)
                        setFilePanelOpen(true)
                      }}
                    />
                  </div>
                )
              }

              if (msg.type === 'app_preview') {
                const appMsg = msg as AppPreviewMessage
                // Collect all app_preview versions for this app (by appId, fallback to title)
                const allVersions = messages.filter(
                  (m): m is AppPreviewMessage => m.type === 'app_preview' && (
                    appMsg.appId
                      ? (m as AppPreviewMessage).appId === appMsg.appId
                      : (m as AppPreviewMessage).title === appMsg.title
                  )
                )
                // Only render the placeholder on the LAST version's message to avoid duplicates
                const lastVersion = allVersions[allVersions.length - 1]
                if (lastVersion && lastVersion !== appMsg) {
                  return null
                }
                const appId = appMsg.appId || appMsg.title
                const focus: HarnessFocus = {
                  kind: 'app',
                  appId,
                  messageIndex: index,
                }
                return (
                  <div key={index}>
                    <HarnessPlaceholder
                      focus={focus}
                      title={appMsg.title || 'App'}
                      subtitle={allVersions.length > 1 ? `v${appMsg.version} · ${allVersions.length} versions` : `v${appMsg.version}`}
                      isFocused={harnessOpen && harnessFocusEquals(effectiveHarnessFocus, focus)}
                      onOpen={openHarness}
                    />
                  </div>
                )
              }

              if (msg.type === 'distill_preview') {
                const previewMsg = msg as DistillPreviewMessage
                const focus: HarnessFocus = { kind: 'distill', messageIndex: index }
                return (
                  <div key={index}>
                    <HarnessPlaceholder
                      focus={focus}
                      title={previewMsg.flowName || 'Flow draft'}
                      subtitle={previewMsg.description || undefined}
                      isFocused={harnessOpen && harnessFocusEquals(effectiveHarnessFocus, focus)}
                      onOpen={openHarness}
                    />
                  </div>
                )
              }

              if (msg.type === 'tutorial_blueprint_preview') {
                const previewMsg = msg as TutorialBlueprintPreviewMessage
                const focus: HarnessFocus = { kind: 'tutorial_blueprint', messageIndex: index }
                return (
                  <div key={index}>
                    <HarnessPlaceholder
                      focus={focus}
                      title={previewMsg.title || 'Tutorial blueprint'}
                      subtitle={previewMsg.suite || undefined}
                      isFocused={harnessOpen && harnessFocusEquals(effectiveHarnessFocus, focus)}
                      onOpen={openHarness}
                    />
                  </div>
                )
              }

              if (msg.type === 'tutorial_blueprint_approved') {
                const approved = msg as TutorialBlueprintApprovedMessage
                return (
                  <div key={index} className="my-3 rounded-xl overflow-hidden w-full max-w-2xl px-4 py-3"
                    style={{ border: '1px solid var(--border-color)', background: 'var(--bg-secondary)' }}>
                    <div className="flex items-center gap-2 mb-1">
                      <Check size={16} style={{ color: 'var(--success)' }} />
                      <span className="text-sm font-semibold" style={{ color: 'var(--success)' }}>
                        Blueprint approved{approved.drillName ? `: ${approved.drillName}` : ''}
                      </span>
                    </div>
                    <p className="text-xs" style={{ color: 'var(--text-secondary)' }}>
                      {approved.content || 'Screen scenes converted to tutorial drill YAML. Refine selectors, then validate_drill / save_drill.'}
                    </p>
                  </div>
                )
              }

              if (msg.type === 'tutorial_scene_slideshow') {
                const slideshowMsg = msg as TutorialSceneSlideshowMessage
                const focus: HarnessFocus = { kind: 'tutorial_slideshow', messageIndex: index }
                return (
                  <div key={index}>
                    <HarnessPlaceholder
                      focus={focus}
                      title={slideshowMsg.title || slideshowMsg.drill || 'Tutorial scenes'}
                      subtitle={slideshowMsg.suite ? `Suite: ${slideshowMsg.suite}` : 'Open in side panel'}
                      isFocused={harnessOpen && harnessFocusEquals(effectiveHarnessFocus, focus)}
                      onOpen={openHarness}
                    />
                  </div>
                )
              }

              if (msg.type === 'distill_saved') {
                const savedMsg = msg as DistillSavedMessage
                return (
                  <div key={index} className="my-3 rounded-xl overflow-hidden w-full max-w-2xl"
                    style={{ border: '1px solid var(--border-color)', background: 'var(--bg-secondary)', boxShadow: 'var(--shadow-soft)' }}>
                    <div className="px-4 py-3">
                      <div className="flex items-center gap-2 mb-2">
                        <Check size={16} style={{ color: 'var(--success)' }} />
                        <span className="text-sm font-semibold" style={{ color: 'var(--success)' }}>Flow Saved</span>
                      </div>
                      <div className="text-xs mb-2" style={{ color: 'var(--text-secondary)' }}>
                        Saved to: <code className="px-1.5 py-0.5 rounded" style={{ background: 'var(--bg-tertiary)', color: 'var(--brand)' }}>{savedMsg.filePath}</code>
                      </div>
                      <div className="text-xs mb-2" style={{ color: 'var(--text-muted)' }}>Run with:</div>
                      <div className="flex items-center gap-2">
                        <pre className="flex-1 p-2 rounded-lg text-[11px] overflow-x-auto" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}>
                          <code>{savedMsg.runCommand}</code>
                        </pre>
                        <button
                          onClick={() => navigator.clipboard.writeText(savedMsg.runCommand)}
                          className="flex-shrink-0 p-1.5 rounded transition-colors cursor-pointer"
                          style={{ color: 'var(--brand)' }}
                          title="Copy command"
                        >
                          <Copy size={13} />
                        </button>
                      </div>
                    </div>
                  </div>
                )
              }

              if (msg.type === 'app_saved') {
                const savedMsg = msg as AppSavedMessage
                return (
                  <div key={index} className="my-3 rounded-xl overflow-hidden w-full max-w-2xl"
                    style={{ border: '1px solid var(--border-color)', background: 'var(--bg-secondary)', boxShadow: 'var(--shadow-soft)' }}>
                    <div className="px-4 py-3">
                      <div className="flex items-center gap-2 mb-2">
                        <Check size={16} style={{ color: 'var(--success)' }} />
                        <span className="text-sm font-semibold" style={{ color: 'var(--success)' }}>App Saved</span>
                      </div>
                      <div className="text-xs" style={{ color: 'var(--text-secondary)' }}>
                        Saved as <code className="px-1.5 py-0.5 rounded" style={{ background: 'var(--bg-tertiary)', color: 'var(--brand)' }}>{savedMsg.name}</code> — view it in the Apps tab.
                      </div>
                    </div>
                  </div>
                )
              }

              return null
            })
          )}

          {/* Streaming indicator */}
          {isStreaming && !isFleetMode && messages.length > 0 && messages[messages.length - 1]?.type !== 'fleet_execution' && (
            <div className="flex items-center gap-2 px-3 py-2 rounded-lg bg-primary/10 border border-primary/20 w-fit max-w-full">
              <Loader size={14} className="text-primary animate-spin shrink-0" />
              <span className="text-xs text-primary truncate">{liveStreamStatus || 'Thinking…'}</span>
            </div>
          )}
        </div>
        {/* Scroll to bottom button */}
        {showScrollButton && (
          <button
            onClick={scrollToBottom}
            className="absolute bottom-3 right-5 z-10 p-2 rounded-full shadow-lg transition-all hover:scale-105 cursor-pointer"
            style={{
              background: 'var(--bg-secondary)',
              border: '1px solid var(--border-color)',
              color: 'var(--text-secondary)',
            }}
            title="Scroll to bottom"
          >
            <ChevronDown size={16} />
          </button>
        )}
        </div>

        {/* Input Area — equal padding above/below the pill; border sits outside that space */}
        <div
          className="relative border-t border-border bg-transparent px-4 pt-4 pb-4 sm:px-6"
          onDragOver={handleDragOver}
          onDragLeave={handleDragLeave}
          onDrop={handleDrop}
        >
          {/* Drag overlay */}
          {isDragOver && (
            <div className="absolute inset-0 z-50 flex items-center justify-center rounded-lg border-2 border-dashed border-primary bg-primary/10">
              <div className="text-sm font-medium text-primary">
                Drop files here
              </div>
            </div>
          )}

          {/* Slash command popup */}
          {showSlashPopup && filteredSlashCommands.length > 0 && (
            <div className="absolute bottom-full right-4 left-4 z-50 mb-1 overflow-hidden rounded-lg border border-border bg-card shadow-[var(--shadow-elevated)]">
              {filteredSlashCommands.map(({ cmd, desc }, i) => (
                <button
                  key={cmd}
                  type="button"
                  onClick={() => handleSlashSelect(cmd)}
                  className={cn(
                    'flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors',
                    i === slashIndex ? 'bg-primary/15' : 'hover:bg-primary/10'
                  )}
                >
                  <code className="font-mono text-sm text-primary">{cmd}</code>
                  <span className="text-xs text-muted-foreground">{desc}</span>
                </button>
              ))}
            </div>
          )}

          {/* Attachment preview strip */}
          {attachments.length > 0 && (
            <div className="flex items-center gap-2 overflow-x-auto pb-2">
              {attachments.map(att => (
                <div
                  key={att.id}
                  className="relative flex shrink-0 items-center gap-2 rounded-lg border border-border bg-secondary px-2 py-1.5 text-xs"
                >
                  {att.preview ? (
                    <img src={att.preview} alt={att.name} className="size-8 rounded object-cover" />
                  ) : (
                    <FileText size={16} className="text-muted-foreground" />
                  )}
                  <span className="max-w-[120px] truncate text-secondary-foreground">{att.name}</span>
                  <button
                    type="button"
                    onClick={() => removeAttachment(att.id)}
                    className="rounded p-0.5 text-muted-foreground transition-colors hover:bg-destructive/20 hover:text-destructive"
                    title="Remove"
                  >
                    <X size={12} />
                  </button>
                </div>
              ))}
              {attachments.length > 1 && (
                <button
                  type="button"
                  onClick={clearAttachments}
                  className="shrink-0 rounded px-2 py-1 text-xs text-muted-foreground transition-colors hover:bg-destructive/20 hover:text-destructive"
                >
                  Clear all
                </button>
              )}
            </div>
          )}

          {/* Hidden file input */}
          <input
            ref={fileInputRef}
            type="file"
            multiple
            accept={ACCEPTED_EXTENSIONS}
            onChange={handleFileSelect}
            className="hidden"
          />

          <form
            onSubmit={handleSubmit}
            className="flex items-end gap-2.5 rounded-[var(--radius-lg)] border border-[color:var(--border-soft)] bg-[color:var(--input-bg)] p-2.5 pl-3.5 shadow-[var(--input-shadow)]"
          >
            {isStreaming && (
              <Button
                type="button"
                variant="destructive"
                onClick={handleStop}
                className="size-8 shrink-0 rounded-[10px] p-0"
                title="Stop"
              >
                <Square size={14} />
              </Button>
            )}
            {hasSessionMemories && (
              <Button
                type="button"
                variant="ghost"
                className="size-8 shrink-0 rounded-[10px] p-0"
                title="View session memories — click to review and edit"
                onClick={() => setShowMemoryPanel(true)}
              >
                <Brain size={16} className="text-primary" />
              </Button>
            )}
            <Button
              type="button"
              variant="ghost"
              onClick={() => fileInputRef.current?.click()}
              disabled={isStreaming && !isFleetMode}
              className="size-8 shrink-0 rounded-[10px] p-0"
              title="Attach files (images, PDFs, text files)"
            >
              <Paperclip size={16} className={attachments.length > 0 ? 'text-primary' : 'text-muted-foreground'} />
            </Button>
            <Textarea
              data-testid="chat-input"
              ref={inputRef}
              value={input}
              onChange={handleInputChange}
              onKeyDown={(e) => {
                // Enter without Shift submits the form
                if (e.key === 'Enter' && !e.shiftKey) {
                  e.preventDefault()
                  if (showSlashPopup && filteredSlashCommands.length > 0) {
                    const selected = filteredSlashCommands[slashIndex] || filteredSlashCommands[0]
                    handleSlashSelect(selected.cmd)
                  } else if (isFleetMode && input.trim()) {
                    sendFleetHumanMessage(input)
                  } else if (!isStreaming && (input.trim() || attachments.length > 0)) {
                    // Reuse slash validation from handleSubmit
                    if (input.startsWith('/') && !input.includes(' ')) return
                    sendMessage(input)
                  }
                  return
                }
                handleKeyDown(e)
              }}
              onPaste={(e) => {
                // Handle pasted files (e.g., screenshots from clipboard)
                const items = e.clipboardData?.items
                if (items) {
                  const files: File[] = []
                  for (let i = 0; i < items.length; i++) {
                    const item = items[i]
                    if (item.kind === 'file') {
                      const file = item.getAsFile()
                      if (file) files.push(file)
                    }
                  }
                  if (files.length > 0) {
                    e.preventDefault()
                    validateAndAddFiles(files)
                  }
                }
              }}
              disabled={isStreaming && !isFleetMode}
              placeholder={
                isFleetMode
                  ? fleetState?.state === 'waiting_for_customer'
                    ? `${fleetState.active_agent || 'An agent'} is waiting for your response...`
                    : fleetState?.state === 'processing'
                      ? `${fleetState.active_agent || 'Agent'} is working... You can still type.`
                      : 'Type a message to the team...'
                  : isStreaming
                    ? 'Agent is responding...'
                    : attachments.length > 0
                      ? 'Add a message or press Enter to send attachments...'
                      : 'Say hi, or drop a task in here…'
              }
              rows={1}
              className="min-h-8 max-h-[200px] flex-1 resize-none overflow-y-auto border-0 bg-transparent px-1 py-1.5 text-[13.5px] shadow-none focus-visible:border-0 focus-visible:ring-0 dark:bg-transparent dark:shadow-none"
            />
            <Button
              data-testid="send-button"
              type="submit"
              disabled={(isStreaming && !isFleetMode) || (!input.trim() && attachments.length === 0)}
              className="send-gradient size-8 shrink-0 rounded-[10px] p-0 text-white hover:opacity-90"
              aria-label="Send message"
            >
              <Send size={15} />
            </Button>
          </form>
        </div>
      </div>

      {/* Harness panel — ~1080px preferred, resizable; chat keeps a min floor */}
      {harnessOpen && effectiveHarnessFocus && (
        <HarnessPanel
          focus={effectiveHarnessFocus}
          messages={messages}
          sessionArtifacts={sessionArtifacts}
          activeAppId={activeAppId}
          sessionId={activeSessionId}
          theme={theme}
          containerWidth={chatLayoutWidth}
          sidebarWidth={sidebarCollapsed ? SIDEBAR_COLLAPSED_WIDTH : SIDEBAR_EXPANDED_WIDTH}
          onClose={closeHarness}
          onAppSave={(name: string) => sendMessage(`__app_save__:${name}`)}
          onDistillSave={() => sendMessage('__distill_save__')}
          onDistillRequestChanges={() => {
            if (inputRef.current) {
              inputRef.current.focus()
              inputRef.current.placeholder = 'Describe what you want to change in the flow...'
            }
          }}
          onDistillCancel={() => sendMessage('__distill_cancel__')}
          onTutorialApprove={() => sendMessage('__tutorial_blueprint_approve__')}
          onTutorialRequestChanges={() => {
            if (inputRef.current) {
              inputRef.current.focus()
              inputRef.current.placeholder = 'Describe how to revise the Scene | Voiceover | Visual blueprint...'
            }
          }}
          onTutorialCancel={() => sendMessage('__tutorial_blueprint_cancel__')}
          onOpenReportInFiles={(path) => {
            setFilePanelInitialPath(path)
            setFilePanelOpen(true)
            setHarnessOpen(false)
            setManualHarnessFocus(null)
          }}
          onBrowserDone={(messageIndex) => {
            setMessages((prev: ChatMsg[]) => prev.map((m, i) =>
              i === messageIndex ? { ...m, type: 'browser_handoff' } as BrowserHandoffMessage : m
            ))
          }}
        />
      )}

      {/* File Panel — right side split panel for viewing artifacts */}
      {filePanelOpen && sessionArtifacts.length > 0 && (
        <FilePanel
          artifacts={sessionArtifacts}
          initialPath={filePanelInitialPath}
          sessionId={activeSessionId}
          onClose={() => setFilePanelOpen(false)}
        />
      )}

      {/* Todo Panel — right side split panel for plan steps */}
      {todoPanelOpen && (
        <TodoPanel
          messages={messages}
          onClose={() => setTodoPanelOpen(false)}
        />
      )}

      {/* Apps Panel — right side split panel for generated apps */}
      {appsPanelOpen && (
        <AppsPanel
          messages={messages}
          activeAppId={activeAppId}
          onClose={() => setAppsPanelOpen(false)}
        />
      )}
    </div>

    {/* Fleet start dialog */}
    {showFleetDialog && (
      <FleetStartDialog
        onStart={handleFleetStart}
        onCancel={() => { setFleetDialogMessage(''); setShowFleetDialog(false) }}
        defaultMessage={fleetDialogMessage}
      />
    )}

    {/* Fleet template picker for bare /fleet-plan command */}
    {showTemplatePicker && (
      <FleetTemplatePicker
        onSelect={(templateKey) => {
          setShowTemplatePicker(false)
          sendMessage(`/fleet-plan ${templateKey}`)
        }}
        onCancel={() => setShowTemplatePicker(false)}
      />
    )}

    {/* Session Memory Panel */}
    {showMemoryPanel && activeSessionId && (
      <SessionMemoryPanel
        sessionId={activeSessionId}
        isConsolidating={isConsolidatingMemories}
        onClose={() => setShowMemoryPanel(false)}
      />
    )}
    </>
  )
}
