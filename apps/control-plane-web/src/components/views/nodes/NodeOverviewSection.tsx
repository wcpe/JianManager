import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useEffect, useState } from 'react'
import {
  Activity,
  ArrowDown,
  ArrowUp,
  CalendarPlus,
  CircleCheck,
  Copy,
  Cpu,
  Fingerprint,
  Gauge,
  Globe,
  HardDrive,
  Hash,
  HeartPulse,
  MemoryStick,
  MonitorCog,
  Network,
  Plug,
  Radio,
  Server,
  Timer as TimerIcon,
  Workflow,
} from 'lucide-react'

import { cn } from '@jianmanager/ui'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { formatFileSize } from '@/lib/shared/format-file-size'
import {
  NodeInstanceList,
  type NodeInstanceListItem,
  type NodeInstanceUsage,
} from '@/components/views/nodes/NodeInstanceList'
import type { NodeInfo } from '@/lib/nodes/node-types'

/**
 * 节点概览（详情页「概览」分区左栏）。
 *
 * 【为什么重做】上一版是 4 组朴素的 `label ....... value` 纯文字列表：
 * 视觉上没有任何容器与层级，值的大小/字重和标签几乎一样，一屏扫过去分不清
 * 「哪个是重点」；四组纵向堆叠还把左栏拉得很长，右栏留白更多——用户反馈
 * 「太空了、概览太丑」。
 *
 * 现在改成**带图标的卡片网格（2×2）**：
 * - 每组一张卡：图标 + 标题在卡头，条目在卡内，组与组之间有明确边界；
 * - 值用 `tabular-nums` 右对齐、比标签更重，扫读时先看到数字/主值；
 * - 技术标识（UUID / 主机）提供一键复制——排障时要贴进日志/工单，复制是高频动作；
 * - 2×2 铺满左栏宽度与高度，不再是一条细长的列表。
 */

/** 该节点实例汇总（容器取数后注入）。 */
export interface NodeOverviewInstances {
  /** 总数与按状态计数（来自 `/instances/aggregate?nodeId=`）。 */
  total: number
  byStatus: Record<string, number>
  /** 前几台实例（用于右侧列表；没有就不渲染列表行）。 */
  items: NodeInstanceListItem[]
  /** 当前状态筛选（null/缺省=全部）；与上面的 chips 联动。 */
  activeStatus?: string | null
  /** 列表条数上限提示：{ shown, total }，shown < total 时在底部说明「只显示前 N 台」。 */
  cappedHint?: { shown: number; total: number }
  /** 点状态 chip 切换筛选（容器据此改服务端查询）。 */
  onSelectStatus?: (status: string | null) => void
  /**
   * 各实例的实时用量（按实例 id）：CPU 百分比与堆内存字节。
   * 由容器用**一次**批量指标查询取回——不能按行各发一条，8 行就是 8 个 10s 轮询。
   */
  usage?: NodeInstanceUsage
  isLoading?: boolean
  isError?: boolean
}

export interface NodeOverviewSectionProps {
  node: NodeInfo
  /**
   * 本节点实例汇总。省略则不渲染右侧实例栏（保持纯清单布局）。
   *
   * 【为什么要它】概览原先只有一张 4 列清单，1440 宽下大约吃掉 450px，
   * 视口剩下的 250~300px 是纯空白。补一栏实例列表既填掉空白，又回答了
   * 「这台机器上跑着什么」——运营打开节点第一问。
   */
  instances?: NodeOverviewInstances
  /** 点「查看全部」跳到实例列表页（容器注入）。 */
  onOpenInstances?: () => void
  /** 点某台实例跳到它的实例详情页（容器注入）。 */
  onOpenInstance?: (id: number) => void
}

