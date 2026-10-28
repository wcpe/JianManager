import type { ReactNode } from 'react'
import { useNavigate } from 'react-router'
import { isProvisioningInstance, type InstanceInfo } from '@/api/instances'
import { runtimeDriftOf } from '@/lib/runtime-assets/runtime-drift'
import { envOf, freeTagsOf, regionOf, zoneOf } from '@/lib/instances/instance-grouping'
import { useInstanceMetrics } from '@/api/metrics'
import { resolveCapabilities } from '@/lib/instances/capabilities'
import { InstanceWorktableCard as InstanceWorktableCardView } from '@/components/views/instances/InstanceWorktableCard'

/**
 * 实例工作台卡的应用接线层（ADR-103 b 范式）。
 *
 * 卡本体是受控视图（见 components/views）；本层承担四件它不该自己做的事：路由跳转（列表页可自定义、
 * 超级工作台用默认深链）、能力画像判定（是否代理）、搭建中判定、运行态漂移判定，
 * 外加实时指标取数与三个动作。保留同名同路径的具名导出与同一套 props，调用点无需改动。
 */
export function InstanceWorktableCard({
  inst,
  nodeName,
  roleBadge,
  menu,
  onOpen,
  onStart,
  onStop,
  onRestart,
  onContextMenu,
  showType,
  showRole,
  showTags,
  selected,
  onToggleSelect,
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
  /**
   * 启停/重启动作（由页面注入）。
   *
   * 【为什么收归页面】此前本层自己 `useStartInstance().mutate()`，与列表视图各写一条路径：
   * 列表加了二次确认，卡片仍是「一点就执行」——同一台实例在两种视图下的安全语义不一致。
   * 动作的确认流程属于页面级决策，接线层只做渲染，不该自己发写请求。
   */
  onStart?: () => void
  onStop?: () => void
  onRestart?: () => void
  /** 右键菜单入口（与列表视图共用页面级单例菜单）。 */
  onContextMenu?: (inst: InstanceInfo, x: number, y: number) => void
  /** 身份徽章开关：与表格列设置对齐。 */
  showType?: boolean
  showRole?: boolean
  showTags?: boolean
  /** 是否被批量选中（卡片视图补齐多选）。 */
  selected?: boolean
  /** 勾选/点选（修饰键语义由页面统一实现）。 */
  onToggleSelect?: (modifiers: { shiftKey: boolean; ctrlKey: boolean; metaKey: boolean }) => void
}) {
  const navigate = useNavigate()
  const openConsole = onOpen ?? ((id: number) => navigate(`/instances/${id}`))

  const running = inst.status === 'RUNNING'
  // 搭建中硬性禁启（FR-331）：provision 未终态期间启动按钮直接禁用，不再只靠琥珀文案劝阻。
  const provisioning = isProvisioningInstance(inst)
  // 仅运行态拉实时指标；停机/过渡态不轮询（省请求，避免离线 422）。
  const { data: metrics } = useInstanceMetrics(inst.id, running)
  // 代理图标由画像 `bcTopology` 能力判定（FR-445），取代写死的 `inst.role === 'proxy'`。
  const isProxy = resolveCapabilities(inst).capabilities.includes('bcTopology')

  return (
    <InstanceWorktableCardView
      inst={{
        ...inst,
        // 大区/小区由标签推导后注入视图：视图不解析标签格式（那是领域知识），
        // 只负责把给定文字画到左上角。
        region: regionOf(inst),
        zone: zoneOf(inst),
      }}
      nodeName={nodeName}
      isProxy={isProxy}
      provisioning={provisioning}
      drift={runtimeDriftOf(inst) ?? undefined}
      metrics={metrics}
      // 在途状态由视图内部按「已点击」自管：动作已上移到页面，接线层拿不到 mutation 状态。
      starting={false}
      stopping={false}
      restarting={false}
      roleBadge={roleBadge}
      menu={menu}
      onOpen={openConsole}
      onStart={onStart ?? (() => {})}
      onStop={onStop ?? (() => {})}
      onRestart={onRestart ?? (() => {})}
      onContextMenu={onContextMenu ? (x, y) => onContextMenu(inst, x, y) : undefined}
      selected={selected}
      onToggleSelect={onToggleSelect}
      /*
        身份徽章的数据与开关：与表格身份列**完全同源**——
        env/freeTags 都由同一组取数函数推导，开关也跟随同一份 visibleColumns。
        这样两个视图对同一台实例的「类型/角色/环境/标签」呈现不会再分叉。
      */
      env={envOf(inst) || undefined}
      freeTags={freeTagsOf(inst)}
      showType={showType}
      showRole={showRole}
      showTags={showTags}
    />
  )
}
