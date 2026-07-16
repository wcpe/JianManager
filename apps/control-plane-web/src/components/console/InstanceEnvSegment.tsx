import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Plus, Save, Trash2 } from 'lucide-react'
import { useInstanceEnv, useUpdateInstance } from '@/api/instances'
import { Button } from '@jianmanager/ui/components/button'
import { Panel } from '@jianmanager/ui/components/panel'

interface EnvRow {
  k: string
  v: string
}

/**
 * 环境变量页签（FR-344）：上区编辑自定义启动环境变量（保存写入 instance.EnvVars，启动时 Worker 物化为 .env
 * 并注入进程）；下区展示运行中 JVM 进程的实际完整环境（含继承 PATH/JAVA_HOME，只读）。
 */
export default function InstanceEnvSegment({ instanceId }: { instanceId: number }) {
  const { t } = useTranslation()
  const { data: env } = useInstanceEnv(instanceId)
  // configured 内容变（首载 / 保存后 invalidate 刷新）时按 key 重挂编辑器，令其从最新值重新初始化草稿；
  // 15s 轮询内容不变时 key 稳定不重挂，用户未保存编辑不被清（避免在 effect 里 setState 的反模式）。
  const configuredKey = env?.configured ? JSON.stringify(env.configured) : 'loading'
  const runtimeEntries = env?.runtime ? Object.entries(env.runtime).sort((a, b) => a[0].localeCompare(b[0])) : []

  return (
    <div className="space-y-3 p-4">
      <Panel title={t('env.customTitle')}>
        <EnvEditor key={configuredKey} instanceId={instanceId} initial={env?.configured ?? {}} />
      </Panel>

      <Panel title={t('env.runtimeTitle')}>
        <div className="p-3">
          {env?.runtimeAvailable && runtimeEntries.length > 0 ? (
            <div className="max-h-96 overflow-auto rounded-md border">
              <table className="w-full text-xs">
                <tbody>
                  {runtimeEntries.map(([k, v]) => (
                    <tr key={k} className="border-b align-top last:border-0">
                      <td className="whitespace-nowrap px-2 py-1 font-mono font-medium text-muted-foreground">{k}</td>
                      <td className="break-all px-2 py-1 font-mono">{v}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <p className="text-xs text-muted-foreground">{env?.note || t('env.runtimeUnavailable')}</p>
          )}
        </div>
      </Panel>
    </div>
  )
}

/** 自定义启动 env 编辑器：草稿由 initial 初始化（父按 configured 内容 key 重挂），保存组装 map 提交。 */
function EnvEditor({ instanceId, initial }: { instanceId: number; initial: Record<string, string> }) {
  const { t } = useTranslation()
  const update = useUpdateInstance()
  const [rows, setRows] = useState<EnvRow[]>(() => Object.entries(initial).map(([k, v]) => ({ k, v })))

  const setRow = (i: number, patch: Partial<EnvRow>) => setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  const addRow = () => setRows((rs) => [...rs, { k: '', v: '' }])
  const delRow = (i: number) => setRows((rs) => rs.filter((_, j) => j !== i))

  const save = () => {
    // 去空键、后者覆盖前者；空对象=清空。
    const map: Record<string, string> = {}
    for (const { k, v } of rows) {
      const key = k.trim()
      if (key) map[key] = v
    }
    update.mutate({ id: instanceId, body: { envVars: map } }, { onSuccess: () => toast.success(t('env.saved')) })
  }

  return (
    <div className="space-y-2 p-3">
      <p className="text-xs text-muted-foreground">{t('env.customHint')}</p>
      {rows.length === 0 && <p className="text-xs text-muted-foreground">{t('env.empty')}</p>}
      {rows.map((r, i) => (
        <div key={i} className="flex items-center gap-2">
          <input
            className="h-8 w-40 shrink-0 rounded-md border bg-background px-2 font-mono text-xs"
            placeholder="KEY"
            aria-label={t('env.keyLabel')}
            value={r.k}
            onChange={(e) => setRow(i, { k: e.target.value })}
          />
          <span className="text-muted-foreground">=</span>
          <input
            className="h-8 min-w-0 flex-1 rounded-md border bg-background px-2 font-mono text-xs"
            placeholder="value"
            aria-label={t('env.valueLabel')}
            value={r.v}
            onChange={(e) => setRow(i, { v: e.target.value })}
          />
          <Button size="icon" variant="ghost" className="size-8 shrink-0" onClick={() => delRow(i)} aria-label={t('common.delete')}>
            <Trash2 className="size-3.5" />
          </Button>
        </div>
      ))}
      <div className="flex items-center gap-2 pt-1">
        <Button size="sm" variant="outline" onClick={addRow}>
          <Plus className="mr-1 size-3.5" />
          {t('env.add')}
        </Button>
        <Button size="sm" disabled={update.isPending} onClick={save}>
          <Save className="mr-1 size-3.5" />
          {t('env.save')}
        </Button>
      </div>
    </div>
  )
}
