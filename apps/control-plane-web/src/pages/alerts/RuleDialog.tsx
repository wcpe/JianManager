import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNodes } from '@/api/nodes'
import { InstancePicker } from '@/components/InstancePicker'
import {
  useCreateAlertRule,
  useUpdateAlertRule,
  type AlertRuleInfo,
  type AlertChannelInfo,
} from '@/api/alerts'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import {
  ScrollableDialogBody,
  scrollableDialogContentClass,
} from '@jianmanager/ui/components/scrollable-dialog'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import {
  triggerUsesMetric,
  triggerUsesKeyword,
  triggerUsesEventMatch,
  targetTypeForTrigger,
  triggerAllowsTargetSwitch,
  isValidHHMM,
  parseChannelIds,
} from './alert-helpers'

interface RuleDialogProps {
  /** 编辑目标；null 表示创建。 */
  rule: AlertRuleInfo | null
  channels: AlertChannelInfo[]
  onClose: () => void
}

const TRIGGER_TYPES = ['metric', 'instance_crash', 'node_offline', 'log_keyword', 'player_event', 'backup_failed', 'saturation', 'baseline', 'quota_exceeded'] as const
const LEVELS = ['info', 'warn', 'critical'] as const
const PLAYER_EVENTS = ['join', 'quit', 'chat', 'cross_server'] as const
/** Radix Select 不接受空字符串选项值，用哨兵值表达「全部（不指定目标）」。 */
const TARGET_ALL = 'all'

