import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Check, History, Upload } from 'lucide-react'
import { useUpdaterCoreVersions, useSelectUpdaterCore, useUploadUpdaterCore } from '@/api/clientChannels'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import DangerConfirm from '@/components/DangerConfirm'

type ErrResp = { response?: { data?: { message?: string } } }
const errMsg = (e: unknown, fallback: string) => (e as ErrResp)?.response?.data?.message || fallback

/**
 * updater-core 版本选择器（FR-259）。频道工作台「Core 版本」tab：
 * 列出所有归档 core 版本，当前选定版本高亮，一键切换实现回滚。
 * 切换后提示"客户端下次启动生效"。
 */
export default function ClientUpdaterCoreSelector({ channelId }: { channelId: string }) {
  const { t } = useTranslation()
  const { data: versions, isLoading } = useUpdaterCoreVersions(channelId)
  const select = useSelectUpdaterCore()
  const [target, setTarget] = useState<string | null>(null)
  const [uploadOpen, setUploadOpen] = useState(false)

  const selectedVersion = versions?.find((v) => v.selected)
  const latestVersion = versions?.[0]

  const doSelect = () => {
    if (!target) return
    const sha = target
    setTarget(null)
    select.mutate(
      { channelId, sha256: sha },
      {
        onSuccess: () => toast.success(t('clientCore.switched', '已切换，客户端下次启动生效')),
        onError: (e) => toast.error(errMsg(e, t('clientCore.switchFailed', '切换失败'))),
      },
    )
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between flex-wrap gap-2">
        <p className="text-sm text-muted-foreground max-w-2xl">
          {t(
            'clientCore.subtitle',
            '列出所有归档的 updater-core 版本。切换选定版本后，客户端下次启动按 endpoint 自动查询并使用该版本——用于坏 core 应急回滚。',
          )}
        </p>
        <Button variant="outline" onClick={() => setUploadOpen(true)}>
          <Upload className="size-4" /> {t('clientCore.upload', '上传 updater-core.jar')}
        </Button>
      </div>

      <div className="grid gap-2 sm:grid-cols-2">
        <div className="rounded-lg border bg-card p-3">
          <div className="text-[11px] text-muted-foreground">{t('clientCore.latestArchived', '最新归档版本')}</div>
          <div className="mt-1 font-mono text-lg font-semibold">
            {latestVersion ? displayCoreVersion(latestVersion) : '—'}
          </div>
          <div className="mt-1 text-[11px] text-muted-foreground">
            {latestVersion ? `${latestVersion.sha256.slice(0, 12)}… · ${formatBytes(latestVersion.size)}` : t('clientCore.noVersionsShort', '暂无归档')}
          </div>
        </div>
        <div className="rounded-lg border bg-card p-3">
          <div className="text-[11px] text-muted-foreground">{t('clientCore.currentSelected', '当前选定版本')}</div>
          <div className="mt-1 font-mono text-lg font-semibold">
            {selectedVersion ? displayCoreVersion(selectedVersion) : latestVersion ? t('clientCore.followLatest', '跟随最新') : '—'}
          </div>
          <div className="mt-1 text-[11px] text-muted-foreground">
            {selectedVersion
              ? `${selectedVersion.sha256.slice(0, 12)}… · ${formatBytes(selectedVersion.size)}`
              : t('clientCore.followLatestHint', '频道未固定版本时，客户端使用最新归档 updater-core。')}
          </div>
        </div>
      </div>

      <div className="overflow-hidden rounded-lg border">
        <Table>
          <TableHeader className="bg-muted/50">
            <TableRow>
              <TableHead>{t('clientCore.colVersion', '版本')}</TableHead>
              <TableHead>{t('clientCore.colSha256', 'SHA256')}</TableHead>
              <TableHead>{t('clientCore.colSize', '大小')}</TableHead>
              <TableHead>{t('clientCore.colCreatedAt', '归档时间')}</TableHead>
              <TableHead className="text-right">{t('common.actions', '操作')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {versions?.map((v) => (
              <TableRow key={v.sha256} className={v.selected ? 'bg-primary/5' : undefined}>
                <TableCell className="font-medium">
                  <span className="flex items-center gap-2">
                    <History className="size-3.5 text-muted-foreground" />
                    {displayCoreVersion(v)}
                  </span>
                </TableCell>
                <TableCell className="font-mono text-xs text-muted-foreground">
                  <div>{v.sha256.slice(0, 12)}…</div>
                  <div className="mt-1 flex items-center gap-1 text-[11px]">
                    <span>{v.gitCommit ? `${v.gitCommit.slice(0, 12)}${v.dirty ? '.dirty' : ''}` : '—'}</span>
                    {v.dirty ? <Badge variant="outline">dirty</Badge> : null}
                  </div>
                </TableCell>
                <TableCell className="text-xs">{formatBytes(v.size)}</TableCell>
                <TableCell className="text-xs">{new Date(v.createdAt).toLocaleString()}</TableCell>
                <TableCell>
                  <div className="flex justify-end items-center gap-2">
                    {v.selected ? (
                      <Badge variant="default" className="gap-1">
                        <Check className="size-3" /> {t('clientCore.selected', '当前选定')}
                      </Badge>
                    ) : (
                      <Button
                        variant="outline"
                        size="xs"
                        disabled={select.isPending}
                        onClick={() => setTarget(v.sha256)}
                      >
                        {t('clientCore.select', '选定')}
                      </Button>
                    )}
                  </div>
                </TableCell>
              </TableRow>
            ))}
            {versions?.length === 0 && !isLoading && (
              <TableRow>
                <TableCell colSpan={5} className="h-16 text-center text-muted-foreground">
                  {t('clientCore.noVersions', '暂无归档版本（需 make embed-client-updater 构建后启动 CP 归档）')}
                </TableCell>
              </TableRow>
            )}
            {isLoading && (
              <TableRow>
                <TableCell colSpan={5} className="h-16 text-center text-muted-foreground">
                  {t('common.loading', '加载中…')}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>

      <UploadCoreDialog channelId={channelId} open={uploadOpen} onOpenChange={setUploadOpen} />

      <DangerConfirm
        open={target !== null}
        title={t('clientCore.switchConfirm', '切换 updater-core 版本？')}
        description={t(
          'clientCore.switchDesc',
          '切换后客户端下次启动按 endpoint 自动查询并使用该版本。本地已有该版本 jar 的客户端直接用、没有的自动下载。请确认确需切换。',
        )}
        scope="platform"
        confirmLabel={t('clientCore.select', '选定')}
        onConfirm={doSelect}
        onCancel={() => setTarget(null)}
      />
    </div>
  )
}

function UploadCoreDialog({
  channelId,
  open,
  onOpenChange,
}: {
  channelId: string
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation()
  const upload = useUploadUpdaterCore()
  const [file, setFile] = useState<File | null>(null)
  const [version, setVersion] = useState('')
  const [select, setSelect] = useState(true)

  const reset = () => {
    setFile(null)
    setVersion('')
    setSelect(true)
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!file || upload.isPending) return
    try {
      const res = await upload.mutateAsync({ channelId, file, version, select })
      toast.success(
        t('clientCore.uploaded', 'updater-core 已上传：v{{version}} {{sha}}', {
          version: res.version,
          sha: `${res.sha256.slice(0, 12)}…`,
        }),
      )
      reset()
      onOpenChange(false)
    } catch (e) {
      toast.error(errMsg(e, t('clientCore.uploadFailed', '上传 updater-core 失败')))
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!v) reset()
        onOpenChange(v)
      }}
    >
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('clientCore.uploadTitle', '上传 updater-core.jar')}</DialogTitle>
          <DialogDescription>
            {t(
              'clientCore.uploadDesc',
              '用于紧急 hotfix：上传后会归档为 client-updater-core 制品，可选择立即作为当前频道版本。',
            )}
          </DialogDescription>
        </DialogHeader>
        <form id="upload-core-form" className="space-y-4" onSubmit={submit}>
          <label className="flex flex-col gap-1 text-sm">
            {t('clientCore.jarFile', 'Jar 文件')}
            <input
              type="file"
              accept=".jar,application/java-archive"
              className="p-2 border rounded bg-background"
              onChange={(e) => setFile(e.target.files?.[0] ?? null)}
            />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            {t('clientCore.version', '版本号')}
            <input
              className="p-2 border rounded bg-background"
              placeholder="9"
              value={version}
              onChange={(e) => setVersion(e.target.value)}
            />
            <span className="text-xs text-muted-foreground">
              {t('clientCore.versionHint', '通常无需填写，后端优先读取 jar 内版本；紧急 hotfix 缺少元信息也可上传。')}
            </span>
          </label>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={select} onChange={(e) => setSelect(e.target.checked)} />
            {t('clientCore.selectAfterUpload', '上传后立即选为当前频道版本')}
          </label>
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel', '取消')}
          </Button>
          <Button type="submit" form="upload-core-form" disabled={!file || upload.isPending}>
            {upload.isPending ? t('common.uploading', '上传中…') : t('clientCore.upload', '上传 updater-core.jar')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function displayCoreVersion(v: { version: number; displayVersion?: string }): string {
  return v.displayVersion || `v${v.version}`
}

/** formatBytes 把字节数格式化为人类可读（KB/MB）。 */
function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}
