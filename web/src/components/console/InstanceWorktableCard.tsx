import { useTranslation } from 'react-i18next'
import { Cpu, MemoryStick, Users, Zap, Play, Square, RotateCw, Route, Box } from 'lucide-react'
import {
  useStartInstance,
  useStopInstance,
  useRestartInstance,
  type InstanceInfo,
} from '@/api/instances'
import { useInstanceMetrics } from '@/api/metrics'
import { useConsoleStore } from '@/stores/console'
import { MiniBar } from '@/components/ui/mini-bar'
import { Button } from '@/components/ui/button'
import { StatusBadge } from '@/components/ui/status-badge'
import { instanceStatusLevel, type StatusLevel } from '@/lib/threshold'
import { toneChipClass, type Tone } from '@/lib/tone'
import { cn } from '@/lib/utils'

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

/**
 * 实例工作台卡（FR-136，§4.5 运行实体范式）。
 * 内嵌资源（CPU/内存条 + 玩家/TPS）+ 呼吸灯（运行时脉动）+ 启停/重启按钮；点名进控制台工作区。
 * 仅运行态拉实时指标（useInstanceMetrics 惰性 enable），停机卡不轮询、资源显「--」。
 */
export function InstanceWorktableCard({
  inst,
  nodeName,
  roleBadge,
  menu,
}: {
  inst: InstanceInfo
  /** 所属节点名（由列表统一解析后传入，避免卡内各自查节点表）。 */
  nodeName: string
  /** 角色徽标元素（统一语义色，由页面渲染）。 */
  roleBadge: React.ReactNode
  /** 「⋯」次要操作菜单元素（标签/限额/克隆/删除，由页面渲染）。 */
  menu: React.ReactNode
}) {
  const { t } = useTranslation()
  const openInstance = useConsoleStore((s) => s.openInstance)
  const start = useStartInstance()
  const stop = useStopInstance()
  const restart = useRestartInstance()

  const running = inst.status === 'RUNNING'
  const stopped = inst.status === 'STOPPED' || inst.status === 'CRASHED'
  // 仅运行态拉实时指标；停机/过渡态不轮询（省请求，避免离线 422）。
  const { data: metrics } = useInstanceMetrics(inst.id, running)
  const level: StatusLevel = instanceStatusLevel(inst.status)
  const isProxy = inst.role === 'proxy'
  const Icon = isProxy ? Route : Box

  const statusLabel = t(`instances.${inst.status.toLowerCase()}`, inst.status)
  const cpuPct = running ? (metrics?.cpuPercent ?? 0) : 0
  const memPct =
    running && metrics && metrics.heapMaxMb > 0
      ? Math.min(100, (metrics.memoryMb / metrics.heapMaxMb) * 100)
      : 0

  return (
    <div className="group flex flex-col rounded-xl border bg-card p-4 text-card-foreground shadow-soft transition-[box-shadow] duration-300 ease-ios hover:shadow-lift">
      {/* 头部：图标块 + 名称 + 状态（运行呼吸灯）+ 菜单 */}
      <div className="flex items-center gap-3">
        <span className={cn('flex size-10 shrink-0 items-center justify-center rounded-xl', toneChipClass(statusTone(inst.status)))}>
          <Icon className="size-5" />
        </span>
        <div className="min-w-0 flex-1">
          <button
            type="button"
            className="block max-w-full truncate text-left text-sm font-semibold hover:text-primary"
            onClick={() => openInstance(inst.id)}
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
        </div>
        {roleBadge}
        {menu}
      </div>

      {/* 类型 · 节点:端口 */}
      <div className="mt-3 truncate text-xs text-muted-foreground" title={`${inst.type} · ${nodeName}`}>
        {inst.type} · {nodeName}
        {inst.serverPort > 0 && <span className="tabular-nums">:{inst.serverPort}</span>}
      </div>

      {/* 内嵌资源条：CPU / 内存（仅运行态有值，否则空轨） */}
      <div className="mt-3 space-y-1.5">
        <ResourceLine icon={<Cpu className="size-3" />} label={t('nodes.cpu')} pct={cpuPct} active={running} />
        <ResourceLine icon={<MemoryStick className="size-3" />} label={t('nodes.memory')} pct={memPct} active={running} />
      </div>

      {/* 玩家 / TPS + 启停按钮 */}
      <div className="mt-3 flex items-center gap-3 border-t pt-3">
        <span className="inline-flex items-center gap-1 text-sm font-semibold text-primary">
          <Users className="size-3.5" />
          <span className="tabular-nums">{running && metrics ? metrics.onlinePlayers : '--'}</span>
        </span>
        {!isProxy && (
          <span className="inline-flex items-center gap-1 text-sm text-muted-foreground">
            <Zap className="size-3.5" />
            <span className="tabular-nums">
              {running && metrics?.probeAvailable ? metrics.tps.toFixed(1) : '--'}
            </span>
          </span>
        )}
        <div className="ml-auto flex items-center gap-1">
          {stopped && (
            <Button
              variant="ghost"
              size="icon-xs"
              disabled={start.isPending && start.variables === inst.id}
              onClick={() => start.mutate(inst.id)}
              aria-label={t('instances.start')}
              title={t('instances.start')}
              className="text-status-success hover:text-status-success"
            >
              <Play className="size-3.5" />
            </Button>
          )}
          {running && (
            <>
              <Button
                variant="ghost"
                size="icon-xs"
                disabled={restart.isPending && restart.variables === inst.id}
                onClick={() => restart.mutate(inst.id)}
                aria-label={t('instances.restart')}
                title={t('instances.restart')}
                className="text-status-info hover:text-status-info"
              >
                <RotateCw className="size-3.5" />
              </Button>
              <Button
                variant="ghost"
                size="icon-xs"
                disabled={stop.isPending && stop.variables === inst.id}
                onClick={() => stop.mutate(inst.id)}
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

/** 卡内单条资源行：图标 + 标签 + MiniBar（停机时空轨 + 「--」）。 */
function ResourceLine({
  icon,
  label,
  pct,
  active,
}: {
  icon: React.ReactNode
  label: string
  pct: number
  active: boolean
}) {
  return (
    <div className="flex items-center gap-2">
      <span className="flex w-12 shrink-0 items-center gap-1 text-[10px] text-muted-foreground">
        {icon}
        {label}
      </span>
      <MiniBar value={active ? pct : 0} className="flex-1" />
      <span className="w-9 shrink-0 text-right text-[10px] tabular-nums text-muted-foreground">
        {active ? `${pct.toFixed(0)}%` : '--'}
      </span>
    </div>
  )
}
