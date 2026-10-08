/**
 * @file InstanceRowView：实例列表的行渲染件（平铺表与分组树表复用）——勾选 + 名称（proxy 内联展开 /
 *       就地导入徽章 / 运行态漂移标记）+ 类型 + 节点:端口 + 角色 + 环境与标签 + 状态 + 行内主操作与「⋯」菜单；
 *       proxy 展开时在行下追加后端摘要行。虚拟窗口逐行调用，故行内不持有任何取数或跨行共享状态。
 * @input lib/instance-types（InstanceInfo）、lib/instance-grouping（envOf/freeTagsOf、定义于包内）、
 *        lib/threshold（instanceStatusLevel）、views/instances/InstanceTableParts（RoleBadge）、
 *        views/instances/RuntimeDriftNotice（RuntimeDriftBadge 与 RuntimeDriftInfoView）、
 *        Badge/StatusBadge/Button/Checkbox/Table 原语、翻译上下文
 * @output InstanceRowView、InstanceRowViewProps
 * @sync apps/control-plane-web/src/pages/InstancesPage.tsx（容器保留取数与装配，经 renderRow 逐行调用）、
 *        apps/control-plane-web/src/pages/InstancesPage.dom.test.tsx（与 grouping / runtimeDrift / rowMenu 三个针对性测试）
 * @since FR-502（组件受控化迁包；原页 FR-047 多维筛选、FR-058 批量选择、FR-136 分组与节点:端口、
 *        FR-138 行内主操作与「⋯」次要菜单、FR-302 就地导入徽章、FR-310 运行中删除、FR-331 搭建中禁启、
 *        FR-445 代理能力画像、FR-452 分组树表、FR-471 运行态漂移）
 */
import { Fragment, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, ChevronRight } from 'lucide-react'

import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { TableCell, TableRow } from '@jianmanager/ui/components/table'
import { instanceStatusLevel } from '@jianmanager/ui'
import { envOf, freeTagsOf } from '../../../lib/instance-grouping'
import type { InstanceInfo } from '../../../lib/instance-types'
import { RoleBadge } from './InstanceTableParts'
import { RuntimeDriftBadge, type RuntimeDriftInfoView } from './RuntimeDriftNotice'

/**
 * 实例状态 → i18n 文案键（与实例页状态筛选项同源）。
 * 未知状态回退「已停止」文案，与迁移前 `statusConfig[status] || statusConfig.STOPPED` 等价。
 */
const STATUS_LABEL_KEY: Record<string, string> = {
  STOPPED: 'instances.stopped',
  STARTING: 'instances.starting',
  RUNNING: 'instances.running',
  STOPPING: 'instances.stopping',
  CRASHED: 'instances.crashed',
}

/**
 * 行渲染件的受控边界（ADR-097 b 范式）：**不取数、不发请求、不碰路由**。
 * - 七类实例派生判断由容器注入：节点名（`nodeName`，容器查节点表）、是否代理（`isProxy`，按能力画像）、
 *   是否搭建中（`provisioning`，`isProvisioningInstance` 属应用侧 API）、运行态漂移（`drift`）、
 *   三个动作的在途态（`starting` / `stopping` / `restarting`，容器按实例 id 比对——别的行在提交不该禁用本行）；
 * - 跨行共享的状态由容器持有后逐行下发：批量勾选（`selected`，与表头全选、批量栏同源）与 proxy 内联展开
 *   （`proxyExpanded`，虚拟窗口滚动会卸载行，行内自持会丢失展开态）；
 * - 「⋯」菜单与 proxy 展开的后端摘要都是**插槽**（`menu` / `renderBackends`）：前者接各弹窗目标，
 *   后者接 `useRegistrations` 取数容器，两者都属应用侧装配；
 * - 启动在途文案在包内选取，只上报意图（`onStart` / `onStop` / `onRestart` / `onKill` / `onToggleSelect` / `onToggleProxy`）。
 *
 * 行盒结构（两个 `<TableRow>`：主行 + 可选展开行）与迁移前逐字一致——虚拟窗口按固定 `itemSize` 定位，
 * 行内任何尺寸变化都会造成滚动累积漂移，故不得改动。
 */
