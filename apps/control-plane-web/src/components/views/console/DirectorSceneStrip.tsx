import { useTranslation } from 'react-i18next'
import { Gauge, Trash2, Zap } from 'lucide-react'
import { cn } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'
import { Slider } from '@jianmanager/ui/components/slider'
import { MAX_PREHEAT_LIMIT, MIN_PREHEAT_LIMIT, sceneStatus } from '@/lib/director/director'
import type { DirectorState, SceneStatus } from '@/lib/director/director'

export interface DirectorSceneStripProps {
  /** 场景列表（应用侧 store；此处只需 id 与展示名）。 */
  scenes: { id: string; name: string }[]
  /** 导播状态机快照（预热顺序与并发上限）。 */
  machine: DirectorState
  onActivate: (sceneId: string) => void
  onRemove: (sceneId: string) => void
  onLimitChange: (limit: number) => void
}

/**
 * 导播台缩略图条（FR-168）：一排场景，点击瞬切；显三态（active/预热/cold）+ 序号（快捷键提示）。
 * 右侧并发上限控制：调小即时 LRU 驱逐溢出保活连接（ADR-035）。
 *
 * 三态视觉：active=主色实心 + 闪电（全速）；预热=绿点（WS 保活，瞬切零延迟）；cold=灰点（切换需重连）。
 *
 * 取值/落值由 props 注入（ADR-102）：本视图不触达 store。
 */
export function DirectorSceneStrip({
  scenes,
  machine,
  onActivate,
  onRemove,
  onLimitChange,
}: DirectorSceneStripProps) {
  const { t } = useTranslation()

  const preheatCount = machine.preheatOrder.length

  return (
    <div className="flex shrink-0 items-center gap-2 border-t bg-card/40 px-3 py-2">
      <div className="flex min-w-0 flex-1 items-center gap-1.5 overflow-x-auto scrollbar-none">
        {scenes.map((scene, i) => {
          const status = sceneStatus(machine, scene.id)
          return (
            <SceneThumb
              key={scene.id}
              index={i}
              name={scene.name || t('director.unnamedScene', { n: i + 1 })}
              status={status}
              onActivate={() => onActivate(scene.id)}
              onRemove={() => onRemove(scene.id)}
            />
          )
        })}
      </div>

      {/* 并发上限：保活连接池大小硬约束（含 active）。 */}
      <div
        className="flex shrink-0 items-center gap-1.5 border-l pl-2 text-xs text-muted-foreground"
        title={t('director.preheatLimit')}
      >
        <Gauge className="size-3.5" aria-hidden />
        <span className="tabular-nums">
          {preheatCount}/{machine.limit}
        </span>
        {/*
          并发上限是**离散小整数区间**（1..6），原生 range 在这里尤其难受：
          `accent-primary` 只在 Chromium 生效，其余引擎滑块外观不可控；窄至 w-20 时，
          6 格的可点击精度也不足。Slider 取语义 token（主题切换无需逐套重调），
          拇指带 aria-valuenow，键盘/读屏能拿到「当前上限是几」。
          `w-20`（80px）保持原占位宽度，右侧 '已预热/上限' 读数仍同源。
        */}
        <Slider
          min={MIN_PREHEAT_LIMIT}
          max={MAX_PREHEAT_LIMIT}
          step={1}
          value={[machine.limit]}
          onValueChange={([next]) => {
            // 单拇指且 min/max 已界定，越界只可能来自异常输入；仍走 clamp 保持原语义。
            if (next != null) onLimitChange(next)
          }}
          aria-label={t('director.preheatLimit')}
          title={t('director.preheatLimitHint', { max: MAX_PREHEAT_LIMIT })}
          className="w-20"
        />
      </div>
    </div>
  )
}

/** 单个场景缩略卡：序号 + 名称 + 状态点；点击瞬切；hover 显删除。 */
function SceneThumb({
  index,
  name,
  status,
  onActivate,
  onRemove,
}: {
  index: number
  name: string
  status: SceneStatus
  onActivate: () => void
  onRemove: () => void
}) {
  const { t } = useTranslation()
  const active = status === 'active'

  return (
    <div
      className={cn(
        'group relative flex shrink-0 items-center gap-1.5 rounded-lg border px-2.5 py-1.5 text-xs transition-all',
        active
          ? 'border-primary bg-primary/10 text-primary shadow-soft'
          : 'cursor-pointer border-transparent bg-background text-foreground/80 hover:border-border hover:bg-accent/60',
      )}
      role="button"
      tabIndex={0}
      aria-pressed={active}
      aria-label={t('director.switchTo', { name })}
      onClick={onActivate}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onActivate()
        }
      }}
    >
      {/* 序号（1-9 对应数字键快捷键） */}
      {index < 9 && (
        <kbd
          className={cn(
            'grid size-4 place-items-center rounded text-[10px] font-medium',
            active ? 'bg-primary/20 text-primary' : 'bg-muted text-muted-foreground',
          )}
        >
          {index + 1}
        </kbd>
      )}
      <StatusDot status={status} />
      <span className="max-w-[10rem] truncate font-medium">{name}</span>
      {active && <Zap className="size-3 shrink-0" />}

      {/* 删除（hover 显，不抢点击瞬切）。尺寸 max 16px 且靠 group-hover 显隐，库的按钮盒会撑开卡片 → 保留原样式的透明按钮 */}
      <Button
        type="button"
        variant="invisible"
        size="auto"
        onClick={(e) => {
          e.stopPropagation()
          onRemove()
        }}
        aria-label={t('director.removeScene', { name })}
        title={t('director.removeScene', { name })}
        className="ml-0.5 hidden size-4 shrink-0 place-items-center rounded text-muted-foreground/60 hover:text-destructive group-hover:grid"
      >
        <Trash2 className="size-3" />
      </Button>
    </div>
  )
}

/** 三态指示点：active=主色脉动 / preheated=绿（保活）/ cold=灰（需重连）。 */
function StatusDot({ status }: { status: SceneStatus }) {
  const { t } = useTranslation()
  const cls =
    status === 'active'
      ? 'bg-primary animate-pulse'
      : status === 'preheated'
        ? 'bg-emerald-500'
        : 'bg-muted-foreground/30'
  const label =
    status === 'active'
      ? t('director.statusActive')
      : status === 'preheated'
        ? t('director.statusPreheated')
        : t('director.statusCold')
  return <span className={cn('size-1.5 shrink-0 rounded-full', cls)} title={label} aria-label={label} />
}
