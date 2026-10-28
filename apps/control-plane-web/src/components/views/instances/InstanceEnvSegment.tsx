import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Save, Trash2 } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Panel } from '@jianmanager/ui/components/panel'
import { Table, TableBody, TableCell, TableRow } from '@jianmanager/ui/components/table'

interface EnvRow {
  /** 行 id：只服务于渲染 key，不参与保存载荷（保存只取 k/v）。 */
  id: string
  k: string
  v: string
}

/** 环境变量行的 id 序号（模块级单调递增，同一页面生命周期内唯一即可）。 */
let envRowSeq = 0

/** 铸一个新的环境变量行 id。 */
function nextEnvRowId(): string {
  envRowSeq += 1
  return `env-row-${envRowSeq}`
}

/**
 * 环境变量页签（FR-344）：上区编辑自定义启动环境变量（保存写入 instance.EnvVars，启动时 Worker 物化为 .env
 * 并注入进程）；下区展示运行中 JVM 进程的实际完整环境（含继承 PATH/JAVA_HOME，只读）。
 *
 * 受控视图（ADR-103 b 范式）：不取数、不发请求、不弹 toast——三份数据经 props 注入，
 * 保存经 `onSave` 上报。重挂策略留在视图内：`configured` 内容变化时按内容作 key 重挂编辑器，
 * 令其从最新值重新初始化草稿；轮询内容不变则 key 稳定、用户未保存的编辑不被清
 * （这是为了避免「在 effect 里 setState」的反模式，属视图自身的实现细节）。
 */
export interface InstanceEnvSegmentProps {
  /** 已配置的启动环境变量。 */
  configured?: Record<string, string> | null
  /** 运行中进程的实际环境（只读展示）。 */
  runtime?: Record<string, string> | null
  /** 运行环境是否可取。 */
  runtimeAvailable?: boolean | null
  /** 不可取时的说明（后端给出，如「实例未运行」）。 */
  note?: string | null
  /** 保存在途。 */
  saving?: boolean
  /** 保存自定义环境变量（空对象=清空）。返回是否成功。 */
  onSave: (envVars: Record<string, string>) => Promise<boolean>
}

export default function InstanceEnvSegment({
  configured,
  runtime,
  runtimeAvailable,
  note,
  saving = false,
  onSave,
}: InstanceEnvSegmentProps) {
  const { t } = useTranslation()
  // configured 内容变（首载 / 保存后 invalidate 刷新）时按 key 重挂编辑器，令其从最新值重新初始化草稿；
  // 轮询内容不变时 key 稳定不重挂，用户未保存编辑不被清（避免在 effect 里 setState 的反模式）。
  const configuredKey = configured ? JSON.stringify(configured) : 'loading'
  const runtimeEntries = runtime ? Object.entries(runtime).sort((a, b) => a[0].localeCompare(b[0])) : []

  return (
    <div className="space-y-3 p-4">
      <Panel title={t('env.customTitle')}>
        <EnvEditor key={configuredKey} initial={configured ?? {}} saving={saving} onSave={onSave} />
      </Panel>

      <Panel title={t('env.runtimeTitle')}>
        <div className="p-3">
          {runtimeAvailable && runtimeEntries.length > 0 ? (
            <div className="max-h-96 overflow-auto rounded-md border">
              {/* Table 自带外层横向滚动容器，纵向滚动与圆角边框仍由本层控制 */}
              <Table className="text-xs">
                <TableBody>
                  {runtimeEntries.map(([k, v]) => (
                    <TableRow key={k}>
                      <TableCell className="whitespace-nowrap px-2 py-1 align-top font-mono font-medium text-muted-foreground">{k}</TableCell>
                      {/* whitespace-normal：抵消 TableCell 基类的 whitespace-nowrap，让 break-all 长值能换行 */}
                      <TableCell className="whitespace-normal break-all px-2 py-1 align-top font-mono">{v}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          ) : (
            <p className="text-xs text-muted-foreground">{note || t('env.runtimeUnavailable')}</p>
          )}
        </div>
      </Panel>
    </div>
  )
}

/** 自定义启动 env 编辑器：草稿由 initial 初始化（父按 configured 内容 key 重挂），保存组装 map 上报。 */
function EnvEditor({
  initial,
  saving,
  onSave,
}: {
  initial: Record<string, string>
  saving: boolean
  onSave: (envVars: Record<string, string>) => Promise<boolean>
}) {
  const { t } = useTranslation()
  const [rows, setRows] = useState<EnvRow[]>(() => Object.entries(initial).map(([k, v]) => ({ id: nextEnvRowId(), k, v })))

  const setRow = (i: number, patch: Partial<EnvRow>) => setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  const addRow = () => setRows((rs) => [...rs, { id: nextEnvRowId(), k: '', v: '' }])
  const delRow = (i: number) => setRows((rs) => rs.filter((_, j) => j !== i))

  const save = () => {
    // 去空键、后者覆盖前者；空对象=清空。
    const map: Record<string, string> = {}
    for (const { k, v } of rows) {
      const key = k.trim()
      if (key) map[key] = v
    }
    void onSave(map)
  }

  return (
    <div className="space-y-2 p-3">
      <p className="text-xs text-muted-foreground">{t('env.customHint')}</p>
      {rows.length === 0 && <p className="text-xs text-muted-foreground">{t('env.empty')}</p>}
      {rows.map((r, i) => (
        // key 用行自己的稳定 id，不用下标：这是**可编辑行**，删掉中间一行时下标会让其后每行
        // 的 DOM 身份前移一格——正在编辑的输入框（焦点/光标/输入法组合态）会落到下一条数据上。
        <div key={r.id} className="flex items-center gap-2">
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
        <Button size="sm" disabled={saving} onClick={save}>
          <Save className="mr-1 size-3.5" />
          {t('env.save')}
        </Button>
      </div>
    </div>
  )
}
