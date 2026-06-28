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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
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

      <div className="overflow-hidden rounded-lg border">
        <Table>
          <TableHeader className="bg-muted/50">
            <TableRow>
              <TableHead>{t('clientVersions.version', '版本')}</TableHead>
              <TableHead>{t('clientVersions.fileCount', '文件数')}</TableHead>
              <TableHead>{t('clientVersions.note', '备注')}</TableHead>
              <TableHead>{t('clientVersions.createdAt', '发布时间')}</TableHead>
              <TableHead className="text-right">{t('common.actions', '操作')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {(versions ?? []).map((v) => (
              <TableRow key={v.version}>
                <TableCell>
                  <span className="font-mono">v{v.version}</span>
                  {v.isLatest && (
                    <Badge className="ml-2" variant="default">{t('clientVersions.latest', 'latest')}</Badge>
                  )}
                </TableCell>
                <TableCell>{v.fileCount}</TableCell>
                <TableCell className="max-w-[20rem] truncate" title={v.note}>{v.note || '-'}</TableCell>
                <TableCell className="text-xs">{new Date(v.createdAt).toLocaleString()}</TableCell>
                <TableCell className="text-right">
                  <div className="flex justify-end gap-1">
                    <Button variant="ghost" size="xs" onClick={() => setDetailVersion(v.version)}>
                      <Eye className="size-3.5" /> {t('clientVersions.view', '查看')}
                    </Button>
                    <Button
                      variant="ghost"
                      size="xs"
                      onClick={() => setRollbackTarget(v)}
                      disabled={v.isLatest}
                      title={v.isLatest ? t('clientVersions.alreadyLatest', '已是 latest') : ''}
                    >
                      <RotateCcw className="size-3.5" /> {t('clientVersions.rollback', '回滚')}
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
            {(!versions || versions.length === 0) && !isLoading && (
              <TableRow>
                <TableCell colSpan={5} className="h-16 text-center text-muted-foreground">
                  {t('clientVersions.empty', '暂无版本，点击「发布新版本」开始')}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
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
