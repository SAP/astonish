import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ArtifactCard from './ArtifactCard'
import ArtifactMediaPlayer from './ArtifactMediaPlayer'
import EmbeddedFileViewer from './EmbeddedFileViewer'
import FilePanel from './FilePanel'
import type { SessionArtifact } from './chatTypes'

const { teamFetch, saveAs } = vi.hoisted(() => ({ teamFetch: vi.fn(), saveAs: vi.fn() }))
vi.mock('../../api/teamContext', () => ({ teamFetch }))
vi.mock('file-saver', () => ({ saveAs }))

const artifact: SessionArtifact = {
  path: '/tmp/report.md', fileName: 'report.md', fileType: 'Markdown', toolName: 'write_file', isReport: true,
}
const surfaces = ['card', 'embedded', 'panel'] as const
function surface(kind: typeof surfaces[number], sessionId?: string) {
  if (kind === 'card') return <ArtifactCard data={{ type: 'artifact', path: artifact.path, toolName: artifact.toolName }} sessionId={sessionId} />
  if (kind === 'embedded') return <EmbeddedFileViewer artifact={artifact} sessionId={sessionId} />
  return <FilePanel artifacts={[artifact]} initialPath={artifact.path} sessionId={sessionId} onClose={() => {}} />
}
async function download(kind: typeof surfaces[number], pdf = false) {
  if (kind === 'card') fireEvent.click(screen.getByRole('button', { name: 'Download file' }))
  else {
    fireEvent.click(await screen.findByRole('button', { name: 'Download' }))
    fireEvent.click(screen.getByRole('button', { name: pdf ? 'Download as PDF' : 'Download as Markdown' }))
  }
}

beforeEach(() => {
  teamFetch.mockReset()
  saveAs.mockReset()
  teamFetch.mockImplementation(async (url: string) => url.includes('/content?')
    ? { ok: true, text: async () => '# Report content' }
    : { ok: true, blob: async () => new Blob(['download']) })
  vi.spyOn(window, 'open').mockImplementation(() => null)
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('bare fetch must not deliver artifacts')))
})
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })

describe.each(surfaces)('%s artifact downloads', kind => {
  it.each(['live-session', 'restored/session', undefined])('uses scoped blob transport for session %s, including local mode', async sessionId => {
    render(surface(kind, sessionId))
    await download(kind)
    await waitFor(() => expect(saveAs).toHaveBeenCalledWith(expect.any(Blob), 'report.md'))
    const url = '/api/studio/artifacts?path=%2Ftmp%2Freport.md' + (sessionId ? `&session=${encodeURIComponent(sessionId)}` : '')
    expect(teamFetch).toHaveBeenCalledWith(url)
    expect(window.open).not.toHaveBeenCalled()
    expect(globalThis.fetch).not.toHaveBeenCalled()
  })

  it('shows denied download errors without falling back to a direct URL', async () => {
    teamFetch.mockImplementation(async (url: string) => url.includes('/content?')
      ? { ok: true, text: async () => '# Report content' }
      : { ok: false, status: 403 })
    render(surface(kind, 'restored/session'))
    await download(kind)
    expect(await screen.findByRole('alert')).toHaveTextContent('403')
    expect(saveAs).not.toHaveBeenCalled()
    expect(window.open).not.toHaveBeenCalled()
    expect(globalThis.fetch).not.toHaveBeenCalled()
  })
})

describe.each(['embedded', 'panel'] as const)('%s PDF export', kind => {
  it('uses scoped transport and preserves the restored session', async () => {
    render(surface(kind, 'restored/session'))
    await download(kind, true)
    await waitFor(() => expect(saveAs).toHaveBeenCalledWith(expect.any(Blob), 'report.pdf'))
    expect(teamFetch).toHaveBeenCalledWith('/api/studio/artifacts/pdf?path=%2Ftmp%2Freport.md&session=restored%2Fsession')
    expect(globalThis.fetch).not.toHaveBeenCalled()
  })
  it('shows failed PDF export without saving the error response', async () => {
    teamFetch.mockImplementation(async (url: string) => url.includes('/content?')
      ? { ok: true, text: async () => '# Report content' }
      : { ok: false, status: 404 })
    render(surface(kind, 'restored/session'))
    await download(kind, true)
    expect(await screen.findByRole('alert')).toHaveTextContent('404')
    expect(saveAs).not.toHaveBeenCalled()
  })
})

it('refetches media for a changed session and shows denial without a direct-url fallback', async () => {
  vi.stubGlobal('URL', class extends URL {
    static createObjectURL = vi.fn(() => 'blob:authorized')
    static revokeObjectURL = vi.fn()
  })
  const props = { path: '/tmp/video.mp4', fileName: 'video.mp4', kind: 'video' as const }
  const { container, rerender } = render(<ArtifactMediaPlayer {...props} sessionId="live-session" />)
  await waitFor(() => expect(container.querySelector('video')).toHaveAttribute('src', 'blob:authorized'))
  teamFetch.mockResolvedValue({ ok: false, status: 403 })
  rerender(<ArtifactMediaPlayer {...props} sessionId="restored/session" />)
  expect(await screen.findByText(/Failed to load media:.*403/)).toBeInTheDocument()
  expect(teamFetch).toHaveBeenLastCalledWith('/api/studio/artifacts?path=%2Ftmp%2Fvideo.mp4&session=restored%2Fsession')
  expect(container.querySelector('video')).toBeNull()
  expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:authorized')
})

it('Bot adapter uses native download and disables unsupported PDF conversion', async () => {
  const access = { identity: 'bot/run/artifact', freshness: 'sha', preview: true, readPreview: vi.fn().mockResolvedValue({ content: '# Immutable report', truncated: true }), download: vi.fn().mockResolvedValue(undefined) }
  const view = render(<EmbeddedFileViewer artifact={artifact} fileAccess={access} />)
  expect(await screen.findByText(/Preview truncated/)).toBeVisible()
  fireEvent.click(screen.getByRole('button', { name: 'Download' }))
  expect(screen.queryByText('Download as PDF')).toBeNull()
  fireEvent.click(screen.getByText('Download as Markdown'))
  await waitFor(() => expect(access.download).toHaveBeenCalledTimes(1))
  expect(saveAs).not.toHaveBeenCalled(); expect(teamFetch).not.toHaveBeenCalled()
  const signal = access.readPreview.mock.calls[0][0]
  view.unmount(); expect(signal.aborted).toBe(true)
})
