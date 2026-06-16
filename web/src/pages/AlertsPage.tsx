import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useAlertRules, useAlertEvents, useCreateAlertRule, useDeleteAlertRule, type AlertRuleInfo } from '@/api/alerts'

export default function AlertsPage() {
  const { t } = useTranslation()
  const { data: rules } = useAlertRules()
  const { data: events } = useAlertEvents()
  const createRule = useCreateAlertRule()
  const deleteRule = useDeleteAlertRule()
  const [showCreate, setShowCreate] = useState(false)
  const [form, setForm] = useState({ name: '', metric: 'cpu_usage', operator: '>', threshold: 80, durationSec: 60, notifyType: 'webhook', notifyTarget: '' })

  const handleCreate = async () => {
    await createRule.mutateAsync(form)
    setShowCreate(false)
    setForm({ name: '', metric: 'cpu_usage', operator: '>', threshold: 80, durationSec: 60, notifyType: 'webhook', notifyTarget: '' })
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold">{t('alerts.title', '告警规则')}</h1>
        <button className="px-4 py-2 bg-primary text-primary-foreground rounded-md hover:bg-primary/90" onClick={() => setShowCreate(true)}>
          {t('alerts.create', '新建规则')}
        </button>
      </div>

      {showCreate && (
        <div className="border rounded-lg p-4 space-y-3">
          <input className="w-full p-2 border rounded" placeholder={t('alerts.name', '规则名称')} value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} />
          <div className="grid grid-cols-4 gap-2">
            <select className="p-2 border rounded" value={form.metric} onChange={e => setForm({ ...form, metric: e.target.value })}>
              <option value="cpu_usage">CPU</option>
              <option value="memory_usage">内存</option>
              <option value="disk_usage">磁盘</option>
            </select>
            <select className="p-2 border rounded" value={form.operator} onChange={e => setForm({ ...form, operator: e.target.value })}>
              <option value=">">&gt;</option>
              <option value="<">&lt;</option>
              <option value=">=">&gt;=</option>
              <option value="<=">&lt;=</option>
            </select>
            <input type="number" className="p-2 border rounded" placeholder={t('alerts.threshold', '阈值')} value={form.threshold} onChange={e => setForm({ ...form, threshold: Number(e.target.value) })} />
            <input type="number" className="p-2 border rounded" placeholder={t('alerts.duration', '持续秒数')} value={form.durationSec} onChange={e => setForm({ ...form, durationSec: Number(e.target.value) })} />
          </div>
          <input className="w-full p-2 border rounded" placeholder={t('alerts.webhook', 'Webhook URL')} value={form.notifyTarget} onChange={e => setForm({ ...form, notifyTarget: e.target.value })} />
          <div className="flex gap-2">
            <button className="px-4 py-2 bg-primary text-primary-foreground rounded-md" onClick={handleCreate}>{t('common.save', '保存')}</button>
            <button className="px-4 py-2 border rounded-md" onClick={() => setShowCreate(false)}>{t('common.cancel', '取消')}</button>
          </div>
        </div>
      )}

      <div className="border rounded-lg overflow-hidden">
        <table className="w-full">
          <thead className="bg-muted"><tr><th className="p-3 text-left">{t('alerts.name', '名称')}</th><th className="p-3 text-left">{t('alerts.metric', '指标')}</th><th className="p-3 text-left">{t('alerts.condition', '条件')}</th><th className="p-3 text-left">{t('alerts.webhook', 'Webhook')}</th><th className="p-3 text-left">{t('common.actions', '操作')}</th></tr></thead>
          <tbody>
            {(rules ?? []).map((r: AlertRuleInfo) => (
              <tr key={r.id} className="border-t">
                <td className="p-3">{r.name}</td>
                <td className="p-3">{r.metric}</td>
                <td className="p-3">{r.operator} {r.threshold} ({r.durationSec}s)</td>
                <td className="p-3 truncate max-w-[200px]">{r.notifyTarget}</td>
                <td className="p-3"><button className="text-destructive hover:underline" onClick={() => deleteRule.mutate(r.id)}>{t('common.delete', '删除')}</button></td>
              </tr>
            ))}
            {(!rules || rules.length === 0) && <tr><td colSpan={5} className="p-3 text-center text-muted-foreground">{t('alerts.empty', '暂无告警规则')}</td></tr>}
          </tbody>
        </table>
      </div>

      <h2 className="text-xl font-bold mt-6">{t('alerts.events', '告警事件')}</h2>
      <div className="border rounded-lg overflow-hidden">
        <table className="w-full">
          <thead className="bg-muted"><tr><th className="p-3 text-left">{t('alerts.time', '时间')}</th><th className="p-3 text-left">{t('alerts.rule', '规则')}</th><th className="p-3 text-left">{t('alerts.message', '消息')}</th><th className="p-3 text-left">{t('alerts.status', '状态')}</th></tr></thead>
          <tbody>
            {(events ?? []).map((e) => (
              <tr key={e.id} className="border-t">
                <td className="p-3">{new Date(e.createdAt).toLocaleString()}</td>
                <td className="p-3">{e.ruleId}</td>
                <td className="p-3">{e.message}</td>
                <td className="p-3">{e.status === 0 ? '触发' : '恢复'}</td>
              </tr>
            ))}
            {(!events || events.length === 0) && <tr><td colSpan={4} className="p-3 text-center text-muted-foreground">{t('alerts.noEvents', '暂无告警事件')}</td></tr>}
          </tbody>
        </table>
      </div>
    </div>
  )
}
