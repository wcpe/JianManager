import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { Badge } from '@/components/ui/badge'
import {
  useConfigFiles,
  useConfigRead,
  useConfigVersions,
  useWriteConfig,
  useWriteConfigFields,
  useCrossCheck,
  useRollbackConfig,
  useConfigDiff,
  type ConfigFileInfo,
  type ConfigVersion,
  type ValidationIssue,
  type ModelSchema,
  type CrossCheckIssue,
} from '@/api/configs'

interface ConfigEditorProps {
  instanceId: number
}

type EditMode = 'text' | 'form'

/**
 * ConfigEditor 是实例详情页的 Config tab 主体：
 * 左侧文件列表 / 中间编辑区（文本 ↔ 表单双模式）/ 右侧版本列表与 diff。
 * 表单模式按内置 schema 渲染字段，保存走字段级补丁（保留注释）；提供跨实例一致性校验。
 */
export default function ConfigEditor({ instanceId }: ConfigEditorProps) {
  const { t } = useTranslation()
  const [selectedPath, setSelectedPath] = useState<string | null>(null)
  const [mode, setMode] = useState<EditMode>('text')
  const [draft, setDraft] = useState<string>('')
  const [formDraft, setFormDraft] = useState<Record<string, string>>({})
  const [message, setMessage] = useState<string>('')
  const [diffFrom, setDiffFrom] = useState<number | null>(null)
  const [diffTo, setDiffTo] = useState<number | null>(null)
  const [crossIssues, setCrossIssues] = useState<CrossCheckIssue[] | null>(null)

  const filesQ = useConfigFiles(instanceId)
  const readQ = useConfigRead(instanceId, selectedPath)
  const versionsQ = useConfigVersions(instanceId, selectedPath)
  const writeMut = useWriteConfig(instanceId)
  const writeFieldsMut = useWriteConfigFields(instanceId)
  const crossMut = useCrossCheck(instanceId)
  const rollbackMut = useRollbackConfig(instanceId, selectedPath)
  const diffQ = useConfigDiff(instanceId, selectedPath, diffFrom ?? undefined, diffTo ?? undefined)

  // 解析 schema 与当前字段值
  // eslint-disable-next-line react-hooks/preserve-manual-memoization -- 手动 useMemo 解析 schema，行为正确
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

  // 切换文件 / 读取完成时同步草稿；无 schema 时强制文本模式
  useEffect(() => {
    if (!readQ.data) return
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 读取完成后初始化可编辑草稿，属合法同步
    setDraft(readQ.data.content)
    const init: Record<string, string> = {}
    if (schema) {
      for (const key of Object.keys(schema.fields)) {
        init[key] = valueByKey[key] ?? schema.fields[key].default ?? ''
      }
    }
    setFormDraft(init)
    setMessage('')
    setDiffFrom(null)
    setDiffTo(null)
    setCrossIssues(null)
    if (!schema) setMode('text')
    // eslint-disable-next-line react-hooks/exhaustive-deps -- 仅在文件切换/重读时初始化草稿，valueByKey 故意不入依赖
  }, [readQ.data?.path, readQ.data?.content, schema])

  const files: ConfigFileInfo[] = filesQ.data ?? []
  const versions: ConfigVersion[] = versionsQ.data ?? []
  const issues: ValidationIssue[] = readQ.data?.validation?.issues ?? []

  const textDirty = useMemo(() => readQ.data && draft !== readQ.data.content, [draft, readQ.data])
  const changedFields = useMemo(
    () => Object.keys(formDraft).filter((k) => formDraft[k] !== (valueByKey[k] ?? schema?.fields[k]?.default ?? '')),
    [formDraft, valueByKey, schema],
  )
  const formDirty = changedFields.length > 0
  const dirty = mode === 'text' ? textDirty : formDirty
  const saving = writeMut.isPending || writeFieldsMut.isPending

  const handleSave = () => {
    if (!selectedPath) return
    if (mode === 'text') {
      writeMut.mutate({ path: selectedPath, content: draft, message })
    } else {
      const payload: Record<string, string> = {}
      for (const k of changedFields) payload[k] = formDraft[k]
      writeFieldsMut.mutate({ path: selectedPath, fields: payload, message })
    }
  }

  const handleCrossCheck = () => {
    if (!selectedPath) return
    const content = mode === 'text' ? draft : (readQ.data?.content ?? '')
    crossMut.mutate({ path: selectedPath, content }, { onSuccess: (iss) => setCrossIssues(iss) })
  }

  return (
    <div className="grid grid-cols-12 gap-4 h-[640px]">
      {/* 文件列表 */}
      <div className="col-span-3 border rounded-lg overflow-hidden flex flex-col">
        <div className="px-3 py-2 bg-muted/50 text-sm font-medium border-b">
          {t('instanceDetail.configFiles', '配置文件')}
        </div>
        <div className="flex-1 overflow-auto">
          {filesQ.isLoading ? (
            <p className="text-xs text-muted-foreground p-3">{t('common.loading')}</p>
          ) : files.length === 0 ? (
            <p className="text-xs text-muted-foreground p-3">{t('instanceDetail.noConfigFiles', '未发现可管理配置')}</p>
          ) : (
            <ul>
              {files.map((f) => (
                <li key={f.path}>
                  <button
                    type="button"
                    onClick={() => setSelectedPath(f.path)}
                    className={`w-full text-left px-3 py-2 text-sm hover:bg-muted/50 ${
                      selectedPath === f.path ? 'bg-muted' : ''
                    }`}
                  >
                    <div className="font-medium truncate">{f.path}</div>
                    <div className="text-xs text-muted-foreground flex justify-between">
                      <span>{f.format}</span>
                      <span>{f.size}B</span>
                    </div>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>

      {/* 编辑区 */}
      <div className="col-span-6 border rounded-lg overflow-hidden flex flex-col">
        <div className="px-3 py-2 bg-muted/50 text-sm font-medium border-b flex items-center justify-between gap-2">
          <span className="truncate">{selectedPath ?? t('instanceDetail.configSelectFile', '请选择文件')}</span>
          <div className="flex items-center gap-2">
            {/* 文本 / 表单 模式切换 */}
            {selectedPath && (
              <div className="flex rounded-md border overflow-hidden text-xs">
                <button
                  type="button"
                  className={`px-2 py-1 ${mode === 'text' ? 'bg-primary text-primary-foreground' : 'hover:bg-muted'}`}
                  onClick={() => setMode('text')}
                >
                  {t('instanceDetail.configModeText', '文本')}
                </button>
                <button
                  type="button"
                  disabled={!schema}
                  title={schema ? '' : t('instanceDetail.configNoSchema', '该文件无内置 schema，仅文本模式')}
                  className={`px-2 py-1 disabled:opacity-40 disabled:cursor-not-allowed ${
                    mode === 'form' ? 'bg-primary text-primary-foreground' : 'hover:bg-muted'
                  }`}
                  onClick={() => schema && setMode('form')}
                >
                  {t('instanceDetail.configModeForm', '表单')}
                </button>
              </div>
            )}
            {readQ.data && (
              <Badge variant={readQ.data.validation.valid ? 'secondary' : 'destructive'}>
                {readQ.data.validation.valid ? 'valid' : 'invalid'}
              </Badge>
            )}
            {dirty && <Badge variant="outline">{t('instanceDetail.configUnsaved', '未保存')}</Badge>}
          </div>
        </div>

        <div className="flex-1 overflow-hidden flex flex-col">
          {selectedPath == null ? (
            <p className="text-sm text-muted-foreground p-4">{t('instanceDetail.configEmpty', '从左侧选择文件')}</p>
          ) : readQ.isLoading ? (
            <p className="text-sm text-muted-foreground p-4">{t('common.loading')}</p>
          ) : readQ.error ? (
            <p className="text-sm text-red-500 p-4">{(readQ.error as Error).message}</p>
          ) : (
            <>
              {mode === 'text' ? (
                <Textarea
                  className="flex-1 font-mono text-xs resize-none"
                  value={draft}
                  onChange={(e) => setDraft(e.target.value)}
                  spellCheck={false}
                />
              ) : (
                <div className="flex-1 overflow-auto p-3 space-y-3">
                  {schema?.description && <p className="text-xs text-muted-foreground">{schema.description}</p>}
                  {schema &&
                    Object.entries(schema.fields).map(([key, fs]) => {
                      const val = formDraft[key] ?? ''
                      const onChange = (v: string) => setFormDraft((d) => ({ ...d, [key]: v }))
                      return (
                        <div key={key} className="grid grid-cols-3 gap-2 items-start">
                          <label className="text-xs font-mono pt-1.5 break-all" title={fs.description}>
                            {key}
                          </label>
                          <div className="col-span-2 space-y-1">
                            {fs.type === 'bool' ? (
                              <select
                                className="w-full text-xs bg-muted rounded px-2 py-1.5"
                                value={val === 'true' ? 'true' : 'false'}
                                onChange={(e) => onChange(e.target.value)}
                              >
                                <option value="true">true</option>
                                <option value="false">false</option>
                              </select>
                            ) : fs.choices && fs.choices.length > 0 ? (
                              <select
                                className="w-full text-xs bg-muted rounded px-2 py-1.5"
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
                                type={fs.type === 'int' ? 'number' : 'text'}
                                className="w-full text-xs bg-muted rounded px-2 py-1.5"
                                value={val}
                                onChange={(e) => onChange(e.target.value)}
                              />
                            )}
                            {fs.description && (
                              <p className="text-[10px] text-muted-foreground">{fs.description}</p>
                            )}
                          </div>
                        </div>
                      )
                    })}
                </div>
              )}

              {issues.length > 0 && (
                <div className="border-t p-2 max-h-24 overflow-auto bg-red-50 dark:bg-red-950">
                  {issues.map((it, i) => (
                    <p key={i} className="text-xs text-red-600 dark:text-red-300">
                      [{it.level}] {it.key ? `${it.key}: ` : ''}
                      {it.message}
                    </p>
                  ))}
                </div>
              )}

              {/* 跨实例一致性校验结果 */}
              {crossIssues != null && (
                <div className="border-t p-2 max-h-28 overflow-auto bg-amber-50 dark:bg-amber-950">
                  {crossIssues.length === 0 ? (
                    <p className="text-xs text-green-600 dark:text-green-400">
                      {t('instanceDetail.crossCheckPass', '跨实例一致性校验通过')}
                    </p>
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

              <div className="border-t p-2 flex items-center gap-2">
                <input
                  className="flex-1 text-xs bg-muted rounded px-2 py-1"
                  placeholder={t('instanceDetail.configCommitMsg', '提交说明（可选）')}
                  value={message}
                  onChange={(e) => setMessage(e.target.value)}
                />
                <Button
                  size="sm"
                  variant="outline"
                  disabled={crossMut.isPending}
                  onClick={handleCrossCheck}
                >
                  {crossMut.isPending ? t('common.loading') : t('instanceDetail.configCrossCheck', '校验')}
                </Button>
                <Button size="sm" disabled={!dirty || saving} onClick={handleSave}>
                  {saving ? t('instanceDetail.configSaving', '保存中…') : t('common.save', '保存')}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={!dirty || saving}
                  onClick={() => {
                    if (mode === 'text') {
                      setDraft(readQ.data?.content ?? '')
                    } else {
                      const init: Record<string, string> = {}
                      if (schema) for (const k of Object.keys(schema.fields)) init[k] = valueByKey[k] ?? schema.fields[k].default ?? ''
                      setFormDraft(init)
                    }
                    setMessage('')
                  }}
                >
                  {t('instanceDetail.configRevert', '撤销')}
                </Button>
              </div>
            </>
          )}
        </div>
      </div>

      {/* 版本列表 + diff */}
      <div className="col-span-3 border rounded-lg overflow-hidden flex flex-col">
        <div className="px-3 py-2 bg-muted/50 text-sm font-medium border-b">
          {t('instanceDetail.configVersions', '历史版本')}
        </div>
        <div className="flex-1 overflow-auto">
          {selectedPath == null ? (
            <p className="text-xs text-muted-foreground p-3">—</p>
          ) : versionsQ.isLoading ? (
            <p className="text-xs text-muted-foreground p-3">{t('common.loading')}</p>
          ) : versions.length === 0 ? (
            <p className="text-xs text-muted-foreground p-3">{t('instanceDetail.noVersions', '暂无版本')}</p>
          ) : (
            <ul>
              {versions.map((v) => (
                <li key={v.id} className="border-b px-3 py-2 text-xs hover:bg-muted/30">
                  <div className="flex items-center justify-between">
                    <span className="font-medium">#{v.id}</span>
                    <div className="flex gap-1">
                      <button type="button" className="text-blue-600 hover:underline" onClick={() => setDiffFrom(v.id)}>
                        {t('instanceDetail.diffFrom', '从')}
                      </button>
                      <button type="button" className="text-blue-600 hover:underline" onClick={() => setDiffTo(v.id)}>
                        {t('instanceDetail.diffTo', '到')}
                      </button>
                      <button
                        type="button"
                        className="text-amber-600 hover:underline"
                        disabled={rollbackMut.isPending}
                        onClick={() => rollbackMut.mutate({ versionId: v.id, message: `回滚到 #${v.id}` })}
                      >
                        {t('instanceDetail.rollback', '回滚')}
                      </button>
                    </div>
                  </div>
                  <div className="text-muted-foreground truncate">
                    {v.message || '(无说明)'}
                    {v.rollbackOfVersionId ? <span className="ml-2 text-amber-600">← #{v.rollbackOfVersionId}</span> : null}
                  </div>
                  <div className="text-muted-foreground text-[10px]">{new Date(v.createdAt).toLocaleString()}</div>
                </li>
              ))}
            </ul>
          )}
        </div>
        {diffFrom != null && diffTo != null && diffFrom !== diffTo && (
          <div className="border-t p-2 max-h-48 overflow-auto bg-muted/30">
            <div className="text-xs font-medium mb-1">
              diff #{diffFrom} → #{diffTo}
            </div>
            {diffQ.isLoading ? (
              <p className="text-xs">{t('common.loading')}</p>
            ) : diffQ.data ? (
              <pre className="text-[10px] whitespace-pre-wrap">{diffQ.data.unifiedDiff}</pre>
            ) : (
              <p className="text-xs text-red-500">{diffQ.error ? (diffQ.error as Error).message : ''}</p>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
