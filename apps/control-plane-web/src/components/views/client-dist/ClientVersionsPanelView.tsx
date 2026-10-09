/**
 * @file ClientVersionsPanelView：客户端分发「版本」Tab 面板（版本历史列表 + 版本详情 + 回滚二次确认）的受控视图。
 *       版本列表/详情取数、回滚 mutation 与 toast、发布页跳转、以及两个应用侧接线层（内嵌更新器摘要、
 *       制品内容 FileBrowser）均由应用容器注入。
 * @input lib/client-publish-wizard（ManifestFileLike）、views/client-dist/ClientFileTree（结构树）、
 *        views/DangerConfirm（回滚二次确认）、Badge/Button/Table/Dialog 原语、
 *        scrollable-dialog（超高内部滚动）、lucide 图标、翻译上下文
 * @output ClientVersionsPanelView、ClientVersionsPanelViewProps、ClientVersionsPanelVersion、
 *         ClientVersionsPanelVersionDetail
 * @sync apps/control-plane-web/src/components/ClientVersionsPanel.tsx、
 *       apps/control-plane-web/src/components/ClientVersionsPanel.dom.test.tsx
 * @since FR-502（面板受控化迁包；原 FR-088 版本管理面板、FR-191 发布独立页、FR-214 制品预览）
 */
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Eye, RotateCcw, Upload } from 'lucide-react'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import DangerConfirm from '@jianmanager/ui/components/views/DangerConfirm'
import type { ManifestFileLike } from '@/lib/client-publish-wizard'
import { ClientFileTree } from '@/components/views/client-dist/ClientFileTree'

/** 版本历史列表项（视图展示所需最小字段；与 `@/api/clientVersions` 的 `ClientVersionSummary` 结构兼容）。 */
export interface ClientVersionsPanelVersion {
  /** 版本号（单调递增，玩家只认 latest）。 */
  version: number
  /** 版本备注（空串=无备注）。 */
  note: string
  /** 该版本文件数。 */
  fileCount: number
  /** 发布时间（ISO 串，视图按本地时区展示）。 */
  createdAt: string
  /** 是否为频道当前 latest 指针所指版本。 */
  isLatest: boolean
}

/** 版本详情（视图展示所需最小字段；与 `@/api/clientVersions` 的 `ClientVersionDetail` 结构兼容）。 */
export interface ClientVersionsPanelVersionDetail {
  /** 版本备注（空串=无备注）。 */
  note: string
  /** 是否 latest（标题区徽标）。 */
  isLatest: boolean
  /** 该版本的自动清理目录清单。 */
  managedDirs: string[]
  /** 版本文件清单（结构树 / 预览数据源）。 */
  files: ManifestFileLike[]
}

/**
 * 版本面板的注入契约（受控视图，ADR-097 a+b 混合范式）。
 *
 * 受控边界（**不取数、不发 mutation、不弹 toast、不读路由**）：
 * - 版本列表与详情数据经 props 注入：`versions` 由容器 `useClientVersions` 取；`detailVersion` 是
 *   **容器持有的选中版本**（它驱动 `useClientVersion` 取数），视图只经 `onDetailVersionChange` 上报
 *   开合意图，「查看」与关闭都走它；
 * - 回滚：视图只上报目标版本号，`useRollbackClientVersion` + 成功/失败 toast 在容器；
 * - 发布跳转：「发布新版本」经 `onPublishNewVersion` 上报，路由语义（`/client-channels/:id/publish`）在容器；
 * - 内嵌更新器摘要与制品内容浏览器是**应用侧接线层**（前者取更新器版本、后者注入主题与制品文本端点），
 *   经 `updaterSummarySlot` / `previewSlot` 插槽注入，包内不 import 应用侧模块；
 * - 画面门禁：回滚的 `DangerConfirm` 保持 `scope="platform"`，放行结果由容器读登录态角色后经
 *   `dangerAllowed` 注入（包内不持鉴权状态）；
 * - **留本视图**的 UI 状态：回滚二次确认的目标版本与开合、详情弹窗的结构/预览视图切换。
 */
