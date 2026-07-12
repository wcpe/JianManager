import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { History, Save, X } from 'lucide-react'
import { toast } from 'sonner'
import { Button } from '@jianmanager/ui/components/button'
import { Badge } from '@jianmanager/ui/components/badge'
import CodeEditor from '@/components/explorer/editor/CodeEditor'
import EditorShortcutsHelp from '@/components/explorer/editor/EditorShortcutsHelp'
import {
  useConfigRead,
  useWriteConfig,
  useWriteConfigFields,
  useCrossCheck,
  type ValidationIssue,
  type ModelSchema,
  type FieldSchema,
  type CrossCheckIssue,
} from '@/api/configs'

/**
 * 单文件配置编辑器（FR-071）。
 *
 * 在共享资源管理器（FR-070）的「配置」语义下取代默认 CodeEditor 面板：
 * - schema 文件：文本 ↔ 表单双模式（FR-031 保留），表单走字段级补丁（保留注释）；
 * - 非 schema 文件：纯文本 + 多格式高亮（复用共享 CodeEditor）；
 * - Ctrl+S（CodeEditor Mod-s）/保存按钮：写入并生成**配置版本**（FR-031），保存后通知资源管理器刷新；
 * - 跨文件一致性校验（FR-031）保留。
 *
 * 历史版本（diff/回滚）经资源管理器的配置版本抽屉，故本组件只负责编辑与保存。
 */
interface ConfigFileEditorProps {
  instanceId: number
  /** 相对工作目录的文件路径。 */
  path: string
  /** 文件名（多格式高亮用）。 */
  name: string
  /** 关闭编辑器。 */
  onClose: () => void
  /** 保存成功后回调（刷新资源管理器树/列表）。 */
  onAfterSave: () => void
  /** 打开配置版本抽屉。 */
  onOpenVersions: () => void
  /** 内部 dirty 变化时上报，供资源管理器切换/关闭守卫判断（BUG-018 #36）。 */
  onDirtyChange: (dirty: boolean) => void
  /** 搜索命中跳转目标行（1 起，FR-074）；未定位时 undefined。 */
  gotoLine?: number
  /** 定位 nonce：变化即重触发定位（同一行再次点击也能重跳，FR-074）。 */
  gotoNonce?: number
}

type EditMode = 'text' | 'form'

type FieldEntry = { key: string; schema: FieldSchema }
type FieldGroup = { name: string; fields: FieldEntry[] }

function fieldGroupName(key: string, schema: FieldSchema, fallback: string): string {
  if (schema.group) return schema.group
  const dot = key.indexOf('.')
  return dot > 0 ? key.slice(0, dot) : fallback
}

function groupSchemaFields(schema: ModelSchema, fallback: string): FieldGroup[] {
  const groups: FieldGroup[] = []
  const byName = new Map<string, FieldGroup>()
  for (const [key, fs] of Object.entries(schema.fields)) {
    const name = fieldGroupName(key, fs, fallback)
    let group = byName.get(name)
    if (!group) {
      group = { name, fields: [] }
      byName.set(name, group)
      groups.push(group)
    }
    group.fields.push({ key, schema: fs })
  }
  return groups
}

function fieldErrorKey(fs: FieldSchema, value: string): string | null {
  const trimmed = value.trim()
  if (fs.type === 'int' && !/^-?\d+$/.test(trimmed)) return 'invalidInt'
  if (fs.type === 'bool' && value !== 'true' && value !== 'false') return 'invalidBool'
  if (fs.choices?.length && !fs.choices.includes(value)) return 'invalidChoice'
  return null
}

