import { useTranslation } from 'react-i18next'
import type { BotLoadThresholds } from '@/api/botLoad'
import { DEFAULT_STRICT_THRESHOLDS } from '@/lib/bot-load/presets'
import { validateThresholds } from '@/lib/bot-load/validation'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'

interface ThresholdEditorProps {
  value: BotLoadThresholds
  onChange: (next: BotLoadThresholds) => void
}

/** 阈值编辑 + 一键恢复严格默认。 */
export default function ThresholdEditor({ value, onChange }: ThresholdEditorProps) {
  const { t } = useTranslation()
  const errors = validateThresholds(value)
  const err = (path: string) => errors.find((e) => e.path === path)?.message

  const set = (key: keyof BotLoadThresholds, n: number) => {
    onChange({ ...value, [key]: n })
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-2">
        <p className="text-sm text-muted-foreground">{t('botsLoad.thresholdHint')}</p>
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => onChange({ ...DEFAULT_STRICT_THRESHOLDS })}
        >
          {t('botsLoad.restoreStrictThresholds')}
        </Button>
      </div>
      <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
        <RateField
          id="minOnlineRate"
          label={t('botsLoad.minOnlineRate')}
          value={value.minOnlineRate}
          error={err('thresholds.minOnlineRate')}
          onChange={(n) => set('minOnlineRate', n)}
        />
        <RateField
          id="minCommandSentRate"
          label={t('botsLoad.minCommandSentRate')}
          value={value.minCommandSentRate}
          error={err('thresholds.minCommandSentRate')}
          onChange={(n) => set('minCommandSentRate', n)}
        />
        <RateField
          id="minScheduleCompletionRate"
          label={t('botsLoad.minScheduleCompletionRate')}
          value={value.minScheduleCompletionRate}
          error={err('thresholds.minScheduleCompletionRate')}
          onChange={(n) => set('minScheduleCompletionRate', n)}
        />
        <RateField
          id="minWorkerHealthRate"
          label={t('botsLoad.minWorkerHealthRate')}
          value={value.minWorkerHealthRate}
          error={err('thresholds.minWorkerHealthRate')}
          onChange={(n) => set('minWorkerHealthRate', n)}
        />
        <RateField
          id="minBarrierArrivalRate"
          label={t('botsLoad.minBarrierArrivalRate')}
          value={value.minBarrierArrivalRate}
          error={err('thresholds.minBarrierArrivalRate')}
          onChange={(n) => set('minBarrierArrivalRate', n)}
        />
        <div className="space-y-1">
          <FieldLabel htmlFor="maxScheduleLagP95Ms">{t('botsLoad.maxScheduleLagP95Ms')}</FieldLabel>
          <Input
            id="maxScheduleLagP95Ms"
            type="number"
            value={value.maxScheduleLagP95Ms}
            onChange={(e) => set('maxScheduleLagP95Ms', Number(e.target.value))}
            aria-invalid={!!err('thresholds.maxScheduleLagP95Ms')}
          />
          <FieldError error={err('thresholds.maxScheduleLagP95Ms')} />
        </div>
        <div className="space-y-1">
          <FieldLabel htmlFor="maxProcessCrashes">{t('botsLoad.maxProcessCrashes')}</FieldLabel>
          <Input
            id="maxProcessCrashes"
            type="number"
            value={value.maxProcessCrashes}
            onChange={(e) => set('maxProcessCrashes', Number(e.target.value))}
            aria-invalid={!!err('thresholds.maxProcessCrashes')}
          />
          <FieldError error={err('thresholds.maxProcessCrashes')} />
        </div>
      </div>
      <p className="text-xs text-muted-foreground">{t('botsLoad.thresholdDisclaimer')}</p>
    </div>
  )
}

function RateField({
  id,
  label,
  value,
  onChange,
  error,
}: {
  id: string
  label: string
  value: number
  onChange: (n: number) => void
  error?: string
}) {
  return (
    <div className="space-y-1">
      <FieldLabel htmlFor={id}>{label}</FieldLabel>
      <Input
        id={id}
        type="number"
        step="0.01"
        min={0}
        max={1}
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
        aria-invalid={!!error}
      />
      <FieldError error={error} />
    </div>
  )
}
