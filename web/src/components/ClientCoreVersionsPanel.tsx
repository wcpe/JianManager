import { useState, type ChangeEvent, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Upload, RotateCcw, Pin, Loader2, Lock, ArrowUpCircle } from 'lucide-react'
import {
  useClientCoreVersions,
  useClientCorePin,
  useUploadClientCore,
  useRegisterClientCoreVersion,
  useSetClientCorePin,
  useRollbackClientCore,
  type ClientCoreVersionSummary,
} from '@/api/clientVersions'
import { cn } from '@/lib/utils'
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
import { scrollableDialogContentClass, ScrollableDialogBody } from '@/components/ui/scrollable-dialog'
import DangerConfirm from '@/components/DangerConfirm'

type ErrResp = { response?: { data?: { message?: string } } }
const errMsg = (e: unknown, fallback: string) => (e as ErrResp)?.response?.data?.message || fallback

/** 字节数转人类可读。 */
function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

/**
 * updater-core 集中版本管理面板（FR-193，见 ADR-045）。
 *
 * 楔子（wedge）冻结、单版本、不纳入管理，仅做只读展示。仅 updater-core 经 manifest agent.core
 * 做集中版本管理：上传 core jar（一份三平台通用）+ 登记版本 + 按频道 pin / 更新 / 回退坏版本。
 *
 * 两条版本轴：manifest version（内容、防降级）与 agent.core.version（core 自身、由 pin 驱动、对
 * 客户端单调只升不降）。「回退」= 以更高版本号重发旧 core 字节（不降版），客户端照常 promote 到旧内容。
 * 操作均由后端 RBAC 限平台管理员；破坏性操作（回退）走 DangerConfirm（FR-059）。
 */
