import type { ReactNode } from 'react'
import { useNavigate } from 'react-router'
import {
  useStartInstance,
  useStopInstance,
  useRestartInstance,
  isProvisioningInstance,
  type InstanceInfo,
} from '@/api/instances'
import { runtimeDriftOf } from '@/lib/runtime-drift'
import { useInstanceMetrics } from '@/api/metrics'
import { resolveCapabilities } from '@/lib/capabilities'
import { InstanceWorktableCard as InstanceWorktableCardView } from '@jianmanager/ui/components/views/instances/InstanceWorktableCard'

/**
 * 实例工作台卡的应用接线层（ADR-097 b 范式）。
 *
 * 卡本体已迁入组件库并受控；本层承担四件它不该自己做的事：路由跳转（列表页可自定义、
 * 超级工作台用默认深链）、能力画像判定（是否代理）、搭建中判定、运行态漂移判定，
 * 外加实时指标取数与三个动作。保留同名同路径的具名导出与同一套 props，调用点无需改动。
 */
export function InstanceWorktableCard({
  inst,
  nodeName,
  roleBadge,
  menu,
  onOpen,
}: {
  inst: InstanceInfo
  /** 所属节点名（由列表统一解析后传入，避免卡内各自查节点表）。 */
  nodeName: string
  /** 角色徽标元素（统一语义色，由页面渲染）。 */
  roleBadge: ReactNode
  /** 「⋯」次要操作菜单元素（标签/限额/克隆/删除，由页面渲染）。 */
  menu: ReactNode
  /** 打开实例控制台；默认跳转实例深链，列表页可传入自定义跳转。 */
  onOpen?: (id: number) => void
}) {
  const navigate = useNavigate()
  const openConsole = onOpen ?? ((id: number) => navigate(`/instances/${id}`))
  const start = useStartInstance()
  const stop = useStopInstance()
  const restart = useRestartInstance()

  const running = inst.status === 'RUNNING'
  // 搭建中硬性禁启（FR-331）：provision 未终态期间启动按钮直接禁用，不再只靠琥珀文案劝阻。
  const provisioning = isProvisioningInstance(inst)
  // 仅运行态拉实时指标；停机/过渡态不轮询（省请求，避免离线 422）。
  const { data: metrics } = useInstanceMetrics(inst.id, running)
  // 代理图标由画像 `bcTopology` 能力判定（FR-445），取代写死的 `inst.role === 'proxy'`。
  const isProxy = resolveCapabilities(inst).capabilities.includes('bcTopology')

  return (
    <InstanceWorktableCardView
      inst={inst}
      nodeName={nodeName}
      isProxy={isProxy}
      provisioning={provisioning}
      drift={runtimeDriftOf(inst) ?? undefined}
      metrics={metrics}
      // 在途判定按实例 id 比对：别张卡正在提交时，本卡按钮不该被禁用。
      starting={start.isPending && start.variables === inst.id}
      stopping={stop.isPending && stop.variables === inst.id}
      restarting={restart.isPending && restart.variables === inst.id}
      roleBadge={roleBadge}
      menu={menu}
      onOpen={openConsole}
      onStart={() => start.mutate(inst.id)}
      onStop={() => stop.mutate(inst.id)}
      onRestart={() => restart.mutate(inst.id)}
    />
  )
}
