import { useState } from 'react'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Upload, RotateCcw, Eye } from 'lucide-react'
import {
  useClientVersions,
  useClientVersion,
  useRollbackClientVersion,
  type ClientVersionSummary,
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
import { scrollableDialogContentClass, ScrollableDialogBody } from '@/components/ui/scrollable-dialog'
import DangerConfirm from '@/components/DangerConfirm'
import ClientFileTree from '@/components/ClientFileTree'

type ErrResp = { response?: { data?: { message?: string } } }
const errMsg = (e: unknown, fallback: string) => (e as ErrResp)?.response?.data?.message || fallback

/**
 * 客户端分发版本管理面板（FR-088，见 ADR-022）。
 * 历史列表 + 版本详情查看 + 运营回滚（二次确认 FR-059）。
 * 历史仅管理面可见（玩家只认 latest）；发布/回滚由后端 RBAC 限平台管理员。
 *
 * 发布走独立页面（FR-191 纠正：原模态向导点遮罩会丢草稿）：「发布新版本」按钮
 * 导航到 `/client-channels/:id/publish`（{@link ClientPublishPage}），不再在此开模态。
 */
export default function ClientVersionsPanel({ channelId }: { channelId: string }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data: versions, isLoading } = useClientVersions(channelId)
  const rollback = useRollbackClientVersion()

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
        <Button onClick={() => navigate(`/client-channels/${encodeURIComponent(channelId)}/publish`)} className="shrink-0">
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

/** 版本详情弹窗：展示某历史版本的托管目录与文件清单（只读，一次性展示属 ui-modals 例外）。 */
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
          {isLoading ? (
            <p className="text-sm text-muted-foreground">{t('common.loading', '加载中…')}</p>
          ) : (
            <ClientFileTree files={detail?.files ?? []} readonly />
          )}
        </ScrollableDialogBody>

        <DialogFooter>
          <Button onClick={onClose}>{t('common.close', '关闭')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
