/* eslint-disable react-refresh/only-export-components -- 视图与随其导出的纯逻辑/变体同文件（沿用原组织方式），非组件导出按仓库约定在此豁免 */
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useEffect, useState } from 'react'
import { Server } from 'lucide-react'
import { ResourceGauge } from '@jianmanager/ui/components/gauge'
import { Tooltip, TooltipContent, TooltipTrigger } from '@jianmanager/ui/components/tooltip'
import { formatFileSize } from '@/lib/shared/format-file-size'
import { ObjectPageHeader } from '@jianmanager/ui/components/nav'
import { NodeActionsMenu } from '@/components/views/nodes/NodeListParts'
import type { NodeInfo } from '@/lib/nodes/node-types'

/** 右栏分段（§3.3）：概览/实例/JDK/缓存/端口/代理/监控/坏节点修复。 */
export type DetailTab =
  | 'overview'
  /** 进程：受管实例的进程清单（含端口占用），原「实例」与「端口」两页签合并。 */
  | 'process'
  | 'runtime'
  | 'cache'
  | 'proxy'
  | 'probe'
  | 'monitor'
  /** 配置总览：汇总散落在各分区的可写配置 + 只读项（见 NodeConfigSection）。 */
  | 'config'

export const DETAIL_TABS: DetailTab[] = ['overview', 'process', 'monitor', 'config', 'runtime', 'cache', 'proxy', 'probe']

/** 
 * tab 分组（用户要求「tab 栏分组」）。
 *
 * 9 个 tab 平铺一行，窄屏会折行，且「概览 / 实例 / 监控」这些高频项与
 * 「代理 / 探针版本 / 环节点修复」这些低频项混在一起，扫读时没有层次。
 * 按用途分三组：日常巡检 / 运行与资产 / 诊断与修复。
 */
export const DETAIL_TAB_GROUPS: Array<{ key: string; tabs: DetailTab[] }> = [
  { key: 'daily', tabs: ['overview', 'process', 'monitor'] },
  /*
    探针版本并入「运行与资产」组：坏节点修复删除后，原「诊断与修复」组只剩它一项，
    分组名与分隔线都不再成立。它本质也是这个节点的一项资产配置。
  */
  { key: 'assets', tabs: ['config', 'runtime', 'cache', 'proxy', 'probe'] },
]

/** 某 tab 属于哪个顶层分组。 */
export function groupOfTab(tab: DetailTab): string {
  return DETAIL_TAB_GROUPS.find((g) => g.tabs.includes(tab))?.key ?? 'daily'
}

