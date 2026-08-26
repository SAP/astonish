import React from 'react';

interface CompactionNoticeProps {
  beforeTokens: number;
  afterTokens: number;
  strategy?: string;
  messageCount?: number;
}

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${Math.round(n / 1_000)}k`;
  return String(n);
}

/**
 * CompactionNotice renders a compact visual indicator when context compaction
 * occurs during a chat session. It shows before→after token counts and the
 * strategy used (code vs platform).
 */
export default function CompactionNotice({
  beforeTokens,
  afterTokens,
  strategy,
  messageCount,
}: CompactionNoticeProps) {
  return (
    <div className="flex items-center gap-2 px-3 py-1.5 my-1 text-xs text-muted-foreground bg-muted/50 rounded-md border border-border/50">
      <span className="text-sm">📦</span>
      <span>
        Context compacted: {formatTokens(beforeTokens)} → {formatTokens(afterTokens)} tokens
        {strategy && <span className="text-muted-foreground/70"> ({strategy})</span>}
        {messageCount ? <span className="text-muted-foreground/70"> · {messageCount} msgs</span> : null}
      </span>
    </div>
  );
}
