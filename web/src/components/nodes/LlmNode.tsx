import { Brain } from 'lucide-react'
import { type Node, type NodeProps } from '@xyflow/react'
import OverflowNode, { type NodeData } from './OverflowNode'

type LlmNodeType = Node<NodeData, 'llm'>

export default function LlmNode({ id, data, selected }: NodeProps<LlmNodeType>) {
  return (
    <OverflowNode 
      id={id}
      data={data} 
      selected={selected}
      icon={Brain} 
      nodeType="LLM"
      iconColor="var(--node-llm)"
    />
  )
}
