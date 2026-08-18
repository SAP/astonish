import {
  Settings, Key, Server, MessageSquare, Globe, Radio, Database,
  Brain, GitFork, Wand2, Clock, Shield, Box,
  GitBranch, Store, Users, BookOpen, UserCog, FileText, Crown, Building2, KeyRound, Layers, Network
} from 'lucide-react'

export interface SettingsMenuItem {
  id: string
  label: string
  icon: any
}

// Personal — visible to every user regardless of role
export const PERSONAL_ITEMS: SettingsMenuItem[] = [
  { id: 'user-general', label: 'General', icon: Settings },
  { id: 'user-default-model', label: 'Default Model', icon: Key },
  { id: 'channels', label: 'Channels', icon: Radio },
  { id: 'knowledge', label: 'Knowledge', icon: BookOpen },
  { id: 'credentials', label: 'Credentials', icon: KeyRound },
  { id: 'scheduler', label: 'Scheduler', icon: Clock },
]

// Legacy alias for backward compatibility
export const PREFERENCE_ITEMS = PERSONAL_ITEMS

// Team resources — shared within a team (platform mode)
export const TEAM_ITEMS: SettingsMenuItem[] = [
  { id: 'team-members', label: 'Members', icon: Users },
  { id: 'team-providers', label: 'Providers', icon: Key },
  { id: 'team-skills', label: 'Skills', icon: Wand2 },
  { id: 'team-mcp', label: 'MCP Servers', icon: Server },
  { id: 'team-a2a', label: 'A2A Agents', icon: Globe },
  { id: 'team-network', label: 'Network Policy', icon: Network },
  { id: 'team-taps', label: 'Repositories', icon: GitBranch },
  { id: 'team-flows', label: 'Flow Store', icon: Store },
  { id: 'team-container', label: 'Container', icon: Box },
]

// Organization management — org admin/owner only (platform mode)
export const ORG_ITEMS: SettingsMenuItem[] = [
  { id: 'org-general', label: 'General', icon: Settings },
  { id: 'org-teams', label: 'Teams', icon: Layers },
  { id: 'org-users', label: 'Users', icon: UserCog },
  { id: 'org-providers', label: 'Providers', icon: Key },
  { id: 'org-skills', label: 'Skills', icon: Wand2 },
  { id: 'org-mcp', label: 'MCP Servers', icon: Server },
  { id: 'org-a2a', label: 'A2A Agents', icon: Globe },
  { id: 'org-network', label: 'Network Policy', icon: Network },
  { id: 'org-audit', label: 'Audit', icon: FileText },
]

// Platform administration — superadmin only
// Combines infrastructure management + system configuration.
export const PLATFORM_ITEMS: SettingsMenuItem[] = [
  { id: 'platform-orgs', label: 'Organizations', icon: Building2 },
  { id: 'platform-users', label: 'Users', icon: UserCog },
  { id: 'platform-providers', label: 'Providers', icon: Key },
  { id: 'platform-skills', label: 'Skills', icon: Wand2 },
  { id: 'platform-mcp', label: 'MCP Servers', icon: Server },
  { id: 'platform-a2a', label: 'A2A Agents', icon: Globe },
  { id: 'platform-network', label: 'Network Policy', icon: Network },
  { id: 'platform-channels', label: 'Channels', icon: Radio },
  { id: 'platform-auth', label: 'Authentication', icon: Crown },
  { id: 'platform-sandbox', label: 'Base Sandbox', icon: Box },
  // System configuration (formerly "System" section)
  { id: 'platform-general', label: 'General', icon: Settings },
  { id: 'platform-chat', label: 'Chat', icon: MessageSquare },
  { id: 'platform-memory', label: 'Memory', icon: Brain },
  { id: 'platform-sessions', label: 'Sessions', icon: Database },
  { id: 'platform-sub-agents', label: 'Sub-Agents', icon: GitFork },
  { id: 'platform-browser', label: 'Browser', icon: Globe },
  { id: 'platform-daemon', label: 'Daemon', icon: Shield },
  { id: 'platform-sandbox-settings', label: 'Sandbox', icon: Box },
]

// Mapping from platform-prefixed system section IDs to their SettingsContent IDs.
// Used by SettingsPage to strip the prefix before delegating to SettingsContent.
export const PLATFORM_SYSTEM_SECTIONS: Record<string, string> = {
  'platform-general': 'general',
  'platform-chat': 'chat',
  'platform-memory': 'memory',
  'platform-sessions': 'sessions',
  'platform-sub-agents': 'sub_agents',
  'platform-browser': 'browser',
  'platform-daemon': 'daemon',
  'platform-sandbox-settings': 'sandbox',
}

// Section keys that use the full config API
export const FULL_CONFIG_SECTIONS = [
  'chat', 'browser', 'channels', 'sessions', 'memory',
  'sub_agents', 'skills', 'scheduler', 'daemon', 'sandbox',
  'identity'
]

// --- Deprecated exports (kept for backward compatibility) ---

// @deprecated Use PLATFORM_ITEMS (system items are now merged into Platform)
export const SYSTEM_ITEMS: SettingsMenuItem[] = [
  { id: 'general', label: 'General', icon: Settings },
  { id: 'chat', label: 'Chat', icon: MessageSquare },
  { id: 'providers', label: 'Providers', icon: Key },
  { id: 'memory', label: 'Memory', icon: Brain },
  { id: 'mcp', label: 'MCP Servers', icon: Server },
  { id: 'sessions', label: 'Sessions', icon: Database },
  { id: 'sub_agents', label: 'Sub-Agents', icon: GitFork },
  { id: 'browser', label: 'Browser', icon: Globe },
  { id: 'daemon', label: 'Daemon', icon: Shield },
  { id: 'sandbox', label: 'Sandbox', icon: Box },
]

// @deprecated Legacy alias
export const ADMIN_ITEMS = SYSTEM_ITEMS

// @deprecated Personal mode no longer exists
export const RESOURCE_ITEMS: SettingsMenuItem[] = [
  { id: 'skills', label: 'Skills', icon: Wand2 },
  { id: 'taps', label: 'Repositories', icon: GitBranch },
  { id: 'flows', label: 'Flow Store', icon: Store },
]

// @deprecated Personal mode no longer exists
export const ALL_MENU_ITEMS: SettingsMenuItem[] = [
  ...PERSONAL_ITEMS,
  ...RESOURCE_ITEMS,
  ...SYSTEM_ITEMS,
]