export interface InstanceRowViewProps {
  /** 本行渲染的实例实体（虚拟窗口内逐行传入）。 */
  inst: InstanceInfo
  /** 所属节点名（容器由 `useNodes` 统一解析后注入，避免每行各自查节点表）。 */
  nodeName: string
  /** 本行是否处于批量选中态（选择集归容器，与表头全选、批量栏共享）。 */
  selected: boolean
  /** 切换本行勾选。 */
  onToggleSelect: () => void
  /** 是否代理实例（容器按能力画像 `bcTopology` 判定；行内不写死 `role === 'proxy'`）。 */
  isProxy?: boolean
  /** 是否搭建中（provision 未终态）：启动按钮硬性禁用并给提示。 */
  provisioning?: boolean
  /** 运行态漂移；无漂移为 `undefined`（行内据此决定是否出标记）。 */
  drift?: RuntimeDriftInfoView
  /** proxy 行内联后端是否展开（跨虚拟窗口卸载仍需保持，故由容器持有）。 */
  proxyExpanded?: boolean
  /** 展开/收起 proxy 行内联后端摘要。 */
  onToggleProxy?: () => void
  /** proxy 行内联后端摘要插槽（容器注入含 `useRegistrations` 取数的容器；仅在展开时调用）。 */
  renderBackends?: () => ReactNode
  /** 启动在途（容器按实例 id 比对后注入，避免别的行的提交禁用本行）。 */
  starting?: boolean
  /** 停止在途。 */
  stopping?: boolean
  /** 重启在途。 */
  restarting?: boolean
  /** 「⋯」次要操作菜单元素（标签 / 配置 / 限额 / 代理后端 / 克隆 / 接管 / 删除，由容器渲染）。 */
  menu: ReactNode
  /** 打开实例控制台（容器注入路由跳转）。 */
  onOpenInstance?: (id: number) => void
  /** 启动。 */
  onStart: () => void
  /** 停止。 */
  onStop: () => void
  /** 重启。 */
  onRestart: () => void
  /** 强杀（过渡态）：接入回调后由容器弹二次确认框，行内不直发写请求。 */
  onKill: () => void
}

/**
 * 实例列表单行：平铺虚拟表与分组树表共用同一渲染件。
 * 单元格内容（就地导入徽章、漂移标记、环境/标签徽标、状态徽章、按状态切换的主操作）与迁移前逐项一致。
 */
