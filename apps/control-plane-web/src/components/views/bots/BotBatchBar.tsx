import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@jianmanager/ui/components/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import DangerConfirm from '@/components/views/DangerConfirm'
import { BehaviorConfigDialog } from '@/components/views/bots/BotListParts'
import { groupFilter, type GroupByDim, type OverviewFilter } from '@jianmanager/ui/lib/bots-overview'
import type { BotBatchAction, BotSummaryGroup } from '@jianmanager/ui/lib/bot'

/** 批量下发请求体（逐组调用时由本组件构造）。 */
export interface BotBatchBody {
  action: BotBatchAction
  filter: ReturnType<typeof groupFilter>
  behavior?: string
  target?: string
}

/** 需目标参数的行为（跟随 / 巡逻）：选到它们时旁置「配置」入口。 */
const BEHAVIOR_NEEDS_TARGET = new Set(['follow', 'patrol'])

/** 批量条可下发的行为集合（与后端 behavior 枚举对齐）。 */
const BEHAVIOR_OPTIONS = ['idle', 'guard', 'follow', 'patrol'] as const

export interface BotBatchBarProps {
  groupBy: GroupByDim
  groups: BotSummaryGroup[]
  baseFilter: OverviewFilter
  onClear: () => void
  /** 逐组下发批量（容器注入 mutation）；返回该组的成功/失败计数。 */
  onBatch: (body: BotBatchBody) => Promise<{ succeeded: number; failed: number }>
  /** 批量进行中（容器注入 mutation pending）。 */
  batchPending?: boolean
  /** 危险操作（批量删除）是否放行：应用侧读角色等级后注入（组件库不持鉴权状态）。 */
  dangerAllowed?: boolean
  /** 结果回执（容器注入 toast；组件库不依赖 toast 实现）。 */
  onNotify?: (level: 'success' | 'error', message: string) => void
}

/**
 * 顶部批量条：对已勾选的多个分组逐组下发批量（每组一次调用，聚合结果）。
 * 删除前 DangerConfirm 二次确认 + 串行进度提示（FR-147）；选 follow/patrol 行为时旁置「配置」入口
 * 暴露目标参数（跟随目标 / 巡逻路径），随 set-behavior 一并下发。
 */
export function BotBatchBar({
  groupBy,
  groups,
  baseFilter,
  onClear,
  onBatch,
  batchPending = false,
  dangerAllowed,
  onNotify,
}: BotBatchBarProps) {
  const { t } = useTranslation()
  const [behavior, setBehavior] = useState<string>('')
  const [target, setTarget] = useState<string>('')
  const [showConfig, setShowConfig] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  // 串行进度：已处理组数 / 总组数（null=未在进行）。
  const [progress, setProgress] = useState<{ done: number; total: number } | null>(null)

  const totalSelected = groups.reduce((sum, g) => sum + g.total, 0)
  const needsTarget = BEHAVIOR_NEEDS_TARGET.has(behavior)

  // 逐组下发同一动作（后端批量按单一 filter 收敛，多组需多次调用），聚合成功/失败计数，逐组报进度
  const runAll = async (action: BotBatchAction, beh?: string, tgt?: string) => {
    let succeeded = 0
    let failed = 0
    setProgress({ done: 0, total: groups.length })
    for (let i = 0; i < groups.length; i++) {
      const g = groups[i]
      try {
        const res = await onBatch({
          action,
          filter: groupFilter(groupBy, g, baseFilter),
          behavior: beh,
          target: tgt,
        })
        succeeded += res.succeeded
        failed += res.failed
      } catch {
        failed += g.total
      }
      setProgress({ done: i + 1, total: groups.length })
    }
    setProgress(null)
    onNotify?.('success', t('bots.batchDone', { succeeded, failed }))
    onClear()
  }

  const busy = batchPending || progress !== null

  return (
    <div className="flex flex-wrap items-center gap-2 rounded-lg border bg-muted/40 p-2">
      <span className="text-sm font-medium">
        {t('bots.selectedGroups', { groups: groups.length, bots: totalSelected })}
      </span>
      {progress && (
        <span className="text-xs text-muted-foreground tabular-nums">
          {t('bots.batchProgress', { done: progress.done, total: progress.total })}
        </span>
      )}
      <div className="ml-auto flex items-center gap-2">
        <Select
          value={behavior}
          onValueChange={(v) => {
            setBehavior(v)
            // 切到不需目标的行为时清空已填目标，避免误带。
            if (!BEHAVIOR_NEEDS_TARGET.has(v)) setTarget('')
          }}
        >
          <SelectTrigger size="sm" className="w-32">
            <SelectValue placeholder={t('bots.setBehavior')} />
          </SelectTrigger>
          <SelectContent>
            {BEHAVIOR_OPTIONS.map((b) => (
              <SelectItem key={b} value={b}>
                {t(`bots.${b}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {needsTarget && (
          <Button size="sm" variant="outline" onClick={() => setShowConfig(true)}>
            {t('bots.behaviorConfig')}
            {target && <span className="ml-1 max-w-24 truncate text-xs text-muted-foreground">· {target}</span>}
          </Button>
        )}
        <Button
          size="sm"
          variant="outline"
          disabled={!behavior || busy || (needsTarget && !target)}
          title={needsTarget && !target ? t('bots.behaviorTargetRequired') : undefined}
          onClick={() => runAll('set-behavior', behavior, target || undefined)}
        >
          {t('bots.apply')}
        </Button>
        <Button size="sm" variant="outline" disabled={busy} onClick={() => runAll('stop')}>
          {t('bots.batchStop')}
        </Button>
        <Button
          size="sm"
          variant="destructive"
          disabled={busy}
          onClick={() => setConfirmDelete(true)}
        >
          {t('bots.batchDelete')}
        </Button>
        <Button size="sm" variant="ghost" disabled={busy} onClick={onClear}>
          {t('common.cancel')}
        </Button>
      </div>

      {/* 行为目标配置（跟随目标玩家名 / 巡逻路径点），随 set-behavior 一并下发 */}
      <BehaviorConfigDialog
        open={showConfig}
        behavior={behavior}
        target={target}
        onApply={(v) => {
          setTarget(v)
          setShowConfig(false)
        }}
        onClose={() => setShowConfig(false)}
      />

      {/* 多组批量删除二次确认（FR-147） */}
      <DangerConfirm
        open={confirmDelete}
        title={t('bots.batchDeleteTitle')}
        description={t('bots.batchDeleteConfirm', { count: totalSelected })}
        confirmLabel={t('bots.batchDelete')}
        scope="group"
        allowed={dangerAllowed}
        onConfirm={() => {
          setConfirmDelete(false)
          runAll('delete')
        }}
        onCancel={() => setConfirmDelete(false)}
      />
    </div>
  )
}
