import { useRef } from 'react'
import { ChevronsUp, ChevronUp, ChevronDown, ChevronsDown } from 'lucide-react'
import type { SelectionMetadata } from '@/components/docs/slides/runtime/EditController'

interface ElementPropertiesPanelProps {
  selection: SelectionMetadata
  onPropertyChange?: (key: string, value: string) => void
  onZOrder?: (direction: 'front' | 'forward' | 'backward' | 'back') => void
}

function SectionHeader({ children }: { children: React.ReactNode }) {
  return (
    <h3
      className="mb-2 text-[11px] font-semibold uppercase tracking-wider"
      style={{ color: 'var(--text-muted)' }}
    >
      {children}
    </h3>
  )
}

function PropField({ label, value, unit, attrKey, onChange }: {
  label: string; value: string | number; unit?: string;
  attrKey?: string; onChange?: (key: string, value: string) => void
}) {
  return (
    <div className="flex flex-col gap-0.5">
      <label className="text-[10px] uppercase tracking-wide" style={{ color: 'var(--text-muted)' }}>
        {label}
      </label>
      <div className="flex items-center">
        <input
          type="text"
          value={String(value)}
          onChange={(e) => {
            if (attrKey && onChange) {
              const v = e.target.value
              if (/^-?\d+$/.test(v) || v === '' || v === '-') {
                onChange(attrKey, v)
              }
            }
          }}
          className="w-full rounded border px-2 py-1 text-xs"
          style={{
            background: 'var(--bg-primary)',
            borderColor: 'var(--border-color)',
            color: 'var(--text-primary)',
          }}
          data-testid={`prop-${label.toLowerCase()}`}
        />
        {unit && (
          <span className="ml-1 text-[10px]" style={{ color: 'var(--text-muted)' }}>
            {unit}
          </span>
        )}
      </div>
    </div>
  )
}

function ColorSwatch({ color, label, attrKey, onChange }: {
  color: string; label: string;
  attrKey?: string; onChange?: (key: string, value: string) => void
}) {
  const colorRef = useRef<HTMLInputElement>(null)
  return (
    <div className="flex items-center gap-2">
      <label className="text-[10px] uppercase tracking-wide" style={{ color: 'var(--text-muted)' }}>
        {label}
      </label>
      <div
        className="rounded border cursor-pointer"
        style={{
          width: 24,
          height: 24,
          background: color || 'transparent',
          borderColor: 'var(--border-color)',
        }}
        data-testid={`swatch-${label.toLowerCase()}`}
        onClick={() => colorRef.current?.click()}
      />
      <input
        ref={colorRef}
        type="color"
        value={color || '#000000'}
        onChange={(e) => { if (attrKey && onChange) onChange(attrKey, e.target.value) }}
        className="sr-only"
        data-testid={`color-input-${label.toLowerCase()}`}
        tabIndex={-1}
      />
      <span className="text-xs" style={{ color: 'var(--text-primary)' }}>
        {color || 'none'}
      </span>
    </div>
  )
}

export default function ElementPropertiesPanel({ selection, onPropertyChange, onZOrder }: ElementPropertiesPanelProps) {
  return (
    <div
      className="flex shrink-0 flex-col gap-4 overflow-y-auto p-3"
      style={{
        width: 260,
        background: 'var(--bg-secondary)',
        borderLeft: '1px solid var(--border-color)',
      }}
      data-testid="element-properties-panel"
    >
      {/* POSITION & SIZE */}
      <section>
        <SectionHeader>Position &amp; Size</SectionHeader>
        <div className="grid grid-cols-2 gap-2">
          <PropField label="X" value={Math.round(selection.x)} attrKey="x" onChange={onPropertyChange} />
          <PropField label="Y" value={Math.round(selection.y)} attrKey="y" onChange={onPropertyChange} />
          <PropField label="W" value={Math.round(selection.w)} attrKey="w" onChange={onPropertyChange} />
          <PropField label="H" value={Math.round(selection.h)} attrKey="h" onChange={onPropertyChange} />
        </div>
        <div className="mt-2">
          <PropField label="Rotation" value={Math.round(selection.rotation)} unit="°" attrKey="rot" onChange={onPropertyChange} />
        </div>
      </section>

      {/* FILL */}
      <section>
        <SectionHeader>Fill</SectionHeader>
        <ColorSwatch color={selection.fill} label="Fill" attrKey="fill" onChange={onPropertyChange} />
        <div className="mt-2 flex items-center gap-2">
          <label className="text-[10px] uppercase tracking-wide" style={{ color: 'var(--text-muted)' }}>
            Opacity
          </label>
          <input
            type="range"
            min={0}
            max={100}
            value={Math.round(selection.opacity * 100)}
            onChange={(e) => { if (onPropertyChange) onPropertyChange('opacity', String(Number(e.target.value) / 100)) }}
            className="flex-1"
            data-testid="prop-opacity"
          />
          <span className="text-xs" style={{ color: 'var(--text-primary)' }}>
            {Math.round(selection.opacity * 100)}%
          </span>
        </div>
      </section>

      {/* STROKE */}
      <section>
        <SectionHeader>Stroke</SectionHeader>
        <ColorSwatch color={selection.stroke} label="Stroke" attrKey="line" onChange={onPropertyChange} />
        <div className="mt-2">
          <PropField label="Width" value={selection.strokeWidth} attrKey="line-width" onChange={onPropertyChange} />
        </div>
      </section>

      {/* Z-ORDER */}
      <section>
        <SectionHeader>Z-Order</SectionHeader>
        <div className="flex items-center gap-1">
          {[
            { icon: ChevronsUp, label: 'Bring to front', testId: 'z-front', dir: 'front' as const },
            { icon: ChevronUp, label: 'Bring forward', testId: 'z-forward', dir: 'forward' as const },
            { icon: ChevronDown, label: 'Send backward', testId: 'z-backward', dir: 'backward' as const },
            { icon: ChevronsDown, label: 'Send to back', testId: 'z-back', dir: 'back' as const },
          ].map(item => {
            const Icon = item.icon
            return (
              <button
                key={item.testId}
                type="button"
                title={item.label}
                aria-label={item.label}
                data-testid={item.testId}
                onClick={() => onZOrder?.(item.dir)}
                className="flex items-center justify-center rounded-md border p-1.5 transition-colors"
                style={{
                  borderColor: 'var(--border-color)',
                  color: 'var(--text-secondary)',
                  background: 'var(--bg-primary)',
                }}
              >
                <Icon size={16} />
              </button>
            )
          })}
        </div>
      </section>
    </div>
  )
}
