import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import EmbeddedFileViewer from './EmbeddedFileViewer'
import type { SessionArtifact } from './chatTypes'

// Mock the studio chat API so no real network request is made.
const fetchArtifactContent = vi.fn()
vi.mock('../../api/studioChat', () => ({
  fetchArtifactContent: (...args: unknown[]) => fetchArtifactContent(...args),
  getArtifactDownloadUrl: () => 'blob:download',
  getArtifactPDFUrl: () => 'blob:pdf',
}))

// Stub the media player — it makes its own blob fetch we don't want in this test.
vi.mock('./ArtifactMediaPlayer', () => ({
  default: () => <div data-testid="media-player" />,
}))

const MARKDOWN = '# Report\n\nSome **markdown** content.'

const markdownArtifact: SessionArtifact = {
  path: '/tmp/report.md',
  fileName: 'report.md',
  fileType: 'Markdown',
  toolName: 'write_file',
  isReport: true,
}

const videoArtifact: SessionArtifact = {
  path: '/tmp/clip.mp4',
  fileName: 'clip.mp4',
  fileType: 'Video',
  toolName: 'write_file',
}

describe('EmbeddedFileViewer copy button', () => {
  beforeEach(() => {
    fetchArtifactContent.mockReset()
    Object.assign(navigator, {
      clipboard: { writeText: vi.fn().mockResolvedValue(undefined) },
    })
  })

  it('copies the loaded markdown content to the clipboard', async () => {
    fetchArtifactContent.mockResolvedValue(MARKDOWN)
    render(<EmbeddedFileViewer artifact={markdownArtifact} sessionId="s1" />)

    // Wait for content to load, then open the Download dropdown.
    const downloadBtn = await screen.findByText('Download')
    fireEvent.click(downloadBtn.closest('button')!)

    // The Copy option lives inside the dropdown.
    const copyBtn = await screen.findByText('Copy content')
    fireEvent.click(copyBtn.closest('button')!)

    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(MARKDOWN)
    await waitFor(() => expect(screen.getByText('Copied')).toBeInTheDocument())
  })

  it('does not render the copy option for media artifacts (no text content)', async () => {
    render(<EmbeddedFileViewer artifact={videoArtifact} sessionId="s1" />)

    // Media artifacts never fetch text content.
    expect(fetchArtifactContent).not.toHaveBeenCalled()

    // Open the dropdown — no Copy option should be present for media.
    const downloadBtn = await screen.findByText('Download')
    fireEvent.click(downloadBtn.closest('button')!)
    await waitFor(() => {
      expect(screen.queryByText('Copy content')).not.toBeInTheDocument()
    })
  })
})