export function NodeOverviewSection({ node, instances, onOpenInstances, onOpenInstance }: NodeOverviewSectionProps) {
  const { t } = useTranslation()
  const online = node.status === 1
  /*
    「已接入时长」需要一个会走的现在。渲染期直接调 `Date.now()` 违反 React 纯度规则
    （同一次渲染可能给出不同值、不可缓存），且值不会自己前进——页面开着时「已接入 3h」
    会一直停在挂载那一刻。用 15s 一跳的 state 交替代它。
  */
  const [nowMs, setNowMs] = useState<number | null>(null)
  useEffect(() => {
    const tick = () => setNowMs(Date.now())
    tick()
    const id = setInterval(tick, 15_000)
    return () => clearInterval(id)
  }, [])

  /**
   * 已用/剩余由「比例 × 总量」推导。
   * 列表页只有一条水位条（无绝对值），这里补上绝对值是概览的独占价值。
   */
  const usedOf = (ratio: number | undefined, totalMb: number): string => {
    if (!online || !totalMb) return '--'
    return formatFileSize((ratio ?? 0) * totalMb * 1024 * 1024)
  }
  const freeOf = (ratio: number | undefined, totalMb: number): string => {
    if (!online || !totalMb) return '--'
    return formatFileSize(Math.max(0, totalMb - (ratio ?? 0) * totalMb) * 1024 * 1024)
  }
  const mbOf = (mb: number): string => (mb > 0 ? formatFileSize(mb * 1024 * 1024) : '--')

  /** 一键复制：排障时 UUID / 主机要贴进日志与工单。 */
  const copy = (text: string) => {
    void navigator.clipboard?.writeText(text)
  }

  /**
   * 每行都带图标 + 可选的进度条（用户要求「图文并茂」「补齐 icon」）。
   *
   * 【为什么把图标做到行级而不是只留组级】组图标只说明「这组是什么」，
   * 而行级图标让「CPU 核数 vs CPU 已用」「上行 vs 下行」这类同类项在扫读时
   * 一眼分得开——4 列并排时文字密度高，纯文字容易混行。
   *
   * 【进度条只给「有分母」的项】端口、时间戳、UUID 没有 0~100 的含义，
   * 硬画一条条会制造假信息。所以 `pct` 只在真正是「比例」的字段上传：
   * CPU/内存/磁盘水位、负载占核数比例。
   */
  type Row = {
    label: string
    value: ReactNode
    icon: typeof Cpu
    mono?: boolean
    copyable?: string
    /** 0~100：有此值才画进度条。 */
    pct?: number | null
    /**
     * 关键指标：用更大字号 + 阈值配色 + 百分比后缀，成为一眼看到的那几个数。
     * 其余行保持小字弱色，形成对比——「全部一样大」等于「没有重点」。
     */
    prominent?: boolean
  }

  const groups: Array<{ key: string; title: string; icon: typeof Cpu; rows: Row[] }> = [
    {
      key: 'capacity',
      title: t('nodes.groupCapacity', '容量明细'),
      icon: HardDrive,
      rows: [
        { label: t('nodes.cpuCores'), value: node.cpuCores > 0 ? node.cpuCores : '--', icon: Cpu },
        {
          // 已用核数 = 核数 × 使用率，比百分比更贴近「还剩几核」的判断。
          label: t('nodes.cpuUsed', 'CPU 已用'),
          // 关键指标：百分比 + 已用核数，一个数同时回答「忙不忙」与「还剩几核」。
          value: online
            ? `${Math.round((node.cpuUsage ?? 0) * 100)}% · ${((node.cpuUsage ?? 0) * node.cpuCores).toFixed(1)}/${node.cpuCores}`
            : '--',
          icon: Gauge,
          pct: online ? (node.cpuUsage ?? 0) * 100 : null,
          prominent: true,
        },
        { label: t('nodes.memoryTotal', '内存总量'), value: mbOf(node.memoryMb), icon: MemoryStick },
        {
          label: t('nodes.memoryUsed', '内存已用'),
          value: online ? `${Math.round((node.memoryUsage ?? 0) * 100)}% · ${usedOf(node.memoryUsage, node.memoryMb)}` : '--',
          icon: MemoryStick,
          pct: online ? (node.memoryUsage ?? 0) * 100 : null,
          prominent: true,
        },
        { label: t('nodes.diskTotal', '磁盘总量'), value: mbOf(node.diskTotalMb), icon: HardDrive },
        {
          label: t('nodes.diskFree', '磁盘剩余'),
          value: online ? `${100 - Math.round((node.diskUsage ?? 0) * 100)}% · ${freeOf(node.diskUsage, node.diskTotalMb)}` : '--',
          icon: HardDrive,
          // 剩余量画「已用比例」的条，与头部球的读数口径一致（都是已用率）。
          pct: online ? (node.diskUsage ?? 0) * 100 : null,
          prominent: true,
        },
      ],
    },
    {
      key: 'network',
      title: t('nodes.groupNetworkDetail', '端口与流量'),
      icon: Globe,
      rows: [
        { label: t('nodes.grpcPort'), value: node.grpcPort > 0 ? node.grpcPort : '--', icon: Plug },
        { label: t('nodes.wsPort'), value: node.wsPort > 0 ? node.wsPort : '--', icon: Radio },
        {
          // 网络合计：排障时先看总量级，再决定是否深挖上下行明细。
          label: t('nodes.trafficTotal', '流量合计'),
          value: online ? formatFileSize((node.networkBytesSent ?? 0) + (node.networkBytesRecv ?? 0)) : '--',
          icon: Network,
        },
        { label: t('nodes.trafficOut', '上行（累计）'), value: online && node.networkBytesSent ? formatFileSize(node.networkBytesSent) : '--', icon: ArrowUp },
        { label: t('nodes.trafficIn', '下行（累计）'), value: online && node.networkBytesRecv ? formatFileSize(node.networkBytesRecv) : '--', icon: ArrowDown },
        {
          label: t('nodes.tunnel', '隧道'),
          icon: Network,
          value: online ? (
            <Badge
              variant="outline"
              className={
                node.tunnelConnected
                  ? 'h-5 border-status-success/50 px-1.5 text-[10px] text-status-success'
                  : 'h-5 px-1.5 text-[10px] text-muted-foreground'
              }
            >
              {node.tunnelConnected ? t('nodes.tunnelConnected') : t('nodes.tunnelDirect')}
            </Badge>
          ) : (
            '--'
          ),
        },
      ],
    },
    {
      key: 'lifecycle',
      title: t('nodes.groupLifecycle', '生命周期'),
      icon: TimerIcon,
      rows: [
        { label: t('nodes.registeredAt', '接入时间'), value: formatDateTime(node.createdAt), mono: true, icon: CalendarPlus },
        {
          label: t('nodes.uptime', '已接入时长'),
          value:
            node.createdAt && nowMs !== null
              ? formatDuration(nowMs - new Date(node.createdAt).getTime(), t)
              : '--',
          icon: TimerIcon,
        },
        { label: t('nodes.lastHeartbeatAbs', '最后心跳'), value: formatDateTime(node.lastHeartbeat), mono: true, icon: HeartPulse },
        { label: t('nodes.heartbeatFreshness', '心跳新鲜度'), value: formatRelativeAge(node.lastHeartbeat), icon: Activity },
        {
          // load 在头部球里只有环与 1m 值，5m/15m 后端未提供，这里显式标注口径。
          label: t('nodes.loadShort', '负载 1m'),
          // 关键指标：负载 + 占核比例，后者才让「1.60 算不算高」可判断。
          value: online
            ? `${(node.loadAvg1 ?? 0).toFixed(2)}${
                node.cpuCores > 0 ? ` · ${Math.round(((node.loadAvg1 ?? 0) / node.cpuCores) * 100)}%` : ''
              }`
            : '--',
          icon: Gauge,
          // 负载的「分母」是核数：load/cores 即队列占了多少核，与头部球同一口径。
          pct: online && node.cpuCores > 0 ? Math.min(100, ((node.loadAvg1 ?? 0) / node.cpuCores) * 100) : null,
          prominent: true,
        },
        {
          label: t('nodes.statusDetail', '运行状态'),
          value: online ? t('nodes.online') : node.status === 2 ? t('nodes.starting') : t('nodes.offline'),
          icon: CircleCheck,
        },
      ],
    },
    {
      key: 'identity',
      title: t('nodes.groupIdentity', '标识（排障用）'),
      icon: Fingerprint,
      rows: [
        { label: t('nodes.uuid', '节点 UUID'), value: node.uuid, mono: true, copyable: node.uuid, icon: Fingerprint },
        { label: t('nodes.host', '主机'), value: node.host, mono: true, copyable: node.host, icon: Server },
        { label: t('nodes.id', '节点 ID'), value: node.id, icon: Hash },
        { label: t('nodes.osArch', '系统 / 架构'), value: `${node.os || '--'} / ${node.arch || '--'}`, icon: MonitorCog },
        {
          label: t('nodes.schedulingState', '调度状态'),
          value: node.maintenance ? t('nodes.unschedulable', '不可调度') : t('nodes.schedulable', '可调度'),
          icon: Workflow,
        },
        {
          /*
            补第 6 行：四列行数一致（6/6/6/6）后，四列末行底部才能对齐——
            之前是 6/6/6/5，第四列短一行，视觉上「右下角缺一块」。
            「接入方式」与「调度状态」同属该节点如何被平台纳管，放这组合适。
          */
          label: t('nodes.enrollMode', '接入方式'),
          value: online ? (node.tunnelConnected ? t('nodes.viaTunnel', '隧道') : t('nodes.viaDirect', '直拨')) : '--',
          icon: Network,
        },
      ],
    },
  ]

  return (
    /*
      【紧凑清单】用户选定样式：**一个面板 + 组分节 + 细线分隔 + 组内成对排列**。
      不用卡片框的原因：4 个边框 + 4 个卡头，为「5 行文字」付出了过多视觉成本，
      看起来既空又碎。现在只有一条外框，层级靠「组标题 + 细线」表达。

      列数随宽度自适应（1 → 2 → 4）：4 组各自成列时，同一个标签-值对仍在同一列内，
      扫读时视线只需纵向移动，比横跨两列找值快。
    */
    /*
      【不再拉伸】上一版 `h-full` 把面板撑到 443px，而内容只有 149px——
      294px 的空白正是「还是太空」的来源。可用的富余高度交给上方的图表
      （图表用实测高度自适应），清单只按内容高。
    */
    /*
      【两栏并拉伸】上一版只有一张 4 列清单且 `shrink-0`，1440×900 下清单约 450px 高，
      下方剩下近 300px 是纯空白（正是「空了一大块」）。清单本身已无更多信息可塞，
      于是补一栏「本节点实例」并把两栏撑满可用高度：左栏是规格与水位，右栏是跑着什么。
      窄屏单列堆叠，右栏自然落到清单下面。
    */
    <div className="grid min-h-0 flex-1 grid-cols-1 gap-3 lg:grid-cols-[minmax(0,1.05fr)_minmax(0,1fr)]">
      {/*
        左栏不再自带 overflow：页签外壳已经统一滚动（NodeDetailPane 的 overflow-y-auto），
        这里再套一层会出现同屏两条滚动条，且滚轮停在左栏时外层不动，很难用。
      */}
      <div className="rounded-lg border bg-card/60 p-3" data-testid="node-overview-grid">
      {/*
        【单列】左栏现在是「两栏布局里的左栏」（宽度约 500px），若继续按视口断点分 4 列，
        每列只剩 ~120px——标签被截断、数值贴到边上，整块挤成一团。
        四组改为纵向堆叠：每组一行组标题 + 6 行标签值，行内 label 与 value 各自有喘息空间。
      */}
      <div className="divide-y">
        {groups.map((group) => (
          <section
            key={group.key}
            data-testid={`node-overview-${group.key}`}
            className="flex min-w-0 flex-col py-2 first:pt-0 last:pb-0"
          >
            {/* 组标题：图标 + 短标题，字号小、颜色弱——它是分隔符而不是重点。 */}
            <header className="mb-2 flex h-5 items-center gap-2 border-b pb-1.5">
              <group.icon className="size-4 shrink-0 text-primary/70" aria-hidden="true" />
              {/* 标题放大到 13px 并用正文色：它是四列的「入口」，此前 11px 弱色看着像注释。 */}
              <h4 className="truncate text-[13px] font-semibold text-foreground">{group.title}</h4>
            </header>
            <dl className="flex flex-col">
              {group.rows.map((row) => {
                /*
                  【整齐的三条约束】
                  ① **行高统一 26px**：进度条用绝对定位于行底，不参与布局——
                     否则「有图表的行」比「没图表的行」高，四列扫过去高低不平。
                  ② **值列最小宽度 64px + 右对齐**：不同长度的时间/容量数值右边缘对齐，
                     视线竖着扫能一眼比较大小。
                  ③ **标签单行截断**：不换行，避免某一行因标签过长而撑高。
                  条的颜色复用状态阈值（≥90 危险 / ≥80 注意 / 其余正常），与头部球一致。
                */
                const pct = row.pct
                const tone =
                  pct == null ? null : pct >= 90 ? 'bg-status-danger' : pct >= 80 ? 'bg-status-warning' : 'bg-status-success'
                return (
                  <div
                    key={row.label}
                    data-testid={`node-overview-row-${row.label}`}
                    className={cn(
                      'group/section relative flex items-center gap-2 text-xs',
                      /*
                        关键指标行更高（30px vs 24px）：高度差本身就是层级——
                        四个大数所在的行明显「占地方」，一眼就能定位。
                        其余行 24px 保持紧凑。列内行数一致（6/6/6/6）时，
                        高度差不会破坏四列底边对齐（各列的关键指标数不同也不影响，
                        因为整列最终由 6 行累加，差值体现在列的呼吸上而非错位）。
                      */
                      row.prominent ? 'h-[30px]' : 'h-[24px]',
                    )}
                  >
                    <row.icon
                      className={cn('shrink-0', row.prominent ? 'size-3.5 text-primary/70' : 'size-3 text-muted-foreground/50')}
                      aria-hidden="true"
                    />
                    <dt
                      className={cn(
                        'min-w-0 flex-1 truncate',
                        row.prominent ? 'text-[11px] font-medium text-muted-foreground' : 'text-[11px] text-muted-foreground',
                      )}
                    >
                      {row.label}
                    </dt>
                    <dd className="flex min-w-[64px] shrink-0 items-center justify-end gap-1">
                      <span
                        className={cn(
                          'truncate text-right tabular-nums',
                          row.prominent
                            ? // 关键指标：大字 + 阈值色（与进度条、头部球同一判据）。
                              cn('text-[15px] font-bold', prominentTone(row.pct))
                            : row.mono
                              ? 'font-mono text-[11px] font-medium text-foreground/75'
                              : 'text-[12px] font-medium text-foreground/75',
                        )}
                        title={typeof row.value === 'string' ? row.value : undefined}
                      >
                        {row.value}
                      </span>
                      {row.copyable && (
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon-xs"
                          className="size-4 shrink-0 opacity-0 transition-opacity group-hover/section:opacity-60 hover:opacity-100 focus-visible:opacity-100"
                          onClick={() => copy(row.copyable!)}
                          aria-label={t('nodes.copyValue', '复制 {{label}}', { label: row.label })}
                          title={t('nodes.copyValue', '复制 {{label}}', { label: row.label })}
                        >
                          <Copy className="size-2.5" aria-hidden />
                        </Button>
                      )}
                    </dd>
                    {/* 进度条：关键指标用 3px 加粗并顶到行底，次要行不画。 */}
                    {pct != null && (
                      <span
                        className={cn(
                          'pointer-events-none absolute bottom-0 right-0 overflow-hidden rounded-full bg-muted',
                          row.prominent ? 'left-[22px] h-[3px]' : 'left-[20px] h-[2px]',
                        )}
                        role="img"
                        aria-label={`${row.label} ${Math.round(pct)}%`}
                      >
                        <span
                          className={cn('block h-full rounded-full transition-[width] duration-300', tone)}
                          style={{ width: `${Math.min(100, Math.max(0, pct))}%` }}
                        />
                      </span>
                    )}
                  </div>
                )
              })}
            </dl>
          </section>
        ))}
      </div>
      </div>
      {instances && (
        <NodeInstancesPanel
          instances={instances}
          {...(onOpenInstances ? { onOpen: onOpenInstances } : {})}
          {...(onOpenInstance ? { onOpenInstance } : {})}
        />
      )}
    </div>
  )
}

