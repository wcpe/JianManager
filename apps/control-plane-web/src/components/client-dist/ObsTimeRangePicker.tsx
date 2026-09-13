import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { CalendarRange, Check, ChevronsDownUp } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import { Input } from '@jianmanager/ui/components/input'
import { Label } from '@jianmanager/ui/components/label'
import {
  OBS_PRESETS, obsWindowLabel, presetLabel, toLocalInput,
  type ObsWindow,
} from './obs-window'

/**
 * 分发三页统一时间筛选（FR-425）：预设档 + 任意起止日期时间。
 * 后端 `/client-dist/observability` 等端点的 parseObsRange 原生支持 from/to（RFC3339），本组件纯前端。
 * 值形态与 obs-window.ts 的 ObsWindow 对齐（clientStats/clientDistObservability 的窗口类型结构相同）。
 */
interface ObsTimeRangePickerProps {
  value: ObsWindow
  onChange: (w: ObsWindow) => void
  /** 紧凑模式（页面头部用）。 */
  size?: 'sm' | 'default'
}

export function ObsTimeRangePicker({ value, onChange, size = 'sm' }: ObsTimeRangePickerProps) {
  const { t } = useTranslation()
  const [customOpen, setCustomOpen] = useState(false)
  // 草稿只在打开「自定义」对话框的事件回调里初始化（渲染期不调 Date.now，react-hooks/purity）。
  const [draftFrom, setDraftFrom] = useState<string | null>(null)
  const [draftTo, setDraftTo] = useState<string | null>(null)

  const openCustom = () => {
    const baseFrom = 'from' in value ? value.from : new Date(Date.now() - 7 * 86_400_000).toISOString()
    const baseTo = 'to' in value ? value.to : new Date().toISOString()
    setDraftFrom(toLocalInput(baseFrom))
    setDraftTo(toLocalInput(baseTo))
    setCustomOpen(true)
  }

  const applyCustom = () => {
    if (!draftFrom || !draftTo) return
    const f = new Date(draftFrom)
    const tt = new Date(draftTo)
    if (Number.isNaN(f.getTime()) || Number.isNaN(tt.getTime()) || tt <= f) return
    onChange({ from: f.toISOString(), to: tt.toISOString() })
    setCustomOpen(false)
  }

  const invalid = !!draftFrom && !!draftTo && new Date(draftTo) <= new Date(draftFrom)

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="outline" size={size} className="gap-1.5" data-testid="obs-time-range">
            <CalendarRange className="size-3.5" />
            <span className="max-w-64 truncate">{obsWindowLabel(value, t)}</span>
            <ChevronsDownUp className="size-3 opacity-50" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-44">
          <DropdownMenuLabel>{t('clientDistObs.presetTitle', '时间范围')}</DropdownMenuLabel>
          {OBS_PRESETS.map((p) => (
            <DropdownMenuItem key={p} onSelect={() => onChange({ range: p })}>
              <span className="flex-1">{presetLabel(p, t)}</span>
              {'range' in value && value.range === p && <Check className="size-3.5" />}
            </DropdownMenuItem>
          ))}
          <DropdownMenuItem onSelect={openCustom}>
            <span className="flex-1">{t('clientDistObs.customRange', '自定义时间段')}</span>
            {'from' in value && <Check className="size-3.5" />}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <Dialog open={customOpen} onOpenChange={setCustomOpen}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>{t('clientDistObs.customRangeTitle', '自定义时间范围')}</DialogTitle>
          </DialogHeader>
          <div className="grid gap-3">
            <div className="grid gap-1.5">
              <Label htmlFor="obs-from">{t('clientDistObs.from', '开始时间')}</Label>
              <Input
                id="obs-from"
                type="datetime-local"
                value={draftFrom ?? ''}
                onChange={(e) => setDraftFrom(e.target.value)}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="obs-to">{t('clientDistObs.to', '结束时间')}</Label>
              <Input
                id="obs-to"
                type="datetime-local"
                value={draftTo ?? ''}
                onChange={(e) => setDraftTo(e.target.value)}
              />
            </div>
            {invalid && (
              <p className="text-xs text-destructive">{t('clientDistObs.rangeInvalid', '结束时间必须晚于开始时间')}</p>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" size="sm" onClick={() => setCustomOpen(false)}>
              {t('common.cancel', '取消')}
            </Button>
            <Button size="sm" disabled={invalid} onClick={applyCustom}>
              {t('clientDistObs.apply', '应用')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

export default ObsTimeRangePicker
