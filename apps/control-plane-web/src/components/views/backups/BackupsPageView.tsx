/**
 * @file BackupsPageView：备份管理页的受控视图，备份列表取数/轮询、写动作与 toast 由应用容器负责。
 * @input lib/backup（状态归并/依赖计数/大小格式化）、views/config-explorer/ConfigRow（卡片/汇总/切换）、
 *        views/DangerConfirm（恢复与删除二次确认）、Panel/Table/StatusBadge/Button 等原语、翻译上下文
 * @output BackupsPageView、BackupsPageViewProps、BackupsInstancePickerArgs、BackupStorageChoice
 * @sync apps/control-plane-web/src/pages/BackupsPage.tsx、apps/control-plane-web/src/pages/BackupsPage.dom.test.tsx
 * @since FR-502（组件受控化迁包；原页 FR-056 全量/增量备份 + FR-057 远程存储 + FR-151 进度轮询/汇总/链路依赖）
 */
import { useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Archive } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { Panel } from '@jianmanager/ui/components/panel'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import DangerConfirm from '@jianmanager/ui/components/views/DangerConfirm'
import {
  ConfigRow,
  ConfigSummaryChips,
  ConfigViewToggle,
  type ConfigView,
} from '@jianmanager/ui/components/views/config-explorer/ConfigRow'
import {
  BACKUP_COMPLETED,
  BACKUP_MODE_INCREMENTAL,
  backupStatusKey,
  backupStatusLevel,
  countDependents,
  formatSizeMb,
  isIncrementalChild,
  summarizeBackups,
  type BackupInfo,
} from '@jianmanager/ui/lib/backup'

/**
 * 可选备份存储位置（本视图渲染下拉与解析存储名所需的最小字段集）。
 *
 * 刻意只声明用到的 id/name：容器直接传 `useBackupStorages()` 返回的完整对象也结构兼容，
 * 无需把应用侧 `@/api/backupStorages` 的 `BackupStorage` 迁进包
 * （同 `BackupStoragesPageView`、`views/instances/InstanceBackupSegment` 的取舍）。
 */
export interface BackupStorageChoice {
  id: number
  name: string
}

/**
 * 实例选择器插槽参数。
 *
 * 选择器本体在包内（`views/instances/InstancePicker`），但「何时发请求、防抖多久、候选窗口多大」
 * 是外壳策略——千级实例必须走服务端搜索（不能一次性拉全量），故由外壳提供实现注入。
 */
export interface BackupsInstancePickerArgs {
  /** 当前选中实例 id；未选为 null。 */
  value: number | null
  /** 选中变更上报（外壳据此重取备份列表与选中实例详情）。 */
  onChange: (id: number | null) => void
}

/**
 * 受控边界（ADR-097 b 范式）：**不取数、不发请求、不弹 toast**。
 * - 备份列表与加载态、可用存储位置经 props 注入（容器调 `useBackups` / `useBackupStorages`）；
 * - **归容器**的受控状态：`instanceId` 是备份列表与实例详情查询的键（一变即重新取数），
 *   轮询启停也由容器按「是否有进行中备份」决定，视图只按注入的 `backupsActive` 显示刷新提示；
 * - **留本组件**的纯 UI 状态：卡片/列表视图切换、创建目标存储位置（只进创建请求体、不触发取数）、
 *   恢复与删除的确认目标（对话框开合）；
 * - 写动作以回调上报，由容器执行 mutation 并决定成功/失败文案：恢复回传 `Promise` 供视图在
 *   请求结束后关窗（原页成败均关窗），创建与删除为普通回调（容器自行 toast）；
 * - 在途标志（`creating` / `restoring`）只有容器知道具体 mutation 状态，故注入，视图不自行判定；
 *   `instanceLive` 同理——它是容器查到的实例存活状态，也是恢复守卫的判据。
 */
