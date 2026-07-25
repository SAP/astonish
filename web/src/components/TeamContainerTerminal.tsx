import React, { useEffect, useRef, useCallback } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'
import { getTeamTerminalWsUrl } from '../api/sandbox'

interface TeamContainerTerminalProps {
  teamSlug: string
  theme: 'dark' | 'light'
  /** Called when the WebSocket connection is closed (shell exited). */
  onDisconnect?: () => void
}

/**
 * TeamContainerTerminal renders an xterm.js terminal connected via WebSocket
 * to the team's sandbox template container.
 *
 * Protocol:
 * - Binary frames: stdin/stdout (raw PTY data)
 * - Text frames from client: JSON control messages (resize)
 */
export default function TeamContainerTerminal({ teamSlug, theme, onDisconnect }: TeamContainerTerminalProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const terminalRef = useRef<Terminal | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const fitAddonRef = useRef<FitAddon | null>(null)
  // Store onDisconnect in a ref so the connect callback doesn't depend on it.
  // This prevents re-renders (from parent state changes) from tearing down
  // and recreating the entire terminal + WebSocket connection.
  const onDisconnectRef = useRef(onDisconnect)
  useEffect(() => { onDisconnectRef.current = onDisconnect }, [onDisconnect])
  // Guard: when we intentionally close the WS during cleanup, don't fire onDisconnect
  const cleaningUpRef = useRef(false)

  const connect = useCallback(() => {
    if (!containerRef.current) return
    cleaningUpRef.current = false

    // Create terminal instance
    const terminal = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: 'JetBrains Mono, Menlo, Monaco, Consolas, monospace',
      theme: theme === 'dark' ? {
        background: '#1a1a2e',
        foreground: '#e4e4e7',
        cursor: 'var(--brand)',
        selectionBackground: 'color-mix(in oklab, var(--brand) 30%, transparent)',
      } : {
        background: '#ffffff',
        foreground: '#1f2937',
        cursor: 'var(--brand-strong)',
        selectionBackground: 'color-mix(in oklab, var(--brand-strong) 20%, transparent)',
      },
      allowProposedApi: true,
    })

    const fitAddon = new FitAddon()
    const webLinksAddon = new WebLinksAddon()
    terminal.loadAddon(fitAddon)
    terminal.loadAddon(webLinksAddon)

    terminal.open(containerRef.current)
    fitAddon.fit()

    terminalRef.current = terminal
    fitAddonRef.current = fitAddon

    // Connect WebSocket
    const wsUrl = getTeamTerminalWsUrl(teamSlug)
    const ws = new WebSocket(wsUrl)
    ws.binaryType = 'arraybuffer'
    wsRef.current = ws

    ws.onopen = () => {
      terminal.writeln('\x1b[90m--- Connected to team container ---\x1b[0m')
      // Send initial size
      const dims = fitAddon.proposeDimensions()
      if (dims) {
        ws.send(JSON.stringify({ type: 'resize', cols: dims.cols, rows: dims.rows }))
      }
    }

    ws.onmessage = (event) => {
      if (event.data instanceof ArrayBuffer) {
        terminal.write(new Uint8Array(event.data))
      } else if (typeof event.data === 'string') {
        // Server shouldn't send text frames, but handle gracefully
        terminal.write(event.data)
      }
    }

    ws.onclose = () => {
      terminal.writeln('\r\n\x1b[90m--- Disconnected ---\x1b[0m')
      // Only fire callback for unexpected disconnects (not cleanup)
      if (!cleaningUpRef.current) {
        onDisconnectRef.current?.()
      }
    }

    ws.onerror = () => {
      terminal.writeln('\r\n\x1b[31m--- Connection error ---\x1b[0m')
    }

    // Terminal input → WebSocket (binary)
    terminal.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        const encoder = new TextEncoder()
        ws.send(encoder.encode(data))
      }
    })

    // Handle resize
    terminal.onResize(({ cols, rows }) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'resize', cols, rows }))
      }
    })
  }, [teamSlug, theme])

  // Fit terminal on container resize
  useEffect(() => {
    if (!containerRef.current) return

    const observer = new ResizeObserver(() => {
      if (fitAddonRef.current) {
        try {
          fitAddonRef.current.fit()
        } catch { /* container might be hidden */ }
      }
    })

    observer.observe(containerRef.current)
    return () => observer.disconnect()
  }, [])

  // Connect on mount, cleanup on unmount
  useEffect(() => {
    connect()

    return () => {
      cleaningUpRef.current = true
      if (wsRef.current) {
        wsRef.current.close()
        wsRef.current = null
      }
      if (terminalRef.current) {
        terminalRef.current.dispose()
        terminalRef.current = null
      }
    }
  }, [connect])

  return (
    <div
      ref={containerRef}
      className="w-full h-full min-h-[300px]"
      style={{
        background: theme === 'dark' ? '#1a1a2e' : '#ffffff',
        borderRadius: '8px',
        padding: '4px',
      }}
    />
  )
}
