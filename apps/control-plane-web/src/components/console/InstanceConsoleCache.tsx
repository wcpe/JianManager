import { Activity, useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import type { InstanceInfo } from '@/api/instances'
import { clearInstanceDrafts, hasInstanceDraft } from '@/lib/console-draft-registry'
import { pickEvictionTarget, promoteHotSet } from '@/lib/console-hot-cache'
import { HOT_SET_SIZE, terminalSessionManager } from '@/lib/terminal-session-manager'
import InstanceConsolePage from './InstanceConsolePage'

interface InstanceConsoleCacheProps {
  /** 当前路由指向的实例 id。 */
  instanceId: number
}

/**
 * 跨服务器控制台热缓存宿主（FR-296，ADR-067）：维护最近打开的 ≤{@link HOT_SET_SIZE} 个
 * 服控制台（LRU），每个成员一份 `<Activity>` 包裹的 {@link InstanceConsolePage}——
 * 命中热集切 visible 瞬时呈现，未命中入列、超容按淘汰偏好（先无草稿者）整体卸载释放。
 * 成员可见性联动终端连接管理器：hidden 起 10 分钟闲置断连计时、visible 取消并按需重连。
 */
export default function InstanceConsoleCache({ instanceId }: InstanceConsoleCacheProps) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [hotSet, setHotSet] = useState<number[]>(() => [instanceId])
  // 路由切换即置顶/入列并即时裁容（React 官方「渲染期间调整状态」模式，避免一帧旧内容闪现）。
  // 淘汰目标在纯更新器内选定，hotSet 恒 ≤ 容量；被淘汰实例的副作用（断连/清签/toast）
  // 交由下方「外部系统同步」effect 按成员差集统一处理，不在渲染期/更新器里做副作用。
  if (hotSet[0] !== instanceId) {
    setHotSet((prev) => {
      const promoted = promoteHotSet(prev, instanceId)
      if (promoted.length <= HOT_SET_SIZE) return promoted
      const victim = pickEvictionTarget(promoted, hasInstanceDraft)
      return victim == null ? promoted : promoted.filter((id) => id !== victim)
    })
  }

  // 成员差集 → 外部系统同步：离开热集的实例断连管理器会话 + 清草稿登记 + 带草稿者 toast 警示。
  // 只同步外部系统、不 setState，契合 effect 正当用途（避免渲染期做副作用 / effect 里 setState）。
  const committedHotSetRef = useRef(hotSet)
  useEffect(() => {
    const removed = committedHotSetRef.current.filter((id) => !hotSet.includes(id))
    for (const id of removed) {
      if (hasInstanceDraft(id)) {
        // 被迫淘汰带草稿实例：显式警示防静默丢稿（实例名取查询缓存，冷缓存退化为 #id）。
        const name = qc.getQueryData<InstanceInfo>(['instances', id])?.name ?? `#${id}`
        toast.warning(t('serverConsole.evictedWithDraft', { name }))
      }
      terminalSessionManager.dispose(id)
      clearInstanceDrafts(id)
    }
    committedHotSetRef.current = hotSet
  }, [hotSet, qc, t])

  // 成员可见性 → 连接管理器：pin 全部成员（护住独立表面 release），active 标 visible、
  // 其余标 hidden（起闲置断连计时；已闲置断连者回切时自动重连并现取新 token）。
  useEffect(() => {
    for (const id of hotSet) {
      terminalSessionManager.pin(id)
      if (id === instanceId) terminalSessionManager.markVisible(id)
      else terminalSessionManager.markHidden(id)
    }
  }, [hotSet, instanceId])

  // 离开 /instances/*（宿主真卸载）：成员全部转入后台闲置计时（10 分钟内返回秒续、
  // 超时自动断连降级），草稿登记随 DOM 销毁一并清理；登出的整体释放走 disposeAll。
  useEffect(
    () => () => {
      for (const id of committedHotSetRef.current) {
        terminalSessionManager.unpin(id)
        terminalSessionManager.markHidden(id)
        clearInstanceDrafts(id)
      }
    },
    [],
  )

  return (
    <>
      {hotSet.map((id) => (
        <Activity key={id} mode={id === instanceId ? 'visible' : 'hidden'}>
          <InstanceConsolePage instanceId={id} />
        </Activity>
      ))}
    </>
  )
}
