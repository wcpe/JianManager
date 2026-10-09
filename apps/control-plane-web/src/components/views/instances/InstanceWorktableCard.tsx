import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Cpu, MemoryStick, Users, Zap, Play, Square, RotateCw, Route, Box } from 'lucide-react'

import { MiniBar } from '@jianmanager/ui/components/mini-bar'
import { Button } from '@jianmanager/ui/components/button'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { instanceStatusLevel, type StatusLevel } from '@jianmanager/ui'
import { toneChipClass, type Tone } from '@jianmanager/ui/lib/tone'
import { instanceStatusGlowClass } from '@/lib/instances/instance-glow'
import { cn } from '@jianmanager/ui'

/** 实例状态 → 图标块语义色调（与状态徽章同色系，运行=主色块）。 */
function statusTone(status: string): Tone {
  switch (status) {
    case 'RUNNING':
      return 'primary'
    case 'STARTING':
    case 'STOPPING':
      return 'warning'
    case 'CRASHED':
      return 'danger'
    default:
      return 'neutral'
  }
}

/** 实例展示数据（本组件所需的最小结构；外壳传 InstanceInfo 会结构兼容）。 */
export interface InstanceWorktableCardInstanceView {
  id: number
  name: string
  status: string
  /** 类型标识（如 minecraft_java），卡面直接展示。 */
  type: string
  /** 游戏端口；0 表示未分配（不显示端口段）。 */
  serverPort: number
  /** 配置的内存上限（探针与容器上限都取不到时的分母回退）。 */
  memLimitMb?: number
  /** 状态原因（失败原因或搭建中提示，二者共用该字段）。 */
  statusReason?: string
}

/** 实时指标（本组件所需的最小结构）。 */
export interface InstanceWorktableMetricsView {
  cpuPercent?: number
  memoryMb?: number
  /** JVM 堆上限（探针）；> 0 时优先作为内存条分母。 */
  heapMaxMb?: number
  onlinePlayers?: number
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

/**
 * 卡片内「附加提示区」（失败原因 / 搭建中 / 运行态漂移）的最大高度（px）= 35。
 *
 * 两段提示各按一行（16.5）加段间距（2）计满；再多（如失败原因本身折了两行）就从这里裁掉——
 * 卡面只需把事实说清到两行以内，完整文本仍在各段的 `title` 里。
 *
 * 【为什么必须硬封】卡片实高 = 固定骨架 199.5 + 头部超出基础高（40）的部分，而不封顶时该区
 * 可到 4 行（66），卡片实高 267.5 会顶穿列表行盒（`CARD_ROW_HEIGHT` − 行间距 = 244）
 * 与下一行重叠。封到 35 后卡片实高上限 = 234.5，行盒内留有 9.5px 余量——「声明值 = 真实行盒」
 * 由构造保证，而不是靠「现有数据恰好不长」。
 */
export const CARD_EXTRA_MAX_HEIGHT = 35

/**
 * 实例工作台卡（FR-136，§4.5 运行实体范式）。
 * 内嵌资源（CPU/内存条 + 玩家/TPS）+ 呼吸灯（运行时脉动）+ 启停/重启按钮；点名进控制台工作区。
 * 仅运行态拉实时指标（原先在卡内惰性 enable），停机卡不轮询、资源显「--」。
 *
 * 受控视图（ADR-097 b 范式）：不取数、不碰路由——三处「谁来做判断」的边界在此划清：
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
  /** 启动。 */
  onStart: () => void
  /** 停止。 */
  onStop: () => void
  /** 重启。 */
  onRestart: () => void
}

