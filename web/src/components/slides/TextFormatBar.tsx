import { useMemo, useRef } from 'react'

const SYSTEM_FONTS = [
  'Arial',
  'Helvetica',
  'Times New Roman',
  'Georgia',
  'Courier New',
  'Verdana',
]

interface TextFormatBarProps {
  font: string
  fontSize: number
  fontWeight: string
  color?: string
  /** Custom font families from the deck theme (e.g. Manrope, 72 Brand). */
  customFonts?: string[]
  onPropertyChange?: (key: string, value: string) => void
}

export default function TextFormatBar({
  font,
  fontSize,
  fontWeight,
  color,
  customFonts,
  onPropertyChange,
}: TextFormatBarProps) {
  const colorRef = useRef<HTMLInputElement>(null)
  const isBold = fontWeight === '700' || fontWeight === 'bold'

  // Build font list: custom deck fonts first, then system fonts.
  // Always include the current font value so the dropdown never silently resets.
  const fontFamilies = useMemo(() => {
    const seen = new Set<string>()
    const result: string[] = []
    // Custom fonts from deck theme (e.g. Manrope) go first
    for (const f of customFonts ?? []) {
      const name = f.trim()
      if (name && !seen.has(name)) {
        seen.add(name)
        result.push(name)
      }
    }
    // System fonts
    for (const f of SYSTEM_FONTS) {
      if (!seen.has(f)) {
        seen.add(f)
        result.push(f)
      }
    }
    // Ensure the currently-selected font is always present (defensive)
    if (font && !seen.has(font)) {
      result.unshift(font)
    }
    return result
  }, [customFonts, font])

  return (
    <div
      className="flex items-center gap-1 rounded-lg px-2 py-1 shadow-lg"
      style={{
        background: 'var(--bg-secondary)',
        border: '1px solid var(--border-color)',
      }}
      role="toolbar"
      aria-label="Text formatting"
      data-testid="text-format-bar"
    >
      {/* Bold / Italic / Underline */}
      <button
        type="button"
        title="Bold"
        aria-label="Bold"
        aria-pressed={isBold}
        data-testid="fmt-bold"
        onClick={() => onPropertyChange?.('weight', isBold ? '400' : '700')}
        className="flex items-center justify-center rounded px-1.5 py-1 text-sm font-bold transition-colors"
        style={{
          color: isBold ? 'var(--brand)' : 'var(--text-primary)',
          background: isBold ? 'var(--brand-soft, rgba(99,102,241,0.1))' : 'transparent',
          minWidth: 28,
        }}
      >
        B
      </button>
      <button
        type="button"
        title="Italic"
        aria-label="Italic"
        data-testid="fmt-italic"
        className="flex items-center justify-center rounded px-1.5 py-1 text-sm italic transition-colors"
        style={{ color: 'var(--text-primary)', minWidth: 28 }}
      >
        I
      </button>
      <button
        type="button"
        title="Underline"
        aria-label="Underline"
        data-testid="fmt-underline"
        className="flex items-center justify-center rounded px-1.5 py-1 text-sm underline transition-colors"
        style={{ color: 'var(--text-primary)', minWidth: 28 }}
      >
        U
      </button>

      {/* Separator */}
      <div style={{ width: 1, height: 20, background: 'var(--border-color)' }} role="separator" />

      {/* Font family */}
      <select
        value={font || 'Arial'}
        className="rounded border px-1.5 py-1 text-xs"
        style={{
          background: 'var(--bg-primary)',
          borderColor: 'var(--border-color)',
          color: 'var(--text-primary)',
          maxWidth: 130,
        }}
        data-testid="fmt-font"
        onChange={(e) => onPropertyChange?.('font', e.target.value)}
      >
        {fontFamilies.map(f => (
          <option key={f} value={f}>{f}</option>
        ))}
      </select>

      {/* Font size */}
      <input
        type="number"
        value={fontSize || 24}
        onChange={(e) => {
          const v = Number(e.target.value)
          if (v > 0) onPropertyChange?.('size', String(v))
        }}
        onKeyDown={(e) => {
          if (e.key === 'Enter') e.currentTarget.blur()
        }}
        className="w-12 rounded border px-1.5 py-1 text-center text-xs"
        style={{
          background: 'var(--bg-primary)',
          borderColor: 'var(--border-color)',
          color: 'var(--text-primary)',
        }}
        data-testid="fmt-size"
      />

      {/* Separator */}
      <div style={{ width: 1, height: 20, background: 'var(--border-color)' }} role="separator" />

      {/* Color swatch */}
      <div
        className="rounded border"
        style={{
          width: 24,
          height: 24,
          background: color || '#000000',
          borderColor: 'var(--border-color)',
          cursor: 'pointer',
        }}
        title="Text color"
        data-testid="fmt-color"
        onClick={() => colorRef.current?.click()}
      />
      <input
        ref={colorRef}
        type="color"
        value={color || '#000000'}
        onChange={(e) => onPropertyChange?.('color', e.target.value)}
        className="sr-only"
        data-testid="fmt-color-input"
        tabIndex={-1}
      />
    </div>
  )
}