export function InstanceRowView({
  inst,
  nodeName,
  selected,
  onToggleSelect,
  isProxy = false,
  provisioning = false,
  drift,
  proxyExpanded = false,
  onToggleProxy,
  renderBackends,
  starting = false,
  stopping = false,
  restarting = false,
  menu,
  onOpenInstance,
  onStart,
  onStop,
  onRestart,
  onKill,
}: InstanceRowViewProps) {
  const { t } = useTranslation()
  const statusLabel = t(STATUS_LABEL_KEY[inst.status] ?? STATUS_LABEL_KEY.STOPPED)
  const instEnv = envOf(inst)
  const free = freeTagsOf(inst)

  return (
    <Fragment key={inst.id}>
      <TableRow data-state={selected ? 'selected' : undefined}>
        <TableCell>
          <Checkbox checked={selected} onCheckedChange={onToggleSelect} aria-label={inst.name} />
        </TableCell>
        <TableCell className="font-medium">
          <div className="flex items-center gap-1.5">
            {isProxy && (
              <button
                type="button"
                onClick={onToggleProxy}
                aria-label={t('proxy.manageBackends')}
                className="text-muted-foreground hover:text-foreground"
              >
                {proxyExpanded ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
              </button>
            )}
            <button
              type="button"
              className="text-left text-primary hover:underline"
              onClick={() => onOpenInstance?.(inst.id)}
            >
              {inst.name}
            </button>
            {/* 就地导入徽章（FR-302）：工作目录在托管区外，删除实例不删原目录。 */}
            {inst.workDirInPlace && (
              <Badge variant="outline" className="shrink-0 border-amber-500/50 text-amber-600 dark:text-amber-400">
                {t('importServer.inPlaceBadge')}
              </Badge>
            )}
            {/* 运行态漂移标记（FR-471）：目录下有未纳管活进程，面板状态可能不准（行内只做标记，
                接管入口收在「⋯」菜单，避免每行多插一个破坏密度的按钮）。 */}
            {drift && <RuntimeDriftBadge pid={drift.pid} cmdline={drift.cmdline} />}
          </div>
        </TableCell>
        <TableCell className="text-muted-foreground">{inst.type}</TableCell>
        {/* 节点:端口（FR-136）：serverPort 已有数据 */}
        <TableCell className="text-muted-foreground text-xs whitespace-nowrap">
          {nodeName}
          {inst.serverPort > 0 && <span className="tabular-nums">:{inst.serverPort}</span>}
        </TableCell>
        <TableCell>
          <RoleBadge role={inst.role} />
        </TableCell>
        <TableCell>
          <div className="flex flex-wrap items-center gap-1">
            {instEnv && (
              <Badge variant="outline" className="border-primary/40 text-primary">
                {t(`grouping.env_${instEnv}`, { defaultValue: instEnv })}
              </Badge>
            )}
            {free.map((tg) => (
              <Badge key={tg} variant="secondary" className="font-normal">
                {tg}
              </Badge>
            ))}
            {!instEnv && free.length === 0 && <span className="text-muted-foreground text-xs">--</span>}
          </div>
        </TableCell>
        <TableCell>
          <StatusBadge
            level={instanceStatusLevel(inst.status)}
            label={statusLabel}
            pulse={inst.status === 'STARTING' || inst.status === 'STOPPING'}
          />
        </TableCell>
        <TableCell>
          <div className="flex items-center gap-1">
            {/* 主操作随状态，操作进行中禁用防连点（FR-138）；
                搭建中硬性禁启（FR-331），tooltip 由外层 span 承载（禁用态 pointer-events-none）。 */}
            {(inst.status === 'STOPPED' || inst.status === 'CRASHED') && (
              <span title={provisioning ? t('instances.provisioningBlocked') : undefined}>
                <Button
                  variant="ghost"
                  size="xs"
                  disabled={provisioning || starting}
                  onClick={onStart}
                  aria-label={t('instances.start')}
                  className="text-green-600 hover:text-green-700"
                >
                  {starting ? t('instances.processing') : t('instances.start')}
                </Button>
              </span>
            )}
            {inst.status === 'RUNNING' && (
              <>
                <Button
                  variant="ghost"
                  size="xs"
                  disabled={stopping}
                  onClick={onStop}
                  aria-label={t('instances.stop')}
                  className="text-yellow-600 hover:text-yellow-700"
                >
                  {stopping ? t('instances.processing') : t('instances.stop')}
                </Button>
                <Button
                  variant="ghost"
                  size="xs"
                  disabled={restarting}
                  onClick={onRestart}
                  aria-label={t('instances.restart')}
                  className="text-blue-600 hover:text-blue-700"
                >
                  {restarting ? t('instances.processing') : t('instances.restart')}
                </Button>
              </>
            )}
            {(inst.status === 'STARTING' || inst.status === 'STOPPING') && (
              <Button
                variant="ghost"
                size="xs"
                onClick={onKill}
                aria-label={t('instances.kill')}
                className="text-yellow-600 hover:text-yellow-700"
              >
                {t('instances.kill')}
              </Button>
            )}
            {menu}
          </div>
        </TableCell>
      </TableRow>
      {isProxy && proxyExpanded && renderBackends && (
        <TableRow className="bg-muted/30 hover:bg-muted/30">
          <TableCell colSpan={8} className="p-0">
            {renderBackends()}
          </TableCell>
        </TableRow>
      )}
    </Fragment>
  )
}

export default InstanceRowView
