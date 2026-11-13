import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Cpu, MemoryStick, Users, Zap, Play, Square, RotateCw, Server, Globe } from 'lucide-react'

import { MiniBar } from '@jianmanager/ui/components/mini-bar'
import { Button } from '@jianmanager/ui/components/button'
import { Card } from '@jianmanager/ui/components/card'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { cn } from '@jianmanager/ui'
import { InstanceIdentityBadges } from '@/components/views/instances/InstanceIdentityBadges'
import { InstanceLifecycleStatus } from '@/lib/instances/InstanceLifecycleStatus'
import { instanceLifecyclePresentation } from '@/lib/instances/instance-lifecycle-status'



/** 实例展示数据（本组件所需的最小结构；外壳传 InstanceInfo 会结构兼容）。 */
export interface InstanceWorktableCardInstanceView {
  id: number
  name: string
  status: string
  /** 类型标识（如 minecraft_java）：用于推导「插件服 / 代理 / 二进制」类别徽章。 */
  type: string
  /** 角色（backend / proxy / universal）：身份徽章里与类型并列展示，与表格身份列同源。 */
  role: string
  /** 游戏端口；0 表示未分配（不显示端口段）。 */
  serverPort: number
  /** 配置的内存上限（探针与容器上限都取不到时的分母回退）。 */
  memLimitMb?: number
  /** 状态原因（失败原因或搭建中提示，二者共用该字段）。 */
  statusReason?: string
  /** 
   * 大区 / 小区（由外壳从实例标签 `region:` / `zone:` 解析后注入）。
   *
   * 【为什么放在卡片而不是分组头】大区是虚拟分区、没有独立实体，做成折叠层级后
   * 两层组头把真正的信息压到屏幕下方。改为卡面左上角一行文字：既保留归属信息，
   * 又不占额外的行。任一为空时该段退化为另一项，两者都空则不渲染。
   */
  region?: string
  zone?: string
}

/** 实时指标（本组件所需的最小结构）。 */
export interface InstanceWorktableMetricsView {
  cpuPercent?: number
  memoryMb?: number
  /** JVM 堆上限（探针）；> 0 时优先作为内存条分母。 */
  heapMaxMb?: number
  onlinePlayers?: number
  maxPlayers?: number
  /** 玩家容量与在线人数分别可用，不能用缺失容量拼出 /0。 */
  maxPlayersAvailable?: boolean
  tps?: number
  /** 在线数是否可得（探针/SLP/Query 任一有值）。 */
  playersAvailable?: boolean
  /** TPS 是否可得（仅探针）。 */
  probeAvailable?: boolean
}

/** 运行态漂移（本组件所需的最小结构）。 */
export interface InstanceWorktableDriftView {
  pid: number
  cmdline?: string
}

/** 可选故障提示的高度上限，完整文本仍可通过 title 读取。 */
export const CARD_EXTRA_MAX_HEIGHT = 35

/** 身份徽章保持紧凑单行；没有徽章时不留空白占位。 */
export const CARD_IDENTITY_HEIGHT = 24

/** 
 * 实例工作台卡（§4.5 运行实体范式）。
 * 内嵌资源（CPU/内存条 + 玩家/TPS）+ 呼吸灯（运行时脉动）+ 启停/重启按钮；点名进控制台工作区。
 * 仅运行态拉实时指标（原先在卡内惰性 enable），停机卡不轮询、资源显「--」。
 *
 * 受控视图：不取数、不碰路由——三处「谁来做判断」的边界在此划清：
 * - **能力画像**（是否代理）由外壳判定后以 `isProxy` 注入，卡内不写死 `role === 'proxy'`；
 * - **搭建中**（provision 未终态）由外壳判定后注入，卡内据此禁用启动；
 * - **运行态漂移**由外壳判定后注入（卡面只说清事实，接管入口在「⋯」菜单里）；
 * - **路由跳转**由外壳注入 `onOpen`（列表页要自定义跳转，超级工作台则用默认深链）。
 * 状态光晕是纯展示映射（`instanceStatusGlowClass` 已随包），故留在卡内计算。
 */
