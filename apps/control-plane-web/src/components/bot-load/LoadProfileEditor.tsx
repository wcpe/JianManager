import { useTranslation } from 'react-i18next'
import { Plus, Trash2 } from 'lucide-react'
import type { BotLoadProfile } from '@/api/botLoad'
import { estimateProfileDurationSeconds, targetBotsFromProfile } from '@/lib/bot-load/presets'
import { validateLoadProfile } from '@/lib/bot-load/validation'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { Checkbox } from '@jianmanager/ui/components/checkbox'

interface LoadProfileEditorProps {
  value: BotLoadProfile
  onChange: (next: BotLoadProfile) => void
}

/** 负载曲线编辑：stable / step / spike。 */
export default function LoadProfileEditor({ value, onChange }: LoadProfileEditorProps) {
  const { t } = useTranslation()
  const errors = validateLoadProfile(value)
  const err = (path: string) => errors.find((e) => e.path === path)?.message
  const target = targetBotsFromProfile(value)
  const duration = estimateProfileDurationSeconds(value)

  const setType = (type: BotLoadProfile['type']) => {
    if (type === 'stable') {
      onChange({ type: 'stable', targetBots: target || 50, rampUpSeconds: 30, durationSeconds: 300 })
    } else if (type === 'step') {
      onChange({
        type: 'step',
        stages: [
          { targetBots: 20, holdSeconds: 60 },
          { targetBots: 50, holdSeconds: 120 },
        ],
        stopOnThresholdFailure: true,
      })
    } else {
      onChange({
        type: 'spike',
        targetBots: target || 100,
        connectWindowSeconds: 30,
        holdSeconds: 120,
      })
    }
  }

  return (
    <div className="space-y-4">
      <div className="space-y-1">
        <FieldLabel>{t('botsLoad.profileType')}</FieldLabel>
        <Select value={value.type} onValueChange={(v) => setType(v as BotLoadProfile['type'])}>
          <SelectTrigger className="w-48">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="stable">{t('botsLoad.profileStable')}</SelectItem>
            <SelectItem value="step">{t('botsLoad.profileStep')}</SelectItem>
            <SelectItem value="spike">{t('botsLoad.profileSpike')}</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <div className="rounded-lg border bg-muted/30 px-3 py-2 text-sm" aria-live="polite">
        {t('botsLoad.profileSummary', { target, duration })}
      </div>

      {value.type === 'stable' && (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
          <NumField
            id="stable-target"
            label={t('botsLoad.targetBots')}
            value={value.targetBots}
            error={err('loadProfile.targetBots')}
            onChange={(n) => onChange({ ...value, targetBots: n })}
          />
          <NumField
            id="stable-ramp"
            label={t('botsLoad.rampUpSeconds')}
            value={value.rampUpSeconds}
            error={err('loadProfile.rampUpSeconds')}
            onChange={(n) => onChange({ ...value, rampUpSeconds: n })}
          />
          <NumField
            id="stable-dur"
            label={t('botsLoad.durationSeconds')}
            value={value.durationSeconds}
            error={err('loadProfile.durationSeconds')}
            onChange={(n) => onChange({ ...value, durationSeconds: n })}
          />
        </div>
      )}

      {value.type === 'step' && (
        <div className="space-y-3">
          <label className="flex items-center gap-2 text-sm">
            <Checkbox
              checked={value.stopOnThresholdFailure}
              onCheckedChange={(c) => onChange({ ...value, stopOnThresholdFailure: !!c })}
            />
            {t('botsLoad.stopOnThresholdFailure')}
          </label>
          {value.stages.map((stage, i) => (
            <div key={i} className="flex flex-wrap items-end gap-2 rounded-md border p-3">
              <NumField
                id={`step-t-${i}`}
                label={t('botsLoad.targetBots')}
                value={stage.targetBots}
                error={err(`loadProfile.stages[${i}].targetBots`)}
                onChange={(n) => {
                  const stages = value.stages.map((s, j) => (j === i ? { ...s, targetBots: n } : s))
                  onChange({ ...value, stages })
                }}
              />
              <NumField
                id={`step-h-${i}`}
                label={t('botsLoad.holdSeconds')}
                value={stage.holdSeconds}
                error={err(`loadProfile.stages[${i}].holdSeconds`)}
                onChange={(n) => {
                  const stages = value.stages.map((s, j) => (j === i ? { ...s, holdSeconds: n } : s))
                  onChange({ ...value, stages })
                }}
              />
              <Button
                type="button"
                size="xs"
                variant="ghost"
                disabled={value.stages.length <= 1}
                onClick={() => onChange({ ...value, stages: value.stages.filter((_, j) => j !== i) })}
                aria-label={t('common.delete')}
              >
                <Trash2 className="size-3.5" />
              </Button>
            </div>
          ))}
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => {
              const last = value.stages[value.stages.length - 1]
              onChange({
                ...value,
                stages: [...value.stages, { targetBots: (last?.targetBots ?? 10) + 10, holdSeconds: 60 }],
              })
            }}
          >
            <Plus className="size-4" /> {t('botsLoad.addStage')}
          </Button>
        </div>
      )}

      {value.type === 'spike' && (
        <div className="space-y-3">
          <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
            <NumField
              id="spike-target"
              label={t('botsLoad.targetBots')}
              value={value.targetBots}
              error={err('loadProfile.targetBots')}
              onChange={(n) => onChange({ ...value, targetBots: n })}
            />
            <NumField
              id="spike-win"
              label={t('botsLoad.connectWindowSeconds')}
              value={value.connectWindowSeconds}
              error={err('loadProfile.connectWindowSeconds')}
              onChange={(n) => onChange({ ...value, connectWindowSeconds: n })}
            />
            <NumField
              id="spike-hold"
              label={t('botsLoad.holdSeconds')}
              value={value.holdSeconds}
              error={err('loadProfile.holdSeconds')}
              onChange={(n) => onChange({ ...value, holdSeconds: n })}
            />
          </div>
          <label className="flex items-center gap-2 text-sm">
            <Checkbox
              checked={!!value.barrier}
              onCheckedChange={(c) =>
                onChange(
                  c
                    ? { ...value, barrier: { key: 'wave-1', releaseWindowMs: 5000 } }
                    : { ...value, barrier: undefined },
                )
              }
            />
            {t('botsLoad.enableBarrier')}
          </label>
          {value.barrier && (
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1">
                <FieldLabel htmlFor="barrier-key">{t('botsLoad.barrierKey')}</FieldLabel>
                <Input
                  id="barrier-key"
                  value={value.barrier.key}
                  onChange={(e) =>
                    onChange({ ...value, barrier: { ...value.barrier!, key: e.target.value } })
                  }
                />
                <FieldError error={err('loadProfile.barrier.key')} />
              </div>
              <NumField
                id="barrier-rw"
                label={t('botsLoad.releaseWindowMs')}
                value={value.barrier.releaseWindowMs}
                error={err('loadProfile.barrier.releaseWindowMs')}
                onChange={(n) =>
                  onChange({ ...value, barrier: { ...value.barrier!, releaseWindowMs: n } })
                }
              />
            </div>
          )}
        </div>
      )}
    </div>
  )
}

function NumField({
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
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
        aria-invalid={!!error}
      />
      <FieldError error={error} />
    </div>
  )
}