export interface BackupsPageViewProps {
  /** 当前选中实例 id；null/缺省 = 未选（渲染引导提示并禁用创建）。归容器：它是列表查询的键。 */
  instanceId?: number | null
  /** 实例选择变更上报（容器据此重取备份列表与实例详情）。 */
  onInstanceChange: (id: number | null) => void
  /** 实例选择器插槽；千级实例须走服务端搜索，由外壳注入（必填）。 */
  renderInstancePicker: (args: BackupsInstancePickerArgs) => ReactNode
  /** 可选备份存储位置（缺省本地）；容器取数后注入。 */
  storages?: BackupStorageChoice[]
  /** 备份列表；容器取数后注入（缺省或空数组渲染空态）。 */
  backups?: BackupInfo[]
  /** 列表加载态（渲染「加载中」文案）。 */
  isLoading?: boolean
  /** 列表中是否存在进行中备份（显示自动刷新提示；轮询启停归容器）。 */
  backupsActive?: boolean
  /** 选中实例进程是否存活（存活时禁止恢复，与后端恢复守卫一致）。 */
  instanceLive?: boolean
  /** 创建备份在途：禁用两个创建按钮。 */
  creating?: boolean
  /** 恢复在途：禁用各行的恢复按钮。 */
  restoring?: boolean
  /** 创建备份（`incremental` 区分全量/增量，`storageId` 缺省表示本地）；成败提示归容器。 */
  onCreate: (payload: { incremental: boolean; storageId?: number }) => void
  /**
   * 恢复已确认的备份。返回的 Promise 用于在请求结束后关窗（与原页一致：成败均关窗），
   * 故容器必须吞掉错误并自行 toast，不得让 Promise 以拒绝收场。
   */
  onRestore: (backupId: number) => Promise<void>
  /** 删除已确认的备份（二次确认已在本视图内完成）。 */
  onDelete: (backupId: number) => void
}