export interface InstanceWorktableCardProps {
  /** 实例展示数据。 */
  inst: InstanceWorktableCardInstanceView
  /** 所属节点名（由列表统一解析后传入，避免卡内各自查节点表）。 */
  nodeName: string
  /** 节点的主机地址，用于展示游戏连接地址。 */
  nodeHost?: string
  /** 是否为代理实例（外壳按能力画像判定）。 */
  isProxy?: boolean
  /** 是否搭建中（provision 未终态）：启动按钮硬性禁用并给提示。 */
  provisioning?: boolean
  /** 运行态漂移；无漂移为 undefined。 */
  drift?: InstanceWorktableDriftView
  /** 实时指标；未运行或未取到为 undefined。 */
  metrics?: InstanceWorktableMetricsView
  /** 启动在途（外壳按实例 id 比对后注入，避免别的卡的提交禁用本卡）。 */
  starting?: boolean
  /** 停止在途。 */
  stopping?: boolean
  /** 重启在途。 */
  restarting?: boolean
  /** 角色徽标元素（统一语义色，由页面渲染）。 */
  roleBadge: ReactNode
  /** 「⋯」次要操作菜单元素（标签/限额/克隆/删除，由页面渲染）。 */
  menu: ReactNode
  /** 打开实例控制台；外壳注入（默认深链或自定义跳转）。 */
  onOpen?: (id: number) => void
  /** 
   * 右键菜单入口：只上报**光标坐标**，不上报实例。
   *
   * 【为什么不传实例】本视图用的是自己的最小结构，而菜单需要完整领域对象；
   * 让视图往外递领域对象会把 api 层模型反向耦合进受控视图。改为接线层用闭包
   * 捕获 `inst`——视图只管「在哪儿右键了」，语义更窄也更难用错。
   */
  onContextMenu?: (x: number, y: number) => void
  onStart: () => void
  /** 停止。 */
  onStop: () => void
  /** 重启。 */
  onRestart: () => void
  /** 环境标签值（与表格身份列同源，用于渲染环境徽章）。 */
  env?: string
  /** 自由标签（与表格身份列同源）。 */
  freeTags?: string[]
  /** 身份徽章的显示开关：与表格列设置对齐（type/role/tags 三个可选列）。 */
  showType?: boolean
  showRole?: boolean
  showTags?: boolean
  /** 是否被批量选中（用户提出：卡片补齐多选）。 */
  selected?: boolean
  /** 
   * 勾选框/卡片被点选。
   *
   * `event` 用于读 Shift / Ctrl / Meta 修饰键——连选与点选的语义由外壳统一实现
   * （见 `applySelectionClick`），本视图只负责把修饰键如实上报，不自己算区间。
   */
  onToggleSelect?: (modifiers: { shiftKey: boolean; ctrlKey: boolean; metaKey: boolean }) => void
}