export default function ConfigFileEditor({
  instanceId,
  path,
  name,
  onClose,
  onAfterSave,
  onOpenVersions,
  onDirtyChange,
  gotoLine,
  gotoNonce,
}: ConfigFileEditorProps) {
  const { t } = useTranslation()
  const [mode, setMode] = useState<EditMode>('text')
  const [draft, setDraft] = useState('')
  const [formDraft, setFormDraft] = useState<Record<string, string>>({})
  const [message, setMessage] = useState('')
  const [crossIssues, setCrossIssues] = useState<CrossCheckIssue[] | null>(null)

  const readQ = useConfigRead(instanceId, path)
  const writeMut = useWriteConfig(instanceId)
  const writeFieldsMut = useWriteConfigFields(instanceId)
  const crossMut = useCrossCheck(instanceId)

  // eslint-disable-next-line react-hooks/preserve-manual-memoization -- 手动 useMemo 解析 schema JSON，行为正确
  const schema = useMemo<ModelSchema | null>(() => {
    if (!readQ.data?.schemaJson) return null
    try {
      const s = JSON.parse(readQ.data.schemaJson) as ModelSchema
      return s && s.fields && Object.keys(s.fields).length > 0 ? s : null
    } catch {
      return null
    }
  }, [readQ.data?.schemaJson])

  const valueByKey = useMemo<Record<string, string>>(() => {
    const m: Record<string, string> = {}
    for (const f of readQ.data?.fields ?? []) m[f.key] = f.value
    return m
  }, [readQ.data?.fields])

  // 切文件/重读完成：初始化草稿；无 schema 强制文本模式。
  useEffect(() => {
    if (!readQ.data) return
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 读取完成后初始化草稿，属合法同步
    setDraft(readQ.data.content)
    const init: Record<string, string> = {}
    if (schema) for (const key of Object.keys(schema.fields)) init[key] = valueByKey[key] ?? schema.fields[key].default ?? ''
    setFormDraft(init)
    setMessage('')
    setCrossIssues(null)
    if (!schema) setMode('text')
    // eslint-disable-next-line react-hooks/exhaustive-deps -- 仅在文件切换/重读时初始化，valueByKey 故意不入依赖
  }, [readQ.data?.path, readQ.data?.content, schema])

  const issues: ValidationIssue[] = readQ.data?.validation?.issues ?? []
  const textDirty = useMemo(() => readQ.data != null && draft !== readQ.data.content, [draft, readQ.data])
  const changedFields = useMemo(
    () => Object.keys(formDraft).filter((k) => formDraft[k] !== (valueByKey[k] ?? schema?.fields[k]?.default ?? '')),
    [formDraft, valueByKey, schema],
  )
  const fieldGroups = useMemo(
    () => (schema ? groupSchemaFields(schema, t('configExplorer.defaultGroup')) : []),
    [schema, t],
  )
  const formErrors = useMemo(() => {
    if (!schema) return {}
    const out: Record<string, string> = {}
    for (const [key, fs] of Object.entries(schema.fields)) {
      const err = fieldErrorKey(fs, formDraft[key] ?? '')
      if (err) out[key] = t(`configExplorer.${err}`)
    }
    return out
  }, [formDraft, schema, t])
  const formDirty = changedFields.length > 0
  const dirty = mode === 'text' ? textDirty : formDirty
  const formInvalid = mode === 'form' && Object.keys(formErrors).length > 0
  const saving = writeMut.isPending || writeFieldsMut.isPending

  // 搜索命中定位（FR-074）：行定位只在文本模式有意义，命中到来时强制切回文本模式
  //（本组件跨文件复用同一实例，上一个文件可能停留在表单模式）。
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 命中跳转到来时切模式，属合法同步
    if (gotoLine && gotoLine > 0) setMode('text')
  }, [gotoLine, gotoNonce])

  // 把内部 dirty 上报给资源管理器，供切换/关闭守卫判断（BUG-018 #36）；卸载时复位为干净。
  useEffect(() => {
    onDirtyChange(dirty)
  }, [dirty, onDirtyChange])
  useEffect(() => () => onDirtyChange(false), [onDirtyChange])

  const handleSave = () => {
    if (!dirty || saving || formInvalid) return
    if (mode === 'text') {
      writeMut.mutate(
        { path, content: draft, message },
        { onSuccess: onAfterSave },
      )
    } else {
      const payload: Record<string, string> = {}
      for (const k of changedFields) payload[k] = formDraft[k]
      writeFieldsMut.mutate({ path, fields: payload, message }, { onSuccess: onAfterSave })
    }
  }

  const handleCrossCheck = () => {
    const content = mode === 'text' ? draft : (readQ.data?.content ?? '')
    crossMut.mutate({ path, content }, { onSuccess: (iss) => setCrossIssues(iss) })
  }

  const handleRevert = () => {
    if (mode === 'text') {
      setDraft(readQ.data?.content ?? '')
    } else {
      const init: Record<string, string> = {}
      if (schema) for (const k of Object.keys(schema.fields)) init[k] = valueByKey[k] ?? schema.fields[k].default ?? ''
      setFormDraft(init)
    }
    setMessage('')
    toast.message(t('configExplorer.reverted'))
  }

  return (
    <div className="flex h-full min-w-0 flex-col">
      {/* 头部：文件名 + 模式切换 + 校验/历史/关闭 */}
      <div className="flex items-center justify-between gap-2 border-b bg-muted/30 px-2 py-1 text-sm">
        <span className="truncate font-medium">
          {name}
          {dirty && <span className="ml-1 text-amber-500">•</span>}
        </span>
        <div className="flex items-center gap-1.5">
          <div className="flex overflow-hidden rounded-md border text-xs">
            <button
              type="button"
              className={`px-2 py-1 ${mode === 'text' ? 'bg-primary text-primary-foreground' : 'hover:bg-muted'}`}
              onClick={() => setMode('text')}
            >
              {t('configExplorer.modeText')}
            </button>
            <button
              type="button"
              disabled={!schema}
              title={schema ? '' : t('configExplorer.noSchema')}
              className={`px-2 py-1 disabled:cursor-not-allowed disabled:opacity-40 ${
                mode === 'form' ? 'bg-primary text-primary-foreground' : 'hover:bg-muted'
              }`}
              onClick={() => schema && setMode('form')}
            >
              {t('configExplorer.modeForm')}
            </button>
          </div>
          {readQ.data && (
            <Badge variant={readQ.data.validation.valid ? 'secondary' : 'destructive'}>
              {readQ.data.validation.valid ? 'valid' : 'invalid'}
            </Badge>
          )}
          {mode === 'text' && <EditorShortcutsHelp />}
          <Button size="sm" variant="ghost" className="h-7 gap-1 px-2 text-xs" onClick={onOpenVersions}>
            <History className="size-3.5" /> {t('configExplorer.versions')}
          </Button>
          <Button size="sm" variant="ghost" className="h-7 px-1.5" title={t('common.close')} onClick={onClose}>
            <X className="size-3.5" />
          </Button>
        </div>
      </div>

      {/* 主体 */}
      <div className="flex min-h-0 flex-1 flex-col">
        {readQ.isLoading ? (
          <p className="p-4 text-sm text-muted-foreground">{t('common.loading')}</p>
        ) : readQ.error ? (
          <p className="p-4 text-sm text-destructive">{(readQ.error as Error).message}</p>
        ) : mode === 'text' ? (
          <div className="min-h-0 flex-1">
            <CodeEditor
              value={draft}
              filename={name}
              gotoLine={gotoLine}
              gotoNonce={gotoNonce}
              onChange={setDraft}
              onSave={handleSave}
            />
          </div>
        ) : (
          <div className="min-h-0 flex-1 space-y-3 overflow-auto p-3">
            {schema?.description && <p className="text-xs text-muted-foreground">{schema.description}</p>}
            {fieldGroups.map((group) => (
              <fieldset key={group.name} className="space-y-2 rounded-md border bg-card/35 p-3">
                <legend className="px-1 text-xs font-medium text-muted-foreground">{group.name}</legend>
                {group.fields.map(({ key, schema: fs }) => {
                  const val = formDraft[key] ?? ''
                  const inputId = `config-field-${key.replace(/[^a-zA-Z0-9_-]/g, '-')}`
                  const error = formErrors[key]
                  const errorId = `${inputId}-error`
                  const onChange = (v: string) => setFormDraft((d) => ({ ...d, [key]: v }))
                  return (
                    <div key={key} className="grid grid-cols-3 items-start gap-2">
                      <label htmlFor={inputId} className="break-all pt-1.5 font-mono text-xs" title={fs.description}>
                        {key}
                      </label>
                      <div className="col-span-2 space-y-1">
                        {fs.type === 'bool' ? (
                          <select
                            id={inputId}
                            className="w-full rounded bg-muted px-2 py-1.5 text-xs"
                            value={val === 'true' ? 'true' : 'false'}
                            onChange={(e) => onChange(e.target.value)}
                          >
                            <option value="true">true</option>
                            <option value="false">false</option>
                          </select>
                        ) : fs.choices && fs.choices.length > 0 ? (
                          <select
                            id={inputId}
                            aria-invalid={!!error}
                            aria-describedby={error ? errorId : undefined}
                            className="w-full rounded bg-muted px-2 py-1.5 text-xs"
                            value={val}
                            onChange={(e) => onChange(e.target.value)}
                          >
                            {fs.choices.map((c) => (
                              <option key={c} value={c}>
                                {c}
                              </option>
                            ))}
                          </select>
                        ) : (
                          <input
                            id={inputId}
                            type="text"
                            inputMode={fs.type === 'int' ? 'numeric' : undefined}
                            aria-invalid={!!error}
                            aria-describedby={error ? errorId : undefined}
                            className="w-full rounded bg-muted px-2 py-1.5 text-xs"
                            value={val}
                            onChange={(e) => onChange(e.target.value)}
                          />
                        )}
                        {error && (
                          <p id={errorId} className="text-[10px] text-destructive">
                            {error}
                          </p>
                        )}
                        {fs.description && <p className="text-[10px] text-muted-foreground">{fs.description}</p>}
                      </div>
                    </div>
                  )
                })}
              </fieldset>
            ))}
          </div>
        )}

        {/* 校验告警（解析/字段级） */}
        {issues.length > 0 && (
          <div className="max-h-24 overflow-auto border-t bg-red-50 p-2 dark:bg-red-950">
            {issues.map((it, i) => (
              <p key={i} className="text-xs text-red-600 dark:text-red-300">
                [{it.level}] {it.key ? `${it.key}: ` : ''}
                {it.message}
              </p>
            ))}
          </div>
        )}

        {/* 跨文件一致性校验结果 */}
        {crossIssues != null && (
          <div className="max-h-28 overflow-auto border-t bg-amber-50 p-2 dark:bg-amber-950">
            {crossIssues.length === 0 ? (
              <p className="text-xs text-green-600 dark:text-green-400">{t('configExplorer.crossCheckPass')}</p>
            ) : (
              crossIssues.map((it, i) => (
                <p key={i} className="text-xs text-amber-700 dark:text-amber-300">
                  [{it.level}] {it.key ? `${it.key}: ` : ''}
                  {it.message}
                </p>
              ))
            )}
          </div>
        )}

        {/* 底部：提交说明 + 校验/保存/撤销 */}
        <div className="flex items-center gap-2 border-t p-2">
          <input
            className="flex-1 rounded bg-muted px-2 py-1 text-xs"
            placeholder={t('configExplorer.commitMsg')}
            value={message}
            onChange={(e) => setMessage(e.target.value)}
          />
          <Button size="sm" variant="outline" disabled={crossMut.isPending} onClick={handleCrossCheck}>
            {crossMut.isPending ? t('common.loading') : t('configExplorer.crossCheck')}
          </Button>
          <Button size="sm" variant="outline" disabled={!dirty || saving} onClick={handleRevert}>
            {t('configExplorer.revert')}
          </Button>
          <Button size="sm" className="gap-1" disabled={!dirty || saving || formInvalid} onClick={handleSave}>
            <Save className="size-3.5" /> {saving ? t('configExplorer.saving') : t('common.save')}
          </Button>
        </div>
      </div>
    </div>
  )
}
