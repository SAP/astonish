import { useState, useCallback, useMemo, useEffect, useRef, lazy, Suspense } from 'react'
import type { Node, Edge } from '@xyflow/react'
import * as yaml from 'js-yaml'
import TopBar from './components/TopBar'
import Sidebar from './components/Sidebar'
import ChatPanel from './components/ChatPanel'
import Header from './components/Header'
import NodeEditor from './components/NodeEditor'
import EdgeEditor from './components/EdgeEditor'
import CreateAgentModal from './components/CreateAgentModal'
import ConfirmDeleteModal from './components/ConfirmDeleteModal'
import AIChatPanel from './components/AIChatPanel'
import SetupWizard from './components/SetupWizard'
import LoginPage from './components/LoginPage'
import MCPDependenciesPanel from './components/MCPDependenciesPanel'
import InstallMcpModal from './components/InstallMcpModal'
import { omitEmptySensitiveEnv } from './components/credentials/credentialPlaceholders'
import { useTheme } from './hooks/useTheme'
import { useAuth } from './hooks/useAuth'
import { useHashRouter, buildPath } from './hooks/useHashRouter'
import { setActiveTeam as setActiveTeamContext, getActiveTeam as getStoredTeam, onTeamRejected, onAuthExpired, teamFetch, setPersonalMemoryMode as setPersonalMemoryModeContext } from './api/teamContext'
import { yamlToFlowAsync, extractLayout } from './utils/yamlToFlow'
import { addStandaloneNode, addConnection, removeConnection, updateNode, orderYamlKeys } from './utils/flowToYaml'
import { fetchAgents, fetchAgent, saveAgent, deleteAgent, fetchTools, checkMcpDependencies, installMcpServer, getMcpStoreServer, installInlineMcpServer, publishFlowToTeam, forkFlowToPersonal } from './api/agents'
import type { Agent, Tool, McpDependencyCheckResult } from './api/agents'
import { publishAppToTeam, forkAppToPersonal } from './api/apps'
import type { AppListItem } from './api/apps'
import { fetchSandboxStatus } from './api/sandbox'
import type { SandboxStatus } from './api/sandbox'
import { snakeToTitleCase } from './utils/formatters'
import ToastNotification from './components/ToastNotification'
import UpgradeDialog from './components/UpgradeDialog'
import {
  defaultYaml,
  type FlowNode,
  type FlowEdge,
  type ToastData,
  type UpgradeDialogData,
  type UpdateInfo,
  type ChatMessage,
  type FocusedNodeData,
  type VariableGroup,
  type InstallModalServerData,
  type AppAgent,
  type YamlData,
} from './components/AppTypes'
import './index.css'

// Lazy-loaded components for code-splitting.
// These bring in large dependencies (React Flow, CodeMirror, etc.)
// and are only rendered conditionally.
const FlowCanvas = lazy(() => import('./components/FlowCanvas'))
const YamlDrawer = lazy(() => import('./components/YamlDrawer'))
const SettingsPage = lazy(() => import('./components/SettingsPage'))
const StudioChat = lazy(() => import('./components/StudioChat'))
const FleetView = lazy(() => import('./components/FleetView'))
const DrillView = lazy(() => import('./components/DrillView'))
const AppsView = lazy(() => import('./components/AppsView'))

