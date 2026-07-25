import type { RefObject } from 'react'
import { Edit3, Brain, Wrench, Settings, MessageSquare } from 'lucide-react'

// --- Types ---

export interface VariableGroup {
  nodeName: string
  variables: string[]
}

export interface AvailableTool {
  name: string
  [key: string]: any
}

export interface FlowNode {
  id: string
  type?: string
  data?: {
    label?: string
    nodeType?: string
    yaml?: Record<string, any>
    [key: string]: any
  }
}

export type NodeType = 'input' | 'llm' | 'tool' | 'updateState' | 'update_state' | 'output'

// Node type icons
export const NODE_ICONS: Record<string, React.ComponentType<any>> = {
  input: Edit3,
  llm: Brain,
  tool: Wrench,
  updateState: Settings,
  update_state: Settings,
  output: MessageSquare,
}

// Node type colors — CSS vars follow the active brand pack
export const NODE_COLORS: Record<string, string> = {
  input: 'var(--brand-muted)',
  llm: 'var(--node-llm)',
  tool: 'var(--node-tool)',
  updateState: '#4A5568',
  update_state: '#4A5568',
  output: 'var(--node-output)',
}

export interface VariablePanelProps {
  variableGroups: VariableGroup[]
  activeTextareaRef?: RefObject<HTMLTextAreaElement | null>
  getValue?: () => string
  setValue?: (val: string) => void
  onVariableClick?: (v: string) => void
}

export interface HighlightedTextareaProps {
  value: string
  onChange: (e: React.ChangeEvent<HTMLTextAreaElement>) => void
  onFocus?: () => void
  isActive?: boolean
  placeholder?: string
  className?: string
  style?: React.CSSProperties
  validVariables?: VariableGroup[]
}

export interface RawToolOutputEditorProps {
  value: Record<string, string> | undefined
  onChange: (val: Record<string, string> | undefined) => void
}

export interface OutputModelEditorProps {
  value: Record<string, string> | undefined
  onChange: (val: Record<string, string>) => void
  theme?: string
  hideLabel?: boolean
  singleField?: boolean
}

export interface UpdateStateFormProps {
  data: Record<string, any>
  onChange: (data: Record<string, any>) => void
  theme?: string
}

export interface InputNodeFormProps {
  data: Record<string, any>
  onChange: (data: Record<string, any>) => void
  theme?: string
}

export interface LlmNodeFormProps {
  data: Record<string, any>
  onChange: (data: Record<string, any>) => void
  theme?: string
  availableTools?: AvailableTool[]
  availableVariables?: VariableGroup[]
}

export interface ToolNodeFormProps {
  data: Record<string, any>
  onChange: (data: Record<string, any>) => void
  theme?: string
  availableTools?: AvailableTool[]
}

export interface OutputNodeFormProps {
  data: Record<string, any>
  onChange: (data: Record<string, any>) => void
  theme?: string
  availableVariables?: VariableGroup[]
}

export interface NodeEditorProps {
  node: FlowNode | null
  onSave: (nodeId: string, data: Record<string, any>) => void
  onClose: () => void
  theme?: string
  availableTools?: AvailableTool[]
  availableVariables?: VariableGroup[]
  readOnly?: boolean
  onAIAssist?: (node: FlowNode, name: string, data: Record<string, any>) => void
}