export interface ClientVersionsPanelViewProps {
  /** 版本历史列表（容器取数）；缺省按空展示。 */
  versions?: ClientVersionsPanelVersion[]
  /** 列表取数中（抑制空态文案）。 */
  isLoading: boolean
  /** 当前打开详情的版本号；null=不开弹窗（容器持有，驱动详情取数）。 */
  detailVersion: number | null
  /** 详情开合 / 目标版本上报（容器据此改 `detailVersion`）。 */
  onDetailVersionChange: (version: number | null) => void
  /** 选中版本的详情（容器 `useClientVersion` 注入）；未选中或取数中缺省。 */
  detail?: ClientVersionsPanelVersionDetail
  /** 详情取数中（弹窗内展示加载态）。 */
  detailLoading: boolean
  /** 回滚是否放行：应用侧读角色等级后注入（组件库不持鉴权状态）。 */
  dangerAllowed?: boolean
  /** 回滚某版本（容器注入 mutation + toast）。 */
  onRollback: (version: number) => void
  /** 发布新版本（容器导航到频道发布页）。 */
  onPublishNewVersion: () => void
  /** 内嵌更新器摘要槽（应用侧取数接线层）；缺省不渲染。 */
  updaterSummarySlot?: ReactNode
  /** 制品内容预览槽（应用侧 `FileBrowser` 接线层：主题 + 管理面制品端点），详情弹窗预览视图内渲染。 */
  previewSlot?: ReactNode
}

/**
 * 客户端分发版本管理面板（FR-088，见 ADR-022）。
 * 历史列表 + 版本详情查看 + 运营回滚（二次确认 FR-059）。
 * 历史仅管理面可见（玩家只认 latest）；发布/回滚由后端 RBAC 限平台管理员。
 *
 * 发布走独立页面（FR-191 纠正：原模态向导点遮罩会丢草稿）：「发布新版本」按钮经
 * `onPublishNewVersion` 由容器导航到 `/client-channels/:id/publish`，不再在此开模态。
 */
export function ClientVersionsPanelView({
  versions,
  isLoading,
  detailVersion,
  onDetailVersionChange,
  detail,
  detailLoading,
  dangerAllowed,
  onRollback,
  onPublishNewVersion,
  updaterSummarySlot,
  previewSlot,
}: ClientVersionsPanelViewProps) {
  const { t } = useTranslation()

  // 回滚二次确认的目标版本（null=关闭）：弹窗开合属 UI 状态，留视图；确认后经 onRollback 交容器执行。
  const [rollbackTarget, setRollbackTarget] = useState<number | null>(null)

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between flex-wrap gap-2">
        <div className="max-w-2xl space-y-1">
          <p className="text-sm text-muted-foreground">
            {t('clientVersions.subtitle', '历史版本仅管理台可见；玩家侧只拉取 latest。回滚以更高版本号重发旧内容，不会触发客户端防降级。')}
          </p>
          {updaterSummarySlot}
        </div>
        <Button onClick={onPublishNewVersion} className="shrink-0">
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
                    <Button variant="ghost" size="xs" onClick={() => onDetailVersionChange(v.version)}>
                      <Eye className="size-3.5" /> {t('clientVersions.view', '查看')}
                    </Button>
                    <Button
                      variant="ghost"
                      size="xs"
                      onClick={() => setRollbackTarget(v.version)}
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
          version={detailVersion}
          detail={detail}
          isLoading={detailLoading}
          previewSlot={previewSlot}
          onClose={() => onDetailVersionChange(null)}
        />
      )}

      <DangerConfirm
        open={rollbackTarget !== null}
        title={t('clientVersions.rollbackConfirm', '确定回滚到 v{{n}}？', { n: rollbackTarget ?? '' })}
        description={t('clientVersions.rollbackConfirmDesc', '将以更高的新版本号重发该版本内容为 latest（保持版本单调，客户端正常前进、不被防降级拒绝）。')}
        scope="platform"
        allowed={dangerAllowed}
        confirmLabel={t('clientVersions.rollback', '回滚')}
        onConfirm={() => {
          const target = rollbackTarget
          setRollbackTarget(null)
          if (target !== null) onRollback(target)
        }}
        onCancel={() => setRollbackTarget(null)}
      />
    </div>
  )
}