function App() {
  const { theme, toggleTheme, refreshBrandTheme } = useTheme()
  const { path, navigate, replaceHash } = useHashRouter()
  const pathRef = useRef(path)
  useEffect(() => { pathRef.current = path }, [path])
  const handleAgentSelectInternalRef = useRef<(agent: Agent, updateUrl?: boolean) => Promise<void>>(async () => {})

  // Platform mode detection: check if the platform auth endpoint exists.
  // In personal mode (file backend), this endpoint doesn't exist (404).
  // In platform mode (postgres backend), it returns setup status.
  const [isPlatformMode, setIsPlatformMode] = useState(false)
  const [isPlatformChecked, setIsPlatformChecked] = useState(false)
  // Personal memory mode: when true, the agent saves memories to personal scope by default
  const [personalMemoryMode, setPersonalMemoryMode] = useState(() => {
    try { return localStorage.getItem('astonish_personal_memory_mode') === 'true' } catch { return false }
  })
  const togglePersonalMemoryMode = useCallback(() => {
    setPersonalMemoryMode(prev => {
      const next = !prev
      try { localStorage.setItem('astonish_personal_memory_mode', String(next)) } catch { /* ignore */ }
      setPersonalMemoryModeContext(next)
      return next
    })
  }, [])
  useEffect(() => {
    fetch('/api/auth/setup-status')
      .then(async res => {
        if (res.ok) {
          setIsPlatformMode(true)
        }
      })
      .catch(() => {})
      .finally(() => setIsPlatformChecked(true))
  }, [])

  // Auth hook (only active in platform mode)
  const auth = useAuth(isPlatformMode && isPlatformChecked ? true : false)

  // Re-resolve brand theme after login (user preference) or personal-mode boot.
  useEffect(() => {
    if (!isPlatformChecked) return
    if (isPlatformMode && !auth.isAuthenticated) return
    void refreshBrandTheme()
  }, [isPlatformChecked, isPlatformMode, auth.isAuthenticated, refreshBrandTheme])

  // Platform team list — loaded once after authentication
  const [platformTeams, setPlatformTeams] = useState<{ slug: string; name: string }[] | null>(null)
  const [activeTeam, setActiveTeam] = useState<string | null>(getStoredTeam)
  useEffect(() => {
    if (!isPlatformMode || !auth.isAuthenticated) return
    // Set initial active team: prefer localStorage, fall back to JWT default
    if (!activeTeam && auth.team) {
      setActiveTeam(auth.team)
      setActiveTeamContext(auth.team)
    }
    // Fetch team list
    import('./api/platform').then(mod => {
      mod.fetchTeams().then(teams => {
        const mapped = teams.map((t: any) => ({ slug: t.slug, name: t.name }))
        setPlatformTeams(mapped)
        // If no team is active yet (e.g. first login, or auth.team was null),
        // default to the first available team.
        if (!activeTeam && !auth.team && mapped.length > 0) {
          setActiveTeam(mapped[0].slug)
          setActiveTeamContext(mapped[0].slug)
        }
      }).catch(() => {})
    })
  }, [isPlatformMode, auth.isAuthenticated, auth.team, activeTeam])

  const handleTeamChange = useCallback((teamSlug: string) => {
    setActiveTeam(teamSlug)
    setActiveTeamContext(teamSlug)

    // Clear the selected flow — it belongs to the previous team.
    setSelectedAgent(null)
    setYamlContent(defaultYaml)

    // If viewing a specific agent, navigate back to the flows home.
    if (path.view === 'agent') {
      navigate(buildPath('canvas'))
    }

    // If on a team-scoped settings page, update the URL to the new team
    if (path.view === 'settings' && path.params.section === 'team') {
      const tab = path.params.teamTab || 'members'
      replaceHash(buildPath('settings', { section: `team/${teamSlug}/${tab}` }))
    }
  }, [path.view, path.params, navigate, replaceHash])

  // Switch to a different organization — tokens are re-issued, teams reload.
  const handleOrgSwitch = useCallback(async (orgSlug: string) => {
    try {
      await auth.switchOrg(orgSlug)
      // Reset team state — new org has different teams
      setActiveTeam(null)
      setActiveTeamContext(null)
      setPlatformTeams(null)
      setSelectedAgent(null)
      setYamlContent(defaultYaml)
      // Reload teams for the new org
      import('./api/platform').then(mod => {
        mod.fetchTeams().then(teams => {
          const mapped = teams.map((t: any) => ({ slug: t.slug, name: t.name }))
          setPlatformTeams(mapped)
          // Auto-select first team (or use the JWT default which auth.switchOrg resets to null)
          if (mapped.length > 0) {
            setActiveTeam(mapped[0].slug)
            setActiveTeamContext(mapped[0].slug)
          }
        }).catch(() => {})
      })
      // Navigate to chat view to avoid stale content from previous org
      navigate(buildPath('chat'))
    } catch (err: any) {
      setToast({ message: err.message || 'Failed to switch organization', type: 'error' })
    }
  }, [auth, navigate])

  // Handle middleware team rejection (user removed from a team while active).
  useEffect(() => {
    onTeamRejected((rejectedSlug) => {
      setActiveTeam(null)
      setToast({ message: `You are no longer a member of team "${rejectedSlug}". Switched to personal scope.`, type: 'error' })
      // Re-fetch teams to update the dropdown.
      import('./api/platform').then(mod => {
        mod.fetchTeams().then(teams => {
          setPlatformTeams(teams.map(t => ({ slug: t.slug, name: t.name })))
        }).catch(() => {})
      })
    })
  }, [])

  // When silent token refresh fails after a 401, clear auth and show login.
  useEffect(() => {
    onAuthExpired(() => {
      auth.markSessionExpired()
    })
  }, [auth.markSessionExpired])

  const [agents, setAgents] = useState<Agent[]>([])
  const [isLoadingAgents, setIsLoadingAgents] = useState(true)
  const [selectedAgent, setSelectedAgent] = useState<AppAgent | null>(null)
  const [yamlContent, setYamlContent] = useState(defaultYaml)
  const [showYaml, setShowYaml] = useState(false)
  const [isRunning, setIsRunning] = useState(false)
  const [autoApprove, setAutoApprove] = useState(false)
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null)
  const [editingNode, setEditingNode] = useState<FlowNode | null>(null)
  const [editingEdge, setEditingEdge] = useState<FlowEdge | null>(null)
  
  // UI State
  const [showCreateModal, setShowCreateModal] = useState(false)
  const [showAIChat, setShowAIChat] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<AppAgent | null>(null)
  const [toast, setToast] = useState<ToastData | null>(null) // { message, type: 'success' | 'error' | 'info', action?: { label, onClick }, persistent?: boolean }
  const [showUpgradeDialog, setShowUpgradeDialog] = useState<UpgradeDialogData | null>(null) // { version, url }
  const [updateAvailable, setUpdateAvailable] = useState<UpdateInfo | null>(null) // { version, url }
  const [appVersion, setAppVersion] = useState('dev') // App version from backend

  // Flow State
  const [availableTools, setAvailableTools] = useState<Tool[]>([])
  const [nodes, setNodes] = useState<FlowNode[]>([])
  const [edges, setEdges] = useState<FlowEdge[]>([])

  // Refs
  const currentFlowNodesRef = useRef<FlowNode[]>([])
  const currentFlowEdgesRef = useRef<FlowEdge[]>([])
  const abortControllerRef = useRef<AbortController | null>(null)
  const autoSaveTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)  // Debounce timer for auto-save
  const _layoutSaveTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null) // Debounce timer for layout changes
  
  // Chat State
  const [chatMessages, setChatMessages] = useState<ChatMessage[]>([
    { type: 'agent', content: 'Welcome! Click "Run" to start the agent flow.' },
  ])
  const [aiChatContext, setAIChatContext] = useState('create_flow')
  const [aiFocusedNode, setAIFocusedNode] = useState<FocusedNodeData | null>(null)  // Node being edited when AI chat opens
  const [aiSelectedNodeIds, setAISelectedNodeIds] = useState<string[]>([])  // Multi-selected nodes for AI
  const [defaultProvider, setDefaultProvider] = useState(() => localStorage.getItem('astonish_provider') || '')
  const [defaultModel, setDefaultModel] = useState(() => localStorage.getItem('astonish_model') || '')
  const [runningNodeId, setRunningNodeId] = useState<string | null>(null)
  const [sessionId, setSessionId] = useState<string | null>(null)
  const [isWaitingForInput, setIsWaitingForInput] = useState(false)
  
  // MCP Dependencies State
  const [mcpDependencies, setMcpDependencies] = useState<McpDependencyCheckResult | null>(null) // {dependencies: [], all_installed: bool, missing: int}
  const [installingDep, setInstallingDep] = useState<string | null>(null) // server name being installed
  const [installModalServer, setInstallModalServer] = useState<InstallModalServerData | null>(null)
  
  // Undo/Redo History (max 100 versions)
  const [yamlHistory, setYamlHistory] = useState<string[]>([])
  const [historyIndex, setHistoryIndex] = useState(-1)
  const MAX_HISTORY = 100

  // Derive settings params from path
  const settingsSection = path.params.section || 'chat'
  const settingsTeamSlug = path.params.teamSlug || ''
  const settingsTeamTab = path.params.teamTab || 'members'
  const settingsSubsection = path.params.subsection || ''

  // Extract available variables from all nodes' output_model and raw_tool_output, grouped by node
  const availableVariables = useMemo(() => {
    const grouped: VariableGroup[] = []
    nodes.forEach(node => {
      const outputModel = node.data?.yaml?.output_model
      const rawToolOutput = node.data?.yaml?.raw_tool_output
      
      // Collect variables from both output_model and raw_tool_output
      const vars = new Set<string>()
      
      if (outputModel && typeof outputModel === 'object') {
        Object.keys(outputModel as Record<string, unknown>).forEach(key => vars.add(key))
      }
      
      if (rawToolOutput && typeof rawToolOutput === 'object') {
        Object.keys(rawToolOutput as Record<string, unknown>).forEach(key => vars.add(key))
      }
      
      if (vars.size > 0) {
        grouped.push({
          nodeName: node.data?.label || node.id,
          nodeType: node.data?.nodeType || node.type,
          variables: Array.from(vars).sort() as string[]
        })
      }
    })
    return grouped
  }, [nodes])

  // Setup wizard state
  const [showSetupWizard, setShowSetupWizard] = useState(false)
  const [isCheckingSetup, setIsCheckingSetup] = useState(true)
  const [view, setView] = useState('chat')
  const [pendingChatMessage, setPendingChatMessage] = useState<{ message: string; systemContext?: string } | null>(null)
  const [sandboxStatus, setSandboxStatus] = useState<SandboxStatus | null>(null)

  // Check if setup is required (wait for platform detection + auth in platform mode)
  useEffect(() => {
    if (!isPlatformChecked) return
    if (isPlatformMode && !auth.isAuthenticated) return
    checkSetupStatus()
  }, [isPlatformChecked, isPlatformMode, auth.isAuthenticated])

  const checkSetupStatus = async () => {
    try {
      setIsCheckingSetup(true)
      const res = await teamFetch('/api/settings/status')
      if (res.ok) {
        const data = await res.json()
        setShowSetupWizard(data.setupRequired)
      }
    } catch (err: any) {
      console.error('Failed to check setup status:', err)
      // If we can't check, assume setup is required
      setShowSetupWizard(true)
    } finally {
      setIsCheckingSetup(false)
    }
  }

  // Fetch sandbox status (for security badge in top bar).
  // In platform mode, this endpoint requires auth — wait until authenticated.
  // In personal mode, fetch immediately (no auth required).
  const sandboxAuthReady = isPlatformChecked && (!isPlatformMode || auth.isAuthenticated)
  useEffect(() => {
    if (!sandboxAuthReady) return
    fetchSandboxStatus()
      .then((status) => setSandboxStatus(status))
      .catch(() => setSandboxStatus(null))
  }, [sandboxAuthReady])

  // Load app version from backend (always runs)
  const loadAppVersion = async () => {
    try {
      const versionRes = await fetch('/api/version')
      if (!versionRes.ok) {
        return 'dev'
      }
      const versionData = await versionRes.json()
      const currentVersion = (versionData.version || 'dev').replace(/^v/, '').trim()
      setAppVersion(currentVersion)
      return currentVersion
    } catch (err: any) {
      console.error('Failed to load app version:', err)
      return 'dev'
    }
  }

  // Check for updates (once per 4 hours)
  const checkForUpdates = useCallback(async () => {
    const lastCheck = localStorage.getItem('astonish_update_check')
    const now = Date.now()

    if (lastCheck && now - parseInt(lastCheck) < 4 * 60 * 60 * 1000) {
      return
    }

    try {
      const currentVersion = await loadAppVersion()

      // If running dev version, clear any saved update state
      if (currentVersion === 'dev') {
        setUpdateAvailable(null)
        localStorage.removeItem('astonish_update_available')
        return
      }

      // Check GitHub for latest release
      const releaseRes = await fetch('https://api.github.com/repos/SAP/astonish/releases/latest')
      if (!releaseRes.ok) {
        return
      }
      const releaseData = await releaseRes.json()

      // Update last check time BEFORE comparison
      localStorage.setItem('astonish_update_check', now.toString())

      const latestVersion = (releaseData.tag_name || '').replace(/^v/, '').trim()

      // Check if this update was already seen (saved in localStorage)
      const savedUpdate = localStorage.getItem('astonish_update_available')

      // Simple string comparison (both versions normalized without 'v' prefix)
      if (currentVersion !== latestVersion) {
        const updateInfo: UpdateInfo = { version: releaseData.tag_name, url: releaseData.html_url }
        setUpdateAvailable(updateInfo)

        // Save to localStorage so update button persists in Settings
        localStorage.setItem('astonish_update_available', JSON.stringify(updateInfo))

        // Only show toast if this is a newly detected update (not already seen)
        if (!savedUpdate) {
          setToast({
            message: `Astonish ${releaseData.tag_name} is available`,
            type: 'info',
            persistent: true,
            action: {
              label: 'See Options',
              onClick: () => setShowUpgradeDialog({ version: releaseData.tag_name, url: releaseData.html_url })
            }
          })
        }
      } else {
        // No update available - clear saved update state
        setUpdateAvailable(null)
        localStorage.removeItem('astonish_update_available')
      }
    } catch (err: any) {
      console.error('Failed to check for updates:', err)
    }
  }, [])

  // In platform mode, wait until the team context is resolved before making
  // API calls. Without this gate, the effect fires as soon as isCheckingSetup
  // becomes false — which can happen before activeTeam is set (the team is
  // resolved asynchronously via fetchTeams). Firing too early sends requests
  // without the X-Astonish-Team header, which may return empty/wrong data
  // and overwrite the correct results that arrive later.
  const teamReady = isPlatformChecked && (!isPlatformMode || !!activeTeam)

  // Check for updates (wait for platform detection + auth in platform mode)
  const versionAuthReady = isPlatformChecked && (!isPlatformMode || auth.isAuthenticated)
  useEffect(() => {
    if (!versionAuthReady) return
    const initUpdateCheck = async () => {
      const currentVersion = await loadAppVersion()      // Always load version on page load

      // Load saved update state from localStorage
      const savedUpdate = localStorage.getItem('astonish_update_available')
      if (savedUpdate) {
        try {
          const updateInfo = JSON.parse(savedUpdate)
          // Check if saved update matches current version (user updated since last check)
          const savedVersion = (updateInfo.version || '').replace(/^v/, '').trim()
          if (savedVersion === currentVersion) {
            // Version matches - user updated, clear old update notification
            localStorage.removeItem('astonish_update_available')
            setUpdateAvailable(null)
          } else {
            // Version still outdated - keep showing update button
            setUpdateAvailable(updateInfo)
          }
        } catch (err: any) {
          console.error('Failed to parse saved update info:', err)
        }
      }

      checkForUpdates()      // Check for updates with 4-hour interval
    }

    initUpdateCheck()
  }, [versionAuthReady, checkForUpdates])

  // Auto-dismiss toast after 3 seconds (except for persistent toasts like updates)
  useEffect(() => {
    if (toast && !toast.persistent) {
      const timer = setTimeout(() => {
        setToast(null)
      }, 4000)
      return () => clearTimeout(timer)
    }
  }, [toast])

  const loadSettings = async () => {
    try {
      const res = await teamFetch('/api/settings/config')
      if (res.ok) {
        const data = await res.json()
        // Use display name from API for proper formatting
        const provider = data.general?.default_provider_display_name || data.general?.default_provider || ''
        const model = data.general?.default_model || ''
        setDefaultProvider(provider)
        setDefaultModel(model)
        // Persist for instant display on next page load
        try {
          localStorage.setItem('astonish_provider', provider)
          localStorage.setItem('astonish_model', model)
        } catch { /* ignore localStorage errors */ }
      }
    } catch (err: any) {
      console.error('Failed to load settings:', err)
    }
  }

  const loadAgents = useCallback(async () => {
    try {
      setIsLoadingAgents(true)
      const data = await fetchAgents()
      const agentsList = data.agents || []
      setAgents(agentsList)
      
      // Check if URL specifies an agent
      const currentPath = pathRef.current
      const urlAgentName = currentPath.view === 'agent' ? currentPath.params.agentName : null
      
      if (urlAgentName && agentsList.length > 0) {
        // Try to find the agent from URL - prioritize exact ID match
        const urlAgent = agentsList.find(a => a.id === urlAgentName) || agentsList.find(a => a.name === urlAgentName)
        if (urlAgent) {
          handleAgentSelectInternalRef.current(urlAgent, false) // Don't update URL, already there
          setView('canvas')
        } else if (agentsList.length > 0) {
          // Agent not found, select first and update URL
          handleAgentSelectInternalRef.current(agentsList[0], true)
          setView('canvas')
        }
      }
    } catch (err: any) {
      console.error('Failed to load agents:', err)
      setAgents([])
    } finally {
      setIsLoadingAgents(false)
    }
  }, [])

  const loadTools = async () => {
    try {
      const data = await fetchTools()
      setAvailableTools(data.tools || [])
    } catch (err: any) {
      console.error('Failed to load tools:', err)
      setAvailableTools([])
    }
  }

  // Load agents, tools, and settings from API on mount and when team changes
  useEffect(() => {
    if (!showSetupWizard && !isCheckingSetup && teamReady) {
      loadAgents()
      loadTools()
      loadSettings()
    }
  }, [showSetupWizard, isCheckingSetup, teamReady, activeTeam, loadAgents])

  // Refresh agents/flows list when a flow is saved via distillation
  useEffect(() => {
    const handleFlowsUpdated = () => { loadAgents() }
    window.addEventListener('astonish:flows-updated', handleFlowsUpdated)
    return () => window.removeEventListener('astonish:flows-updated', handleFlowsUpdated)
  }, [loadAgents])

  // Push new YAML to history (called after applying changes)
  const pushToHistory = useCallback((newYaml: string) => {
    if (!newYaml) return
    setYamlHistory(prev => {
      // If we're not at the end, truncate forward history
      const truncated = prev.slice(0, historyIndex + 1)
      // Add new entry
      const updated = [...truncated, newYaml]
      // Keep only last MAX_HISTORY entries
      if (updated.length > MAX_HISTORY) {
        return updated.slice(-MAX_HISTORY)
      }
      return updated
    })
    setHistoryIndex(prev => Math.min(prev + 1, MAX_HISTORY - 1))
  }, [historyIndex])

  // Debounced auto-save to disk (1000ms delay) - sends YAML directly without reformatting
  const debouncedAutoSave = useCallback((newYaml: string) => {
    if (!selectedAgent) return
    
    // Skip saving for store flows (read-only)
    if (selectedAgent.source === 'store') {
      return
    }
    
    // Clear any pending save
    if (autoSaveTimerRef.current) {
      clearTimeout(autoSaveTimerRef.current)
    }
    
    // Schedule new save with longer debounce (1s) to avoid interrupting editing
    autoSaveTimerRef.current = setTimeout(async () => {
      try {
        // Save the user's YAML as-is (no reformatting)
        // Layout is saved separately via handleLayoutSave when canvas changes
        const result = await saveAgent(selectedAgent.name, newYaml, selectedAgent.scope) as any
        
        // If server returned YAML with NEW content (like mcp_dependencies), update local state
        // Compare parsed content to avoid triggering on format-only differences
        if (result.yaml) {
          try {
            const serverParsed = yaml.load(result.yaml) as YamlData | undefined
            const localParsed = yaml.load(newYaml) as YamlData | undefined
            
            // Check if mcp_dependencies actually changed (the main reason server modifies YAML)
            const serverDeps = JSON.stringify(serverParsed?.mcp_dependencies || null)
            const localDeps = JSON.stringify(localParsed?.mcp_dependencies || null)
            
            if (serverDeps !== localDeps) {
              setYamlContent(result.yaml)
            }
          } catch (parseErr: any) {
            // If parse fails, fall back to not updating
            console.warn('[Auto-save] Could not compare YAML:', parseErr)
          }
        }
      } catch (err: any) {
        console.error('[Auto-save] Failed:', err)
      }
    }, 1000)
  }, [selectedAgent])

  // Unified function to update YAML - handles state, history, and auto-save
  const updateYaml = useCallback((newYaml: string, skipHistory = false) => {
    if (!newYaml) return
    setYamlContent(newYaml)
    if (!skipHistory) {
      pushToHistory(newYaml)
    }
    debouncedAutoSave(newYaml)
  }, [pushToHistory, debouncedAutoSave])

  // Undo: go back in history
  const handleUndo = useCallback(() => {
    if (historyIndex > 0) {
      const prevYaml = yamlHistory[historyIndex - 1]
      setHistoryIndex(prev => prev - 1)
      setYamlContent(prevYaml)
      debouncedAutoSave(prevYaml)  // Auto-save the undone state
    }
  }, [historyIndex, yamlHistory, debouncedAutoSave])

  // Redo: go forward in history
  const handleRedo = useCallback(() => {
    if (historyIndex < yamlHistory.length - 1) {
      const nextYaml = yamlHistory[historyIndex + 1]
      setHistoryIndex(prev => prev + 1)
      setYamlContent(nextYaml)
      debouncedAutoSave(nextYaml)  // Auto-save the redone state
    }
  }, [historyIndex, yamlHistory, debouncedAutoSave])

  // Keyboard shortcuts for undo/redo
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      // Only handle when not in an input/textarea
      if ((e.target as HTMLElement).tagName === 'INPUT' || (e.target as HTMLElement).tagName === 'TEXTAREA') return
      
      if ((e.metaKey || e.ctrlKey) && e.key === 'z') {
        e.preventDefault()
        if (e.shiftKey) {
          handleRedo()
        } else {
          handleUndo()
        }
      }
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [handleUndo, handleRedo])

  // Parse YAML and generate flow (async with ELKjs)
  useEffect(() => {
    const layoutFlow = async () => {
      try {
        const parsed = yaml.load(yamlContent) as YamlData
        const result = await yamlToFlowAsync(parsed)
        setNodes(result.nodes as FlowNode[])
        setEdges(result.edges as FlowEdge[])
      } catch (e: any) {
        console.error('YAML parse error:', e)
        setNodes([])
        setEdges([])
      }
    }
    layoutFlow()
  }, [yamlContent])

  // Refresh editingNode when nodes update (e.g., after AI applies changes)
  useEffect(() => {
    if (editingNode && nodes.length > 0) {
      const updatedNode = nodes.find(n => n.id === editingNode.id)
      if (updatedNode && JSON.stringify(updatedNode) !== JSON.stringify(editingNode)) {
        setEditingNode(updatedNode)
      }
    }
  }, [nodes, editingNode])

  // Internal agent select (optionally updates URL)
  const handleAgentSelectInternal = useCallback(async (agent: Agent, updateUrl = true) => {
    // Reset running state when switching agents
    if (abortControllerRef.current) {
      abortControllerRef.current.abort()
    }
    setIsRunning(false)
    setRunningNodeId(null)
    setSessionId(null)
    setChatMessages([])
    setIsWaitingForInput(false)
    
    // Reset undo/redo history for new agent
    setYamlHistory([])
    setHistoryIndex(-1)
    
    setSelectedAgent(agent)
    setSelectedNodeId(null)
    setEditingNode(null)
    
    // Update URL if requested
    if (updateUrl) {
      navigate(buildPath('agent', { agentName: agent.id }))
    }
    
    // Load agent YAML from API
    try {
      const data = await fetchAgent(agent.name, agent.scope)
      const loadedYaml = data.yaml || defaultYaml
      setYamlContent(loadedYaml)
      // Initialize history with loaded state
      setYamlHistory([loadedYaml])
      setHistoryIndex(0)
      
      // Check MCP dependencies
      try {
        const parsed = yaml.load(loadedYaml) as YamlData | undefined
        if (parsed?.mcp_dependencies && parsed.mcp_dependencies.length > 0) {
          const depStatus = await checkMcpDependencies(parsed.mcp_dependencies)
          setMcpDependencies(depStatus)
        } else {
          setMcpDependencies(null)
        }
      } catch (depErr: any) {
        console.error('Failed to check MCP dependencies:', depErr)
        setMcpDependencies(null)
      }
    } catch (err: any) {
      console.error('Failed to load agent:', err)
      setYamlContent(defaultYaml)
      setYamlHistory([defaultYaml])
      setHistoryIndex(0)
      setMcpDependencies(null)
    }
  }, [navigate])
  useEffect(() => { handleAgentSelectInternalRef.current = handleAgentSelectInternal }, [handleAgentSelectInternal])

  // React to URL path changes (hash navigation)
  useEffect(() => {
    if (path.view === 'agent' && path.params.agentName && agents.length > 0) {
      // Prioritize exact ID match over name match
      const targetAgent = agents.find(a => a.id === path.params.agentName) || agents.find(a => a.name === path.params.agentName)
      // Only switch if it's a different agent than currently selected
      if (targetAgent && targetAgent.id !== selectedAgent?.id) {
        handleAgentSelectInternal(targetAgent, false)
      }
      setView('canvas')
    } else if (path.view === 'chat') {
      setView('chat')
    } else if (path.view === 'canvas') {
      setView('canvas')
    } else if (path.view === 'fleet') {
      setView('fleet')
    } else if (path.view === 'drill') {
      setView('drill')
    } else if (path.view === 'apps') {
      setView('apps')
    } else if (path.view === 'settings') {
      setView('settings')
    } else if (path.view === 'credentials') {
      // Redirect legacy credentials route to settings/credentials
      setView('settings')
      replaceHash('/settings/credentials')
    }
  }, [path, agents, handleAgentSelectInternal, replaceHash, selectedAgent?.id]) // Re-run when path or agents list changes

  // Public agent select (always updates URL)
  const handleAgentSelect = useCallback(async (agent: Agent) => {
    await handleAgentSelectInternal(agent, true)
    setView('canvas')
  }, [handleAgentSelectInternal])

  const handleCreateNew = useCallback(() => {
    setShowCreateModal(true)
  }, [])

  const handleCreateAgent = useCallback(async ({ id, name, description }: { id: string; name: string; description: string }) => {
    const newYaml = `description: ${description || name}

nodes: []

flow: []

layout:
  nodes:
    START:
      x: 200
      y: 50
    END:
      x: 200
      y: 250
`
    
    setSelectedAgent({ id, name: id, description: description || name, source: '', isNew: true })
    setSelectedNodeId(null)
    setEditingNode(null)
    setYamlContent(newYaml)
    // Reset and initialize history for new agent
    setYamlHistory([newYaml])
    setHistoryIndex(0)
    setShowCreateModal(false)
    
    // Update URL using navigate (triggers hashchange) so we stay on this agent after save
    navigate(`/agent/${encodeURIComponent(id)}`)
    
    // Save immediately so it appears in the left menu
    try {
      await saveAgent(id, newYaml, 'personal')
      await loadAgents()
    } catch (err: any) {
      console.error('Failed to save new agent:', err)
    }
  }, [navigate, loadAgents])

  const connectToChat = useCallback(async (currentSessionId: string, message = '') => {
    try {
      if (message) {
        setChatMessages(prev => [...prev, { type: 'user', content: message }])
        setIsWaitingForInput(false)
      }

      const controller = new AbortController()
      abortControllerRef.current = controller

      const response = await teamFetch('/api/chat', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        signal: controller.signal,
        body: JSON.stringify({
          agentId: selectedAgent!.id,
          message: message,
          sessionId: currentSessionId,
          provider: defaultProvider,
          model: defaultModel,
          autoApprove: autoApprove
        })
      })

      if (!response.ok) {
        throw new Error(`HTTP error! status: ${response.status}`)
      }

      const reader = response.body!.getReader()
      const decoder = new TextDecoder()
      let buffer = ''

      while (true) {
        const { value, done } = await reader.read()
        if (done) break
        
        buffer += decoder.decode(value, { stream: true })
        const lines = buffer.split('\n\n')
        buffer = lines.pop() || '' // Keep incomplete line

        for (const block of lines) {
          // SSE blocks can contain multiple lines (event: ..., data: ...)
          const blockLines = block.split('\n')
          
          for (const line of blockLines) {
            if (line.startsWith('data: ')) {
              const dataStr = line.slice(6)
              try {
                const data = JSON.parse(dataStr)
                
                if (data.error) {
                   setChatMessages(prev => [...prev, { type: 'error', content: data.error }])
                } else if (data.text) {
                  // Determine if we should append to last agent message or create new one
                  setChatMessages(prev => {
                    const last = prev[prev.length - 1]
                    if (last && last.type === 'agent' && !data.preserveWhitespace && !last.preserveWhitespace) {
                      // Only append if both are streaming (not output node)
                      return [...prev.slice(0, -1), { ...last, content: (last.content || '') + data.text }]
                    }
                    return [...prev, { type: 'agent', content: data.text, preserveWhitespace: data.preserveWhitespace || false }]
                  })
                } else if (data.node) {
                  setRunningNodeId(data.node)
                  // Only add node message to chat if not silent
                  if (!data.silent) {
                    setChatMessages(prev => [...prev, { type: 'node', nodeName: data.node }])
                  }
                  if (data.node === 'END') {
                     setRunningNodeId(null)
                     setChatMessages(prev => [...prev, { type: 'flow_complete' }])
                  }
                } else if (data.options !== undefined) { // Handle input_request (options may be empty for free-text)
                  setIsWaitingForInput(true)
                  // Only add to chat if there are options to display
                  if (data.options.length > 0) {
                    setChatMessages(prev => [...prev, { 
                      type: 'input_request', 
                      options: data.options 
                    }])
                  }
                } else if (data.input_request) { // Handle nested format just in case
                   setIsWaitingForInput(true)
                   if (data.input_request.options && data.input_request.options.length > 0) {
                     setChatMessages(prev => [...prev, { 
                       type: 'input_request', 
                       options: data.input_request.options 
                     }])
                   }
                } else if (data.attempt !== undefined && data.maxRetries !== undefined) {
                  // Handle retry events
                  setChatMessages(prev => [...prev, { 
                    type: 'retry', 
                    attempt: data.attempt,
                    maxRetries: data.maxRetries,
                    reason: data.reason
                  }])
                } else if (data.title && data.reason && data.originalError !== undefined) {
                  // Handle error_info events (smart error handling)
                  setChatMessages(prev => [...prev, { 
                    type: 'error_info', 
                    title: data.title,
                    reason: data.reason,
                    suggestion: data.suggestion,
                    originalError: data.originalError
                  }])
                } else if (data.auto_approved && data.approval_tool) {
                  // Handle tool auto-approval notification
                  setChatMessages(prev => [...prev, { 
                    type: 'tool_auto_approved', 
                    toolName: data.approval_tool
                  }])
                } else if (data.done) {
                   // Clean finish
                }
              } catch (e: any) {
                console.error('Error parsing SSE data:', e, 'Line:', line)
              }
            }
          }
        }
      }
    } catch (err: any) {
      if (err.name === 'AbortError') {
        setChatMessages(prev => [
           ...prev, 
           { type: 'system', content: 'Execution stopped by user.' },
           { type: 'flow_complete' }
        ])
        setRunningNodeId(null)
        setIsWaitingForInput(false)
      } else {
        console.error('Chat error:', err)
        setChatMessages(prev => [...prev, { type: 'error', content: err.message }])
        setRunningNodeId(null)
      }
    } finally {
      abortControllerRef.current = null
    }
  }, [selectedAgent, defaultProvider, defaultModel, autoApprove])

  const handleRun = useCallback(() => {
    setIsRunning(true)
    setEditingNode(null)
    setShowAIChat(false) // Close AI Assistant when opening Run dialog
    setChatMessages([]) // Clear history
    setRunningNodeId(null)
    // Don't auto-start, wait for user to click Start
  }, [])


  const handleSendMessage = useCallback((msg: string) => {
    if (sessionId) {
      connectToChat(sessionId, msg)
    }
  }, [sessionId, connectToChat])

  const handleStopRun = useCallback(() => {
    if (abortControllerRef.current) {
      abortControllerRef.current.abort()
    } else if (isWaitingForInput) {
       // If waiting for input, the connection is closed, so we must manually trigger stop state
       setChatMessages(prev => [
         ...prev, 
         { type: 'system', content: 'Execution stopped by user.' },
         { type: 'flow_complete' }
      ])
      setRunningNodeId(null)
      setIsWaitingForInput(false)
    }
    // Cleanup MCP on server side
    if (sessionId) {
      teamFetch(`/api/session/${sessionId}/stop`, { method: 'POST' }).catch(() => {})
    }
    setSessionId(null) // Clear session to re-enable auto-approve toggle
  }, [isWaitingForInput, sessionId])

  const handleExitRun = useCallback(() => {
    if (abortControllerRef.current) {
      abortControllerRef.current.abort()
    }
    // Cleanup MCP on server side
    if (sessionId) {
      teamFetch(`/api/session/${sessionId}/stop`, { method: 'POST' }).catch(() => {})
    }
    setIsRunning(false)
    setRunningNodeId(null)
    setChatMessages([])
    setSessionId(null) // Clear session to re-enable auto-approve toggle
  }, [sessionId])

  // Keepalive: Ping server every 30 seconds while session is active to prevent timeout
  // This keeps the session and MCP servers alive while user is interacting with the flow
  useEffect(() => {
    if (!sessionId || !isRunning) return

    const keepaliveInterval = setInterval(() => {
      teamFetch(`/api/session/${sessionId}/keepalive`, { method: 'POST' })
        .catch(err => console.warn('Keepalive ping failed:', err))
    }, 30000) // 30 seconds

    return () => clearInterval(keepaliveInterval)
  }, [sessionId, isRunning])

  const handleYamlChange = useCallback((newYaml: string) => {
    setYamlContent(newYaml)
  }, [])

  const handleNodeSelect = useCallback((nodeId: string | null) => {
    setSelectedNodeId(nodeId)
  }, [])

  // Double-click to open editor
  const handleNodeDoubleClick = useCallback((nodeId: string) => {
    const node = nodes.find(n => n.id === nodeId)
    if (node && node.type !== 'start' && node.type !== 'end') {
      setEditingNode(node)
      setEditingEdge(null) // Close edge editor if open
    }
  }, [nodes])

  // Click on edge to open edge editor
  const handleEdgeSelect = useCallback((edge: any) => {
    setEditingEdge(edge)
    setEditingNode(null) // Close node editor if open
  }, [])

  // Helper to merge current accumulated layout changes into YAML string
  const getYamlWithLayout = useCallback((baseYaml: string) => {
    try {
      if (!currentFlowNodesRef.current || currentFlowNodesRef.current.length === 0) return baseYaml
      
      const parsed = (yaml.load(baseYaml) || {}) as YamlData
      const layout = extractLayout(currentFlowNodesRef.current as any, currentFlowEdgesRef.current as any)
      parsed.layout = layout
      
      return yaml.dump(orderYamlKeys(parsed), { 
        indent: 2,
        lineWidth: -1, 
        noRefs: true, 
        sortKeys: false
      })
    } catch (e: any) {
      console.error('Failed to merge layout:', e)
      return baseYaml
    }
  }, [])

  // Add standalone node
  const handleAddNode = useCallback((nodeType: string) => {
    // First merge current layout so positions are preserved
    const currentYaml = getYamlWithLayout(yamlContent)
    const newYaml = addStandaloneNode(currentYaml, nodeType)
    updateYaml(newYaml)
  }, [yamlContent, updateYaml, getYamlWithLayout])

  // Create a new connected node from the + button on an existing node
  const handleCreateConnectedNode = useCallback((sourceId: string, nodeType: string, position: { x: number; y: number }) => {
    // First add the standalone node
    const currentYaml = getYamlWithLayout(yamlContent)
    let newYaml = addStandaloneNode(currentYaml, nodeType)
    
    // Parse the new YAML to get the new node's name
    const parsedYaml = yaml.load(newYaml) as YamlData | undefined
    const newNodeName = parsedYaml?.nodes?.[parsedYaml.nodes.length - 1]?.name
    
    if (newNodeName) {
      // Update position for the new node
      if (position && parsedYaml!.layout?.nodes) {
        parsedYaml!.layout.nodes[newNodeName] = {
          x: Math.round(position.x),
          y: Math.round(position.y)
        }
        newYaml = yaml.dump(orderYamlKeys(parsedYaml!), { 
          lineWidth: -1, noRefs: true, quoteStyle: 'double', forceQuotes: false 
        })
      }
      
      // Add the connection from source to new node
      newYaml = addConnection(newYaml, sourceId, newNodeName)
    }
    
    updateYaml(newYaml)
  }, [yamlContent, updateYaml, getYamlWithLayout])

  // Handle new connection
  const handleConnect = useCallback((sourceId: string, targetId: string) => {
    const currentYaml = getYamlWithLayout(yamlContent)
    const newYaml = addConnection(currentYaml, sourceId, targetId)
    updateYaml(newYaml)
  }, [yamlContent, updateYaml, getYamlWithLayout])

  // Handle edge removal
  const handleEdgeRemove = useCallback((sourceId: string, targetId: string) => {
    const currentYaml = getYamlWithLayout(yamlContent)
    const newYaml = removeConnection(currentYaml, sourceId, targetId)
    updateYaml(newYaml)
  }, [yamlContent, updateYaml, getYamlWithLayout])

  // Save node edits (called from NodeEditor on every change)
  const handleNodeSave = useCallback((nodeId: string, newData: Record<string, any>) => {
    // Optimization: Skip update if data hasn't changed (prevents undo history spam)
    if (editingNode && editingNode.id === nodeId && editingNode.data?.yaml) {
      // Simple deep comparison to see if logic actually changed
      if (JSON.stringify(editingNode.data.yaml) === JSON.stringify(newData)) {
        return
      }
    }

    const newYaml = updateNode(yamlContent, nodeId, newData)
    updateYaml(newYaml)
    // Don't close editor here - it auto-saves continuously, user closes via Done button
  }, [yamlContent, updateYaml, editingNode])

  // Close node editor
  const handleNodeEditorClose = useCallback(() => {
    setEditingNode(null)
  }, [])

  // Save edge condition
  const handleEdgeSave = useCallback((edgeId: string, { condition }: { condition: string }) => {
    // Get source and target from the editing edge (which was passed full edge object)
    const edge = editingEdge
    if (!edge) return
    
    const sourceId = edge.source
    const targetId = edge.target
    
    try {
      const parsed = (yaml.load(yamlContent) || {}) as YamlData
      const flow = parsed.flow || []
      
      // Find and update the flow entry
      let updated = false
      for (let i = 0; i < flow.length; i++) {
        const entry = flow[i]
        if (entry.from === sourceId) {
          // Check if this is a simple edge or has edges array
          if (entry.to === targetId) {
            // Simple edge - convert to edges array if condition is set
            if (condition) {
              delete entry.to
              entry.edges = [{ to: targetId, condition }]
            }
            updated = true
            break
          } else if (entry.edges) {
            // Has edges array - find and update specific edge
            for (let j = 0; j < entry.edges.length; j++) {
              if (entry.edges[j].to === targetId) {
                if (condition) {
                  entry.edges[j].condition = condition
                } else {
                  delete entry.edges[j].condition
                }
                updated = true
                break
              }
            }
          }
        }
      }
      
      if (updated) {
        const newYaml = yaml.dump(orderYamlKeys(parsed), { 
          indent: 2,
          lineWidth: -1, 
          noRefs: true, 
          sortKeys: false 
        })
        updateYaml(newYaml)
        setEditingEdge(null)
      }
    } catch (e: any) {
      console.error('Failed to save edge condition:', e)
    }
  }, [yamlContent, updateYaml, editingEdge])

  // Delete edge
  const handleEdgeDelete = useCallback((edgeId: string) => {
    // Get source and target from the editing edge
    const edge = editingEdge
    if (!edge) return
    
    handleEdgeRemove(edge.source, edge.target)
    setEditingEdge(null)
  }, [handleEdgeRemove, editingEdge])

  // Close edge editor
  const handleEdgeEditorClose = useCallback(() => {
    setEditingEdge(null)
  }, [])

  // Track layout changes from FlowCanvas
  // Track layout changes from FlowCanvas (keep refs in sync during drag)
  const handleLayoutChange = useCallback((flowNodes: any[], flowEdges: any[]) => {
    currentFlowNodesRef.current = flowNodes
    currentFlowEdgesRef.current = flowEdges
  }, [])

  // Handle immediate layout save (called on node drag stop)
  const handleLayoutSave = useCallback((flowNodes: any[], flowEdges: any[]) => {
    // Ensure refs are up to date
    currentFlowNodesRef.current = flowNodes
    currentFlowEdgesRef.current = flowEdges
    
    // Save immediately without history
    const newYaml = getYamlWithLayout(yamlContent)
    if (newYaml !== yamlContent) {
      updateYaml(newYaml, true)
    }
  }, [yamlContent, getYamlWithLayout, updateYaml])

  // Handle node deletion from FlowCanvas - update YAML immediately
  // Accepts an array of node IDs to delete (supports multi-select)
  const handleNodeDelete = useCallback((nodeIds: string | string[]) => {
    // Normalize to array if single ID passed
    const idsToDelete = Array.isArray(nodeIds) ? nodeIds : [nodeIds]
    
    // Parse current YAML and remove all nodes at once
    try {
      const parsed = (yaml.load(yamlContent) || {}) as YamlData
      
      if (parsed.nodes && Array.isArray(parsed.nodes)) {
        parsed.nodes = parsed.nodes.filter((n: any) => !idsToDelete.includes(n.name))
      }
      
      // Also clean up flow edges that reference any deleted nodes
      if (parsed.flow && Array.isArray(parsed.flow)) {
        parsed.flow = parsed.flow.filter((edge: any) => {
          if (idsToDelete.includes(edge.from)) return false
          if (idsToDelete.includes(edge.to)) return false
          // Handle conditional edges
          if (edge.edges && Array.isArray(edge.edges)) {
            edge.edges = edge.edges.filter((e: any) => !idsToDelete.includes(e.to))
            return edge.edges.length > 0 || edge.to
          }
          return true
        })
      }
      
      // Clean up layout for all deleted nodes
      if (parsed.layout && parsed.layout.nodes) {
        idsToDelete.forEach(nodeId => {
          delete parsed.layout.nodes[nodeId]
        })
      }
      
      const newYaml = yaml.dump(orderYamlKeys(parsed), { 
        indent: 2, 
        lineWidth: -1,
        noRefs: true,
        sortKeys: false
      })
      
      updateYaml(newYaml)
      
      // Also update local nodes/edges state to keep in sync
      setNodes(prev => prev.filter(n => !idsToDelete.includes(n.id)))
      setEdges(prev => prev.filter(e => !idsToDelete.includes(e.source) && !idsToDelete.includes(e.target)))
      
    } catch (err: any) {
      console.error('Failed to delete nodes from YAML:', err)
    }
  }, [yamlContent, updateYaml])

  // Handle node duplication (copy/paste)
  // Accepts an array of node data to duplicate with new IDs
  const handleDuplicateNodes = useCallback((nodesToDuplicate: any[]) => {
    if (!nodesToDuplicate || nodesToDuplicate.length === 0) return
    
    try {
      const parsed = (yaml.load(yamlContent) || {}) as YamlData
      if (!parsed.nodes) parsed.nodes = []
      if (!parsed.layout) parsed.layout = { nodes: {} }
      if (!parsed.layout.nodes) parsed.layout.nodes = {}
      
      // Get existing node names to avoid conflicts
      const existingNames = new Set(parsed.nodes.map((n: any) => n.name))
      
      // Track new node names for selection after paste
      const newNodeNames: string[] = []
      
      nodesToDuplicate.forEach((sourceNode: any, index: number) => {
        // Find the original node data in YAML
        const originalNode = parsed.nodes.find((n: any) => n.name === sourceNode.id)
        if (!originalNode) {
          console.warn(`[DUPLICATE] Original node not found: ${sourceNode.id}`)
          return
        }
        
        // Generate unique name
        let copyNum = 1
        let newName = `${originalNode.name}_copy`
        while (existingNames.has(newName)) {
          copyNum++
          newName = `${originalNode.name}_copy${copyNum}`
        }
        existingNames.add(newName)
        newNodeNames.push(newName)
        
        // Clone the node with new name
        const newNode = { ...originalNode, name: newName }
        parsed.nodes.push(newNode)
        
        // Set position (offset from original or from source position)
        const sourcePos = sourceNode.position || parsed.layout.nodes[sourceNode.id]
        if (sourcePos) {
          parsed.layout.nodes[newName] = {
            x: Math.round(sourcePos.x + 50 + index * 20),
            y: Math.round(sourcePos.y + 50 + index * 20)
          }
        }
      })
      
      const newYaml = yaml.dump(orderYamlKeys(parsed), { 
        indent: 2, 
        lineWidth: -1,
        noRefs: true,
        sortKeys: false
      })
      
      updateYaml(newYaml)
      
    } catch (err: any) {
      console.error('Failed to duplicate nodes:', err)
    }
  }, [yamlContent, updateYaml])

  // Save layout and sync to disk (used before running and periodically)
  const saveLayoutAndSync = useCallback(async () => {
    if (!selectedAgent || !yamlContent) return
    // Skip saving for store flows (read-only)
    if (selectedAgent.source === 'store') return
    
    try {
      const parsed = (yaml.load(yamlContent) || {}) as YamlData
      const layout = extractLayout(currentFlowNodesRef.current as any, currentFlowEdgesRef.current as any)
      parsed.layout = layout
      const updatedYaml = yaml.dump(orderYamlKeys(parsed), { 
        indent: 2,
        lineWidth: -1, 
        noRefs: true, 
        sortKeys: false 
      })
      await saveAgent(selectedAgent.name, updatedYaml, selectedAgent.scope)
      setYamlContent(updatedYaml)
    } catch (err: any) {
      console.error('Layout save failed:', err)
    }
  }, [selectedAgent, yamlContent])

  // Handle auto layout - remove stored positions to let ELK recalculate
  const handleAutoLayout = useCallback(() => {
    try {
      const parsed = (yaml.load(yamlContent) || {}) as YamlData
      
      // Remove the entire layout section
      if (parsed.layout) {
        delete parsed.layout
      }
      
      const newYaml = yaml.dump(orderYamlKeys(parsed), { 
        indent: 2, 
        lineWidth: -1,
        noRefs: true,
        sortKeys: false
      })
      
      updateYaml(newYaml)
      
      // Also save to disk
      if (selectedAgent && selectedAgent.source !== 'store') {
        saveAgent(selectedAgent.name, newYaml, selectedAgent.scope).catch(err => {
          console.error('Failed to save after auto layout:', err)
        })
      }
    } catch (err: any) {
      console.error('Failed to apply auto layout:', err)
    }
  }, [yamlContent, updateYaml, selectedAgent])

  const handleStartRun = useCallback(async () => {
    // Save layout before running
    await saveLayoutAndSync()
    
    setChatMessages([{ type: 'system', content: 'Execution started...' }])
    setRunningNodeId(null)
    const newSessionId = `session-${Date.now()}`
    setSessionId(newSessionId)
    connectToChat(newSessionId)
  }, [connectToChat, saveLayoutAndSync])

  // Delete agent
  const handleDeleteAgent = useCallback((agent: Agent) => {
    setDeleteTarget(agent)
  }, [])

  // Toast notification helper
  const showToast = useCallback((message: string, type: 'success' | 'error' | 'info' = 'success') => {
    setToast({ message, type })
    setTimeout(() => setToast(null), 4000) // Auto-dismiss after 4 seconds
  }, [])

  // Publish a personal flow to the team
  const handlePublishFlow = useCallback(async (agent: Agent) => {
    try {
      await publishFlowToTeam(agent.name)
      showToast(`Flow "${agent.name}" published to team`, 'success')
      loadAgents()
    } catch (err) {
      showToast(`Failed to publish flow: ${err instanceof Error ? err.message : 'Unknown error'}`, 'error')
    }
  }, [showToast]) // eslint-disable-line react-hooks/exhaustive-deps

  // Fork a team flow to personal
  const handleForkFlow = useCallback(async (agent: Agent) => {
    try {
      await forkFlowToPersonal(agent.name)
      showToast(`Flow "${agent.name}" forked to personal`, 'success')
      loadAgents()
    } catch (err) {
      showToast(`Failed to fork flow: ${err instanceof Error ? err.message : 'Unknown error'}`, 'error')
    }
  }, [showToast]) // eslint-disable-line react-hooks/exhaustive-deps

  // Publish a personal app to the team
  const handlePublishApp = useCallback(async (app: AppListItem) => {
    try {
      await publishAppToTeam(app.slug)
      showToast(`App "${app.name}" published to team`, 'success')
    } catch (err) {
      showToast(`Failed to publish app: ${err instanceof Error ? err.message : 'Unknown error'}`, 'error')
    }
  }, [showToast])

  // Fork a team app to personal
  const handleForkApp = useCallback(async (app: AppListItem) => {
    try {
      await forkAppToPersonal(app.slug)
      showToast(`App "${app.name}" forked to personal`, 'success')
    } catch (err) {
      showToast(`Failed to fork app: ${err instanceof Error ? err.message : 'Unknown error'}`, 'error')
    }
  }, [showToast])

  // Copy store agent to local
  const handleCopyToLocal = useCallback(async (agent: AppAgent) => {
    try {
      const res = await teamFetch(`/api/agents/${encodeURIComponent(agent.name)}/copy-to-local`, {
        method: 'POST'
      })
      if (!res.ok) {
        const errorText = await res.text()
        throw new Error(errorText || 'Failed to copy agent')
      }
      const data = await res.json()
      showToast(`Flow copied to local: ${data.newName}`, 'success')
      // Refresh agent list
      loadAgents()
    } catch (err: any) {
      console.error('Failed to copy agent:', err)
      showToast('Failed to copy: ' + err.message, 'error')
    }
  }, [loadAgents, showToast])

  const confirmDelete = useCallback(async () => {
    if (!deleteTarget) return
    
    try {
      if (deleteTarget.source === 'store') {
        // Uninstall store flow - parse the store:tap:name ID format
        const parts = deleteTarget.id.split(':')
        if (parts.length === 3) {
          const [, tapName, flowName] = parts
          const res = await teamFetch(`/api/flow-store/${encodeURIComponent(tapName)}/${encodeURIComponent(flowName)}`, {
            method: 'DELETE'
          })
          if (!res.ok) {
            const errorText = await res.text()
            throw new Error(errorText || 'Failed to uninstall flow')
          }
        } else {
          throw new Error('Invalid store flow ID format')
        }
      } else {
        // Delete local agent
        await deleteAgent(deleteTarget.name, deleteTarget.scope)
      }
      // Refresh agent list
      loadAgents()
      // If we deleted the selected agent, clear selection
      if (selectedAgent?.id === deleteTarget.id) {
        setSelectedAgent(null)
        setYamlContent(defaultYaml)
      }
    } catch (err: any) {
      console.error('Failed to delete agent:', err)
      alert('Failed to delete agent: ' + err.message)
    } finally {
      setDeleteTarget(null)
    }
  }, [deleteTarget, selectedAgent, loadAgents])

  return (
    <>
      {/* Platform Auth Gate — only in platform mode */}
      {isPlatformMode && (!auth.isAuthenticated || auth.pendingVerificationEmail || auth.noTeamMembership) && !auth.isLoading && isPlatformChecked && (
        <LoginPage
          onLogin={async (email, password) => { await auth.login(email, password) }}
          onRegister={async (email, password, displayName) => { await auth.register(email, password, displayName) }}
          pendingVerificationEmail={auth.pendingVerificationEmail}
          noTeamMembership={auth.noTeamMembership}
        />
      )}

      {/* Loading state while checking platform mode or auth */}
      {(!isPlatformChecked || (isPlatformMode && auth.isLoading)) && (
        <div
          className="flex flex-col h-screen items-center justify-center"
          style={{ background: 'var(--bg-primary)' }}
        >
          <div className="animate-pulse text-primary text-lg">Loading...</div>
        </div>
      )}

      {/* Setup Wizard (personal mode only, or after platform auth) */}
      {showSetupWizard && !isCheckingSetup && (!isPlatformMode || auth.isAuthenticated) && (
        <SetupWizard
          onComplete={() => {
            setShowSetupWizard(false)
            loadSettings()
            loadAgents()
            loadTools()
          }}
        />
      )}

      {/* Loading state while checking setup (personal mode) */}
      {isCheckingSetup && isPlatformChecked && (!isPlatformMode || auth.isAuthenticated) && (
        <div 
          className="flex flex-col h-screen items-center justify-center"
          style={{ background: 'var(--bg-primary)' }}
        >
          <div className="animate-pulse text-primary text-lg">Loading...</div>
        </div>
      )}

      {/* Main App (only show when not in setup wizard and not checking, and authenticated if platform) */}
      {!showSetupWizard && !isCheckingSetup && (!isPlatformMode || auth.isAuthenticated) && (
      <div className="bg-grad-app flex h-screen flex-col">
        {/* Top Bar */}
        <TopBar 
          theme={theme} 
          onToggleTheme={toggleTheme}
          onOpenSandbox={() => navigate(buildPath('settings', { section: 'sandbox' }))}
          defaultProvider={defaultProvider}
          defaultModel={defaultModel}
          currentView={view}
          onNavigate={(v: string) => navigate(buildPath(v))}
          sandboxStatus={sandboxStatus as any}
          isPlatformMode={isPlatformMode}
          user={auth.user}
          org={auth.org}
          orgs={auth.orgs}
          onOrgSwitch={handleOrgSwitch}
          activeTeam={activeTeam}
          teams={platformTeams}
          onTeamChange={handleTeamChange}
          onLogout={async () => { await auth.logout(); navigate(buildPath('chat')) }}
          personalMemoryMode={personalMemoryMode}
          onTogglePersonalMemoryMode={togglePersonalMemoryMode}
        />

        {/* Main Content Area */}
        <div className="flex flex-1 overflow-hidden">
          {/* Sidebar - Show only in Canvas View */}
          {view === 'canvas' && (
            <Sidebar
              agents={agents as any}
              selectedAgent={selectedAgent as any}
              onAgentSelect={(agent: any) => { handleAgentSelect(agent) }}
              onCreateNew={handleCreateNew}
              onDeleteAgent={(agent: any) => { handleDeleteAgent(agent) }}
              onPublishFlow={(agent: any) => { handlePublishFlow(agent) }}
              onForkFlow={(agent: any) => { handleForkFlow(agent) }}
              isLoading={isLoadingAgents}
            />
          )}

        {/* Main Content */}
        <div className="flex-1 flex flex-col overflow-hidden">
          {view === 'chat' ? (
            <Suspense fallback={null}>
            <StudioChat
              key={activeTeam || 'personal'}
              theme={theme}
              initialSessionId={path.view === 'chat' ? path.params.sessionId : ''}
              pendingChatMessage={pendingChatMessage}
              onPendingChatMessageConsumed={() => setPendingChatMessage(null)}
              userDisplayName={auth.user?.display_name}
              onSessionChange={(sid: string | null) => {
                if (path.view !== 'chat') return
                if (sid) {
                  replaceHash(buildPath('chat', { sessionId: sid }))
                } else {
                  replaceHash(buildPath('chat'))
                }
              }}
            />
            </Suspense>
          ) : view === 'fleet' ? (
            <Suspense fallback={null}>
            <FleetView
              key={activeTeam || 'personal'}
              theme={theme}
              path={path}
              onNavigate={(hashPath: string) => navigate(hashPath)}
              onCreatePlan={(templateKey: string, draftId?: string) => {
                const msg = draftId
                  ? `/fleet-plan ${templateKey} --draft=${draftId}`
                  : `/fleet-plan ${templateKey}`
                setPendingChatMessage({ message: msg })
                navigate(buildPath('chat'))
              }}
            />
            </Suspense>
          ) : view === 'drill' ? (
            <Suspense fallback={null}>
            <DrillView
              key={activeTeam || 'personal'}
              theme={theme}
              path={path}
              onNavigate={(hashPath: string) => navigate(hashPath)}
              onRunSuite={(suiteName: string, instructions?: string) => {
                setPendingChatMessage({
                  message: instructions?.trim()
                    || `Prepare then run the drill suite "${suiteName}". Follow its suite_config prep (template, git sync, start services), then call run_drill. run_drill only injects credentials and runs tests.`,
                })
                navigate(buildPath('chat'))
              }}
              onAddDrills={(suiteName: string) => {
                setPendingChatMessage({ message: `/drill-add ${suiteName}` })
                navigate(buildPath('chat'))
              }}
            />
            </Suspense>
          ) : view === 'apps' ? (
            <Suspense fallback={null}>
            <AppsView
              key={activeTeam || 'personal'}
              theme={theme}
              isPlatformMode={isPlatformMode}
              appName={path.view === 'apps' ? path.params.appName : ''}
              onNavigate={(hashPath: string) => navigate(hashPath)}
              onImproveApp={(message: string, systemContext: string) => {
                setPendingChatMessage({ message, systemContext })
                navigate(buildPath('chat'))
              }}
              onPublishApp={isPlatformMode ? handlePublishApp : undefined}
              onForkApp={isPlatformMode ? handleForkApp : undefined}
            />
            </Suspense>
          ) : view === 'settings' ? (
            <Suspense fallback={null}>
            <SettingsPage
              key={activeTeam || 'personal'}
              activeSection={settingsSection}
              onSectionChange={(section: string) => replaceHash(buildPath('settings', { section }))}
              theme={theme}
              onToolsRefresh={loadTools}
              onSettingsSaved={loadSettings}
              updateAvailable={updateAvailable as any}
              onUpdateClick={() => setShowUpgradeDialog(updateAvailable)}
              appVersion={appVersion}
              isPlatformMode={isPlatformMode}
              userRole={auth.user?.role}
              platformRole={auth.user?.platform_role}
              user={auth.user}
              org={auth.org}
              activeTeam={activeTeam}
              teams={platformTeams}
              teamSlug={settingsTeamSlug}
              teamTab={settingsTeamTab}
              subsection={settingsSubsection}
            />
            </Suspense>
          ) : !selectedAgent ? (
             <div
               className="flex flex-1 items-center justify-center p-8 text-center"
               style={{ color: 'var(--text-muted)', background: 'var(--work-background, var(--bg-primary))' }}
             >
               Select a flow from the sidebar to continue
             </div>
          ) : (

          <>
          <Header
            agentName={selectedAgent?.name || 'Select Agent'}
            agentSource={selectedAgent?.source || ''}
            showYaml={showYaml}
            onToggleYaml={() => setShowYaml(!showYaml)}
            isRunning={isRunning}
            onRun={handleRun}
            onExit={handleExitRun}
            canUndo={historyIndex > 0}
            canRedo={historyIndex < yamlHistory.length - 1}
            onUndo={handleUndo}
            onRedo={handleRedo}
            readOnly={selectedAgent?.source === 'store'}
            onCopyToLocal={() => handleCopyToLocal(selectedAgent!)}
          />

          {/* MCP Dependencies Warning Panel */}
          {mcpDependencies && !mcpDependencies.all_installed && (
            <MCPDependenciesPanel
              dependencies={mcpDependencies}
              onDismiss={() => setMcpDependencies(null)}
              isInstalling={installingDep as any}
              onInstall={async (dep: any) => {
                // Handle inline dependencies (embedded config in YAML)
                if (dep.source === 'inline') {
                  if (!dep.config || (!dep.config.command && !dep.config.url)) {
                    setToast({ message: `Cannot install ${dep.server}: Missing configuration. Please add it manually in Settings > MCP.`, type: 'error' })
                    return
                  }
                  
                  setInstallingDep(dep.server)
                  try {
                    // For inline, show modal with the embedded config for env var input
                    if (dep.config.env && Object.keys(dep.config.env).length > 0) {
                      setInstallModalServer({
                        name: dep.server,
                        description: `Inline MCP server required by this flow`,
                        config: dep.config,
                        _depContext: dep,
                        _isInline: true
                      })
                      setInstallingDep(null)
                      return
                    }
                    
                    // No env vars needed, install directly
                    await installInlineMcpServer(dep.server, dep.config)
                    
                    // Refresh tools and check dependencies
                    const toolsData = await fetchTools()
                    setAvailableTools(toolsData.tools || [])
                    
                    const parsed = yaml.load(yamlContent) as YamlData | undefined
                    if (parsed?.mcp_dependencies) {
                      const newStatus = await checkMcpDependencies(parsed.mcp_dependencies)
                      setMcpDependencies(newStatus)
                    }
                    
                    setToast({ message: `Successfully installed ${dep.server}`, type: 'success' })
                  } catch (err: any) {
                    console.error('Failed to install inline server:', err)
                    setToast({ message: `Failed to install ${dep.server}: ${err.message}`, type: 'error' })
                  } finally {
                    setInstallingDep(null)
                  }
                  return
                }
                
                // Support both store and tap sources (both use the same install API)
                if (!dep.store_id || (dep.source !== 'store' && dep.source !== 'tap')) {
                  // For unknown sources
                  console.warn('Unsupported install source:', dep)
                  setToast({ message: `Cannot install ${dep.server}: Unknown source type`, type: 'error' })
                  return
                }
                
                setInstallingDep(dep.server)
                try {
                  // Fetch server details first to check for env vars
                  const serverDetails = await getMcpStoreServer(dep.store_id) as any
                  
                  // If server requires env vars, show modal
                  if (serverDetails.config && serverDetails.config.env && Object.keys(serverDetails.config.env).length > 0) {
                    setInstallModalServer({
                      ...serverDetails,
                      _depContext: dep
                    })
                    setInstallingDep(null)
                    return
                  }
                  
                  // No env vars needed, proceed with install
                  await installMcpServer(dep.store_id)
                  
                  // Refresh tools and check dependencies
                  const toolsData = await fetchTools()
                  setAvailableTools(toolsData.tools || [])
                  
                  const parsed = yaml.load(yamlContent) as YamlData | undefined
                  if (parsed?.mcp_dependencies) {
                    const newStatus = await checkMcpDependencies(parsed.mcp_dependencies)
                    setMcpDependencies(newStatus)
                  }
                  
                  setToast({ message: `Successfully installed ${dep.server}`, type: 'success' })
                } catch (err: any) {
                  console.error('Failed to install:', err)
                  setToast({ message: `Failed to install ${dep.server}: ${err.message}`, type: 'error' })
                } finally {
                  setInstallingDep(null)
                }
              }}
            />
          )}

          <InstallMcpModal 
            isOpen={!!installModalServer}
            server={installModalServer}
            onClose={() => setInstallModalServer(null)}
            onInstall={async (env: Record<string, string>) => {
              const dep = installModalServer!._depContext
              const isInline = installModalServer!._isInline
              try {
                if (isInline) {
                  // For inline servers, merge env vars with the config
                  const configWithEnv = {
                    ...installModalServer!.config,
                    env: omitEmptySensitiveEnv({ ...installModalServer!.config?.env, ...env })
                  }
                  await installInlineMcpServer(dep.server, configWithEnv)
                } else {
                  // For store/tap servers
                  await installMcpServer(dep.store_id, env)
                }
                
                // Refresh tools and check dependencies
                const toolsData = await fetchTools()
                setAvailableTools(toolsData.tools || [])
                
                const parsed = yaml.load(yamlContent) as YamlData | undefined
                if (parsed?.mcp_dependencies) {
                  const newStatus = await checkMcpDependencies(parsed.mcp_dependencies)
                  setMcpDependencies(newStatus)
                }
                
                setToast({ message: `Successfully installed ${dep.server}`, type: 'success' })
              } catch (err: any) {
                console.error('Failed to install with env:', err)
                setToast({ message: `Failed to install ${dep.server}: ${err.message}`, type: 'error' })
                throw err // InstallMcpModal will handle error display
              }
            }}
          />

          {/* Flow + Chat Area */}
          <div className={`flex-1 flex overflow-hidden ${!isRunning && (editingNode || showYaml) ? 'h-1/2' : ''}`}>
            {/* Flow Canvas */}
            <div className={`flex-1 transition-all duration-300 ${isRunning ? 'w-1/2' : ''}`}>
              <Suspense fallback={null}>
              <FlowCanvas
                key={selectedAgent ? selectedAgent.id : 'empty'}
                nodes={nodes as any}
                edges={edges as any}
                isRunning={isRunning}
                readOnly={selectedAgent?.source === 'store'}
                theme={theme}
                onNodeSelect={handleNodeSelect}
                onNodeDoubleClick={handleNodeDoubleClick}
                onEdgeSelect={handleEdgeSelect}
                selectedNodeId={selectedNodeId}
                runningNodeId={runningNodeId}
                onAddNode={handleAddNode}
                onConnect={handleConnect}
                onEdgeRemove={handleEdgeRemove}
                onLayoutChange={handleLayoutChange}
                onLayoutSave={handleLayoutSave}
                onNodeDelete={handleNodeDelete}
                onAutoLayout={handleAutoLayout}
                onCreateConnectedNode={handleCreateConnectedNode}
                onDuplicateNodes={handleDuplicateNodes}
                onOpenAIChat={(options?: { context: string; nodeIds: string[] }) => {
                  if (options?.context === 'multi_node' && options?.nodeIds) {
                    // Multi-node context
                    setAIChatContext('multi_node')
                    setAISelectedNodeIds(options.nodeIds)
                    setAIFocusedNode(null)
                  } else {
                    // Check if flow has existing nodes (not just START->END)
                    const hasExistingNodes = nodes.length > 0 && nodes.some(n => n.id !== 'START' && n.id !== 'END')
                    setAIChatContext(hasExistingNodes ? 'modify_flow' : 'create_flow')
                    setAISelectedNodeIds([])
                    setAIFocusedNode(null)
                  }
                  setShowAIChat(true)
                }}
              />
              </Suspense>
            </div>

            {/* Chat Panel (visible when running) */}
            {isRunning && (
              <div className="w-1/2" style={{ borderLeft: '1px solid var(--border-color)' }}>
                <ChatPanel
                  messages={chatMessages}
                  onSendMessage={handleSendMessage}
                  onStartRun={handleStartRun}
                  onStop={handleStopRun}
                  isWaitingForInput={isWaitingForInput}
                  hasActiveSession={sessionId !== null}
                  theme={theme}
                  autoApprove={autoApprove}
                  onToggleAutoApprove={setAutoApprove}
                />
              </div>
            )}
          </div>

          {/* Bottom Panel - Node Editor OR Edge Editor OR YAML Drawer */}
          {!isRunning && (editingNode || editingEdge || showYaml) && (
            <div className="h-1/2" style={{ borderTop: '1px solid var(--border-color)' }}>
              {editingNode ? (
                <NodeEditor
                  node={editingNode as any}
                  onSave={handleNodeSave}
                  onClose={handleNodeEditorClose}
                  theme={theme}
                  availableTools={availableTools as any}
                  availableVariables={availableVariables as any}
                  readOnly={selectedAgent?.source === 'store'}
                  onAIAssist={(node: any, nodeName: string, nodeData: any) => {
                    setAIChatContext('node_config')
                    setAIFocusedNode({ name: nodeName, type: node.data?.nodeType || node.type, data: nodeData })
                    setShowAIChat(true)
                  }}
                />
              ) : editingEdge ? (
                <EdgeEditor
                  edge={editingEdge as any}
                  sourceNode={nodes.find(n => n.id === editingEdge.source) as any}
                  targetNode={nodes.find(n => n.id === editingEdge.target) as any}
                  onSave={handleEdgeSave}
                  onDelete={handleEdgeDelete}
                  onClose={handleEdgeEditorClose}
                  theme={theme}
                  availableVariables={availableVariables as any}
                  readOnly={selectedAgent?.source === 'store'}
                />
              ) : (
                <Suspense fallback={null}>
                <YamlDrawer
                  content={yamlContent}
                  onChange={handleYamlChange}
                  onClose={() => setShowYaml(false)}
                  theme={theme}
                />
                </Suspense>
              )}
            </div>
          )}
          </>
          )}
        </div>
        </div>
      </div>
      )}
      {/* Create Agent Modal */}
      <CreateAgentModal
        isOpen={showCreateModal}
        onClose={() => setShowCreateModal(false)}
        onCreate={handleCreateAgent}
      />

      {/* Delete Confirmation Modal */}
      <ConfirmDeleteModal
        isOpen={!!deleteTarget}
        onClose={() => setDeleteTarget(null)}
        onConfirm={confirmDelete}
        agentName={deleteTarget ? snakeToTitleCase(deleteTarget.name) : ''}
        isStoreFlow={deleteTarget?.source === 'store'}
      />

      {/* AI Chat Panel */}
      <AIChatPanel
        isOpen={showAIChat}
        onClose={() => { setShowAIChat(false); setAIFocusedNode(null); setAISelectedNodeIds([]); }}
        context={aiChatContext}
        currentYaml={yamlContent}
        selectedNodes={aiSelectedNodeIds.length > 0 ? aiSelectedNodeIds : (selectedNodeId ? [selectedNodeId] : [])}
        focusedNode={aiFocusedNode}
        agentId={selectedAgent?.id}
        tools={availableTools as any}
        onToolsRefresh={loadTools}
        onApplyYaml={(newYaml: string) => {
          // Apply the new YAML
          setYamlContent(newYaml)
          // Push new state to history (for undo)
          pushToHistory(newYaml)
          // Auto-save after applying (skip for store flows)
          if (selectedAgent && selectedAgent.source !== 'store') {
            saveAgent(selectedAgent.name, newYaml, selectedAgent.scope).then(() => {
            }).catch(err => {
              console.error('Failed to auto-save:', err)
            })
          }
        }}
      />

      {/* AI Chat Toggle Button - hidden for store flows (read-only) */}
      {selectedAgent && !isRunning && selectedAgent.source !== 'store' && (
        <button
          onClick={() => {
            // Detect if flow has existing nodes
            const hasExistingNodes = nodes.length > 0 && nodes.some(n => n.id !== 'START' && n.id !== 'END')
            setAIChatContext(hasExistingNodes ? 'modify_flow' : 'create_flow')
            setAISelectedNodeIds([])
            setAIFocusedNode(null)
            setShowAIChat(true)
          }}
          className="send-gradient fixed bottom-4 right-4 z-40 flex size-14 items-center justify-center rounded-full text-white shadow-[var(--shadow-elevated)] transition-all hover:scale-105 hover:opacity-90"
          title="AI Assistant"
        >
          <svg className="size-6" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M9.663 17h4.673M12 3v1m6.364 1.636l-.707.707M21 12h-1M4 12H3m3.343-5.657l-.707-.707m2.828 9.9a5 5 0 117.072 0l-.548.547A3.374 3.374 0 0014 18.469V19a2 2 0 11-4 0v-.531c0-.895-.356-1.754-.988-2.386l-.548-.547z" />
          </svg>
        </button>
      )}

      {/* Toast Notification */}
      {toast && (
        <ToastNotification toast={toast} onDismiss={() => setToast(null)} />
      )}

      {/* Upgrade Dialog */}
      {showUpgradeDialog && (
        <UpgradeDialog info={showUpgradeDialog} onClose={() => setShowUpgradeDialog(null)} />
      )}
    </>
  )
}

export default App
