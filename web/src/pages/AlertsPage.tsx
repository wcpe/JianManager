import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useAlertRules, useAlertEvents, useCreateAlertRule, useDeleteAlertRule, type AlertRuleInfo } from '@/api/alerts'
import { FieldLabel, FieldError } from '@/components/ui/field-label'
import { validateRequired, validateUrl, validatePositiveInt } from '@/lib/form-validation'

export default function AlertsPage() {
  const { t } = useTranslation()
  const { data: rules } = useAlertRules()
  const { data: events } = useAlertEvents()
  const createRule = useCreateAlertRule()
  const deleteRule = useDeleteAlertRule()
  const [showCreate, setShowCreate] = useState(false)
  const [form, setForm] = useState({ name: '', targetType: 'node', targetId: null, metric: 'cpu_usage', operator: '>', threshold: 80, durationSec: 60, notifyType: 'webhook', notifyTarget: '', enabled: true })

  const nameError = validateRequired(form.name)
  const thresholdError = validatePositiveInt(String(form.threshold))
  const durationError = validatePositiveInt(String(form.durationSec))
  const webhookError = validateRequired(form.notifyTarget) || validateUrl(form.notifyTarget)
  const hasError = !!(nameError || thresholdError || durationError || webhookError)

  const handleCreate = async () => {
    if (hasError) return
    await createRule.mutateAsync(form)
    setShowCreate(false)
    setForm({ name: '', targetType: 'node', targetId: null, metric: 'cpu_usage', operator: '>', threshold: 80, durationSec: 60, notifyType: 'webhook', notifyTarget: '', enabled: true })
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold">{t('alerts.title')}</h1>
        <button className="px-4 py-2 bg-primary text-primary-foreground rounded-md hover:bg-primary/90" onClick={() => setShowCreate(true)}>
          {t('alerts.createRule')}
        </button>
      </div>

      {showCreate && (
        <div className="border rounded-lg p-4 space-y-3">
          <div>
            <FieldLabel required>{t('alerts.ruleName')}</FieldLabel>
            <input className="w-full mt-1 p-2 border rounded aria-invalid:border-destructive" placeholder={t('alerts.ruleName')} value={form.name} aria-invalid={!!nameError} onChange={e => setForm({ ...form, name: e.target.value })} />
            <FieldError error={nameError} />
          </div>
          <div className="grid grid-cols-4 gap-2">
            <div>
              <FieldLabel>{t('alerts.metric')}</FieldLabel>
              <select className="w-full mt-1 p-2 border rounded" value={form.metric} onChange={e => setForm({ ...form, metric: e.target.value })}>
                <option value="cpu_usage">{t('alerts.cpu')}</option>
                <option value="memory_usage">{t('alerts.memory')}</option>
                <option value="disk_usage">{t('alerts.disk')}</option>
              </select>
            </div>
            <div>
              <FieldLabel>{t('alerts.condition', 'Condition')}</FieldLabel>
              <select className="w-full mt-1 p-2 border rounded" value={form.operator} onChange={e => setForm({ ...form, operator: e.target.value })}>
                <option value=">">&gt;</option>
                <option value="<">&lt;</option>
                <option value=">=">&gt;=</option>
                <option value="<=">&lt;=</option>
              </select>
            </div>
            <div>
              <FieldLabel required>{t('alerts.threshold')}</FieldLabel>
              <input type="number" className="w-full mt-1 p-2 border rounded aria-invalid:border-destructive" placeholder={t('alerts.threshold')} value={form.threshold} aria-invalid={!!thresholdError} onChange={e => setForm({ ...form, threshold: Number(e.target.value) })} />
              <FieldError error={thresholdError} />
            </div>
            <div>
              <FieldLabel required>{t('alerts.durationSec')}</FieldLabel>
              <input type="number" className="w-full mt-1 p-2 border rounded aria-invalid:border-destructive" placeholder={t('alerts.durationSec')} value={form.durationSec} aria-invalid={!!durationError} onChange={e => setForm({ ...form, durationSec: Number(e.target.value) })} />
              <FieldError error={durationError} />
            </div>
          </div>
          <div>
            <FieldLabel required>{t('alerts.webhook')}</FieldLabel>
            <input className="w-full mt-1 p-2 border rounded aria-invalid:border-destructive" placeholder={t('alerts.webhook')} value={form.notifyTarget} aria-invalid={!!webhookError} onChange={e => setForm({ ...form, notifyTarget: e.target.value })} />
            <FieldError error={webhookError} />
          </div>
          <div className="flex gap-2">
            <button className="px-4 py-2 bg-primary text-primary-foreground rounded-md disabled:opacity-50" disabled={hasError || createRule.isPending} onClick={handleCreate}>{t('common.save')}</button>
            <button className="px-4 py-2 border rounded-md" onClick={() => setShowCreate(false)}>{t('common.cancel')}</button>
          </div>
        </div>
      )}

      <div className="border rounded-lg overflow-hidden">
        <table className="w-full">
          <thead className="bg-muted"><tr><th className="p-3 text-left">{t('alerts.ruleName')}</th><th className="p-3 text-left">{t('alerts.metric')}</th><th className="p-3 text-left">{t('alerts.condition', 'Condition')}</th><th className="p-3 text-left">{t('alerts.webhook')}</th><th className="p-3 text-left">{t('common.actions')}</th></tr></thead>
          <tbody>
            {(rules ?? []).map((r: AlertRuleInfo) => (
              <tr key={r.id} className="border-t">
                <td className="p-3">{r.name}</td>
                <td className="p-3">{r.metric}</td>
                <td className="p-3">{r.operator} {r.threshold} ({r.durationSec}s)</td>
                <td className="p-3 truncate max-w-[200px]">{r.notifyTarget}</td>
                <td className="p-3"><button className="text-destructive hover:underline" onClick={() => deleteRule.mutate(r.id)}>{t('common.delete')}</button></td>
              </tr>
            ))}
            {(!rules || rules.length === 0) && <tr><td colSpan={5} className="p-3 text-center text-muted-foreground">{t('alerts.emptyRules')}</td></tr>}
          </tbody>
        </table>
      </div>

      <h2 className="text-xl font-bold mt-6">{t('alerts.events')}</h2>
      <div className="border rounded-lg overflow-hidden">
        <table className="w-full">
          <thead className="bg-muted"><tr><th className="p-3 text-left">{t('alerts.firedAt')}</th><th className="p-3 text-left">{t('alerts.rule', 'Rule')}</th><th className="p-3 text-left">{t('alerts.message')}</th><th className="p-3 text-left">{t('alerts.status', 'Status')}</th></tr></thead>
          <tbody>
            {(events ?? []).map((e) => (
              <tr key={e.id} className="border-t">
                <td className="p-3">{new Date(e.firedAt).toLocaleString()}</td>
                <td className="p-3">{e.ruleName ?? e.ruleId}</td>
                <td className="p-3">{e.message}</td>
                <td className="p-3">{e.resolved ? t('alerts.resolved') : t('alerts.unresolved')}</td>
              </tr>
            ))}
            {(!events || events.length === 0) && <tr><td colSpan={4} className="p-3 text-center text-muted-foreground">{t('alerts.emptyEvents')}</td></tr>}
          </tbody>
        </table>
      </div>
    </div>
  )
}