/** 告警规则创建/编辑对话框（FR-085）。按触发类型动态展示字段。 */
export function RuleDialog({ rule, channels, onClose }: RuleDialogProps) {
  const { t } = useTranslation()
  const create = useCreateAlertRule()
  const update = useUpdateAlertRule()
  const { data: nodes } = useNodes()
  const isEdit = !!rule
  const initialTrigger = rule?.triggerType ?? 'metric'

  const [form, setForm] = useState({
    name: rule?.name ?? '',
    triggerType: initialTrigger,
    level: rule?.level ?? 'warn',
    targetType: rule?.targetType ?? targetTypeForTrigger(initialTrigger),
    targetId: (rule?.targetId ?? null) as number | null,
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
  const keywordError = triggerUsesKeyword(form.triggerType) && form.keyword.trim() === '' ? t('validation.required') : ''
  const silenceError =
    !isValidHHMM(form.silenceStart) || !isValidHHMM(form.silenceEnd) ? t('alerts.silenceFormatError') : ''
  const hasError = !!(nameError || keywordError || silenceError)
  /**
   * 目标选项只保留**节点**维度。
   *
   * 实例维度已改走 InstancePicker 的服务端搜索：实例数是千级（大档 1200），而 Radix Select
   * 依赖全部 SelectItem mount 才能提供首字母跳转与方向键导航，无法只渲染前 N 项——只能整份
   * 列举，正是要摆脱的形态。节点是个位/十位量级、无搜索需求，保留原生 Select 即可。
   */
  const targetOptions = useMemo(
    () => (nodes ?? []).map((n) => ({ id: n.id, label: n.name })),
    [nodes],
  )
  const targetItems = useMemo(
    () =>
      targetOptions.map((target) => (
        <SelectItem key={target.id} value={String(target.id)}>
          {target.label}
        </SelectItem>
      )),
    [targetOptions],
  )
  const targetAllLabel = form.targetType === 'node' ? t('alerts.allNodes') : t('alerts.allInstances')

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
        targetId: form.targetId,
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
    <Dialog open onOpenChange={(next) => { if (!next) onClose() }}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-lg`}>
        <DialogHeader>
          <DialogTitle>{isEdit ? t('alerts.editRule') : t('alerts.createRule')}</DialogTitle>
        </DialogHeader>

        <ScrollableDialogBody className="space-y-3">
          <div>
            <FieldLabel required>{t('alerts.ruleName')}</FieldLabel>
            <Input
              className="mt-1"
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
              <Select
                value={form.triggerType}
                disabled={isEdit}
                onValueChange={(v) => setForm({ ...form, triggerType: v, targetType: targetTypeForTrigger(v), targetId: null })}
              >
                <SelectTrigger className="w-full mt-1">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {TRIGGER_TYPES.map((tt) => (
                    <SelectItem key={tt} value={tt}>
                      {t(`alerts.trigger_${tt}`, tt)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div>
              <FieldLabel>{t('alerts.level')}</FieldLabel>
              <Select value={form.level} onValueChange={(v) => setForm({ ...form, level: v })}>
                <SelectTrigger className="w-full mt-1">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {LEVELS.map((lv) => (
                    <SelectItem key={lv} value={lv}>
                      {t(`alerts.level_${lv}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          {!isEdit ? (
            <div>
              {triggerAllowsTargetSwitch(form.triggerType) && (
                <div className="mb-2">
                  <FieldLabel>{t('alerts.targetDimension')}</FieldLabel>
                  <div className="flex gap-2 mt-1">
                    {(['node', 'instance'] as const).map((tt) => (
                      <Button
                        key={tt}
                        type="button"
                        size="sm"
                        variant={form.targetType === tt ? 'default' : 'outline'}
                        onClick={() => setForm({ ...form, targetType: tt, targetId: null })}
                      >
                        {t(tt === 'node' ? 'alerts.dimensionNode' : 'alerts.dimensionInstance')}
                      </Button>
                    ))}
                  </div>
                  <p className="mt-1 text-xs text-muted-foreground">{t('alerts.targetDimensionHint')}</p>
                </div>
              )}
              <FieldLabel>{t('alerts.targetScope')}</FieldLabel>
              {form.targetType === 'node' ? (
                <Select
                  value={form.targetId === null ? TARGET_ALL : String(form.targetId)}
                  onValueChange={(v) => setForm({ ...form, targetId: v === TARGET_ALL ? null : Number(v) })}
                >
                  <SelectTrigger className="w-full mt-1">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={TARGET_ALL}>{targetAllLabel}</SelectItem>
                    {targetItems}
                  </SelectContent>
                </Select>
              ) : (
                <InstancePicker
                  value={form.targetId}
                  onChange={(id) => setForm({ ...form, targetId: id })}
                  allowAll
                  allLabel={t('alerts.allInstances')}
                  className="mt-1"
                />
              )}
              <p className="mt-1 text-xs text-muted-foreground">
                {t(form.targetType === 'node' ? 'alerts.targetNodeHint' : 'alerts.targetInstanceHint')}
              </p>
            </div>
          ) : (
            <p className="text-xs text-muted-foreground">
              {t('alerts.targetScope')}: {rule?.targetId ? `#${rule.targetId}` : targetAllLabel}
            </p>
          )}

          {triggerUsesMetric(form.triggerType) && (
            <div className="grid grid-cols-4 gap-2">
              <div>
                <FieldLabel>{t('alerts.metric')}</FieldLabel>
                <Select value={form.metric} onValueChange={(v) => setForm({ ...form, metric: v })}>
                  <SelectTrigger className="w-full mt-1">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="cpu">{t('alerts.cpu')}</SelectItem>
                    <SelectItem value="memory">{t('alerts.memory')}</SelectItem>
                    <SelectItem value="disk">{t('alerts.disk')}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div>
                <FieldLabel>{t('alerts.condition')}</FieldLabel>
                <Select value={form.operator} onValueChange={(v) => setForm({ ...form, operator: v })}>
                  <SelectTrigger className="w-full mt-1">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value=">">&gt;</SelectItem>
                    <SelectItem value="<">&lt;</SelectItem>
                    <SelectItem value=">=">&gt;=</SelectItem>
                    <SelectItem value="<=">&lt;=</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div>
                <FieldLabel>{t('alerts.threshold')}</FieldLabel>
                <Input type="number" className="mt-1" value={form.threshold} onChange={(e) => setForm({ ...form, threshold: Number(e.target.value) })} />
              </div>
              <div>
                <FieldLabel>{t('alerts.durationSec')}</FieldLabel>
                <Input type="number" className="mt-1" value={form.durationSec} onChange={(e) => setForm({ ...form, durationSec: Number(e.target.value) })} />
              </div>
            </div>
          )}

          {triggerUsesKeyword(form.triggerType) && (
            <div>
              <FieldLabel required>{t('alerts.keyword')}</FieldLabel>
              <Input
                className="mt-1"
                placeholder="OutOfMemoryError"
                value={form.keyword}
                aria-invalid={!!keywordError}
                onChange={(e) => setForm({ ...form, keyword: e.target.value })}
              />
              <FieldError error={keywordError} />
            </div>
          )}

          {triggerUsesEventMatch(form.triggerType) && (
            <div>
              <FieldLabel>{t('alerts.eventMatch')}</FieldLabel>
              <Select value={form.eventMatch || undefined} onValueChange={(v) => setForm({ ...form, eventMatch: v })}>
                <SelectTrigger className="w-full mt-1">
                  <SelectValue placeholder={t('alerts.anyEvent')} />
                </SelectTrigger>
                <SelectContent>
                  {PLAYER_EVENTS.map((ev) => (
                    <SelectItem key={ev} value={ev}>
                      {t(`alerts.playerEvent_${ev}`, ev)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
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
                    <Checkbox checked={form.channelIds.includes(c.id)} onCheckedChange={() => toggleChannel(c.id)} aria-label={c.name} />
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
              <Input type="number" className="mt-1" value={form.dedupWindowSec} onChange={(e) => setForm({ ...form, dedupWindowSec: Number(e.target.value) })} />
            </div>
            <div>
              <FieldLabel>{t('alerts.silenceStart')}</FieldLabel>
              <Input className="mt-1" placeholder="23:00" value={form.silenceStart} aria-invalid={!!silenceError} onChange={(e) => setForm({ ...form, silenceStart: e.target.value })} />
            </div>
            <div>
              <FieldLabel>{t('alerts.silenceEnd')}</FieldLabel>
              <Input className="mt-1" placeholder="07:00" value={form.silenceEnd} aria-invalid={!!silenceError} onChange={(e) => setForm({ ...form, silenceEnd: e.target.value })} />
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
            <Checkbox checked={form.notifyRecover} onCheckedChange={(v) => setForm({ ...form, notifyRecover: v === true })} aria-label={t('alerts.notifyRecover')} />
            {t('alerts.notifyRecover')}
          </label>
        </ScrollableDialogBody>

        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button
            type="button"
            disabled={hasError || create.isPending || update.isPending}
            onClick={handleSubmit}
          >
            {t('common.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