/** 备份管理页：全量/增量（FR-056）+ 远程存储（FR-057）+ 进度轮询/汇总/链路依赖（FR-151）。 */
export function BackupsPageView({
  instanceId = null,
  onInstanceChange,
  renderInstancePicker,
  storages,
  backups,
  isLoading = false,
  backupsActive = false,
  instanceLive = false,
  creating = false,
  restoring = false,
  onCreate,
  onRestore,
  onDelete,
}: BackupsPageViewProps) {
  const { t } = useTranslation()
  const [storageId, setStorageId] = useState<number | undefined>()
  const [restoreTarget, setRestoreTarget] = useState<number | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<BackupInfo | null>(null)
  const [view, setView] = useState<ConfigView>('list')

  const selected = instanceId ?? null
  const hasInstance = selected !== null

  // 实例进程可能存活（STARTING/RUNNING/STOPPING）时禁止恢复，与后端恢复守卫一致：
  // 运行中的服务器下次自动存档会覆盖掉刚恢复的文件，恢复会静默失效。
  const restoreDisabledTitle = instanceLive
    ? t('backups.restoreNeedStopped', '实例运行中，请先停止实例再恢复')
    : undefined

  const list = useMemo(() => backups ?? [], [backups])
  const summary = useMemo(() => summarizeBackups(list), [list])
  const backupById = useMemo(() => new Map(list.map((b) => [b.id, b])), [list])

  const storageName = (id?: number) =>
    id ? (storages ?? []).find((s) => s.id === id)?.name ?? `#${id}` : t('backups.localStorage', '本地')
  const parentName = (b: BackupInfo) =>
    b.parentId !== undefined ? backupById.get(b.parentId)?.name ?? `#${b.parentId}` : undefined
  const checksumLabel = (b: BackupInfo) =>
    b.checksum ? `${b.checksum.slice(0, 12)}...` : t('backups.checksumMissing', '未记录')

  /** 恢复确认：与原页一致，请求结束后（成败都）关窗，失败原因由容器 toast。 */
  const confirmRestore = async () => {
    if (restoreTarget === null) return
    try {
      await onRestore(restoreTarget)
    } finally {
      setRestoreTarget(null)
    }
  }

  /** 删除确认：与原页一致立即关窗，mutation 结果由容器提示。 */
  const confirmDelete = () => {
    if (deleteTarget === null) return
    const id = deleteTarget.id
    setDeleteTarget(null)
    onDelete(id)
  }

  // 删除前算直接依赖此备份的增量数，用于二次确认警告（FR-151）。
  const dependents = deleteTarget ? countDependents(list, deleteTarget.id) : 0

  const modeBadge = (b: BackupInfo) =>
    b.mode === BACKUP_MODE_INCREMENTAL ? (
      <StatusBadge level="info" label={t('backups.incremental', '增量')} dot={false} />
    ) : (
      <StatusBadge level="neutral" label={t('backups.full', '全量')} dot={false} />
    )

  const statusBadge = (b: BackupInfo) => (
    <StatusBadge
      level={backupStatusLevel(b.status)}
      label={t(`backups.${backupStatusKey(b.status)}`)}
      pulse={b.status !== BACKUP_COMPLETED && b.status !== 3}
    />
  )

  // 增量行的副信息：存储位置 + 父备份关系（链路可视，FR-151）。
  const rowSubtitle = (b: BackupInfo) => {
    const parts = [storageName(b.storageId)]
    if (isIncrementalChild(b)) parts.push(`${t('backups.basedOn', '基于')} ${parentName(b)}`)
    return parts.join(' · ')
  }

  return (
    // 阶段 6 页面迁移：外壳与页头改用布局层原语。
    // 标题字号由 text-2xl 统一到布局规范的 text-xl（PageHeader 的固定字号）；
    // 原先无 data-page，迁移时补上（e2e 的就绪信号依赖它）。
    <PageShell data-page="backups">
      <PageHeader
        title={t('backups.title', '备份管理')}
        actions={
          <>
            {/* 实例选择器由外壳注入：候选走服务端搜索，视图不持实例候选与查询状态。 */}
            {renderInstancePicker({ value: selected, onChange: onInstanceChange })}
            <select
              className="p-2 border rounded bg-background text-sm"
              value={storageId ?? ''}
              onChange={(e) => setStorageId(e.target.value ? Number(e.target.value) : undefined)}
              title={t('backups.selectStorage', '存储位置')}
            >
              <option value="">{t('backups.localStorage', '本地')}</option>
              {(storages ?? []).map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </select>
            {hasInstance && (
              <ConfigViewToggle view={view} onChange={setView} cardLabel={t('common.cardView')} listLabel={t('common.listView')} />
            )}
            <Button onClick={() => onCreate({ incremental: false, storageId })} disabled={!hasInstance || creating}>
              {t('backups.createFull', '全量备份')}
            </Button>
            <Button variant="outline" onClick={() => onCreate({ incremental: true, storageId })} disabled={!hasInstance || creating}>
              {t('backups.createIncremental', '增量备份')}
            </Button>
          </>
        }
      />

      {!hasInstance && <p className="text-muted-foreground">{t('backups.hint', '请先选择一个实例查看备份列表')}</p>}

      {hasInstance && (
        <>
          {/* 汇总条（FR-151）：总占用 / 份数 / 最近成功；进行中时显轮询提示。 */}
          <div className="flex flex-wrap items-center gap-3">
            <ConfigSummaryChips
              chips={[
                { label: t('backups.summaryTotalSize'), value: formatSizeMb(summary.totalSizeMb) },
                { label: t('backups.summaryCount'), value: summary.count },
                {
                  label: t('backups.summaryLastSuccess'),
                  value: summary.lastSuccessAt ? new Date(summary.lastSuccessAt).toLocaleString() : t('backups.neverSuccess'),
                },
              ]}
            />
            {backupsActive && (
              <span className="inline-flex items-center gap-1.5 text-xs text-status-info">
                <span className="size-1.5 animate-pulse rounded-full bg-status-info" />
                {t('backups.autoRefreshing')}
              </span>
            )}
          </div>

          {isLoading && list.length === 0 ? (
            <p className="text-muted-foreground">{t('common.loading')}</p>
          ) : list.length === 0 ? (
            <Panel>
              <p className="py-6 text-center text-sm text-muted-foreground">{t('backups.empty', '暂无备份')}</p>
            </Panel>
          ) : view === 'card' ? (
            <div className="flex flex-col gap-2.5">
              {list.map((b) => {
                const dep = countDependents(list, b.id)
                return (
                  <ConfigRow
                    key={b.id}
                    icon={<Archive className="size-[18px]" />}
                    tone={backupStatusLevel(b.status) === 'neutral' ? 'primary' : backupStatusLevel(b.status)}
                    title={b.name}
                    subtitle={rowSubtitle(b)}
                    meta={
                      <>
                        <div>{formatSizeMb(b.fileSizeMb)}</div>
                        <div title={b.checksum}>{checksumLabel(b)}</div>
                        <div>{new Date(b.createdAt).toLocaleString()}</div>
                      </>
                    }
                    trailing={
                      <>
                        {modeBadge(b)}
                        {statusBadge(b)}
                        <Button
                          variant="ghost"
                          size="xs"
                          onClick={() => setRestoreTarget(b.id)}
                          disabled={b.status !== BACKUP_COMPLETED || restoring || instanceLive}
                          title={restoreDisabledTitle}
                        >
                          {t('backups.restore', '恢复')}
                        </Button>
                        <Button
                          variant="ghost"
                          size="xs"
                          className="text-status-danger hover:text-status-danger"
                          onClick={() => setDeleteTarget(b)}
                          title={dep > 0 ? t('backups.dependentsWarn', { count: dep }) : undefined}
                        >
                          {t('common.delete', '删除')}
                        </Button>
                      </>
                    }
                  />
                )
              })}
            </div>
          ) : (
            <Panel bodyClassName="p-0">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t('backups.name', '名称')}</TableHead>
                    <TableHead>{t('backups.mode', '模式')}</TableHead>
                    <TableHead>{t('backups.size', '大小')}</TableHead>
                    <TableHead>{t('backups.storageLocation', '存储位置')}</TableHead>
                    <TableHead>{t('backups.checksum', '校验和')}</TableHead>
                    <TableHead>{t('backups.status', '状态')}</TableHead>
                    <TableHead>{t('backups.time', '时间')}</TableHead>
                    <TableHead className="text-right">{t('common.actions', '操作')}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {list.map((b) => (
                    <TableRow key={b.id}>
                      <TableCell className="font-medium">{b.name}</TableCell>
                      <TableCell>
                        <div className="flex flex-col gap-1">
                          {modeBadge(b)}
                          {isIncrementalChild(b) && (
                            <span className="text-xs text-muted-foreground">
                              {t('backups.basedOn', '基于')} {parentName(b)}
                            </span>
                          )}
                        </div>
                      </TableCell>
                      <TableCell>{formatSizeMb(b.fileSizeMb)}</TableCell>
                      <TableCell>{storageName(b.storageId)}</TableCell>
                      <TableCell className="font-mono text-xs" title={b.checksum}>{checksumLabel(b)}</TableCell>
                      <TableCell>{statusBadge(b)}</TableCell>
                      <TableCell className="text-muted-foreground">{new Date(b.createdAt).toLocaleString()}</TableCell>
                      <TableCell className="text-right whitespace-nowrap">
                        <div className="flex justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="xs"
                            onClick={() => setRestoreTarget(b.id)}
                            disabled={b.status !== BACKUP_COMPLETED || restoring || instanceLive}
                            title={restoreDisabledTitle}
                          >
                            {t('backups.restore', '恢复')}
                          </Button>
                          <Button
                            variant="ghost"
                            size="xs"
                            className="text-status-danger hover:text-status-danger"
                            onClick={() => setDeleteTarget(b)}
                            title={countDependents(list, b.id) > 0 ? t('backups.dependentsWarn', { count: countDependents(list, b.id) }) : undefined}
                          >
                            {t('common.delete', '删除')}
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </Panel>
          )}
        </>
      )}

      {/* 恢复二次确认：scope 保持原页的 group、不新增 allowed 门禁——迁移不改动门禁语义。 */}
      <DangerConfirm
        open={restoreTarget !== null}
        title={t('backups.confirmRestore', '确认恢复此备份？')}
        description={t('backups.restoreWarning', '当前文件将被覆盖，此操作不可撤销。')}
        confirmLabel={t('backups.restore', '恢复')}
        scope="group"
        onConfirm={() => { void confirmRestore() }}
        onCancel={() => setRestoreTarget(null)}
      />

      {/* 删除二次确认：同上，scope 固定 group、不传 allowed。 */}
      <DangerConfirm
        open={deleteTarget !== null}
        title={t('backups.deleteConfirm', '确定删除此备份？')}
        description={dependents > 0 ? t('backups.dependentsWarn', { count: dependents }) : t('common.irreversible')}
        confirmLabel={t('common.delete', '删除')}
        scope="group"
        onConfirm={confirmDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </PageShell>
  )
}

export default BackupsPageView