/**
 * 概览右栏的「本节点实例」外壳：边框 + 标题 + 总数 + 「查看全部」，列表体交给共享组件。
 *
 * 【为什么只留外壳】行内容、状态筛选、空/错误/加载态都在 `NodeInstanceList` 里，
 * 「实例」页签用的是同一个组件——两处一旦各写一份，改动必然漂移。
 */
function NodeInstancesPanel({
  instances,
  onOpen,
  onOpenInstance,
}: {
  instances: NodeOverviewInstances
  onOpen?: () => void
  onOpenInstance?: (id: number) => void
}) {
  const { t } = useTranslation()
  const { total, isLoading, isError } = instances
  return (
    <div className="flex min-h-0 flex-col rounded-lg border bg-card/60" data-testid="node-overview-instances">
      <div className="flex items-baseline justify-between gap-2 border-b px-3 py-2">
        <div className="flex items-baseline gap-2">
          <h4 className="text-[13px] font-semibold text-foreground">{t('nodes.overviewInstances')}</h4>
          {!isLoading && !isError && (
            <span className="text-[11px] tabular-nums text-muted-foreground">
              {t('nodes.instancesUnit')} {total}
            </span>
          )}
        </div>
        {onOpen && total > 0 && (
          <Button variant="ghost" size="xs" onClick={onOpen}>
            {t('nodes.viewAllInstances')}
          </Button>
        )}
      </div>
      <NodeInstanceList
        total={total}
        byStatus={instances.byStatus}
        items={instances.items}
        {...(instances.usage ? { usage: instances.usage } : {})}
        {...(isLoading ? { isLoading } : {})}
        {...(isError ? { isError } : {})}
        {...(instances.activeStatus !== undefined ? { activeStatus: instances.activeStatus } : {})}
        {...(instances.onSelectStatus ? { onSelectStatus: instances.onSelectStatus } : {})}
        {...(onOpenInstance ? { onOpenInstance } : {})}
        {...(instances.cappedHint ? { cappedHint: instances.cappedHint } : {})}
        testIdPrefix="node-overview-instance"
      />
    </div>
  )
}

