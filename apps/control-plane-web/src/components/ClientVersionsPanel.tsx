import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { ClientVersionsPanelView } from '@/components/views/client-dist/ClientVersionsPanelView'
import {
  useClientVersions,
  useClientVersion,
  useRollbackClientVersion,
} from '@/api/clientVersions'
import EmbeddedUpdaterSummary from '@/components/EmbeddedUpdaterSummary'
import FileBrowser from '@/components/file-browser/FileBrowser'
import { clientDistSource, manifestFilesToDistFiles } from '@/components/file-browser/sources/clientDistSource'
import { useDangerPermission } from '@/lib/danger'

type ErrResp = { response?: { data?: { message?: string } } }
const errMsg = (e: unknown, fallback: string) => (e as ErrResp)?.response?.data?.message || fallback

/**
 * 客户端分发版本管理面板（FR-088，见 ADR-022）的接线层（ADR-097）。
 *
 * 展示层已归包（`ClientVersionsPanelView`），此处只保留应用侧职责：
 * - 取数：版本列表（`useClientVersions`）与选中版本详情（`useClientVersion(channelId, detailVersion)`；
 *   选中版本由本层持有，因为它是详情查询的入参）；
 * - 写操作：回滚 mutation（`useRollbackClientVersion`）与成功/失败 toast；
 * - 门禁：回滚限平台管理员（FR-059），读角色等级后经 `dangerAllowed` 注入；
 * - 路由：发布走独立页面（FR-191），「发布新版本」导航到 `/client-channels/:id/publish`；
 * - 应用侧接线层注入：内嵌更新器摘要（自行取数）与制品内容 `FileBrowser`（注入主题 + 管理面制品端点），
 *   二者经槽传入视图，包内不 import 应用侧模块。
 */
export default function ClientVersionsPanel({ channelId }: { channelId: string }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data: versions, isLoading } = useClientVersions(channelId)
  const rollback = useRollbackClientVersion()
  const { allowed: dangerAllowed } = useDangerPermission('platform')

  // 选中版本（null=无详情弹窗）：容器持有——它是详情取数的入参。
  const [detailVersion, setDetailVersion] = useState<number | null>(null)
  const { data: detail, isLoading: detailLoading } = useClientVersion(channelId, detailVersion)

  // 预览数据源：把版本清单映射为客户端分发数据源（按文件 path → artifact sha 取内容/下载）。
  const previewSource = useMemo(
    () => clientDistSource(channelId, manifestFilesToDistFiles(detail?.files ?? []), {
      noArtifactContent: t('clientVersions.artifactNoContent', '该文件没有可预览的制品内容。'),
      artifactMissing: t('clientVersions.artifactMissing', '制品内容不存在，可能已从制品库删除；该版本清单仍保留，但无法预览或下载。'),
      artifactPreviewFailed: t('clientVersions.artifactPreviewFailed', '读取制品内容失败。'),
    }),
    [channelId, detail?.files, t],
  )

  /** 运营回滚：以更高版本号重发历史版本内容（保持单调，不触发客户端防降级）。 */
  const doRollback = (version: number) => {
    rollback.mutate(
      { channelId, sourceVersion: version },
      {
        onSuccess: (d: { version?: number }) =>
          toast.success(t('clientVersions.rolledBack', '已回滚（重发为 v{{n}}）', { n: d?.version ?? '' })),
        onError: (e) => toast.error(errMsg(e, t('clientVersions.rollbackFailed', '回滚失败'))),
      },
    )
  }

  return (
    <ClientVersionsPanelView
      versions={versions}
      isLoading={isLoading}
      detailVersion={detailVersion}
      onDetailVersionChange={setDetailVersion}
      detail={detail}
      detailLoading={detailLoading}
      dangerAllowed={dangerAllowed}
      onRollback={doRollback}
      onPublishNewVersion={() => navigate(`/client-channels/${encodeURIComponent(channelId)}/publish`)}
      updaterSummarySlot={<EmbeddedUpdaterSummary />}
      previewSlot={<FileBrowser source={previewSource} className="h-[460px]" />}
    />
  )
}
