import { useTranslation } from 'react-i18next'
import { Plus, Trash2 } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Combobox } from '@jianmanager/ui/components/combobox'
import { FieldError } from '@jianmanager/ui/components/field-label'
import {
  allRowsValid,
  duplicateInstanceIds,
  emptyRow,
  validateRow,
  type PathOverrideRow,
} from '@/lib/settings/path-overrides'

/**
 * `log.acq.path_overrides` 的成对行编辑器。
 *
 * 【为什么不是「一个 JSON 输入框」】这个字段的后端契约是
 * `{"inst:1":"proxy.log.0"}`，但它的语义只是「某个实例的日志在哪个路径」。
 * 让用户手敲 JSON 意味着：引号逗号不能错、键必须写成 `inst:<数字>`（界面从没教过）、
 * 删一条要小心别留下多余逗号。这里换成「实例下拉 + 路径输入」的成对行，
 * 序列化交给 lib/settings/path-overrides.ts。
 *
 * 【为什么校验是逐行即时而不是提交时】用户在第三行输了个绝对路径，
 * 应该当场看到红框提示；等到点保存才报错，是一次没有必要的往返，
 * 而且报错位置不在出错那一行。
 */
export interface InstanceOption {
  /** 实例 ID（数字字符串）。 */
  value: string
  /** 展示文案，如「survival（MC 1.20.1）」。 */
  label: string
}

export interface PathOverridesEditorProps {
  rows: readonly PathOverrideRow[]
  onChange: (next: PathOverrideRow[]) => void
  /** 可选实例清单（来自本节点实例列表）。为空时降级为纯文本输入。 */
  options: readonly InstanceOption[]
  /** 只读（节点离线等场景）。 */
  disabled?: boolean
}

export function PathOverridesEditor({ rows, onChange, options, disabled = false }: PathOverridesEditorProps) {
  const { t } = useTranslation()
  const dups = duplicateInstanceIds(rows)

  const patch = (key: string, next: Partial<PathOverrideRow>) => {
    onChange(rows.map((r) => (r.key === key ? { ...r, ...next } : r)))
  }
  const remove = (key: string) => onChange(rows.filter((r) => r.key !== key))
  const add = () => onChange([...rows, emptyRow()])

  return (
    <div className="space-y-2" data-testid="path-overrides-editor">
      {rows.length === 0 ? (
        <p className="rounded-md border border-dashed px-3 py-2 text-xs text-muted-foreground">
          {t(
            'logAcqOverride.empty',
            '未配置覆盖。默认按每个实例的规则路径采集；只有日志不在规则位置时才需要在这里指定。',
          )}
        </p>
      ) : (
        rows.map((r) => {
          const err = validateRow(r)
          const dup = r.instanceId.trim() !== '' && dups.includes(r.instanceId.trim())
          return (
            <div key={r.key} className="space-y-1" data-testid={`path-override-row-${r.key}`}>
              <div className="flex flex-wrap items-center gap-1.5">
                <div className="w-48 shrink-0">
                  {options.length > 0 ? (
                    <Combobox
                      value={r.instanceId}
                      onChange={(v) => patch(r.key, { instanceId: v })}
                      options={[...options]}
                      allowCustom={false}
                      disabled={disabled}
                      placeholder={t('logAcqOverride.pickInstance', '选择实例')}
                      ariaLabel={t('logAcqOverride.instance', '实例')}
                      /* Combobox 不透传 data-testid（见 packages/ui 的 ComboboxProps），
                         故用 id 暴露可定位锚点；测试里按 id 或 ariaLabel 都能找到。 */
                      id={`path-override-instance-${r.key}`}
                    />
                  ) : (
                    // 拿不到实例清单时（如节点离线）退回手填 ID：不能让编辑器彻底不可用。
                    <Input
                      value={r.instanceId}
                      onChange={(e) => patch(r.key, { instanceId: e.target.value })}
                      placeholder={t('logAcqOverride.instanceId', '实例 ID')}
                      className="h-8 text-xs"
                      disabled={disabled}
                      aria-label={t('logAcqOverride.instance', '实例')}
                      data-testid={`path-override-instance-${r.key}`}
                    />
                  )}
                </div>
                <Input
                  value={r.path}
                  onChange={(e) => patch(r.key, { path: e.target.value })}
                  placeholder={t('logAcqOverride.pathPlaceholder', '相对实例工作目录，如 logs/server.log')}
                  className="h-8 min-w-0 flex-1 text-xs"
                  disabled={disabled}
                  aria-invalid={!!err}
                  aria-label={t('logAcqOverride.logPath', '日志路径')}
                  data-testid={`path-override-path-${r.key}`}
                />
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={disabled}
                  onClick={() => remove(r.key)}
                  aria-label={t('common.delete', '删除')}
                  data-testid={`path-override-remove-${r.key}`}
                >
                  <Trash2 className="size-3.5" />
                </Button>
              </div>
              {dup && (
                <p className="text-[11px] text-status-danger" data-testid={`path-override-dup-${r.key}`}>
                  {t('logAcqOverride.duplicateInstance', '这个实例已在上方配置过，提交时只会保留一条。')}
                </p>
              )}
              <FieldError error={err ? t(err) : undefined} />
            </div>
          )
        })
      )}

      <div className="flex items-center gap-2">
        <Button size="sm" variant="outline" onClick={add} disabled={disabled} data-testid="path-overrides-add">
          <Plus className="mr-1 size-3.5" />
          {t('logAcqOverride.addRow', '添加实例覆盖')}
        </Button>
        {rows.length > 0 && !allRowsValid(rows) && (
          <span className="text-[11px] text-status-warning" data-testid="path-overrides-invalid">
            {t('logAcqOverride.fixBeforeSave', '有未修正的项，修正后才能保存')}
          </span>
        )}
      </div>
    </div>
  )
}