/**
 * 紧凑时间格式：`2026-06-28 16:00`。
 * 默认 `toLocaleString` 输出 `6/28/2026, 4:00:00 PM`（约 22 字符），窄栏会被截断成
 * `6/28/2026, 4:00:...`——时间戳差一个字符就没法用于对日志，故固定短格式。
 */
function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return '--'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '--'
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** 心跳新鲜度（相对）：用于判断节点是否失联。 */
function formatRelativeAge(iso: string | null | undefined): string {
  if (!iso) return '--'
  const t0 = new Date(iso).getTime()
  if (Number.isNaN(t0)) return '--'
  const sec = Math.max(0, Math.round((Date.now() - t0) / 1000))
  if (sec < 60) return `${sec}s`
  const min = Math.round(sec / 60)
  if (min < 60) return `${min}m`
  const hour = Math.round(min / 60)
  if (hour < 24) return `${hour}h`
  return `${Math.round(hour / 24)}d`
}

/** 时长格式化（天/时）：避免直接暴露原始毫秒差。 */
function formatDuration(ms: number, t: (k: string, o?: Record<string, unknown>) => unknown): string {
  if (!Number.isFinite(ms) || ms < 0) return '--'
  const days = Math.floor(ms / 86_400_000)
  const hours = Math.floor((ms % 86_400_000) / 3_600_000)
  if (days > 0) {
    return `${days}${String(t('nodes.days', { defaultValue: '天' }))} ${hours}${String(t('nodes.hours', { defaultValue: '时' }))}`
  }
  return `${hours}${String(t('nodes.hours', { defaultValue: '时' }))}`
}

/** 关键指标数值的阈值配色（与进度条、头部球同一判据：≥90 危险 / ≥80 注意 / 其余正常）。 */
function prominentTone(pct: number | null | undefined): string {
  if (pct == null) return 'text-muted-foreground'
  if (pct >= 90) return 'text-status-danger'
  if (pct >= 80) return 'text-status-warning'
  return 'text-status-success'
}
