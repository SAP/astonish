// ---- Local message types used in the chat UI ----

export interface FleetMessageItem {
  type: 'fleet_message'
  id?: string
  sender: string
  text: string
  mentions?: string[]
  timestamp: number
  metadata?: Record<string, unknown>
  [key: string]: unknown
}

export interface UserMessage {
  type: 'user'
  content: string
  attachments?: AttachmentInfo[]
}

// AttachmentInfo describes a file attachment (used in user messages for history display)
export interface AttachmentInfo {
  filename: string
  mimeType: string
  size: number        // byte count
  data?: string       // base64 data (included for images to render thumbnails)
}

export interface AgentMessage {
  type: 'agent'
  content: string
  _streaming?: boolean
}

export interface ToolCallMessage {
  type: 'tool_call'
  toolName: unknown
  toolArgs: unknown
}

export interface ToolResultMessage {
  type: 'tool_result'
  toolName: unknown
  toolResult: unknown
}

export interface ImageMessage {
  type: 'image'
  data: unknown
  mimeType: unknown
}

export interface ErrorMessage {
  type: 'error'
  content: string
}

export interface ErrorInfoMessage {
  type: 'error_info'
  title: unknown
  reason: unknown
  suggestion: unknown
  originalError: unknown
}

export interface ApprovalMessage {
  type: 'approval'
  toolName: unknown
  options: unknown
}

export interface AutoApprovedMessage {
  type: 'auto_approved'
  toolName: unknown
}

export interface ThinkingMessage {
  type: 'thinking'
  content: unknown
}

export interface SystemMessage {
  type: 'system'
  content: string
}

export interface RetryMessage {
  type: 'retry'
  attempt: unknown
  maxRetries: unknown
  reason: unknown
}

export interface FleetExecutionMessage {
  type: 'fleet_execution'
  events: FleetEvent[]
  currentPhase: string | null
  currentAgent: string | null
  status: string
  maxParallelAgents?: number
}

export interface BrowserHandoffMessage {
  type: 'browser_handoff'
  vncProxyUrl: string
  pageUrl: string
  pageTitle: string
  reason: string
}

// ---- Sub-task execution (delegate_tasks) progress ----

export interface SubTaskExecutionMessage {
  type: 'subtask_execution'
  events: SubTaskEvent[]
  tasks: SubTaskInfo[]        // All tasks in the delegation (from delegation_start)
  status: string              // 'running' | 'complete' | 'error' | 'partial'
}

export interface SubTaskInfo {
  name: string
  description: string
}

export interface SubTaskEvent {
  type: string                // delegation_start, delegation_complete, task_start, task_complete, task_failed, task_tool_call, task_tool_result, task_text
  task_name?: string
  status?: string
  duration?: string
  error?: string
  tool_name?: string
  tool_args?: unknown
  tool_result?: unknown
  text?: string
  tasks?: SubTaskInfo[]
  timestamp?: number
}

export interface FleetEvent {
  type: string
  phase?: string
  agent?: string
  lane_index?: number
  task_id?: string
  title?: string
  status?: string
  claimed_by?: string
  required_capabilities?: string[]
  detail?: string
  text?: string
  message?: string
  args?: unknown
  result?: unknown
  timestamp?: number
  [key: string]: unknown
}

export interface ArtifactMessage {
  type: 'artifact'
  path: string
  toolName: string
  // Monotonic client-side revision for repeated mutations of the same path.
  // Embedded viewers include it in their fetch effect dependencies so a later
  // edit_file event reloads content even when the path is unchanged.
  revision?: number
  // Set true when the same turn also emitted a `report_marker` event whose
  // `path` matches this artifact's path. Drives the "embed inline as
  // EmbeddedFileViewer" gate in StudioChat — without it, the artifact
  // renders as the compact ArtifactCard download tile.
  isReport?: boolean
  // Optional human-readable title pulled from the `astonish-report` fence's
  // frontmatter (e.g., "Q4 Revenue Analysis"). Falls back to the file
  // basename in EmbeddedFileViewer when empty.
  reportTitle?: string
}

// Session-level artifact info (from session detail API)
export interface SessionArtifact {
  path: string
  fileName: string
  fileType: string
  toolName: string
  // Monotonic client-side revision for repeated mutations of the same path.
  // Session history responses omit this; live SSE handling populates it.
  revision?: number
  // Same semantics as ArtifactMessage.isReport, projected onto session
  // detail by the backend's joinReportMarkers step. Persists the report
  // gate decision across server restarts and history reloads.
  isReport?: boolean
  reportTitle?: string
}

// ---- Report marker (inline report gate signal) ----
//
// Emitted by the backend after detecting an ```astonish-report``` fence
// whose `path` matches a same-turn write_file/edit_file artifact. The
// frontend uses this signal to flip the matching ArtifactMessage's
// isReport flag so the chat embeds the file inline instead of showing a
// download card. The marker carries no on-screen content of its own.
export interface ReportMarkerMessage {
  type: 'report_marker'
  path: string
  title?: string
}

// ---- Execution plan (announce_plan / update_plan) ----