/** 版本详情弹窗的受控入参：详情数据由容器取，插槽负责制品内容预览。 */
interface VersionDetailDialogProps {
  /** 目标版本号（标题展示）。 */
  version: number
  /** 版本详情（容器注入）；未取到时缺省。 */
  detail?: ClientVersionsPanelVersionDetail
  /** 取数中。 */
  isLoading: boolean
  /** 预览视图槽（应用侧 `FileBrowser` 接线层）。 */
  previewSlot?: ReactNode
  /** 关闭上报。 */
  onClose: () => void
}

/**
 * 版本详情弹窗：展示某历史版本的托管目录与文件清单（只读，一次性展示属 ui-modals 例外）。
 *
 * 两种视图（FR-214）：
 *  - 结构：{@link ClientFileTree} 文件树 + sync/platform 徽标 + 大小（编排语义，原有能力不减）。
 *  - 预览：应用侧注入的共享 FileBrowser 浏览文件树并预览内容（文本/配置/JSON 高亮，二进制/超大降级 +
 *    下载），经管理面 JWT 制品内容端点取文本（与玩家拉取密钥端点隔离，见 ADR-022/023）。
 */
function VersionDetailDialog({ version, detail, isLoading, previewSlot, onClose }: VersionDetailDialogProps) {
  const { t } = useTranslation()
  // 结构 / 预览视图切换：不触发取数，留视图。
  const [view, setView] = useState<'structure' | 'preview'>('structure')

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
          <div className="flex items-center justify-between gap-2 flex-wrap">
            <div className="text-sm">
              <span className="text-muted-foreground">{t('clientVersions.managedDirs', '托管目录')}：</span>
              <span className="font-mono text-xs">{(detail?.managedDirs ?? []).join(', ') || '-'}</span>
            </div>
            <ViewToggle view={view} onChange={setView} />
          </div>
          {isLoading ? (
            <p className="text-sm text-muted-foreground">{t('common.loading', '加载中…')}</p>
          ) : view === 'structure' ? (
            <ClientFileTree files={detail?.files ?? []} readonly />
          ) : (detail?.files ?? []).length === 0 ? (
            <p className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">
              {t('clientVersions.treeEmpty', '暂无文件')}
            </p>
          ) : (
            <div className="space-y-2">
              <p className="text-xs text-muted-foreground">{t('clientVersions.previewHint')}</p>
              {previewSlot}
            </div>
          )}
        </ScrollableDialogBody>

        <DialogFooter>
          <Button onClick={onClose}>{t('common.close', '关闭')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** 结构 / 预览视图切换（分段按钮，FR-214）。 */
function ViewToggle({ view, onChange }: { view: 'structure' | 'preview'; onChange: (v: 'structure' | 'preview') => void }) {
  const { t } = useTranslation()
  return (
    <div className="inline-flex rounded-lg border p-0.5 text-xs">
      <button
        type="button"
        className={`rounded-md px-2.5 py-1 transition-colors ${view === 'structure' ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:text-foreground'}`}
        onClick={() => onChange('structure')}
      >
        {t('clientVersions.viewStructure', '结构')}
      </button>
      <button
        type="button"
        className={`rounded-md px-2.5 py-1 transition-colors ${view === 'preview' ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:text-foreground'}`}
        onClick={() => onChange('preview')}
      >
        {t('clientVersions.viewPreview', '预览')}
      </button>
    </div>
  )
}
