import { useTranslation } from 'react-i18next'
import { Badge } from '@jianmanager/ui/components/badge'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import { cn } from '@jianmanager/ui'
import { formatFileSize } from '@/lib/shared/format-file-size'

/** 实例行：概览右栏与「实例」页签共用同一套字段。 */
export interface NodeInstanceListItem {
  id: number
  name: string
  status: string
  role: string
  /** 游戏服监听端口（0/缺省=未分配）。 */
  serverPort?: number
  /** 仅环境标签（`env:xxx`）：一行里最该看到的那个标签。 */
  envTags?: string[]
  /** 运行态漂移：面板显示已停止，但工作目录下还有未纳管的活进程。 */
  drift?: boolean
  /** 状态原因（多为 CRASHED 的失败原因），有就显示在行尾。 */
  statusReason?: string
}

/** 各实例实时用量（按实例 id）：CPU 百分比与堆内存字节。 */
export type NodeInstanceUsage = Record<number, { cpuPct?: number; heapBytes?: number }>

export interface NodeInstanceListProps {
  /** 该节点实例总数（聚合值，与 items 的长度无关）。 */
  total: number
  /** 按状态计数（来自 `/instances/aggregate?nodeId=`）。 */
  byStatus: Record<string, number>
  items: NodeInstanceListItem[]
  usage?: NodeInstanceUsage
  isLoading?: boolean
  isError?: boolean
  /** 当前状态筛选（null/缺省=全部）。 */
  activeStatus?: string | null
  onSelectStatus?: (status: string | null) => void
  /** 点某台实例 → 打开它的实例详情页。 */
  onOpenInstance?: (id: number) => void
  /** 条数上限提示：shown < total 时说明「只显示前 N 台」。 */
  cappedHint?: { shown: number; total: number }
  /** 行 testid 前缀，便于两处分别断言（概览 / 实例页签）。 */
  testIdPrefix?: string
}

/** 实例状态 → 文案键（与实例页同源，避免两处各写一套中文）。 */
const INSTANCE_STATUS_KEY: Record<string, string> = {
  RUNNING: 'instances.running',
  STARTING: 'instances.starting',
  STOPPING: 'instances.stopping',
  CRASHED: 'instances.crashed',
  STOPPED: 'instances.stopped',
}

/** 状态点配色：异常红、运行绿、过渡态黄、停止灰。 */
const INSTANCE_STATUS_TONE: Record<string, string> = {
  RUNNING: 'bg-status-success',
  STARTING: 'bg-status-warning',
  STOPPING: 'bg-status-warning',
  CRASHED: 'bg-destructive',
  STOPPED: 'bg-muted-foreground/40',
}

/** 状态筛选 chips 的固定顺序（运行 → 过渡 → 异常 → 停止）。 */
const STATUS_ORDER = ['RUNNING', 'STARTING', 'STOPPING', 'CRASHED', 'STOPPED'] as const

/**
 * 实例清单（概览右栏与「实例」页签共用）。
 *
 * 只读汇总：状态筛选 + 两行式行内容（名称/CPU · 状态/角色/端口/堆/env/崩溃原因），
 * 行本身可点进实例详情。所有写操作都不在这里——概览与页签里「随手能改」会误导操作者。
 */