export interface NodeDetailPaneProps {
  node: NodeInfo
  instanceCount: number
  tab: DetailTab
  onTab: (t: DetailTab) => void
  onToggleMaintenance: () => void
  onDrain: () => void
  onDelete: () => void
  /** 面包屑导航（容器注入路由跳转；组件库不依赖 react-router）。 */
  onNavigate: (to: string) => void
  /** 
   * 分段内容插槽：各分段的取数接线层与面板（含 7 个 tab 容器）留在应用侧，
   * 由调用方按当前 tab 渲染。
   */
  renderTab: (tab: DetailTab, node: NodeInfo) => ReactNode
  /** 
   * 节点切换器插槽（下拉选择器）。原先是左侧的节点列表——与列表页功能重叠，
   * 改为云厂商式的顶部选择器：切换目标用下拉，内容区拿回全宽。
   */
  nodeSelector?: ReactNode
}
export function NodeDetailPane({
  node,
  instanceCount,
  tab,
  onTab,
  onToggleMaintenance,
  onDrain,
  onDelete,
  onNavigate,
  renderTab,
  nodeSelector,
}: NodeDetailPaneProps) {
  const { t } = useTranslation()
  const online = node.status === 1
  /** 当前页签所属的顶层分组（决定顶部哪个 tab 高亮、左侧显示哪组子项）。 */
  /** 
   * 心跳新鲜度 → 0~100 的活性分（喂给状态球）。
   * 90s 内视为完全新鲜，之后随超时线性衰减到 0：这样「在线但 20 分钟没上报」
   * 会显示一个明显变矮的环，比只写「在线」更能暴露假活。
   */
  /* 
    心跳龄需要一个「会走的现在」。渲染期直接调 `Date.now()` 有两个毛病：
    违反 React 纯度规则（同一次渲染可能给出不同值、不可缓存），而且值不会自己前进——
    页面停在同一屏时环会一直显示挂载那一刻的年龄。改用 15s 一跳的 state。
  */
  const [nowMs, setNowMs] = useState<number | null>(null)
  useEffect(() => {
    const tick = () => setNowMs(Date.now())
    tick()
    const id = setInterval(tick, 15_000)
    return () => clearInterval(id)
  }, [])
  /* 首帧 effect 还没跑：以心跳时刻为基准，年龄计 0，下一帧即校正。 */
  const lastBeatMs = node.lastHeartbeat ? Date.parse(node.lastHeartbeat) : Number.NaN
  const ageBaseMs = nowMs ?? (Number.isFinite(lastBeatMs) ? lastBeatMs : 0)

  const heartbeatScore = (() => {
    if (!online) return 0
    if (!node.lastHeartbeat) return 0
    const ageMs = ageBaseMs - new Date(node.lastHeartbeat).getTime()
    if (Number.isNaN(ageMs)) return 0
    const STALE_MS = 90_000
    return Math.max(0, Math.min(100, Math.round((1 - ageMs / STALE_MS) * 100)))
  })()
  /** 心跳相对时间（球旁的小字）。 */
  const heartbeatAge = (() => {
    if (!node.lastHeartbeat) return '--'
    const sec = Math.max(0, Math.round((ageBaseMs - new Date(node.lastHeartbeat).getTime()) / 1000))
    if (sec < 60) return `${sec}s`
    const min = Math.round(sec / 60)
    if (min < 60) return `${min}m`
    const h = Math.round(min / 60)
    if (h < 24) return `${h}h`
    return `${Math.round(h / 24)}d`
  })()

  /* 
    【维护态优先】改成球之后，徽章被移除，状态全部由球的 label 承载——
    这里必须把 maintenance 纳入判断，否则「在线且维护中」会显示成「在线」，
    「维护中」这个信息在详情页彻底消失（移除徽章时踩到的真实回归）。
  */
  const statusLabel = node.maintenance
    ? t('nodes.maintenance')
    : online
      ? t('nodes.online')
      : node.status === 2
        ? t('nodes.starting')
        : t('nodes.offline')

  return (
    /* 
      根容器用 flex 列 + `h-full`：让内容区（两栏）吃掉剩余高度。
      原来 `space-y-3` 只是块级堆叠，子级拿不到可分配高度，
      于是「左栏 331px + 右栏 457px」都只按自身内容高，下方留出 363px 空白——
      这正是用户反复说的「太空」。
    */
    <div className="flex h-full min-h-0 flex-col gap-3">
      {/* 阶段 6 第二步：身份块与分段 Tabs 合并为对象头（原型 `object-head` 的六段：
          面包屑 → 名/状态/元信息/操作 → 指标条 → 工具导航）。
          两个取舍：① 4 个环形仪表**不进 metrics**——原型 `object-stats` 是文字格，但
v2 明确「资源仪表内联右置」，那个设计比原型新且是有意的，故留在内容区；
          ② 隧道/维护徽标同理，对象头只有单个 status，它们在仪表行一并呈现。 */}
      <ObjectPageHeader
        icon={<Server className="size-5" />}
        title={node.name}
        /* 选择器并入标题行（节点名右侧）：云控制台惯例，省掉独立一行。 */
        titleAdornment={nodeSelector}
        /* 
          不再传 status 徽章：状态改由右上角的球承载（用户要求「运行状态换成首页那种球」）。
          同时保留徽章会与球的标签重复表达同一件事。
        */
        /* 
          meta 区放**节点选择器**（云控制台式）：切换节点不必退回列表，
          同时把原左栏的 288px 还给内容区。
          去掉了原来的 IP / 系统两项目——它们与列表页的 address / system 列重复，
          且新选择器的每行已经带 host。
        */
        meta={[
          { label: t('nodes.instancesUnit'), value: instanceCount },
        ]}
        /* 
          actions 插槽放**状态球**（用户要求「放在顶部、节点介绍右侧」）：
          对象头的右侧是这一行里最靠右的位置，球在这里与名称同一视线高度，
          「这台是什么 + 现在怎么样」一眼读完。
          「⋯」更多操作移到了下面的指标行（用户要求「三个点放到下面来」）——
          它是低频动作入口，不该占据头部最显眼的位置。
        */
        /* 
          【所有球都在头部】用户要求「把这些球都移到头部」——CPU/内存/磁盘/负载/状态
          五个球与节点名同一行，从「这台是什么」到「现在什么水位」一次读完，
          下面不再有「指标条」那一层，省出的纵向空间给图表。
          每个球带 tooltip（用户要求「和首页一样可以展示 tooltip 基础数据」）：
          环上只有百分比，绝对值（已用/总量、load 明细）放不下也没必要塞进环里。
        */
        actions={
          <div className="flex items-center gap-3" data-testid="node-detail-header-gauges">
            <GaugeWithTooltip
              label={t('nodes.cpu')}
              value={online ? Math.round((node.cpuUsage ?? 0) * 100) : 0}
              display={online ? `${Math.round((node.cpuUsage ?? 0) * 100)}` : '--'}
              unit="%"
              level={online ? (node.cpuUsage ?? 0) >= 0.9 ? 'danger' : (node.cpuUsage ?? 0) >= 0.8 ? 'warning' : 'success' : 'neutral'}
              tooltip={`${node.cpuCores} ${t('nodes.cpuCores')}`}
            />
            <GaugeWithTooltip
              label={t('nodes.memory')}
              value={online ? Math.round((node.memoryUsage ?? 0) * 100) : 0}
              display={online ? `${Math.round((node.memoryUsage ?? 0) * 100)}` : '--'}
              unit="%"
              level={online ? (node.memoryUsage ?? 0) >= 0.9 ? 'danger' : (node.memoryUsage ?? 0) >= 0.8 ? 'warning' : 'success' : 'neutral'}
              tooltip={`${t('nodes.memoryUsed', '内存已用')} ${online ? formatFileSize((node.memoryUsage ?? 0) * node.memoryMb * 1024 * 1024) : '--'} / ${formatFileSize(node.memoryMb * 1024 * 1024)}`}
            />
            <GaugeWithTooltip
              label={t('nodes.disk')}
              value={online ? Math.round((node.diskUsage ?? 0) * 100) : 0}
              display={online ? `${Math.round((node.diskUsage ?? 0) * 100)}` : '--'}
              unit="%"
              level={online ? (node.diskUsage ?? 0) >= 0.9 ? 'danger' : (node.diskUsage ?? 0) >= 0.8 ? 'warning' : 'success' : 'neutral'}
              tooltip={`${t('nodes.diskUsed', '磁盘已用')} ${online ? formatFileSize((node.diskUsage ?? 0) * node.diskTotalMb * 1024 * 1024) : '--'} / ${formatFileSize(node.diskTotalMb * 1024 * 1024)}`}
            />
            <GaugeWithTooltip
              label={`${t('nodes.load')}/${node.cpuCores}`}
              value={online ? Math.min(100, ((node.loadAvg1 ?? 0) / Math.max(1, node.cpuCores)) * 100) : 0}
              display={online ? (node.loadAvg1 ?? 0).toFixed(2) : '--'}
              unit=""
              level={online ? undefined : 'neutral'}
              tooltip={`load 1m ${online ? (node.loadAvg1 ?? 0).toFixed(2) : '--'} / ${node.cpuCores} ${t('nodes.cpuCores')}`}
            />
            {/* 状态球放最右：心跳活性 + 状态标签。 */}
            <div data-testid="node-detail-status-orb">
              <GaugeWithTooltip
                label={statusLabel}
                value={heartbeatScore}
                display={`${heartbeatScore}`}
                unit=""
                level={online ? (node.maintenance ? 'warning' : 'success') : 'danger'}
                tooltip={`${t('nodes.heartbeat', '心跳')} ${heartbeatAge}${node.maintenance ? ` · ${t('nodes.maintenance')}` : ''}${online ? ` · ${node.tunnelConnected ? t('nodes.tunnelConnected') : t('nodes.tunnelDirect')}` : ''}`}
              />
            </div>
          </div>
        }
        /* 
          顶部 tab = **第一层**：3 个分组（日常巡检 / 运行与资产 / 诊断与修复）。
          组内的具体页签放到左侧栏（第二层），这样顶部只占 3 个位置、窄屏不折行，
          同时保留了「先选大类、再看细项」的两步心智。
          点击某组时进入该组的第一个页签——避免出现「组已选中但没有可见内容」。
        */
        /* 
          【单层 tab】用户选了云控制台式布局：9 个分区平铺一行，组间用竖线分隔。
          原来「顶部 3 组 + 左侧子项」的两层结构在固定视口下要吃掉 145px 纵向空间，
          而它带来的信息增量只是「分两批看」——平铺 + 分隔线同样能表达分组，且零额外高度。
        */
        tools={DETAIL_TAB_GROUPS.flatMap((group, index) => [
          ...(index > 0 ? [{ key: `sep-${group.key}`, label: null, separator: true as const }] : []),
          ...group.tabs.map((k) => ({
            key: k,
            label: t(`nodes.tab.${k}`),
            active: k === tab,
            onSelect: () => onTab(k),
          })),
        ])}
        toolsLabel={t('nodes.tools')}
        /* 「⋯」更多操作挂在 tab 行右端：既不占头部主位，也不单独占一行。 */
        toolsActions={
          <NodeActionsMenu
            node={node}
            onToggleMaintenance={onToggleMaintenance}
            onDrain={onDrain}
            onDelete={onDelete}
          />
        }
        onNavigate={onNavigate}
      />
      {/*
        【概览不画曲线】曲线只在「监控」页签出现。
        原先概览顶部还有一条四图趋势带，和监控页签画的是同一份 `useMetricSeries`：
        双重取数、两套时间范围状态互不同步（用户在监控页切了范围，回概览发现不是那个范围），
        而且自定义时间窗只挂在概览那套上，反倒让「看历史」的监控页只能选预设。
        现在概览只回答「这台机器是什么、现在怎么样」，时序一律归监控。
      */}
      {/*
        【固定壳 + 页签内滚动】所有页签统一成「占满剩余高度 + 自己滚动」：
        切页签时内容区高度不变，滚动条只出现在内容区内部，不会带着整个页面跳。
        之前只有概览是 flex-1、其余是内容高度，从概览切到配置时整页高度骤变、滚动位置乱跳。

        `min-h-full` 内层是关键：概览的两栏要「内容短时撑满、内容长时正常溢出」，
        只写 flex-1 在滚动容器里拿不到确定高度，就撑不满了。
      */}
      <div className="flex min-h-0 flex-1 flex-col overflow-y-auto">
        <div className="flex min-h-full flex-col" data-testid="node-detail-tab-body">
          {renderTab(tab, node)}
        </div>
      </div>
    </div>
  )
}

