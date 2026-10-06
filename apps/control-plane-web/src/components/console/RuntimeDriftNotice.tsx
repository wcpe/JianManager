import { useAdoptInstanceRuntime } from '@/api/instances'
import type { RuntimeDriftInfo } from '@/lib/runtime-drift'
import {
  RuntimeDriftAdoptButton as RuntimeDriftAdoptButtonView,
  RuntimeDriftBanner as RuntimeDriftBannerView,
} from '@jianmanager/ui/components/views/instances/RuntimeDriftNotice'

/**
 * 运行态漂移提示的应用接线层（ADR-097）。
 *
 * 三个导出里 `RuntimeDriftBadge` 本就是纯展示，直接从组件库再导出；
 * 另两个含「接管」写操作，由本层接上 mutation。保留同名同路径的导出，调用点无需改动。
 *
 * 接管不弹 toast——与迁移前一致：结果由后端状态与后续刷新体现，确认框本身已交代副作用。
 */
export { RuntimeDriftBadge } from '@jianmanager/ui/components/views/instances/RuntimeDriftNotice'

export function RuntimeDriftAdoptButton({
  instanceId,
  instanceName,
  pid,
  canOperate,
  size = 'sm',
  className,
}: {
  instanceId: number
  instanceName: string
  pid: number
  /** 实例写权限（FR-432）：无权限时按钮禁用并给 tooltip，最终仍由后端 RBAC 兜底。 */
  canOperate: boolean
  size?: 'sm' | 'xs'
  className?: string
}) {
  const adopt = useAdoptInstanceRuntime()

  return (
    <RuntimeDriftAdoptButtonView
      instanceName={instanceName}
      pid={pid}
      canOperate={canOperate}
      size={size}
      className={className}
      adopting={adopt.isPending}
      onAdopt={async () => {
        try {
          await adopt.mutateAsync(instanceId)
          return true
        } catch {
          return false
        }
      }}
    />
  )
}

export function RuntimeDriftBanner({
  instanceId,
  instanceName,
  pid,
  cmdline,
  canOperate,
}: RuntimeDriftInfo & {
  instanceId: number
  instanceName: string
  canOperate: boolean
}) {
  const adopt = useAdoptInstanceRuntime()

  return (
    <RuntimeDriftBannerView
      instanceName={instanceName}
      pid={pid}
      cmdline={cmdline}
      canOperate={canOperate}
      adopting={adopt.isPending}
      onAdopt={async () => {
        try {
          await adopt.mutateAsync(instanceId)
          return true
        } catch {
          return false
        }
      }}
    />
  )
}