export function NodeInstanceList({
  total,
  byStatus,
  items,
  usage,
  isLoading,
  isError,
  activeStatus,
  onSelectStatus,
  onOpenInstance,
  cappedHint,
  testIdPrefix = 'node-instance',
}: NodeInstanceListProps) {
  const { t } = useTranslation()
  // 零计数的状态不占位：一排「0」只会稀释真正要看的那几个。
  const counts = STATUS_ORDER.map((status) => ({ status, count: byStatus[status] ?? 0 })).filter((x) => x.count > 0)

  return (
    <>
      {/*
        状态汇总同时是筛选器：光看计数没法知道「是哪几台在崩溃」，
        点一下就把列表收到那个状态（服务端按 status 过滤，不受条数上限影响）。
      */}
      {counts.length > 0 && (
        <div className="flex flex-wrap gap-1 border-b px-2 py-1.5" data-testid={`${testIdPrefix}-status-chips`}>
          <button
            type="button"
            onClick={() => onSelectStatus?.(null)}
            data-active={!activeStatus || undefined}
            className={cn(
              'flex items-center gap-1 rounded-full border px-1.5 py-[1px] text-[11px] transition-colors',
              !activeStatus
                ? 'border-primary/40 bg-primary/10 text-primary'
                : 'border-transparent text-muted-foreground hover:bg-muted/60',
            )}
          >
            {t('nodes.instanceStatusAll')}
            <span className="tabular-nums font-medium">{total}</span>
          </button>
          {counts.map(({ status, count }) => (
            <button
              key={status}
              type="button"
              onClick={() => onSelectStatus?.(status)}
              data-active={activeStatus === status || undefined}
              className={cn(
                'flex items-center gap-1 rounded-full border px-1.5 py-[1px] text-[11px] transition-colors',
                activeStatus === status
                  ? 'border-primary/40 bg-primary/10 text-primary'
                  : 'border-transparent text-muted-foreground hover:bg-muted/60',
              )}
            >
              <span className={cn('size-1.5 rounded-full', INSTANCE_STATUS_TONE[status] ?? 'bg-muted-foreground/40')} aria-hidden />
              {t(INSTANCE_STATUS_KEY[status] ?? 'instances.stopped')}
              <span className="tabular-nums font-medium">{count}</span>
            </button>
          ))}
        </div>
      )}

      <div className="min-h-0 flex-1 overflow-auto px-1 py-1">
        {isError ? (
          <p role="alert" className="px-2 py-4 text-xs text-destructive">
            {t('common.error')}
          </p>
        ) : isLoading ? (
          <div className="space-y-1.5 px-2 py-2">
            <Skeleton className="h-5 w-full" />
            <Skeleton className="h-5 w-full" />
            <Skeleton className="h-5 w-full" />
          </div>
        ) : items.length === 0 ? (
          <p className="px-2 py-4 text-xs text-muted-foreground">
            {activeStatus ? t('nodes.instanceStatusEmpty') : t('nodes.overviewInstancesEmpty')}
          </p>
        ) : (
          <ul className="divide-y">
            {items.map((it) => {
              const cpu = usage?.[it.id]?.cpuPct
              const heap = usage?.[it.id]?.heapBytes
              return (
                /*
                  整行可点：看到某台不对劲，下一步必然是打开它的实例页。
                  用 button 而非 div+onClick——键盘可聚焦、读屏会念「按钮」。
                */
                <li key={it.id} data-testid={`${testIdPrefix}-row`}>
                  <button
                    type="button"
                    onClick={() => onOpenInstance?.(it.id)}
                    className="w-full px-2 py-[6px] text-left text-xs transition-colors hover:bg-muted/50 focus-visible:bg-muted/50 focus-visible:outline-none"
                  >
                    {/* 第一行：名字是主语，CPU 是「它此刻有多忙」，同一行便于纵向扫读。 */}
                    <div className="flex items-center gap-2">
                      <span
                        className={cn('size-1.5 shrink-0 rounded-full', INSTANCE_STATUS_TONE[it.status] ?? 'bg-muted-foreground/40')}
                        aria-hidden
                      />
                      <span className="min-w-0 flex-1 truncate font-medium text-foreground" title={it.name}>
                        {it.name}
                      </span>
                      {it.drift && (
                        <Badge
                          variant="outline"
                          className="h-4 shrink-0 border-status-warning/50 px-1 text-[10px] text-status-warning"
                          title={t('nodes.instanceDriftHint')}
                        >
                          {t('nodes.instanceDrift')}
                        </Badge>
                      )}
                      <span className="shrink-0 tabular-nums text-[11px] text-muted-foreground">
                        {cpu == null ? '--' : `${Math.round(cpu)}%`}
                      </span>
                    </div>
                    {/* 第二行：状态 · 角色 · 端口 · 堆 · 环境标签——弱化处理，需要时才有信息量。 */}
                    <div className="mt-0.5 flex items-center gap-1.5 pl-3.5 text-[11px] text-muted-foreground">
                      <span className="shrink-0">{t(INSTANCE_STATUS_KEY[it.status] ?? 'instances.stopped')}</span>
                      {it.role && it.role !== 'backend' && (
                        <>
                          <span aria-hidden>·</span>
                          <span className="shrink-0">{t(`networks.role_${it.role}`, { defaultValue: it.role })}</span>
                        </>
                      )}
                      {!!it.serverPort && (
                        <>
                          <span aria-hidden>·</span>
                          <span className="shrink-0 tabular-nums">{it.serverPort}</span>
                        </>
                      )}
                      {heap != null && (
                        <>
                          <span aria-hidden>·</span>
                          <span className="shrink-0 tabular-nums">
                            {t('nodes.instanceHeapShort')} {formatFileSize(heap)}
                          </span>
                        </>
                      )}
                      {it.envTags && it.envTags.length > 0 && (
                        <>
                          <span aria-hidden>·</span>
                          <span className="min-w-0 truncate">{it.envTags.join(' ')}</span>
                        </>
                      )}
                    </div>
                    {/* 第三行只在有原因时出现：CRASHED 的失败原因是排障的第一手信息。 */}
                    {it.statusReason && (
                      <div className="mt-0.5 truncate pl-3.5 text-[10px] text-destructive/80" title={it.statusReason}>
                        {it.statusReason}
                      </div>
                    )}
                  </button>
                </li>
              )
            })}
          </ul>
        )}
        {!isLoading && !isError && cappedHint && cappedHint.shown < cappedHint.total && (
          <p className="px-2 py-1.5 text-[10px] text-muted-foreground/70">
            {t('nodes.instanceListCapped', { shown: cappedHint.shown, total: cappedHint.total })}
          </p>
        )}
      </div>
    </>
  )
}