/** 
 * 带 tooltip 的环形仪表（与首页同一套 `ResourceGauge`，只是补了悬浮说明）。
 *
 * 【为什么不用首页那个 GaugeAttributionTooltip】那个组件绑定的是「CPU 构成归因快照」
 * （user/system/idle/iowait…），需要额外取数与容器配合，是首页专属的重组件。
 * 节点详情要的是「环上百分比之外的基础数据」——已用/总量、核数、load 明细——
 * 用标准 Tooltip 承载即可，不必为它接一条归因取数链。
 *
 * 【display 与 value 分离】`value` 决定环的填充（0~100），`display` 是球心文字。
 * 负载球就是靠这个分离做到「环按占用核数比例、球心显示原始 load」的。
 */
function GaugeWithTooltip({
  label,
  value,
  display,
  unit,
  level,
  tooltip,
  size = 56,
}: {
  label: string
  value: number
  display: string
  unit: string
  level?: 'success' | 'warning' | 'danger' | 'neutral'
  tooltip: string
  size?: number
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        {/* 包一层 span 承接焦点：ResourceGauge 本身不是可聚焦元素，
            包起来后键盘用户也能通过 Tab 看到 tooltip。 */}
        <span tabIndex={0} className="rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring/40">
          <ResourceGauge
            label={label}
            value={value}
            unit={unit}
            size={size}
            thickness={5}
            level={level}
            decimals={0}
          />
        </span>
      </TooltipTrigger>
      <TooltipContent>
        <span className="text-xs">
          {label}: {display}
          {unit} · {tooltip}
        </span>
      </TooltipContent>
    </Tooltip>
  )
}


