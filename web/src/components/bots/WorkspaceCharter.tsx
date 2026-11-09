import { useState } from 'react'
import { updateBotCharter, type Bot, type BotVersion } from '@/api/bots'
import ReviewedText from './BotControls'

/** Keep a local review draft mounted even when a new version is being fetched.
 * New server revisions never silently replace text the user is reviewing. */
export default function WorkspaceCharter({ bot, version, disabled, refresh }: { bot: Bot; version?: BotVersion; disabled: boolean; refresh: () => void }) {
  const [initial, setInitial] = useState<string | undefined>(version?.charter)
  if (initial === undefined && version) setInitial(version.charter)
  if (initial === undefined) return null
  return <ReviewedText label="Charter" initial={initial} disabled={disabled || !version} onSave={async (text, reason) => {
    await updateBotCharter(bot, text, reason)
    refresh()
  }} />
}
