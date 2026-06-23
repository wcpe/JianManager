import { useState, type ChangeEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Upload, RotateCcw, Eye, Trash2, Loader2 } from 'lucide-react'
import {
  useClientVersions,
  useClientVersion,
  usePublishClientFile,
  usePublishClientVersion,
  useRollbackClientVersion,
  type ClientVersionSummary,
  type ManifestFile,
} from '@/api/clientVersions'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@/components/ui/scrollable-dialog'
import DangerConfirm from '@/components/DangerConfirm'

type ErrResp = { response?: { data?: { message?: string } } }
const errMsg = (e: unknown, fallback: string) => (e as ErrResp)?.response?.data?.message || fallback

/** 平台「全部」哨兵（Radix Select 不允许空字符串值，提交时映射回 ""）。 */
const PLATFORM_ALL = '__all__'

/** 字节数转人类可读。 */
function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

/**
 * 客户端分发版本管理面板（FR-088，见 ADR-022）。
 * 历史列表 + 版本详情查看 + 运营回滚（二次确认 FR-059）+ 完整发布向导。
 * 历史仅管理面可见（玩家只认 latest）；发布/回滚由后端 RBAC 限平台管理员。
 */
export default function ClientVersionsPanel({ channelId }: { channelId: string }) {
  const { t } = useTranslation()
  const { data: versions, isLoading } = useClientVersions(channelId)
  const rollback = useRollbackClientVersion()

  const [showWizard, setShowWizard] = useState(false)
  const [detailVersion, setDetailVersion] = useState<number | null>(null)
  const [rollbackTarget, setRollbackTarget] = useState<ClientVersionSummary | null>(null)

  const doRollback = (v: ClientVersionSummary) => {
    setRollbackTarget(null)
    rollback.mutate(
      { channelId, sourceVersion: v.version },
      {
        onSuccess: (d: { version?: number }) =>
          toast.success(t('clientVersions.rolledBack', '已回滚（重发为 v{{n}}）', { n: d?.version ?? '' })),
        onError: (e) => toast.error(errMsg(e, t('clientVersions.rollbackFailed', '回滚失败'))),
      },
    )
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between flex-wrap gap-2">
        <p className="text-sm text-muted-foreground max-w-2xl">
          {t('clientVersions.subtitle', '历史版本仅管理台可见；玩家侧只拉取 latest。回滚以更高版本号重发旧内容，不会触发客户端防降级。')}
        </p>
        <Button onClick={() => setShowWizard(true)} className="shrink-0">
          <Upload className="size-4" /> {t('clientVersions.publish', '发布新版本')}
        </Button>
      </div>

      <div className="border rounded-lg overflow-hidden">
        <table className="w-full">
          <thead className="bg-muted">
            <tr>
              <th className="p-3 text-left">{t('clientVersions.version', '版本')}</th>
              <th className="p-3 text-left">{t('clientVersions.fileCount', '文件数')}</th>
              <th className="p-3 text-left">{t('clientVersions.note', '备注')}</th>
              <th className="p-3 text-left">{t('clientVersions.createdAt', '发布时间')}</th>
              <th className="p-3 text-left">{t('common.actions', '操作')}</th>
            </tr>
          </thead>
          <tbody>
            {(versions ?? []).map((v) => (
              <tr key={v.version} className="border-t">
                <td className="p-3">
                  <span className="font-mono">v{v.version}</span>
                  {v.isLatest && (
                    <Badge className="ml-2" variant="default">{t('clientVersions.latest', 'latest')}</Badge>
                  )}
                </td>
                <td className="p-3">{v.fileCount}</td>
                <td className="p-3 max-w-[20rem] truncate" title={v.note}>{v.note || '-'}</td>
                <td className="p-3 text-xs">{new Date(v.createdAt).toLocaleString()}</td>
                <td className="p-3">
                  <div className="flex gap-3">
                    <button
                      className="text-primary hover:underline inline-flex items-center gap-1"
                      onClick={() => setDetailVersion(v.version)}
                    >
                      <Eye className="size-3.5" /> {t('clientVersions.view', '查看')}
                    </button>
                    <button
                      className="text-amber-600 dark:text-amber-500 hover:underline inline-flex items-center gap-1 disabled:opacity-40"
                      onClick={() => setRollbackTarget(v)}
                      disabled={v.isLatest}
                      title={v.isLatest ? t('clientVersions.alreadyLatest', '已是 latest') : ''}
                    >
                      <RotateCcw className="size-3.5" /> {t('clientVersions.rollback', '回滚')}
                    </button>
                  </div>
                </td>
              </tr>
            ))}
            {(!versions || versions.length === 0) && !isLoading && (
              <tr>
                <td colSpan={5} className="p-3 text-center text-muted-foreground">
                  {t('clientVersions.empty', '暂无版本，点击「发布新版本」开始')}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {showWizard && (
        <PublishWizard channelId={channelId} onClose={() => setShowWizard(false)} />
      )}

      {detailVersion !== null && (
        <VersionDetailDialog
          channelId={channelId}
          version={detailVersion}
          onClose={() => setDetailVersion(null)}
        />
      )}

      <DangerConfirm
        open={rollbackTarget !== null}
        title={t('clientVersions.rollbackConfirm', '确定回滚到 v{{n}}？', { n: rollbackTarget?.version ?? '' })}
        description={t('clientVersions.rollbackConfirmDesc', '将以更高的新版本号重发该版本内容为 latest（保持版本单调，客户端正常前进、不被防降级拒绝）。')}
        scope="platform"
        confirmLabel={t('clientVersions.rollback', '回滚')}
        onConfirm={() => rollbackTarget && doRollback(rollbackTarget)}
        onCancel={() => setRollbackTarget(null)}
      />
    </div>
  )
}

/** 发布向导草稿文件项（artifact 元数据 + 原始内容元数据；codec=none 时两者同值）。 */
interface DraftFile {
  filename: string
  path: string
  sync: ManifestFile['sync']
  platform: ManifestFile['platform']
  sha256: string
  md5: string
  size: number
  codec: string
}

/**
 * 完整发布向导：上传文件（codec=none，服务端入库即算 sha256/md5/size 自动填充）→
 * 逐文件设 path/sync/platform → 设 managedDirs + 备注 → 发布。
 * zstd 压缩打包不在本期（FR-097）；向导产出 codec=none 版本。
 */
function PublishWizard({ channelId, onClose }: { channelId: string; onClose: () => void }) {
  const { t } = useTranslation()
  const uploadFile = usePublishClientFile()
  const publish = usePublishClientVersion()

  const [drafts, setDrafts] = useState<DraftFile[]>([])
  const [managedDirs, setManagedDirs] = useState('mods, config, resourcepacks')
  const [note, setNote] = useState('')
  const [uploading, setUploading] = useState(false)

  const onPickFiles = async (e: ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(e.target.files ?? [])
    e.target.value = '' // 允许重复选择同名文件再次触发 change
    if (files.length === 0) return
    setUploading(true)
    try {
      for (const file of files) {
        const res = await uploadFile.mutateAsync({ channelId, file, codec: 'none' })
        setDrafts((prev) => [
          ...prev,
          {
            filename: file.name,
            path: file.name,
            sync: 'strict',
            platform: '',
            sha256: res.sha256,
            md5: res.md5,
            size: res.size,
            codec: res.codec,
          },
        ])
      }
    } catch (err) {
      toast.error(errMsg(err, t('clientVersions.uploadFailed', '上传文件失败')))
    } finally {
      setUploading(false)
    }
  }

  const patchDraft = (i: number, patch: Partial<DraftFile>) =>
    setDrafts((prev) => prev.map((d, idx) => (idx === i ? { ...d, ...patch } : d)))

  const removeDraft = (i: number) => setDrafts((prev) => prev.filter((_, idx) => idx !== i))

  const parseManagedDirs = (): string[] => {
    const seen = new Set<string>()
    return managedDirs
      .split(/[\n,]/)
      .map((s) => s.trim().replace(/\/+$/, ''))
      .filter((s) => s !== '' && !seen.has(s) && (seen.add(s), true))
  }

  const pathsValid = drafts.every((d) => d.path.trim() !== '' && !d.path.startsWith('/') && !d.path.includes('..'))
  const canPublish = drafts.length > 0 && pathsValid && !uploading && !publish.isPending

  const doPublish = async () => {
    if (!canPublish) return
    // codec=none：file 原始内容元数据 = artifact 元数据（上传的就是原始文件）。
    const files: ManifestFile[] = drafts.map((d) => ({
      path: d.path.trim(),
      sha256: d.sha256,
      md5: d.md5,
      size: d.size,
      sync: d.sync,
      platform: d.platform,
      artifact: { sha256: d.sha256, size: d.size, codec: d.codec },
    }))
    try {
      const res = await publish.mutateAsync({ channelId, files, managedDirs: parseManagedDirs(), note })
      toast.success(t('clientVersions.published', '已发布 v{{n}}', { n: (res as { version?: number })?.version ?? '' }))
      onClose()
    } catch (err) {
      toast.error(errMsg(err, t('clientVersions.publishFailed', '发布版本失败')))
    }
  }

  return (
    <Dialog open onOpenChange={(v: boolean) => { if (!v) onClose() }}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-3xl`}>
        <DialogHeader>
          <DialogTitle>{t('clientVersions.publish', '发布新版本')}</DialogTitle>
          <DialogDescription>
            {t('clientVersions.wizardDesc', '上传客户端文件（自动计算校验和），设置每个文件的路径与同步策略，再发布。本期为未压缩（codec=none）发布。')}
          </DialogDescription>
        </DialogHeader>

        <ScrollableDialogBody className="space-y-4">
          <div>
            <label className="inline-flex items-center gap-2 px-4 py-2 border rounded-md hover:bg-accent cursor-pointer text-sm">
              {uploading ? <Loader2 className="size-4 animate-spin" /> : <Upload className="size-4" />}
              {t('clientVersions.addFiles', '添加文件')}
              <input type="file" multiple className="hidden" onChange={onPickFiles} disabled={uploading} />
            </label>
            <span className="ml-2 text-xs text-muted-foreground">
              {t('clientVersions.filesCount', '{{n}} 个文件', { n: drafts.length })}
            </span>
          </div>

          {drafts.length > 0 && (
            <div className="border rounded-lg overflow-x-auto">
              <table className="w-full text-sm">
                <thead className="bg-muted">
                  <tr>
                    <th className="p-2 text-left">{t('clientVersions.path', '路径')}</th>
                    <th className="p-2 text-left">{t('clientVersions.sync', '同步')}</th>
                    <th className="p-2 text-left">{t('clientVersions.platform', '平台')}</th>
                    <th className="p-2 text-left">{t('clientVersions.size', '大小')}</th>
                    <th className="p-2"></th>
                  </tr>
                </thead>
                <tbody>
                  {drafts.map((d, i) => (
                    <tr key={`${d.sha256}-${i}`} className="border-t align-top">
                      <td className="p-2">
                        <input
                          className="w-full p-1.5 border rounded bg-background font-mono text-xs aria-invalid:border-destructive"
                          value={d.path}
                          aria-invalid={d.path.trim() === '' || d.path.startsWith('/') || d.path.includes('..')}
                          onChange={(e) => patchDraft(i, { path: e.target.value })}
                        />
                        <span className="text-[10px] text-muted-foreground">{d.filename}</span>
                      </td>
                      <td className="p-2">
                        <Select value={d.sync} onValueChange={(v: string) => patchDraft(i, { sync: v as ManifestFile['sync'] })}>
                          <SelectTrigger size="sm" className="w-28"><SelectValue /></SelectTrigger>
                          <SelectContent>
                            <SelectItem value="strict">{t('clientVersions.syncStrict', 'strict 强制')}</SelectItem>
                            <SelectItem value="once">{t('clientVersions.syncOnce', 'once 仅缺失')}</SelectItem>
                            <SelectItem value="ignore">{t('clientVersions.syncIgnore', 'ignore 不动')}</SelectItem>
                          </SelectContent>
                        </Select>
                      </td>
                      <td className="p-2">
                        <Select
                          value={d.platform === '' ? PLATFORM_ALL : d.platform}
                          onValueChange={(v: string) => patchDraft(i, { platform: (v === PLATFORM_ALL ? '' : v) as ManifestFile['platform'] })}
                        >
                          <SelectTrigger size="sm" className="w-28"><SelectValue /></SelectTrigger>
                          <SelectContent>
                            <SelectItem value={PLATFORM_ALL}>{t('clientVersions.platformAll', '全平台')}</SelectItem>
                            <SelectItem value="windows">windows</SelectItem>
                            <SelectItem value="macos">macos</SelectItem>
                            <SelectItem value="linux">linux</SelectItem>
                          </SelectContent>
                        </Select>
                      </td>
                      <td className="p-2 text-xs text-muted-foreground whitespace-nowrap">{formatBytes(d.size)}</td>
                      <td className="p-2">
                        <button className="text-destructive hover:opacity-70" onClick={() => removeDraft(i)} aria-label={t('common.delete', '删除')}>
                          <Trash2 className="size-4" />
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          <label className="flex flex-col gap-1 text-sm">
            {t('clientVersions.managedDirs', '托管目录')}
            <input
              className="p-2 border rounded bg-background font-mono text-xs"
              value={managedDirs}
              onChange={(e) => setManagedDirs(e.target.value)}
              placeholder="mods, config, resourcepacks"
            />
            <span className="text-xs text-muted-foreground">
              {t('clientVersions.managedDirsHint', '逗号/换行分隔。仅这些目录内会删除「本地有但清单未列」的文件（减量）。')}
            </span>
          </label>

          <label className="flex flex-col gap-1 text-sm">
            {t('clientVersions.note', '备注')}
            <input
              className="p-2 border rounded bg-background"
              value={note}
              onChange={(e) => setNote(e.target.value)}
              placeholder={t('clientVersions.notePlaceholder', '如：更新 mods 至 1.20.4')}
            />
          </label>
        </ScrollableDialogBody>

        <DialogFooter>
          <Button variant="outline" onClick={onClose}>{t('common.cancel', '取消')}</Button>
          <Button disabled={!canPublish} onClick={doPublish}>
            {publish.isPending && <Loader2 className="size-4 animate-spin" />}
            {t('clientVersions.publish', '发布新版本')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** 版本详情弹窗：展示某历史版本的托管目录与文件清单（只读）。 */
function VersionDetailDialog({ channelId, version, onClose }: { channelId: string; version: number; onClose: () => void }) {
  const { t } = useTranslation()
  const { data: detail, isLoading } = useClientVersion(channelId, version)

  return (
    <Dialog open onOpenChange={(v: boolean) => { if (!v) onClose() }}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-3xl`}>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <span className="font-mono">v{version}</span>
            {detail?.isLatest && <Badge variant="default">{t('clientVersions.latest', 'latest')}</Badge>}
          </DialogTitle>
          <DialogDescription>{detail?.note || t('clientVersions.noNote', '（无备注）')}</DialogDescription>
        </DialogHeader>

        <ScrollableDialogBody className="space-y-4">
          <div className="text-sm">
            <span className="text-muted-foreground">{t('clientVersions.managedDirs', '托管目录')}：</span>
            <span className="font-mono text-xs">{(detail?.managedDirs ?? []).join(', ') || '-'}</span>
          </div>
          <div className="border rounded-lg overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="bg-muted">
                <tr>
                  <th className="p-2 text-left">{t('clientVersions.path', '路径')}</th>
                  <th className="p-2 text-left">{t('clientVersions.sync', '同步')}</th>
                  <th className="p-2 text-left">{t('clientVersions.platform', '平台')}</th>
                  <th className="p-2 text-left">{t('clientVersions.size', '大小')}</th>
                  <th className="p-2 text-left">sha256</th>
                </tr>
              </thead>
              <tbody>
                {(detail?.files ?? []).map((f) => (
                  <tr key={f.path} className="border-t">
                    <td className="p-2 font-mono text-xs">{f.path}</td>
                    <td className="p-2">{f.sync}</td>
                    <td className="p-2">{f.platform || t('clientVersions.platformAll', '全平台')}</td>
                    <td className="p-2 text-xs whitespace-nowrap">{formatBytes(f.size)}</td>
                    <td className="p-2 font-mono text-[10px]" title={f.sha256}>{f.sha256.slice(0, 12)}…</td>
                  </tr>
                ))}
                {!isLoading && (detail?.files ?? []).length === 0 && (
                  <tr><td colSpan={5} className="p-3 text-center text-muted-foreground">{t('clientVersions.noFiles', '无文件')}</td></tr>
                )}
              </tbody>
            </table>
          </div>
        </ScrollableDialogBody>

        <DialogFooter>
          <Button onClick={onClose}>{t('common.close', '关闭')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