export function InstanceWorktableCard({
  inst,
  nodeName,
  nodeHost,
  isProxy = false,
  provisioning = false,
  drift,
  metrics,
  starting = false,
  stopping = false,
  restarting = false,
  menu,
  onOpen,
  onContextMenu,
  onStart,
  onStop,
  onRestart,
  env,
  freeTags = [],
  showType = true,
  showRole = true,
  showTags = true,
  selected = false,
  onToggleSelect,
}: InstanceWorktableCardProps) {
  const { t } = useTranslation()

  const running = inst.status === 'RUNNING'
  const stopped = inst.status === 'STOPPED' || inst.status === 'CRASHED'
  const pendingAction = starting ? 'start' : stopping ? 'stop' : restarting ? 'restart' : undefined
  const lifecycle = instanceLifecyclePresentation(inst, { provisioning, pendingAction })
  const busy = lifecycle.transitioning

  // 缺测不能退回 0；合法的零 CPU / 零内存仍是可展示的测量值。
  const cpuPct = running ? nonnegative(metrics?.cpuPercent) : null
  const memMb = running ? nonnegative(metrics?.memoryMb) : null
  const heapMax = nonnegative(metrics?.heapMaxMb)
  const configuredMax = nonnegative(inst.memLimitMb)
  const memMax = heapMax != null && heapMax > 0 ? heapMax : configuredMax != null && configuredMax > 0 ? configuredMax : null
  const memPct = memMb != null && memMax != null ? Math.min(100, (memMb / memMax) * 100) : null
  const cpuLabel = cpuPct == null ? '--' : `${cpuPct.toFixed(cpuPct > 0 && cpuPct < 10 ? 1 : 0)}%`
  const memLabel = memMb == null ? '--' : memMax == null ? fmtMem(memMb) : `${fmtMem(memMb)} / ${fmtMem(memMax)}`
  const unavailable = t('metrics.unavailable')
  const onlinePlayers = nonnegative(metrics?.onlinePlayers)
  const maxPlayers = nonnegative(metrics?.maxPlayers)
  const tps = nonnegative(metrics?.tps)
  const playersAvailable = running && metrics?.playersAvailable === true && onlinePlayers != null
  const maxPlayersAvailable = metrics?.maxPlayersAvailable === true && maxPlayers != null
  const playersLabel = !running ? '--' : !playersAvailable ? unavailable
    : maxPlayersAvailable ? `${Math.round(onlinePlayers)} / ${Math.round(maxPlayers)}` : String(Math.round(onlinePlayers))
  const tpsAvailable = running && metrics?.probeAvailable === true && tps != null
  const tpsLabel = !running ? '--' : tpsAvailable ? tps.toFixed(1) : unavailable

  // IPv6 地址需要方括号；连接端口只取游戏端口，不取节点的 WS / gRPC 端口。
  const host = nodeHost?.trim()
  const addressHost = host && host.includes(':') && !host.startsWith('[') ? `[${host}]` : host
  const validPort = Number.isInteger(inst.serverPort) && inst.serverPort > 0 && inst.serverPort <= 65535
  const endpoint = addressHost
    ? validPort ? `${addressHost}:${inst.serverPort}` : `${addressHost} · ${t('instances.card.portUnassigned', '端口未分配')}`
    : validPort ? `${t('instances.card.addressUnknown', 'IP 未知')} · ${t('instances.card.port', '端口')} ${inst.serverPort}`
      : t('instances.card.endpointUnassigned', 'IP / 端口未分配')

  // 大区 · 小区：两者都有时用「·」分隔；只有一个时直接显示那个；都没有则不渲染整行。
  const region = inst.region?.trim() ?? ''
  const zone = inst.zone?.trim() ?? ''
  const regionZoneLabel = region && zone ? `${region} · ${zone}` : region || zone

  return (
    /* 
      整块可点击（进入工作区）→ 保留原 onClick 语义与 interactive 的 hover 抬升；
      业务特性全部留在 className/children 里：
      - 状态光晕 `instanceStatusGlowClass`（伪元素画外圈，依赖容器自身圆角继承）；
      - `hover:shadow-lift` 悬停反馈；
      - 卡内各段自带 `mt-3` 节奏，故 gap-0 保持原纵向间距。
      注：`@/test/ui-details` 的守护测试断言本文件保留 hover:shadow-lift，勿删。
    */
    <Card
      interactive
      data-testid="instance-worktable-card"
      data-selected={selected ? 'true' : undefined}
      className={cn(
        // 虚拟列表提供固定行盒，内部提示和徽章有界，底部操作区始终保留。
        'group relative flex h-full min-h-0 min-w-0 shrink-0 cursor-pointer flex-col gap-0 overflow-hidden p-3',
        // 选中态用描边：卡片没有行高可挂，靠边框表达最省空间也最不易误读。
        selected && 'ring-2 ring-primary/60 ring-offset-1',
      )}
      onClick={(event) => {
        /* 
          修饰键语义交给外壳（applySelectionClick）：Ctrl/Cmd 切换单项、Shift 连选、
          无修饰键是「替换为单项」而不是「打开控制台」。
          为什么无修饰键不打开：卡片一旦支持多选，单击的首要含义就是选它（与表格行一致——
          表格里单击行也是选中，打开靠名称链接/双击）。保持两种视图手感一致。
        */
        if (selected || event.ctrlKey || event.metaKey || event.shiftKey) {
          onToggleSelect?.({
            shiftKey: event.shiftKey,
            ctrlKey: event.ctrlKey,
            metaKey: event.metaKey,
          })
          return
        }
        if (onToggleSelect) {
          // 支持选择时，单击 = 选中（与表格行一致），打开走「⋯」或双击。
          onToggleSelect({ shiftKey: false, ctrlKey: false, metaKey: false })
          return
        }
        onOpen?.(inst.id)
      }}
      onDoubleClick={() => onOpen?.(inst.id)}
      onContextMenu={
        onContextMenu
          ? (event) => {
              // 阻止浏览器原生菜单，避免与本菜单叠出两套（与列表行同款处理）。
              event.preventDefault()
              onContextMenu(event.clientX, event.clientY)
            }
          : undefined
      }
    >
      {/*
        勾选框：卡片视图补齐多选（用户提出）。放右上角且 `stopPropagation`——
        否则点勾选会同时触发卡片的 onClick，变成「勾上又立刻按修饰键逻辑取消」。
      */}
      {onToggleSelect && (
        // 为 Checkbox 向外扩展的触区留 12px，避免制造几像素的横向溢出。
        <div className="absolute right-3 top-2 z-10" onClick={(e) => e.stopPropagation()}>
          <Checkbox
            checked={selected}
            onCheckedChange={() => onToggleSelect({ shiftKey: false, ctrlKey: false, metaKey: false })}
            aria-label={t('instanceBatch.selectOne', { name: inst.name, defaultValue: '选择 {{name}}' })}
          />
        </div>
      )}
      {/* 左上角：大区 · 小区。大区是虚拟分区，不做折叠层级，只以一行小字交代归属；
          两者都空时不占位（不留空行，卡片高度由构造不变）。 */}
      {regionZoneLabel && (
        <div
          data-testid="instance-card-region-zone"
          className="mb-2 truncate text-micro text-muted-foreground"
          title={regionZoneLabel}
        >
          {regionZoneLabel}
        </div>
      )}
      {/* 头部：名称 + 状态徽章 + 菜单。去掉 40px 图标块——
          卡片每行放 4 张后，图标块占掉近 1/6 宽度却不携带信息（状态已由徽章表达）。 */}
      <div className={cn('flex min-w-0 shrink-0 items-start gap-2', onToggleSelect && 'pr-5')}>
        <div className="min-w-0 flex-1">
          {/* 卡片标题即进详情入口：外观是纯标题文字，取 invisible + auto。
              onClick 内部 stopPropagation 阻止冒泡到卡片自身的选中逻辑，原样保留。 */}
          <Button
            type="button"
            variant="invisible"
            size="auto"
            className="block max-w-full truncate text-left text-sm font-semibold hover:text-primary"
            onClick={(e) => {
              e.stopPropagation()
              onOpen?.(inst.id)
            }}
            title={inst.name}
          >
            {inst.name}
          </Button>
          <div className="mt-0.5 min-w-0">
            <InstanceLifecycleStatus inst={inst} provisioning={provisioning} pendingAction={pendingAction} compact={Boolean(drift)} />
          </div>
          {drift && (
            <div
              className="mt-1 shrink-0 overflow-hidden"
              style={{ maxHeight: CARD_EXTRA_MAX_HEIGHT }}
              data-testid="instance-card-extra"
            >
              <p className="truncate text-meta text-status-warning" title={drift.cmdline}>
                {t('serverConsole.runtimeDriftDesc', { pid: drift.pid })}
              </p>
            </div>
          )}
        </div>
        {/* 角色只在下面的身份徽章行展示，避免头部和正文重复。 */}
        {/* 次要操作菜单（⋯）不冒泡到卡片，避免点菜单误开工作区（FIX-9）。 */}
        <span onClick={(e) => e.stopPropagation()}>{menu}</span>
      </div>

      {/* 徽章按内容占位，只保留有界高度，不再用两行空槽挤压实时指标。 */}
      <div
        className="mt-2 min-h-0 shrink-0 overflow-hidden"
        style={{ maxHeight: CARD_IDENTITY_HEIGHT }}
        data-testid="instance-card-identity"
        title={[env, ...freeTags].filter(Boolean).join(' · ')}
      >
        <InstanceIdentityBadges
          type={inst.type}
          role={inst.role}
          env={env}
          freeTags={freeTags}
          showType={showType}
          showRole={showRole}
          showTags={showTags}
          layout="row"
          className="overflow-hidden"
        />
      </div>
      {/* 节点归属与实际连接地址分别展示，避免把节点名误读为 IP。 */}
      <div className="mt-2 min-w-0 shrink-0 space-y-1 text-xs text-muted-foreground">
        <div className="flex min-w-0 items-center gap-1.5" title={nodeName}>
          <Server className="size-3 shrink-0" aria-hidden />
          <span className="truncate">{nodeName}</span>
        </div>
        <div className="flex min-w-0 items-center gap-1.5" title={endpoint} data-testid="instance-card-endpoint">
          <Globe className="size-3 shrink-0" aria-hidden />
          <span className="truncate font-mono text-[11px] tabular-nums">{endpoint}</span>
        </div>
      </div>

      <div className="mt-2 shrink-0 space-y-1.5">
        <ResourceLine icon={<Cpu className="size-3" />} label={t('nodes.cpu')} pct={cpuPct} valueLabel={cpuLabel} />
        <ResourceLine
          icon={<MemoryStick className="size-3" />}
          label={t('nodes.memory')}
          pct={memPct}
          valueLabel={memLabel}
          hint={memMax == null ? t('instances.card.memoryNoLimit', '未设置内存上限，显示实际已用内存') : undefined}
        />
      </div>

      <div className="mt-auto flex min-w-0 shrink-0 items-end gap-2 border-t pt-2">
        <span
          className="flex min-w-0 flex-1 flex-col gap-0.5"
          title={running && !playersAvailable ? t('instances.card.playersUnavailable', '在线人数需要 ServerProbe、SLP 或 Query 上报') : undefined}
        >
          <span className="inline-flex items-center gap-1 text-[10px] text-muted-foreground">
            <Users className="size-3 shrink-0" aria-hidden />
            {t('instances.card.players', '在线人数')}
          </span>
          <span className={cn('truncate text-xs font-semibold tabular-nums', running && !playersAvailable ? 'font-normal text-muted-foreground' : 'text-primary')}>
            {playersLabel}
          </span>
        </span>
        {!isProxy && (
          <span
            className="flex min-w-0 flex-1 flex-col gap-0.5"
            title={running && !tpsAvailable ? t('instances.card.tpsUnavailable', 'TPS 需要 ServerProbe 上报') : undefined}
          >
            <span className="inline-flex items-center gap-1 text-[10px] text-muted-foreground">
              <Zap className="size-3 shrink-0" aria-hidden />
              TPS
            </span>
            <span className="truncate text-xs tabular-nums text-muted-foreground">{tpsLabel}</span>
          </span>
        )}
        <div className="ml-auto flex items-center gap-1" onClick={(e) => e.stopPropagation()}>
          {stopped && (
            // 禁用按钮带 disabled:pointer-events-none，tooltip 由外层 span 承载。
            <span title={provisioning ? t('instances.provisioningBlocked') : undefined}>
              <Button
                variant="ghost"
                size="icon-xs"
                disabled={provisioning || busy}
                onClick={(e) => {
                  // 阻止冒泡：卡片根 onClick 是「打开实例控制台」，动作按钮不该顺带跳页。
                  e.stopPropagation()
                  onStart()
                }}
                aria-label={t('instances.start')}
                title={provisioning ? t('instances.provisioningBlocked') : t('instances.start')}
                className="text-status-success hover:text-status-success"
              >
                <Play className="size-3.5" />
              </Button>
            </span>
          )}
          {running && (
            <>
              <Button
                variant="ghost"
                size="icon-xs"
                disabled={busy}
                onClick={(e) => {
                  e.stopPropagation()
                  onRestart()
                }}
                aria-label={t('instances.restart')}
                title={t('instances.restart')}
                className="text-status-info hover:text-status-info"
              >
                <RotateCw className="size-3.5" />
              </Button>
              <Button
                variant="ghost"
                size="icon-xs"
                disabled={busy}
                onClick={(e) => {
                  e.stopPropagation()
                  onStop()
                }}
                aria-label={t('instances.stop')}
                title={t('instances.stop')}
                className="text-status-warning hover:text-status-warning"
              >
                <Square className="size-3.5" />
              </Button>
            </>
          )}
        </div>
      </div>
    </Card>
  )
}

/** 资源读数先呈现数值；没有分母或未取到数据时不画误导性的 0% 水位。 */
function ResourceLine({
  icon,
  label,
  pct,
  valueLabel,
  hint,
}: {
  icon: ReactNode
  label: string
  pct: number | null
  valueLabel: string
  hint?: string
}) {
  return (
    <div className="min-w-0 space-y-1" title={hint}>
      <div className="flex min-w-0 items-center gap-2 text-[11px] text-muted-foreground">
        <span className="inline-flex shrink-0 items-center gap-1">{icon}{label}</span>
        <span className="ml-auto min-w-0 truncate text-right tabular-nums">{valueLabel}</span>
      </div>
      {pct == null ? <div className="h-1" aria-hidden /> : <MiniBar value={pct} className="h-1 w-full" />}
    </div>
  )
}

function nonnegative(value: number | undefined): number | null {
  return value != null && Number.isFinite(value) && value >= 0 ? value : null
}

/** 后端内存单位是 MiB，显示时不把二进制容量误标为十进制 MB。 */
function fmtMem(mb: number): string {
  if (mb >= 1024) return `${(mb / 1024).toFixed(1)} GiB`
  return `${Math.round(mb)} MiB`
}
