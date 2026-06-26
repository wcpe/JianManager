import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  useCreateAlertRule,
  useUpdateAlertRule,
  type AlertRuleInfo,
  type AlertChannelInfo,
} from '@/api/alerts'
import { FieldLabel, FieldError } from '@/components/ui/field-label'
import { MODAL_OVERLAY, MODAL_PANEL } from '@/components/ui/scrollable-dialog'
import {
  triggerUsesMetric,
  triggerUsesKeyword,
  triggerUsesEventMatch,
  isValidHHMM,
  parseChannelIds,
} from './alert-helpers'

interface RuleDialogProps {
  /** 编辑目标；null 表示创建。 */
  rule: AlertRuleInfo | null
  channels: AlertChannelInfo[]
  onClose: () => void
}

const TRIGGER_TYPES = ['metric', 'instance_crash', 'node_offline', 'log_keyword', 'player_event', 'backup_failed'] as const
const LEVELS = ['info', 'warn', 'critical'] as const
const PLAYER_EVENTS = ['', 'join', 'quit', 'chat', 'cross_server'] as const

/** 告警规则创建/编辑对话框（FR-085）。按触发类型动态展示字段。 */
export function RuleDialog({ rule, channels, onClose }: RuleDialogProps) {
  const { t } = useTranslation()
  const create = useCreateAlertRule()
  const update = useUpdateAlertRule()
  const isEdit = !!rule

  const [form, setForm] = useState({
    name: rule?.name ?? '',
    triggerType: rule?.triggerType ?? 'metric',
    level: rule?.level ?? 'warn',
    targetType: rule?.targetType ?? 'node',
    metric: rule?.metric || 'cpu',
    operator: rule?.operator || '>',
    threshold: rule?.threshold ?? 80,
    durationSec: rule?.durationSec ?? 60,
    keyword: rule?.keyword ?? '',
    eventMatch: rule?.eventMatch ?? '',
    dedupWindowSec: rule?.dedupWindowSec ?? 300,
    silenceStart: rule?.silenceStart ?? '',
    silenceEnd: rule?.silenceEnd ?? '',
    notifyRecover: rule?.notifyRecover ?? true,
    channelIds: rule ? parseChannelIds(rule.channelIds) : ([] as number[]),
  })

  const nameError = form.name.trim() === '' ? t('validation.required') : ''
  const silenceError =
    !isValidHHMM(form.silenceStart) || !isValidHHMM(form.silenceEnd) ? t('alerts.silenceFormatError') : ''
  const hasError = !!(nameError || silenceError)

  const toggleChannel = (id: number) => {
    setForm((f) => ({
      ...f,
      channelIds: f.channelIds.includes(id) ? f.channelIds.filter((x) => x !== id) : [...f.channelIds, id],
    }))
  }

  const handleSubmit = async () => {
    if (hasError) return
    if (isEdit && rule) {
      // 编辑：提交可变字段子集（触发类型/目标不可改，保持事件归类稳定）。
      await update.mutateAsync({
        id: rule.id,
        level: form.level,
        threshold: form.threshold,
        channelIds: form.channelIds,
        dedupWindowSec: form.dedupWindowSec,
        silenceStart: form.silenceStart,
        silenceEnd: form.silenceEnd,
        notifyRecover: form.notifyRecover,
        keyword: form.keyword,
        eventMatch: form.eventMatch,
      })
    } else {
      await create.mutateAsync({
        name: form.name,
        triggerType: form.triggerType,
        level: form.level,
        targetType: form.targetType,
        metric: form.metric,
        operator: form.operator,
        threshold: form.threshold,
        durationSec: form.durationSec,
        keyword: form.keyword,
        eventMatch: form.eventMatch,
        channelIds: form.channelIds,
        dedupWindowSec: form.dedupWindowSec,
        silenceStart: form.silenceStart,
        silenceEnd: form.silenceEnd,
        notifyRecover: form.notifyRecover,
      })
    }
    onClose()
  }

  return (
    <div className={MODAL_OVERLAY} onClick={onClose}>
      <div className={`${MODAL_PANEL} max-w-lg space-y-3`} onClick={(e) => e.stopPropagation()}>
        <h2 className="text-lg font-bold">{isEdit ? t('alerts.editRule') : t('alerts.createRule')}</h2>

        <div>
          <FieldLabel required>{t('alerts.ruleName')}</FieldLabel>
          <input
            className="w-full mt-1 p-2 border rounded aria-invalid:border-destructive"
            value={form.name}
            disabled={isEdit}
            aria-invalid={!!nameError}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
          />
          <FieldError error={nameError} />
        </div>

        <div className="grid grid-cols-2 gap-2">
          <div>
            <FieldLabel>{t('alerts.triggerType')}</FieldLabel>
            <select
              className="w-full mt-1 p-2 border rounded disabled:opacity-60"
              value={form.triggerType}
              disabled={isEdit}
              onChange={(e) => setForm({ ...form, triggerType: e.target.value })}
            >
              {TRIGGER_TYPES.map((tt) => (
                <option key={tt} value={tt}>
                  {t(`alerts.trigger_${tt}`, tt)}
                </option>
              ))}
            </select>
          </div>
          <div>
            <FieldLabel>{t('alerts.level')}</FieldLabel>
            <select
              className="w-full mt-1 p-2 border rounded"
              value={form.level}
              onChange={(e) => setForm({ ...form, level: e.target.value })}
            >
              {LEVELS.map((lv) => (
                <option key={lv} value={lv}>
                  {t(`alerts.level_${lv}`)}
                </option>
              ))}
            </select>
          </div>
        </div>

        {!isEdit && (
          <div>
            <FieldLabel>{t('alerts.targetType')}</FieldLabel>
            <select
              className="w-full mt-1 p-2 border rounded"
              value={form.targetType}
              onChange={(e) => setForm({ ...form, targetType: e.target.value })}
            >
              <option value="node">{t('alerts.node')}</option>
              <option value="instance">{t('alerts.instance')}</option>
            </select>
          </div>
        )}

        {triggerUsesMetric(form.triggerType) && (
          <div className="grid grid-cols-4 gap-2">
            <div>
              <FieldLabel>{t('alerts.metric')}</FieldLabel>
              <select className="w-full mt-1 p-2 border rounded" value={form.metric} onChange={(e) => setForm({ ...form, metric: e.target.value })}>
                <option value="cpu">{t('alerts.cpu')}</option>
                <option value="memory">{t('alerts.memory')}</option>
                <option value="disk">{t('alerts.disk')}</option>
              </select>
            </div>
            <div>
              <FieldLabel>{t('alerts.condition')}</FieldLabel>
              <select className="w-full mt-1 p-2 border rounded" value={form.operator} onChange={(e) => setForm({ ...form, operator: e.target.value })}>
                <option value=">">&gt;</option>
                <option value="<">&lt;</option>
                <option value=">=">&gt;=</option>
                <option value="<=">&lt;=</option>
              </select>
            </div>
            <div>
              <FieldLabel>{t('alerts.threshold')}</FieldLabel>
              <input type="number" className="w-full mt-1 p-2 border rounded" value={form.threshold} onChange={(e) => setForm({ ...form, threshold: Number(e.target.value) })} />
            </div>
            <div>
              <FieldLabel>{t('alerts.durationSec')}</FieldLabel>
              <input type="number" className="w-full mt-1 p-2 border rounded" value={form.durationSec} onChange={(e) => setForm({ ...form, durationSec: Number(e.target.value) })} />
            </div>
          </div>
        )}

        {triggerUsesKeyword(form.triggerType) && (
          <div>
            <FieldLabel>{t('alerts.keyword')}</FieldLabel>
            <input
              className="w-full mt-1 p-2 border rounded"
              placeholder="OutOfMemoryError"
              value={form.keyword}
              onChange={(e) => setForm({ ...form, keyword: e.target.value })}
            />
          </div>
        )}

        {triggerUsesEventMatch(form.triggerType) && (
          <div>
            <FieldLabel>{t('alerts.eventMatch')}</FieldLabel>
            <select className="w-full mt-1 p-2 border rounded" value={form.eventMatch} onChange={(e) => setForm({ ...form, eventMatch: e.target.value })}>
              {PLAYER_EVENTS.map((ev) => (
                <option key={ev} value={ev}>
                  {ev === '' ? t('alerts.anyEvent') : t(`alerts.playerEvent_${ev}`, ev)}
                </option>
              ))}
            </select>
          </div>
        )}

        {/* 通道路由（多选）。 */}
        <div>
          <FieldLabel>{t('alerts.channels')}</FieldLabel>
          {channels.length === 0 ? (
            <p className="text-sm text-muted-foreground mt-1">{t('alerts.noChannelsHint')}</p>
          ) : (
            <div className="mt-1 flex flex-wrap gap-2">
              {channels.map((c) => (
                <label key={c.id} className="flex items-center gap-1.5 px-2 py-1 border rounded cursor-pointer text-sm">
                  <input type="checkbox" checked={form.channelIds.includes(c.id)} onChange={() => toggleChannel(c.id)} />
                  {c.name}
                </label>
              ))}
            </div>
          )}
        </div>

        {/* 聚合 + 静默 + 恢复。 */}
        <div className="grid grid-cols-3 gap-2">
          <div>
            <FieldLabel>{t('alerts.dedupWindowSec')}</FieldLabel>
            <input type="number" className="w-full mt-1 p-2 border rounded" value={form.dedupWindowSec} onChange={(e) => setForm({ ...form, dedupWindowSec: Number(e.target.value) })} />
          </div>
          <div>
            <FieldLabel>{t('alerts.silenceStart')}</FieldLabel>
            <input className="w-full mt-1 p-2 border rounded aria-invalid:border-destructive" placeholder="23:00" value={form.silenceStart} aria-invalid={!!silenceError} onChange={(e) => setForm({ ...form, silenceStart: e.target.value })} />
          </div>
          <div>
            <FieldLabel>{t('alerts.silenceEnd')}</FieldLabel>
            <input className="w-full mt-1 p-2 border rounded aria-invalid:border-destructive" placeholder="07:00" value={form.silenceEnd} aria-invalid={!!silenceError} onChange={(e) => setForm({ ...form, silenceEnd: e.target.value })} />
          </div>
        </div>
        <FieldError error={silenceError} />
        {(form.silenceStart || form.silenceEnd) && (
          <p className="text-xs text-muted-foreground">
            {t('alerts.silenceTzNote')}
            {isValidHHMM(form.silenceStart) && isValidHHMM(form.silenceEnd) && form.silenceStart > form.silenceEnd
              ? ` · ${t('alerts.silenceCrossMidnight')}`
              : ''}
          </p>
        )}

        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={form.notifyRecover} onChange={(e) => setForm({ ...form, notifyRecover: e.target.checked })} />
          {t('alerts.notifyRecover')}
        </label>

        <div className="flex gap-2 pt-2">
          <button
            className="px-4 py-2 bg-primary text-primary-foreground rounded-md disabled:opacity-50"
            disabled={hasError || create.isPending || update.isPending}
            onClick={handleSubmit}
          >
            {t('common.save')}
          </button>
          <button className="px-4 py-2 border rounded-md" onClick={onClose}>
            {t('common.cancel')}
          </button>
        </div>
      </div>
    </div>
  )
}
