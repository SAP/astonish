import { Loader2 } from 'lucide-react'
import { saveSettings } from './settingsApi'
import type { FullConfig, SettingsData, MCPConfigData, MCPServerConfig, WebCapableTools, StandardServer } from './settingsApi'
import { FULL_CONFIG_SECTIONS } from './settingsMenuItems'
import FlowStorePanel from '../FlowStorePanel'
import ChatSettings from './ChatSettings'
import ConnectedChannelsSettings from './ConnectedChannelsSettings'
import BrowserSettings from './BrowserSettings'
import ChannelsSettings from './ChannelsSettings'
import SessionsSettings from './SessionsSettings'
import MemorySettings from './MemorySettings'
import SubAgentsSettings from './SubAgentsSettings'
import SkillsSettings from './SkillsSettings'
import SchedulerSettings from './SchedulerSettings'
import DaemonSettings from './DaemonSettings'
import IdentitySettings from './IdentitySettings'
import CredentialsSettings from './CredentialsSettings'
import SandboxSettings from './SandboxSettings'
import GeneralSettings from './GeneralSettings'
import ProvidersSettings from './ProvidersSettings'
import UserDefaultModelSettings from './UserDefaultModelSettings'
import MCPServersSettings from './MCPServersSettings'
import TapsSettings from './TapsSettings'
import { useState, lazy, Suspense } from 'react'

const KnowledgeBrowser = lazy(() => import('../KnowledgeBrowser'))

interface SettingsContentProps {
  activeSection: string
  // Settings API data (for General, Providers, MCP)
  settings: SettingsData | null
  mcpConfig: MCPConfigData | null
  webCapableTools: WebCapableTools
  standardServers: StandardServer[]
  loadData: () => Promise<void>
  // Full config data (for all other sections)
  fullConfig: FullConfig | null
  fullConfigLoading: boolean
  invalidateFullConfig: () => void
  // Callbacks
  onToolsRefresh?: () => void
  onSettingsSaved?: () => void
  onSectionChange?: (section: string) => void
  // Theme
  theme?: string
  // Platform mode flags
  isPlatformMode?: boolean
  isOrgAdmin?: boolean
  // User context (for Knowledge browser)
  user?: { id: string; email: string; display_name: string; role: string }
  activeTeam?: string | null
}

/**
 * Renders the correct settings sub-component based on activeSection.
 * Shared by SettingsPage (personal mode), WorkspaceResources, and WorkspaceAdmin.
 */
