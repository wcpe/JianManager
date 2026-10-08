import { SessionOverview as SessionOverviewView } from '@jianmanager/ui/components/views/bot-load/session/SessionOverviewParts'
import type { SessionTab } from '@/lib/bot-load/types'
import { useSessionEvents } from './SessionEventProvider'

/**
 * 会话概览接线层：会话快照与实时流来自 `SessionEventProvider`，展示层已归包。
 */
export function SessionOverview({
  onNavigate,
}: {
  onNavigate?: (tab: SessionTab, params?: Record<string, string>) => void
}) {
  const { run, live } = useSessionEvents()
  if (!run) return null

  return <SessionOverviewView run={run} live={live} onNavigate={onNavigate} />
}
