import { useRef } from 'react'
import { MousePointer2, Type, Square, Circle, Minus, ImageIcon, Undo2, Redo2 } from 'lucide-react'

export const SHAPE_TOOLS = [
  { id: 'select', icon: MousePointer2, label: 'Select' },
  { id: 'text', icon: Type, label: 'Text' },
  { id: 'rect', icon: Square, label: 'Rectangle' },
  { id: 'circle', icon: Circle, label: 'Circle' },
  { id: 'line', icon: Minus, label: 'Line' },
  { id: 'image', icon: ImageIcon, label: 'Image' },
] as const

export type ShapeToolId = (typeof SHAPE_TOOLS)[number]['id']

export const SHAPE_DEFAULTS: Record<string, { tag: string; w: number; h: number; defaults: Record<string, string> }> = {
  rect: { tag: 'ast-shape', w: 300, h: 200, defaults: { kind: 'rect', fill: '#4F46E5', geom: 'rect' } },
  circle: { tag: 'ast-shape', w: 200, h: 200, defaults: { kind: 'ellipse', fill: '#4F46E5', geom: 'ellipse' } },
  line: { tag: 'ast-shape', w: 300, h: 2, defaults: { kind: 'line', line: '#000000', 'line-width': '2', geom: 'line' } },
  text: { tag: 'ast-text', w: 400, h: 60, defaults: { size: '32', weight: '400', align: 'left' } },
}

interface ShapeToolbarProps {
  activeTool: string
  onToolChange: (tool: string) => void
  onImagePick?: (file: File) => void
}

export default function ShapeToolbar({ activeTool, onToolChange, onImagePick }: ShapeToolbarProps) {
  const fileRef = useRef<HTMLInputElement>(null)

  return (
    <div
      className="flex shrink-0 flex-col items-center gap-1 py-2"
      style={{
        width: 48,
        background: 'var(--bg-secondary)',
        borderRight: '1px solid var(--border-color)',
      }}
      role="toolbar"
      aria-label="Shape tools"
    >
      {SHAPE_TOOLS.map(tool => {
        const Icon = tool.icon
        const isActive = activeTool === tool.id
        return (
          <button
            key={tool.id}
            type="button"
            title={tool.label}
            aria-label={tool.label}
            aria-pressed={isActive}
            data-testid={`tool-${tool.id}`}
            onClick={() => {
              if (tool.id === 'image') {
                fileRef.current?.click()
                return
              }
              onToolChange(tool.id)
            }}
            className="flex items-center justify-center rounded-md transition-colors"
            style={{
              width: 36,
              height: 36,
              background: isActive ? 'var(--brand)' : 'transparent',
              color: isActive ? 'var(--brand-foreground, #fff)' : 'var(--text-secondary)',
            }}
          >
            <Icon size={18} />
          </button>
        )
      })}

      {/* Hidden file input for image upload */}
      <input
        ref={fileRef}
        type="file"
        accept="image/*"
        className="hidden"
        data-testid="image-file-input"
        onChange={e => {
          const file = e.target.files?.[0]
          if (file) onImagePick?.(file)
          // Reset so the same file can be re-selected
          e.target.value = ''
        }}
      />

      {/* Separator */}
      <div
        className="my-1"
        style={{ width: 24, height: 1, background: 'var(--border-color)' }}
        role="separator"
      />

      {/* Undo / Redo */}
      <button
        type="button"
        title="Undo"
        aria-label="Undo"
        disabled
        data-testid="tool-undo"
        className="flex items-center justify-center rounded-md transition-colors disabled:opacity-30"
        style={{ width: 36, height: 36, color: 'var(--text-secondary)' }}
      >
        <Undo2 size={18} />
      </button>
      <button
        type="button"
        title="Redo"
        aria-label="Redo"
        disabled
        data-testid="tool-redo"
        className="flex items-center justify-center rounded-md transition-colors disabled:opacity-30"
        style={{ width: 36, height: 36, color: 'var(--text-secondary)' }}
      >
        <Redo2 size={18} />
      </button>
    </div>
  )
}
