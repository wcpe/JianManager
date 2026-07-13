import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { Cpu, MemoryStick, Users, Zap, Play, Square, RotateCw, Route, Box } from 'lucide-react'
import {
  useStartInstance,
  useStopInstance,
  useRestartInstance,
  type InstanceInfo,
} from '@/api/instances'
import { useInstanceMetrics } from '@/api/metrics'
import { MiniBar } from '@jianmanager/ui/components/mini-bar'
import { Button } from '@jianmanager/ui/components/button'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { instanceStatusLevel, type StatusLevel } from '@jianmanager/ui'
import { toneChipClass, type Tone } from '@/lib/tone'
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
  onOpen,
}: {
  inst: InstanceInfo
  /** 所属节点名（由列表统一解析后传入，避免卡内各自查节点表）。 */
  nodeName: string
  /** 角色徽标元素（统一语义色，由页面渲染）。 */
  roleBadge: React.ReactNode
  /** 「⋯」次要操作菜单元素（标签/限额/克隆/删除，由页面渲染）。 */
  menu: React.ReactNode
  /** 打开实例控制台；默认跳转实例深链，列表页可传入自定义跳转。 */
  onOpen?: (id: number) => void
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const openConsole = onOpen ?? ((id: number) => navigate(`/instances/${id}`))
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
  // 内存条分母：优先 JVM 堆上限（探针），其次容器内存上限（docker，经 heapMaxMb 承载），
  // 再次实例配置限额；均无则为 0（仅以绝对值标签呈现，不画占比）。
  const memMax = running && metrics ? (metrics.heapMaxMb > 0 ? metrics.heapMaxMb : (inst.memLimitMb ?? 0)) : 0
  const memMb = running && metrics ? metrics.memoryMb : 0
  const memPct = memMax > 0 ? Math.min(100, (memMb / memMax) * 100) : 0
  // 标签用绝对值（docker/无探针实例 heapMaxMb 为 0 时，百分比恒 0 看不出占用，故统一显绝对内存）。
  const cpuLabel = running ? `${cpuPct.toFixed(cpuPct > 0 && cpuPct < 10 ? 1 : 0)}%` : '--'
  const memLabel = running && memMb > 0 ? fmtMem(memMb) : '--'

  return (
    <div
      className="group flex cursor-pointer flex-col rounded-xl border bg-card p-4 text-card-foreground shadow-soft transition-[box-shadow] duration-300 ease-ios hover:shadow-lift"
      onClick={() => openConsole(inst.id)}
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
              openConsole(inst.id)
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
          {/* 崩溃原因（FR-#2）：异步委托失败的具体错误，不再只见「崩溃」无因。 */}
          {inst.status === 'CRASHED' && inst.statusReason && (
            <p className="mt-0.5 line-clamp-2 text-[11px] text-status-danger" title={inst.statusReason}>
              {inst.statusReason}
            </p>
          )}
          {/* 搭建中提示（FR-319）：一键搭建异步化后核心下载期间实例为 STOPPED，
              标注「搭建中」让用户知道尚不可启动（后端启动闸也会拦）。 */}
          {inst.status === 'STOPPED' && inst.statusReason?.startsWith('搭建中') && (
            <p className="mt-0.5 line-clamp-2 text-[11px] text-status-warning" title={inst.statusReason}>
              {inst.statusReason}
            </p>
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
        <div className="ml-auto flex items-center gap-1" onClick={(e) => e.stopPropagation()}>
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

/** 卡内单条资源行：图标 + 标签 + MiniBar（停机时空轨 + 「--」）。
 * valueLabel 覆盖右侧数值文案（如内存显绝对值、CPU 小数）；缺省回退百分比。 */
function ResourceLine({
  icon,
  label,
  pct,
  active,
  valueLabel,
}: {
  icon: React.ReactNode
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
