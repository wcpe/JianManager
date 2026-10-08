import { toast } from 'sonner'
import { SessionConfig as SessionConfigView } from '@jianmanager/ui/components/views/bot-load/session/SessionOverviewParts'
import { useSessionEvents } from './SessionEventProvider'

/**
 * 会话配置快照接线层：会话快照来自 `SessionEventProvider`，复制回执桥接为 sonner toast
 * （展示层已归包，包内不依赖 sonner）。
 */
export function SessionConfig() {
  const { run } = useSessionEvents()
  if (!run) return null

  return (
    <SessionConfigView
      run={run}
      onNotify={(level, message) => {
        if (level === 'success') toast.success(message)
        else toast.error(message)
      }}
    />
  )
}