export default function SettingsContent({
  activeSection,
  settings,
  mcpConfig,
  webCapableTools,
  standardServers,
  loadData,
  fullConfig,
  fullConfigLoading,
  invalidateFullConfig,
  onToolsRefresh,
  onSettingsSaved,
  onSectionChange,
  theme = 'dark',
  isPlatformMode = false,
  isOrgAdmin = false,
  user,
  activeTeam,
}: SettingsContentProps) {
  const [saving, setSaving] = useState(false)
  const [, setSaveSuccess] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // State for General form
  const [generalForm, setGeneralForm] = useState({
    default_provider: '',
    default_model: '',
    web_search_tool: '',
    web_extract_tool: '',
    timezone: ''
  })

  // State for Providers
  const [providerForms, setProviderForms] = useState<Record<string, Record<string, string>>>({})

  // State for MCP
  const [mcpServers, setMcpServers] = useState<Record<string, MCPServerConfig>>({})
  const [mcpServerNames, setMcpServerNames] = useState<Record<string, string>>({})
  const [mcpServerArgs, setMcpServerArgs] = useState<Record<string, string>>({})
  const [, setMcpHasChanges] = useState(false)

  // Initialize forms when settings load
  const settingsRef = useState<SettingsData | null>(null)
  if (settings && settings !== settingsRef[0]) {
    settingsRef[1](settings)
    setGeneralForm({
      default_provider: settings.general.default_provider || '',
      default_model: settings.general.default_model || '',
      web_search_tool: settings.general.web_search_tool || '',
      web_extract_tool: settings.general.web_extract_tool || '',
      timezone: settings.general.timezone || ''
    })
    const pForms: Record<string, Record<string, string>> = {}
    settings.providers.forEach((p: any) => {
      pForms[p.name] = { ...p.fields }
    })
    setProviderForms(pForms)
  }

  const mcpRef = useState<MCPConfigData | null>(null)
  if (mcpConfig && mcpConfig !== mcpRef[0]) {
    mcpRef[1](mcpConfig)
    const servers = mcpConfig.mcpServers || {}
    setMcpServers(servers)
    const names: Record<string, string> = {}
    const args: Record<string, string> = {}
    Object.entries(servers).forEach(([name, server]) => {
      names[name] = name
      args[name] = (server.args || []).join(', ')
    })
    setMcpServerNames(names)
    setMcpServerArgs(args)
  }

  const handleSaveGeneral = async () => {
    setSaving(true)
    setSaveSuccess(false)
    try {
      await saveSettings({ general: generalForm })
      setSaveSuccess(true)
      if (onSettingsSaved) onSettingsSaved()
      setTimeout(() => setSaveSuccess(false), 2000)
    } catch (err: any) {
      setError(err.message)
    } finally {
      setSaving(false)
    }
  }

  const onSaved = () => invalidateFullConfig()

  return (
    <>
      {activeSection === 'general' && (
        <GeneralSettings
          settings={settings}
          generalForm={generalForm}
          setGeneralForm={setGeneralForm}
          webCapableTools={webCapableTools}
          standardServers={standardServers}
          saving={saving}
          onSave={handleSaveGeneral}
          onSectionChange={onSectionChange}
          isPlatform={isPlatformMode}
        />
      )}

      {activeSection === 'providers' && (
        <ProvidersSettings
          settings={settings}
          providerForms={providerForms}
          setProviderForms={setProviderForms}
          generalForm={generalForm}
          setGeneralForm={setGeneralForm}
          saving={saving}
          setSaving={setSaving}
          setSaveSuccess={setSaveSuccess}
          error={error}
          setError={setError}
          loadData={loadData}
          onSettingsSaved={onSettingsSaved}
          inheritedProviders={[]}
          onSaveDefault={async (provider, model) => {
            await saveSettings({ general: { default_provider: provider, default_model: model } })
            if (onSettingsSaved) onSettingsSaved()
          }}
        />
      )}

      {activeSection === 'mcp' && (
        <MCPServersSettings
          mcpServers={mcpServers}
          setMcpServers={setMcpServers}
          mcpServerNames={mcpServerNames}
          setMcpServerNames={setMcpServerNames}
          mcpServerArgs={mcpServerArgs}
          setMcpServerArgs={setMcpServerArgs}
          setMcpHasChanges={setMcpHasChanges}
          standardServers={isPlatformMode ? [] : standardServers}
          saving={saving}
          setSaving={setSaving}
          setSaveSuccess={setSaveSuccess}
          setError={setError}
          onToolsRefresh={onToolsRefresh}
          loadData={loadData}
          setGeneralForm={setGeneralForm}
          theme={theme}
        />
      )}

      {activeSection === 'taps' && <TapsSettings />}
      {activeSection === 'flows' && <FlowStorePanel />}

      {FULL_CONFIG_SECTIONS.includes(activeSection) && fullConfigLoading && (
        <div className="flex items-center justify-center gap-2 rounded-xl border bg-card py-12 text-sm text-muted-foreground shadow-sm">
          <Loader2 className="size-5 animate-spin text-primary" />
          <span>Loading settings...</span>
        </div>
      )}

      {activeSection === 'chat' && fullConfig && (
        <ChatSettings config={fullConfig.chat} onSaved={onSaved} />
      )}
      {activeSection === 'channels' && (
        isPlatformMode
          ? <ConnectedChannelsSettings isAdmin={isOrgAdmin} />
          : fullConfig && <ChannelsSettings config={fullConfig.channels} onSaved={onSaved} />
      )}
      {activeSection === 'browser' && fullConfig && (
        <BrowserSettings config={fullConfig.browser} onSaved={onSaved} />
      )}
      {activeSection === 'sessions' && fullConfig && (
        <SessionsSettings config={fullConfig.sessions} onSaved={onSaved} />
      )}
      {activeSection === 'memory' && fullConfig && (
        <MemorySettings config={fullConfig.memory} onSaved={onSaved} />
      )}
      {activeSection === 'sub_agents' && fullConfig && (
        <SubAgentsSettings config={fullConfig.sub_agents} onSaved={onSaved} />
      )}
      {activeSection === 'skills' && fullConfig && (
        <div className="flex-1 overflow-hidden p-6 flex flex-col">
          <SkillsSettings
            config={fullConfig.skills}
            onSaved={onSaved}
            theme={theme}
            scope={isPlatformMode ? 'org' : undefined}
            isPlatform={isPlatformMode}
            canManage={isPlatformMode ? isOrgAdmin : true}
          />
        </div>
      )}
      {activeSection === 'scheduler' && fullConfig && (
        <SchedulerSettings config={fullConfig.scheduler} onSaved={onSaved} isPlatform={isPlatformMode} />
      )}
      {activeSection === 'daemon' && fullConfig && (
        <DaemonSettings config={fullConfig.daemon} onSaved={onSaved} />
      )}
      {activeSection === 'sandbox' && fullConfig && (
        <SandboxSettings config={fullConfig.sandbox} onSaved={onSaved} />
      )}
      {activeSection === 'identity' && fullConfig && (
        <IdentitySettings config={fullConfig.agent_identity} onSaved={onSaved} />
      )}
      {activeSection === 'knowledge' && user && (
        <Suspense fallback={<div className="flex items-center justify-center rounded-xl border bg-card py-12 text-primary"><Loader2 className="size-5 animate-spin" /></div>}>
          <div className="flex-1 overflow-hidden p-6 flex flex-col">
            <KnowledgeBrowser theme={theme as 'dark' | 'light'} user={user} activeTeam={activeTeam} />
          </div>
        </Suspense>
      )}

      {activeSection === 'credentials' && <CredentialsSettings isPlatform={isPlatformMode} />}

      {activeSection === 'user-default-model' && <UserDefaultModelSettings />}
    </>
  )
}