export interface PlanMessage {
  type: 'plan'
  goal: string                // Plan title (e.g., "Source-Level GitHub Comparison")
  steps: PlanStepInfo[]       // Ordered list of plan steps
}

export interface PlanStepInfo {
  name: string
  description: string
  status: 'pending' | 'running' | 'complete' | 'failed'
}

// ---- Generative UI app preview ----

export interface AppPreviewMessage {
  type: 'app_preview'
  code: string
  title: string
  description: string
  version: number
  appId?: string
}

// ---- Distill flow preview ----

export interface DistillPreviewMessage {
  type: 'distill_preview'
  yaml: string
  flowName: string
  description: string
  tags: string[]
  explanation: string
}

export interface DistillSavedMessage {
  type: 'distill_saved'
  filePath: string
  runCommand: string
}

export interface TutorialBlueprintSceneRow {
  id: string
  title: string
  voiceover: string
  visual_kind: string
  visual_description: string
  duration_hint_s?: number
}

export interface TutorialBlueprintPreviewMessage {
  type: 'tutorial_blueprint_preview'
  title: string
  suite: string
  blueprintYaml: string
  scenes: TutorialBlueprintSceneRow[]
}

export interface TutorialBlueprintApprovedMessage {
  type: 'tutorial_blueprint_approved'
  title: string
  suite: string
  blueprintYaml: string
  drillYaml?: string
  drillName?: string
  content?: string
}

export interface TutorialSceneRow {
  id: string
  voiceover?: string
  narration?: string
  visual_kind: string
  visual_description?: string
  hold_ms?: number
  path?: string
  duration_seconds?: number
}

export interface TutorialSceneSlideshowMessage {
  type: 'tutorial_scene_slideshow'
  title: string
  suite: string
  drill: string
  manifestPath: string
  scenes: TutorialSceneRow[]
}

export interface AppSavedMessage {
  type: 'app_saved'
  name: string
  path: string
}

export interface ReportPreviewMessage {
  type: 'report_preview'
  content: string
  title: string
}

export interface SourcesMessage {
  type: 'sources'
  urls: string[]
}

export interface NetworkDenialMessage {
  type: 'network_denial'
  denials: Array<{
    chunk_id?: string
    host: string
    port: number
    binary?: string
    rationale?: string
    security_notes?: string
    broader_pattern?: string
  }>
  sandbox_name: string
}

export type ChatMsg =
  | FleetMessageItem
  | UserMessage
  | AgentMessage
  | ToolCallMessage
  | ToolResultMessage
  | ImageMessage
  | ErrorMessage
  | ErrorInfoMessage
  | ApprovalMessage
  | AutoApprovedMessage
  | ThinkingMessage
  | SystemMessage
  | RetryMessage
  | FleetExecutionMessage
  | SubTaskExecutionMessage
  | ArtifactMessage
  | BrowserHandoffMessage
  | PlanMessage
  | AppPreviewMessage
  | DistillPreviewMessage
  | DistillSavedMessage
  | TutorialBlueprintPreviewMessage
  | TutorialBlueprintApprovedMessage
  | TutorialSceneSlideshowMessage
  | AppSavedMessage
  | ReportPreviewMessage
  | ReportMarkerMessage
  | SourcesMessage
  | NetworkDenialMessage

// ---- Fleet info / state ----

export interface FleetInfo {
  fleet_key: string
  fleet_name: string
  agents?: unknown
}

export interface FleetStateInfo {
  state: string
  active_agent: string
}

// ---- Deferred prompt types ----

export interface DeferredPrompt {
  message: string
  systemContext: string
  pinnedToolGroups?: string[]
}

// Agent identity colors for the team conversation view
export const AGENT_COLORS: Record<string, { bg: string; border: string; text: string; label: string }> = {
  po: { bg: 'rgba(59, 130, 246, 0.1)', border: 'rgba(59, 130, 246, 0.3)', text: '#60a5fa', label: 'PO' },
  architect: { bg: 'var(--brand-muted)', border: 'color-mix(in oklab, var(--brand) 30%, transparent)', text: 'var(--brand)', label: 'Architect' },
  ux: { bg: 'rgba(236, 72, 153, 0.1)', border: 'rgba(236, 72, 153, 0.3)', text: '#f472b6', label: 'UX' },
  dev: { bg: 'rgba(34, 197, 94, 0.1)', border: 'rgba(34, 197, 94, 0.3)', text: '#4ade80', label: 'Dev' },
  qa: { bg: 'rgba(234, 179, 8, 0.1)', border: 'rgba(234, 179, 8, 0.3)', text: '#facc15', label: 'QA' },
  system: { bg: 'rgba(107, 114, 128, 0.1)', border: 'rgba(107, 114, 128, 0.3)', text: '#9ca3af', label: 'System' },
}

export function getAgentColor(sender: string) {
  return AGENT_COLORS[sender] || { bg: 'rgba(6, 182, 212, 0.1)', border: 'rgba(6, 182, 212, 0.3)', text: '#22d3ee', label: sender }
}
