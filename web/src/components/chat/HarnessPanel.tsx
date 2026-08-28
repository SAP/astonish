import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { X, GripVertical } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import AppPreviewCard from './AppPreviewCard'
import DistillPreviewCard from './DistillPreviewCard'
import TutorialBlueprintCard from './TutorialBlueprintCard'
import TutorialSceneSlideshowCard from './TutorialSceneSlideshowCard'
import EmbeddedFileViewer from './EmbeddedFileViewer'
import BrowserView from './BrowserView'
import SlidesDeckView from './SlidesDeckView'
import {
  appVersionsForFocus,
  clampHarnessWidth,
  harnessKindLabel,
  messageAt,
  readStoredHarnessWidth,
  slidesHarnessLabel,
  writeStoredHarnessWidth,
  HARNESS_DEFAULT_WIDTH,
  type HarnessFocus,
} from './chatHarness'
import type {
  AppPreviewMessage,
  BrowserHandoffMessage,
  ChatMsg,
  DistillPreviewMessage,
  DocsUpdateMessage,
  SessionArtifact,
  TutorialBlueprintPreviewMessage,
  TutorialSceneSlideshowMessage,
} from './chatTypes'

interface HarnessPanelProps {
  focus: HarnessFocus
  messages: ChatMsg[]
  sessionArtifacts: SessionArtifact[]
  activeAppId: string | null
  sessionId?: string | null
  theme?: string
  /** Width of the StudioChat flex row (sidebar + chat + panels). */
  containerWidth: number
  /** Current session sidebar width in px. */
  sidebarWidth: number
  onClose: () => void
  onAppSave?: (name: string) => void
  onDistillSave?: () => void
  onDistillRequestChanges?: () => void
  onDistillCancel?: () => void
  onTutorialApprove?: () => void
  onTutorialRequestChanges?: () => void
  onTutorialCancel?: () => void
  onOpenReportInFiles?: (path: string) => void
  onBrowserDone?: (messageIndex: number) => void
}

