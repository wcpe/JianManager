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
  useRollbackConfig,
  useConfigDiff,
  type ConfigFileInfo,
  type ConfigVersion,
  type ValidationIssue,
} from '@/api/configs'

interface ConfigEditorProps {
  instanceId: number
}

/**
 * ConfigEditor 是实例详情页的 Config tab 主体：
 * 左侧文件列表 / 中间原文编辑 / 右侧版本列表与 diff。
 */
export default function ConfigEditor({ instanceId }: ConfigEditorProps) {
  const { t } = useTranslation()
  const [selectedPath, setSelectedPath] = useState<string | null>(null)
  const [draft, setDraft] = useState<string>('')
  const [message, setMessage] = useState<string>('')
  const [diffFrom, setDiffFrom] = useState<number | null>(null)
  const [diffTo, setDiffTo] = useState<number | null>(null)

  const filesQ = useConfigFiles(instanceId)
  const readQ = useConfigRead(instanceId, selectedPath)
  const versionsQ = useConfigVersions(instanceId, selectedPath)
  const writeMut = useWriteConfig(instanceId)
  const rollbackMut = useRollbackConfig(instanceId, selectedPath)
  const diffQ = useConfigDiff(instanceId, selectedPath, diffFrom ?? undefined, diffTo ?? undefined)

  // 切换文件 / 读取完成时同步 draft
  useEffect(() => {
    if (readQ.data) {
      setDraft(readQ.data.content)
      setMessage('')
      setDiffFrom(null)
      setDiffTo(null)
    }
  }, [readQ.data?.path, readQ.data?.content])

  const files: ConfigFileInfo[] = filesQ.data ?? []
  const versions: ConfigVersion[] = versionsQ.data ?? []
  const dirty = useMemo(() => readQ.data && draft !== readQ.data.content, [draft, readQ.data])
  const issues: ValidationIssue[] = readQ.data?.validation?.issues ?? []

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
        <div className="px-3 py-2 bg-muted/50 text-sm font-medium border-b flex items-center justify-between">
          <span className="truncate">{selectedPath ?? t('instanceDetail.configSelectFile', '请选择文件')}</span>
          <div className="flex items-center gap-2">
            {readQ.data && (
              <Badge variant={readQ.data.validation.valid ? 'secondary' : 'destructive'}>
                {readQ.data.validation.valid ? 'valid' : 'invalid'}
              </Badge>
            )}
            {dirty && <Badge variant="outline">未保存</Badge>}
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
              <Textarea
                className="flex-1 font-mono text-xs resize-none"
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                spellCheck={false}
              />
              {issues.length > 0 && (
                <div className="border-t p-2 max-h-32 overflow-auto bg-red-50 dark:bg-red-950">
                  {issues.map((it, i) => (
                    <p key={i} className="text-xs text-red-600 dark:text-red-300">
                      [{it.level}] {it.key ? `${it.key}: ` : ''}
                      {it.message}
                    </p>
                  ))}
                </div>
              )}
              <div className="border-t p-2 flex items-center gap-2">
                <input
                  className="flex-1 text-xs bg-muted rounded px-2 py-1"
                  placeholder="提交说明（可选）"
                  value={message}
                  onChange={(e) => setMessage(e.target.value)}
                />
                <Button
                  size="sm"
                  disabled={!dirty || writeMut.isPending}
                  onClick={() => writeMut.mutate({ path: selectedPath, content: draft, message })}
                >
                  {writeMut.isPending ? '保存中…' : '保存'}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={!dirty || writeMut.isPending}
                  onClick={() => {
                    setDraft(readQ.data?.content ?? '')
                    setMessage('')
                  }}
                >
                  撤销
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
                <li
                  key={v.id}
                  className="border-b px-3 py-2 text-xs hover:bg-muted/30"
                >
                  <div className="flex items-center justify-between">
                    <span className="font-medium">#{v.id}</span>
                    <div className="flex gap-1">
                      <button
                        type="button"
                        className="text-blue-600 hover:underline"
                        onClick={() => setDiffFrom(v.id)}
                      >
                        从
                      </button>
                      <button
                        type="button"
                        className="text-blue-600 hover:underline"
                        onClick={() => setDiffTo(v.id)}
                      >
                        到
                      </button>
                      <button
                        type="button"
                        className="text-amber-600 hover:underline"
                        disabled={rollbackMut.isPending}
                        onClick={() => rollbackMut.mutate({ versionId: v.id, message: `回滚到 #${v.id}` })}
                      >
                        回滚
                      </button>
                    </div>
                  </div>
                  <div className="text-muted-foreground truncate">
                    {v.message || '(无说明)'}
                    {v.rollbackOfVersionId ? (
                      <span className="ml-2 text-amber-600">← #{v.rollbackOfVersionId}</span>
                    ) : null}
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