export function InstanceWorktableCard({
  inst,
  nodeName,
  isProxy = false,
  provisioning = false,
  drift,
  metrics,
  starting = false,
  stopping = false,
  restarting = false,
  roleBadge,
  menu,
  onOpen,
  onStart,
  onStop,
  onRestart,
}: InstanceWorktableCardProps) {
  const { t } = useTranslation()

  const running = inst.status === 'RUNNING'
  const stopped = inst.status === 'STOPPED' || inst.status === 'CRASHED'
  const level: StatusLevel = instanceStatusLevel(inst.status)
  const Icon = isProxy ? Route : Box

  const statusLabel = t(`instances.${inst.status.toLowerCase()}`, inst.status)
  const cpuPct = running ? (metrics?.cpuPercent ?? 0) : 0
  // 内存条分母：优先 JVM 堆上限（探针），其次容器内存上限（docker，经 heapMaxMb 承载），
  // 再次实例配置限额；均无则为 0（仅以绝对值标签呈现，不画占比）。
  const memMax = running && metrics ? (metrics.heapMaxMb && metrics.heapMaxMb > 0 ? metrics.heapMaxMb : (inst.memLimitMb ?? 0)) : 0
  const memMb = running && metrics ? (metrics.memoryMb ?? 0) : 0
  const memPct = memMax > 0 ? Math.min(100, (memMb / memMax) * 100) : 0
  // 标签用绝对值（docker/无探针实例 heapMaxMb 为 0 时，百分比恒 0 看不出占用，故统一显绝对内存）。
  const cpuLabel = running ? `${cpuPct.toFixed(cpuPct > 0 && cpuPct < 10 ? 1 : 0)}%` : '--'
  const memLabel = running && memMb > 0 ? fmtMem(memMb) : '--'
  // FR-446/447 可用性位：运行态缺测显示「不可用」，不再把 0 当「0 人在线」/0.0 TPS。
  // 停机态保留「--」（是明确的「不适用」，非缺测）。在线数任一来源（探针/SLP/Query）有值即显值。
  const unavailable = t('metrics.unavailable')
  const playersAvailable = running && (metrics?.playersAvailable ?? false)
  const playersLabel = !running ? '--' : playersAvailable ? String(metrics?.onlinePlayers ?? 0) : unavailable
  const tpsAvailable = running && (metrics?.probeAvailable ?? false)
  const tpsLabel = !running ? '--' : tpsAvailable ? (metrics?.tps ?? 0).toFixed(1) : unavailable

  return (
    <div
      className={cn('group flex cursor-pointer flex-col rounded-xl border bg-card p-4 text-card-foreground shadow-soft transition-[box-shadow] duration-300 ease-ios hover:shadow-lift', instanceStatusGlowClass(inst.status, true))}
      onClick={() => onOpen?.(inst.id)}
    >
      {/* 头部：图标块 + 名称 + 状态（运行呼吸灯）+ 菜单 */}
      <div className="flex items-center gap-3">
        <span className={cn('flex size-10 shrink-0 items-center justify-center rounded-xl', toneChipClass(statusTone(inst.status)))}>
          <Icon className="size-5" />
        </span>
        <div className="min-w-0 flex-1">
          <button
            type="button"
            className="block max-w-full truncate text-left text-sm font-semibold hover:text-primary"
            onClick={(e) => {
              e.stopPropagation()
              onOpen?.(inst.id)
            }}
            title={inst.name}
          >
            {inst.name}
          </button>
          <div className="mt-0.5 flex items-center gap-1.5">
            <StatusBadge
              level={level}
              label={statusLabel}
              pulse={inst.status === 'STARTING' || inst.status === 'STOPPING'}
              className="bg-transparent px-0 py-0"
            />
          </div>
          {/* 附加提示区（失败原因 / 搭建中 / 运行态漂移）——整区高度硬封在 CARD_EXTRA_MAX_HEIGHT
              以内：两段 `line-clamp-2` 同时出现时，不封顶的卡片实高会顶穿行盒、与下一行重叠
              （行高口径见 InstanceCardViews 的 CARD_ROW_HEIGHT 说明）。
              失败原因非空即显、不看 status（FR-312）：Worker 心跳会把 CRASHED 冲回 STOPPED，
              以 CRASHED 为前置条件时原因随之不可见；搭建中的 reason 是进行时状态而非失败（FR-331），
              故只走琥珀行、不落红。运行态漂移（FR-471）只说清事实，接管入口在「⋯」菜单。 */}
          {(inst.statusReason || drift) && (
            <div className="mt-0.5 overflow-hidden" style={{ maxHeight: CARD_EXTRA_MAX_HEIGHT }}>
              {inst.statusReason && (
                <p
                  className={cn('line-clamp-2 text-[11px]', provisioning ? 'text-status-warning' : 'text-status-danger')}
                  title={inst.statusReason}
                >
                  {inst.statusReason}
                </p>
              )}
              {drift && (
                <p className="mt-0.5 line-clamp-2 text-[11px] text-status-warning" title={drift.cmdline}>
                  {t('serverConsole.runtimeDriftDesc', { pid: drift.pid })}
                </p>
              )}
            </div>
          )}
        </div>
        {roleBadge}
        {/* 次要操作菜单（⋯）不冒泡到卡片，避免点菜单误开工作区（FIX-9）。 */}
        <span onClick={(e) => e.stopPropagation()}>{menu}</span>
      </div>

      {/* 类型 · 节点:端口 */}
      <div className="mt-3 truncate text-xs text-muted-foreground" title={`${inst.type} · ${nodeName}`}>
        {inst.type} · {nodeName}
        {inst.serverPort > 0 && <span className="tabular-nums">:{inst.serverPort}</span>}
      </div>

      {/* 内嵌资源条：CPU / 内存（仅运行态有值，否则空轨） */}
      <div className="mt-3 space-y-1.5">
        <ResourceLine icon={<Cpu className="size-3" />} label={t('nodes.cpu')} pct={cpuPct} active={running} valueLabel={cpuLabel} />
        <ResourceLine icon={<MemoryStick className="size-3" />} label={t('nodes.memory')} pct={memPct} active={running} valueLabel={memLabel} />
      </div>

      {/* 玩家 / TPS + 启停按钮 */}
      <div className="mt-3 flex items-center gap-3 border-t pt-3">
        <span className="inline-flex items-center gap-1 text-sm font-semibold text-primary">
          <Users className="size-3.5" />
          <span className={cn('tabular-nums', running && !playersAvailable && 'text-xs font-normal text-muted-foreground')}>
            {playersLabel}
          </span>
        </span>
        {!isProxy && (
          <span className="inline-flex items-center gap-1 text-sm text-muted-foreground">
            <Zap className="size-3.5" />
            <span className={cn('tabular-nums', running && !tpsAvailable && 'text-xs')}>
              {tpsLabel}
            </span>
          </span>
        )}
        <div className="ml-auto flex items-center gap-1" onClick={(e) => e.stopPropagation()}>
          {stopped && (
            // 禁用按钮带 disabled:pointer-events-none，tooltip 由外层 span 承载（FR-331）。
            <span title={provisioning ? t('instances.provisioningBlocked') : undefined}>
              <Button
                variant="ghost"
                size="icon-xs"
                disabled={provisioning || starting}
                onClick={onStart}
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
                disabled={restarting}
                onClick={onRestart}
                aria-label={t('instances.restart')}
                title={t('instances.restart')}
                className="text-status-info hover:text-status-info"
              >
                <RotateCw className="size-3.5" />
              </Button>
              <Button
                variant="ghost"
                size="icon-xs"
                disabled={stopping}
                onClick={onStop}
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
    </div>
  )
}

/** 卡内单条资源行：图标 + 标签 + MiniBar（停机时空轨 + 「--」）。
 * valueLabel 覆盖右侧数值文案（如内存显绝对值、CPU 小数）；缺省回退百分比。 */
function ResourceLine({
  icon,
  label,
  pct,
  active,
  valueLabel,
}: {
  icon: ReactNode
  label: string
  pct: number
  active: boolean
  valueLabel?: string
}) {
  return (
    <div className="flex items-center gap-2">
      <span className="flex w-12 shrink-0 items-center gap-1 text-[10px] text-muted-foreground">
        {icon}
        {label}
      </span>
      <MiniBar value={active ? pct : 0} className="flex-1" />
      <span className="w-12 shrink-0 text-right text-[10px] tabular-nums text-muted-foreground">
        {active ? (valueLabel ?? `${pct.toFixed(0)}%`) : '--'}
      </span>
    </div>
  )
}

/** 内存 MiB → 人类可读（≥1024 显 GB 一位小数，否则整 MB）。 */
function fmtMem(mb: number): string {
  if (mb >= 1024) return `${(mb / 1024).toFixed(1)}G`
  return `${Math.round(mb)}M`
}