export default function HarnessPanel({
  focus,
  messages,
  sessionArtifacts,
  activeAppId,
  sessionId,
  theme = 'dark',
  containerWidth,
  sidebarWidth,
  onClose,
  onAppSave,
  onDistillSave,
  onDistillRequestChanges,
  onDistillCancel,
  onTutorialApprove,
  onTutorialRequestChanges,
  onTutorialCancel,
  onOpenReportInFiles,
  onBrowserDone,
}: HarnessPanelProps) {
  const [appVersionIndex, setAppVersionIndex] = useState(0)
  const [preferredWidth, setPreferredWidth] = useState(readStoredHarnessWidth)
  const [dragging, setDragging] = useState(false)
  const [handleHovered, setHandleHovered] = useState(false)
  const panelRef = useRef<HTMLDivElement>(null)
  const preferredWidthRef = useRef(preferredWidth)

  const appId = focus.kind === 'app' ? focus.appId : ''
  const appVersions =
    focus.kind === 'app' ? appVersionsForFocus(messages, focus.appId) : []

  const width = containerWidth > 0
    ? clampHarnessWidth(preferredWidth, containerWidth, sidebarWidth)
    : preferredWidth

  useEffect(() => {
    preferredWidthRef.current = preferredWidth
  }, [preferredWidth])

  // Always land on the latest version when the focused app gains a new revision.
  useEffect(() => {
    if (focus.kind !== 'app') return
    setAppVersionIndex(Math.max(0, appVersions.length - 1))
  }, [focus.kind, appId, appVersions.length])

  const persistWidth = useCallback((next: number) => {
    writeStoredHarnessWidth(next)
  }, [])

  const handleResizePointerDown = useCallback((e: React.PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return
    e.preventDefault()
    const handleEl = e.currentTarget
    handleEl.setPointerCapture(e.pointerId)
    setDragging(true)
    let latest = preferredWidthRef.current

    const onMove = (ev: PointerEvent) => {
      const panel = panelRef.current
      const parent = panel?.parentElement
      if (!parent) return
      const parentRight = parent.getBoundingClientRect().right
      const next = parentRight - ev.clientX
      const clamped = clampHarnessWidth(next, containerWidth, sidebarWidth)
      latest = clamped
      preferredWidthRef.current = clamped
      setPreferredWidth(clamped)
    }

    const onUp = (ev: PointerEvent) => {
      setDragging(false)
      setHandleHovered(false)
      persistWidth(latest)
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      window.removeEventListener('pointercancel', onUp)
      try {
        handleEl.releasePointerCapture(ev.pointerId)
      } catch {
        // already released
      }
    }

    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    window.addEventListener('pointercancel', onUp)
  }, [containerWidth, sidebarWidth, persistWidth])

  const handleResizeDoubleClick = useCallback(() => {
    setPreferredWidth(HARNESS_DEFAULT_WIDTH)
    persistWidth(HARNESS_DEFAULT_WIDTH)
  }, [persistWidth])

  const title = (() => {
    switch (focus.kind) {
      case 'app': {
        const latest = appVersions[appVersions.length - 1]
        return latest?.title || harnessKindLabel(focus.kind)
      }
      case 'report':
      case 'video': {
        const art = sessionArtifacts.find(a => a.path === focus.path)
        return art?.reportTitle || art?.fileName || focus.path
      }
      case 'distill': {
        const msg = messageAt<DistillPreviewMessage>(messages, focus.messageIndex)
        return msg?.flowName || harnessKindLabel(focus.kind)
      }
      case 'tutorial_blueprint': {
        const msg = messageAt<TutorialBlueprintPreviewMessage>(messages, focus.messageIndex)
        return msg?.title || harnessKindLabel(focus.kind)
      }
      case 'tutorial_slideshow': {
        const msg = messageAt<TutorialSceneSlideshowMessage>(messages, focus.messageIndex)
        return msg?.title || msg?.drill || harnessKindLabel(focus.kind)
      }
      case 'browser_handoff': {
        const msg = messageAt<BrowserHandoffMessage>(messages, focus.messageIndex)
        return msg?.reason || msg?.pageTitle || harnessKindLabel(focus.kind)
      }
      case 'slides': {
        const msg = messageAt<DocsUpdateMessage>(messages, focus.messageIndex)
        return msg ? slidesHarnessLabel(msg) : harnessKindLabel(focus.kind)
      }
    }
  })()

  const fillBody =
    focus.kind === 'report' ||
    focus.kind === 'video' ||
    focus.kind === 'browser_handoff' ||
    focus.kind === 'slides' ||
    focus.kind === 'app'

  // Per-deck refresh signal for the Slides panel: as the agent writes slides,
  // each write_slide emits a `docs_update` message. Counting those (plus the
  // latest reported total) yields a monotonically-increasing number that drives
  // SlidesDeckView to re-fetch + re-mount its iframe, so the panel updates live
  // instead of showing the empty "render slides" placeholder until a reload.
  const slidesRefreshSignal = useMemo(() => {
    if (focus.kind !== 'slides') return 0
    let count = 0
    let maxTotal = 0
    for (const m of messages) {
      if (m.type !== 'docs_update') continue
      const d = m as DocsUpdateMessage
      if (d.docType === 'slides' && d.deckSlug === focus.deckSlug) {
        count++
        if (typeof d.totalSlides === 'number' && d.totalSlides > maxTotal) maxTotal = d.totalSlides
      }
    }
    return count * 1000 + maxTotal
  }, [messages, focus])

  return (
    <div
      ref={panelRef}
      data-testid="harness-panel"
      data-harness-kind={focus.kind}
      className={cn(
        'relative flex flex-shrink-0 flex-col overflow-hidden border-l border-border bg-background',
        dragging && 'select-none'
      )}
      style={{ width: `${width}px` }}
    >
      {/* Left-edge resize handle — full-height hit target + mid-edge visual grip */}
      <div
        data-testid="harness-resize-handle"
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize harness panel"
        title="Drag to resize · double-click to reset"
        onPointerDown={handleResizePointerDown}
        onDoubleClick={handleResizeDoubleClick}
        onPointerEnter={() => setHandleHovered(true)}
        onPointerLeave={() => { if (!dragging) setHandleHovered(false) }}
        className="absolute top-0 bottom-0 left-0 z-10 ml-[-6px] flex w-3 cursor-col-resize items-center justify-center touch-none"
      >
        <div
          data-testid="harness-resize-grip"
          className={cn(
            'pointer-events-none flex h-10 w-[18px] items-center justify-center rounded-full border bg-card shadow-[var(--shadow-soft)] transition-colors',
            dragging || handleHovered
              ? 'border-primary text-primary'
              : 'border-border text-muted-foreground'
          )}
        >
          <GripVertical size={14} />
        </div>
      </div>

      {/* Elevated chrome bar (card, not near-black panel-background) so it reads on bg-background */}
      <div className="flex shrink-0 items-center justify-between border-b border-border bg-card px-4 py-3">
        <div className="min-w-0">
          <div className="text-[10px] font-medium tracking-wide text-muted-foreground uppercase">
            {harnessKindLabel(focus.kind)}
          </div>
          <div className="truncate text-sm font-semibold text-foreground">
            {title}
          </div>
        </div>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          onClick={onClose}
          className="size-8"
          title="Close panel"
          data-testid="harness-panel-close"
          aria-label="Close panel"
        >
          <X />
        </Button>
      </div>

      <div
        className={
          fillBody
            ? 'flex flex-col flex-1 min-h-0 overflow-hidden p-3'
            : 'flex-1 overflow-auto p-3 min-h-0'
        }
      >
        {focus.kind === 'app' && appVersions.length > 0 && (() => {
          const data = appVersions[Math.min(appVersionIndex, appVersions.length - 1)] as AppPreviewMessage
          const isActive = activeAppId != null && (data.appId === activeAppId || (!data.appId && data.title === activeAppId))
          return (
            <div className="flex-1 min-h-0 h-full">
              <AppPreviewCard
                data={data}
                versions={appVersions.length > 1 ? appVersions : undefined}
                versionIndex={appVersionIndex}
                onNavigateVersion={setAppVersionIndex}
                isActive={isActive}
                onSave={isActive && onAppSave ? onAppSave : undefined}
                sessionId={sessionId}
                fillHeight
              />
            </div>
          )
        })()}

        {(focus.kind === 'report' || focus.kind === 'video') && (() => {
          const artifact = sessionArtifacts.find(a => a.path === focus.path)
          if (!artifact) {
            return (
              <p className="text-sm text-muted-foreground">
                {focus.kind === 'video' ? 'Video' : 'Report'} not available.
              </p>
            )
          }
          return (
            <div className="flex-1 min-h-0 h-full">
              <EmbeddedFileViewer
                artifact={artifact}
                sessionId={sessionId}
                fillHeight
                onOpenInPanel={onOpenReportInFiles}
              />
            </div>
          )
        })()}

        {focus.kind === 'distill' && (() => {
          const previewMsg = messageAt<DistillPreviewMessage>(messages, focus.messageIndex)
          if (!previewMsg || previewMsg.type !== 'distill_preview') {
            return (
              <p className="text-sm" style={{ color: 'var(--text-muted)' }}>
                Flow draft not available.
              </p>
            )
          }
          const isLastPreview = (() => {
            for (let j = focus.messageIndex + 1; j < messages.length; j++) {
              const m = messages[j]
              if (m.type === 'distill_preview' || m.type === 'distill_saved') return false
            }
            return true
          })()
          return (
            <DistillPreviewCard
              data={previewMsg}
              isActive={isLastPreview}
              fillWidth
              onSave={() => onDistillSave?.()}
              onRequestChanges={() => onDistillRequestChanges?.()}
              onCancel={() => onDistillCancel?.()}
            />
          )
        })()}

        {focus.kind === 'tutorial_blueprint' && (() => {
          const previewMsg = messageAt<TutorialBlueprintPreviewMessage>(messages, focus.messageIndex)
          if (!previewMsg || previewMsg.type !== 'tutorial_blueprint_preview') {
            return (
              <p className="text-sm" style={{ color: 'var(--text-muted)' }}>
                Tutorial blueprint not available.
              </p>
            )
          }
          const isLastPreview = (() => {
            for (let j = focus.messageIndex + 1; j < messages.length; j++) {
              const m = messages[j]
              if (m.type === 'tutorial_blueprint_preview' || m.type === 'tutorial_blueprint_approved') return false
            }
            return true
          })()
          return (
            <TutorialBlueprintCard
              data={previewMsg}
              isActive={isLastPreview}
              onApprove={() => onTutorialApprove?.()}
              onRequestChanges={() => onTutorialRequestChanges?.()}
              onCancel={() => onTutorialCancel?.()}
            />
          )
        })()}

        {focus.kind === 'tutorial_slideshow' && (() => {
          const slideshowMsg = messageAt<TutorialSceneSlideshowMessage>(messages, focus.messageIndex)
          if (!slideshowMsg || slideshowMsg.type !== 'tutorial_scene_slideshow') {
            return (
              <p className="text-sm" style={{ color: 'var(--text-muted)' }}>
                Tutorial scenes not available.
              </p>
            )
          }
          return (
            <TutorialSceneSlideshowCard
              data={slideshowMsg}
              sessionId={sessionId}
              fillWidth
            />
          )
        })()}

        {focus.kind === 'browser_handoff' && (() => {
          const handoff = messageAt<BrowserHandoffMessage>(messages, focus.messageIndex)
          if (!handoff || handoff.type !== 'browser_handoff') {
            return (
              <p className="text-sm" style={{ color: 'var(--text-muted)' }}>
                Browser session not available.
              </p>
            )
          }
          return (
            <div className="flex-1 min-h-0 h-full">
              <BrowserView
                data={handoff}
                theme={theme}
                fillHeight
                onDone={() => onBrowserDone?.(focus.messageIndex)}
              />
            </div>
          )
        })()}

        {focus.kind === 'slides' && (
          <div className="flex-1 min-h-0 h-full">
            <SlidesDeckView deckSlug={focus.deckSlug} scope="personal" fillHeight refreshSignal={slidesRefreshSignal} />
          </div>
        )}
      </div>
    </div>
  )
}