export default function ClientCoreVersionsPanel({ channelId }: { channelId: string }) {
  const { t } = useTranslation()
  const { data, isLoading } = useClientCoreVersions(channelId)
  const { data: pin } = useClientCorePin(channelId)
  const setPin = useSetClientCorePin()
  const rollback = useRollbackClientCore()

  const [uploadOpen, setUploadOpen] = useState(false)
  const [rollbackTarget, setRollbackTarget] = useState<ClientCoreVersionSummary | null>(null)

  const versions = data?.versions ?? []
  const wedgeVersion = data?.wedge?.version ?? '-'
  // pinnedCoreVersion=0 表示自动用最新；effectiveVersion 为实际生效版本。
  const pinned = pin?.pinnedCoreVersion ?? 0
  const effective = pin?.effectiveVersion ?? 0

  const doSetPin = (version: number) => {
    setPin.mutate(
      { channelId, version },
      {
        onSuccess: () => toast.success(t('clientCore.pinned', '已 pin 到 core v{{n}}', { n: version })),
        onError: (e) => toast.error(errMsg(e, t('clientCore.pinFailed', '设置 pin 失败'))),
      },
    )
  }

  const doAuto = () => {
    setPin.mutate(
      { channelId, version: 0 },
      {
        onSuccess: () => toast.success(t('clientCore.autoSet', '已恢复为自动用最新 core')),
        onError: (e) => toast.error(errMsg(e, t('clientCore.pinFailed', '设置 pin 失败'))),
      },
    )
  }

  const doRollback = (v: ClientCoreVersionSummary) => {
    setRollbackTarget(null)
    rollback.mutate(
      { channelId, sourceVersion: v.version },
      {
        onSuccess: (d: { version?: number }) =>
          toast.success(t('clientCore.rolledBack', '已回退（以更高版本 v{{n}} 重发旧 core）', { n: d?.version ?? '' })),
        onError: (e) => toast.error(errMsg(e, t('clientCore.rollbackFailed', '回退失败'))),
      },
    )
  }

  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between flex-wrap gap-2">
        <p className="text-sm text-muted-foreground max-w-2xl">
          {t(
            'clientCore.subtitle',
            '集中管理 updater-core 版本：上传 core jar、按频道 pin / 更新 / 回退。楔子（wedge）固定单版本、不纳管。回退以更高版本号重发旧内容，不触发客户端防降级。',
          )}
        </p>
        <Button onClick={() => setUploadOpen(true)} className="shrink-0">
          <Upload className="size-4" /> {t('clientCore.upload', '上传 core 版本')}
        </Button>
      </div>

      {/* 当前 pin 状态 + 楔子冻结只读提示 */}
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <div className="rounded-lg border bg-card/40 p-4 space-y-1">
          <div className="flex items-center gap-2 text-sm font-medium">
            <Pin className="size-4 text-primary" /> {t('clientCore.currentPin', '当前 core pin')}
          </div>
          <div className="text-sm text-muted-foreground">
            {effective > 0 ? (
              <>
                {pinned === 0
                  ? t('clientCore.autoLatest', '自动用最新（当前 v{{n}}）', { n: effective })
                  : t('clientCore.pinnedTo', '已 pin 到 v{{n}}', { n: pinned })}
              </>
            ) : (
              t('clientCore.noneRegistered', '尚无已登记 core 版本，manifest 沿用发布时手填透传')
            )}
          </div>
          {pinned !== 0 && (
            <Button variant="ghost" size="sm" className="mt-1 h-7 px-2 text-xs" onClick={doAuto} disabled={setPin.isPending}>
              {t('clientCore.useAuto', '改为自动用最新')}
            </Button>
          )}
        </div>
        <div className="rounded-lg border bg-card/40 p-4 space-y-1">
          <div className="flex items-center gap-2 text-sm font-medium">
            <Lock className="size-4 text-muted-foreground" /> {t('clientCore.wedge', '楔子（wedge）')}
          </div>
          <div className="text-sm text-muted-foreground">
            {t('clientCore.wedgeFrozen', '版本 {{v}} · 固定单版本、不纳入版本管理（随基础包分发）', { v: wedgeVersion })}
          </div>
        </div>
      </div>

      {/* core 版本列表 */}
      <div className="border rounded-lg overflow-hidden">
        <table className="w-full">
          <thead className="bg-muted">
            <tr>
              <th className="p-3 text-left">{t('clientCore.version', '版本')}</th>
              <th className="p-3 text-left">{t('clientCore.artifact', '制品')}</th>
              <th className="p-3 text-left">{t('clientCore.size', '大小')}</th>
              <th className="p-3 text-left">{t('clientCore.note', '备注')}</th>
              <th className="p-3 text-left">{t('common.actions', '操作')}</th>
            </tr>
          </thead>
          <tbody>
            {versions.map((v) => {
              const isEffective = v.version === effective
              return (
                <tr key={v.version} className="border-t">
                  <td className="p-3">
                    <div className="flex items-center gap-2">
                      <span className="font-medium">v{v.version}</span>
                      {isEffective && (
                        <Badge variant="default" className="text-xs">
                          {t('clientCore.inUse', '生效中')}
                        </Badge>
                      )}
                      {v.sourceVersion > 0 && (
                        <Badge variant="outline" className="text-xs text-muted-foreground">
                          {t('clientCore.republished', '重发自 v{{n}}', { n: v.sourceVersion })}
                        </Badge>
                      )}
                    </div>
                  </td>
                  <td className="p-3 font-mono text-xs text-muted-foreground">{v.artifactSha256.slice(0, 12)}…</td>
                  <td className="p-3 text-xs">{formatBytes(v.artifactSize)}</td>
                  <td className="p-3 text-xs text-muted-foreground max-w-[16rem] truncate">
                    {v.note || t('clientCore.noNote', '（无备注）')}
                  </td>
                  <td className="p-3">
                    <div className="flex gap-3">
                      <button
                        className="text-primary hover:underline inline-flex items-center gap-1 disabled:opacity-40 disabled:no-underline"
                        onClick={() => doSetPin(v.version)}
                        disabled={isEffective || setPin.isPending}
                        title={isEffective ? t('clientCore.alreadyInUse', '已生效') : undefined}
                      >
                        <ArrowUpCircle className="size-3.5" /> {t('clientCore.pinHere', 'pin 此版本')}
                      </button>
                      <button
                        className="text-destructive hover:underline inline-flex items-center gap-1"
                        onClick={() => setRollbackTarget(v)}
                      >
                        <RotateCcw className="size-3.5" /> {t('clientCore.rollback', '回退到此')}
                      </button>
                    </div>
                  </td>
                </tr>
              )
            })}
            {versions.length === 0 && !isLoading && (
              <tr>
                <td colSpan={5} className="p-6 text-center text-muted-foreground">
                  {t('clientCore.empty', '暂无 core 版本，点击「上传 core 版本」开始')}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <UploadCoreDialog channelId={channelId} open={uploadOpen} onOpenChange={setUploadOpen} />

      <DangerConfirm
        open={rollbackTarget !== null}
        title={t('clientCore.rollbackConfirm', '确定回退到 core v{{n}}？', { n: rollbackTarget?.version ?? '' })}
        description={t(
          'clientCore.rollbackConfirmDesc',
          '将以更高的新版本号重发该版本 core 字节并 pin（保持 agent.core.version 单调只升，客户端照常 promote 到该旧内容、不被防降级拒绝）。用于坏 core 应急下线。',
        )}
        scope="platform"
        confirmLabel={t('clientCore.rollback', '回退到此')}
        onConfirm={() => rollbackTarget && doRollback(rollbackTarget)}
        onCancel={() => setRollbackTarget(null)}
      />
    </div>
  )
}

/**
 * 上传 core jar 模态：选 jar → 上传（内容寻址入库）→ 自动登记为新 core 版本。
 * 一份 jar 三平台通用（ADR-021），无需分平台上传；上传后即可在列表 pin / 更新。
 */
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
  const upload = useUploadClientCore()
  const register = useRegisterClientCoreVersion()

  const [file, setFile] = useState<File | null>(null)
  const [note, setNote] = useState('')
  const [pinAfter, setPinAfter] = useState(true)
  const setPin = useSetClientCorePin()

  const busy = upload.isPending || register.isPending
  const canSubmit = file !== null && !busy

  const reset = () => {
    setFile(null)
    setNote('')
    setPinAfter(true)
  }

  const onPick = (e: ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0] ?? null
    setFile(f)
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!file || busy) return
    try {
      // codec=none：上传原始 jar 字节（内容寻址元数据 sha256/size 即制品本身）。
      const meta = await upload.mutateAsync({ file, codec: 'none' })
      const reg = await register.mutateAsync({
        channelId,
        artifactSha256: meta.sha256,
        artifactSize: meta.size,
        codec: meta.codec,
        note: note.trim() || undefined,
      })
      const newVersion = (reg as { version?: number })?.version
      if (pinAfter && typeof newVersion === 'number') {
        // 默认 pin 到刚登记的新版本，即「更新」到该 core。
        await setPin.mutateAsync({ channelId, version: newVersion })
      }
      toast.success(t('clientCore.registered', '已登记 core v{{n}}', { n: newVersion ?? '' }))
      reset()
      onOpenChange(false)
    } catch (err) {
      toast.error(errMsg(err, t('clientCore.uploadFailed', '上传 / 登记 core 失败')))
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(v: boolean) => {
        if (!v) reset()
        onOpenChange(v)
      }}
    >
      <DialogContent className={cn(scrollableDialogContentClass, 'sm:max-w-lg')}>
        <DialogHeader>
          <DialogTitle>{t('clientCore.uploadTitle', '上传 updater-core 版本')}</DialogTitle>
          <DialogDescription>
            {t(
              'clientCore.uploadDesc',
              '上传一份 updater-core jar（三平台通用），登记为新版本。默认 pin 到新版本即「更新」到该 core；客户端下次启动自动升级。',
            )}
          </DialogDescription>
        </DialogHeader>
        <form id="upload-core-form" onSubmit={submit}>
          <ScrollableDialogBody className="space-y-3">
            <label className="flex flex-col gap-1 text-sm">
              {t('clientCore.coreJar', 'updater-core jar')}
              <input
                type="file"
                accept=".jar,.zst,application/java-archive,application/octet-stream"
                className="p-2 border rounded bg-background text-sm file:mr-3 file:rounded file:border-0 file:bg-muted file:px-3 file:py-1"
                onChange={onPick}
              />
              <span className="text-xs text-muted-foreground">
                {t('clientCore.coreJarHint', '一份 jar 适用 Windows / macOS / Linux，无需分平台上传。')}
              </span>
            </label>
            <label className="flex flex-col gap-1 text-sm">
              {t('clientCore.note', '备注')}
              <input
                className="p-2 border rounded bg-background"
                placeholder={t('clientCore.notePlaceholder', '如：修复 SwingProgressView NPE')}
                value={note}
                onChange={(e) => setNote(e.target.value)}
              />
            </label>
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={pinAfter} onChange={(e) => setPinAfter(e.target.checked)} />
              {t('clientCore.pinAfterUpload', '登记后立即 pin 到该版本（更新到此 core）')}
            </label>
          </ScrollableDialogBody>
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            {t('common.cancel', '取消')}
          </Button>
          <Button type="submit" form="upload-core-form" disabled={!canSubmit}>
            {busy && <Loader2 className="size-4 animate-spin" />}
            {t('clientCore.uploadAndRegister', '上传并登记')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
