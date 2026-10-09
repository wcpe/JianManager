/**
 * @file ArtifactStoragesPageView：制品外置存储渠道页（含存量迁移）的受控视图，渠道取数、迁移状态轮询、全部写动作与 toast 由应用容器负责。
 * @input lib/form-validation（字段校验）、lib/use-field-gate（错误展示时机门控）、lib/task-status（任务终态判定）、
 *        Table/Dialog/Checkbox/DropdownMenu 等原语、views/DangerConfirm（删除二次确认）、翻译上下文
 * @output ArtifactStoragesPageView、ArtifactStoragesPageViewProps、ArtifactStorageChannelView、ArtifactStorageForm、
 *        ArtifactStorageTestOutcome、ArtifactMigrationTaskView、ArtifactMigrationInfoView、ArtifactMigrationFailureView
 * @sync apps/control-plane-web/src/pages/ArtifactStoragesPage.tsx、apps/control-plane-web/src/pages/ArtifactStoragesPage.dom.test.tsx、apps/control-plane-web/src/pages/ArtifactStoragesPage.migration.dom.test.tsx
 * @since FR-502（组件受控化迁包；原页 FR-347 外置存储渠道管理 + FR-348 存量迁移）
 */
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowRightLeft, CircleCheck, HardDrive, MoreHorizontal, Pencil, Trash2, Zap } from 'lucide-react'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import {
  Table,
  TableBody,
  TableCard,
  TableCardFooter,
  TableCardHeader,
  TableCell,
  TableEmptyRow,
  TableHead,
  TableHeader,
  TableIconButton,
  TableRow,
  TableSkeletonRows,
} from '@jianmanager/ui/components/table'
import DangerConfirm from '@/components/views/DangerConfirm'
import { validateRequired, validateFields, hasErrors } from '@/lib/shared/form-validation'
import { isTerminalTask } from '@/lib/tasks/task-status'
import type { TaskState } from '@/lib/tasks/task-status'
import { useFieldGate } from '@/lib/hooks/use-field-gate'

/**
 * 存储渠道行（本视图渲染、表单回填与迁移展示所需的最小字段集）。
 *
 * 刻意只声明用到的字段，而非照搬应用侧 `@/api/artifactStorages` 的 `ArtifactStorageChannel`：
 * 容器直接传 API 返回的完整对象也结构兼容，无需把该 API 类型迁进包
 * （同 `BackupStoragesPageView`、`ArtifactVersionsPageView` 的取舍）。
 *
 * 凭证安全：后端从不返回 Access Key / Secret Key 的明文或密文，只给 `hasAccessKey` /
 * `hasSecretKey` 两个存在标志；本视图据此决定「留空保留」的占位提示，任何位置都不得回显凭据。
 */
export interface ArtifactStorageChannelView {
  id: number
  name: string
  /** local（内置「本机存储」独占）| s3 */
  type: string
  endpoint: string
  bucket: string
  region: string
  prefix: string
  useSsl: boolean
  /** 预签名下载地址的有效期秒数（仅 s3 渠道展示，范围 60~3600）。 */
  presignTtlSeconds: number
  /** 活跃渠道 = 新上传制品的落点（全表恰一条）。 */
  active: boolean
  /** 内置「本机存储」：不可编辑、不可删除。 */
  builtin: boolean
  /** 是否已配置 Access Key（凭证本身永不返回，仅用于占位提示）。 */
  hasAccessKey: boolean
  /** 是否已配置 Secret Key（同上）。 */
  hasSecretKey: boolean
  /** 最近一次测试时间；为空表示从未测试。 */
  lastTestAt?: string
  lastTestOk: boolean
  lastTestMessage: string
}

/**
 * 表单草稿（创建与编辑同形）。面板仅可创建 s3 渠道，local 由内置「本机存储」独占（ADR-073 决策 2）。
 * 字段全部必填（字符串不以 undefined 受控）以贴合运行时取值——编辑时由行值整体回填、
 * 创建时由 `emptyForm` 铺满，故永不出现受控/非受控切换。
 */
export interface ArtifactStorageForm {
  name: string
  type: string
  endpoint: string
  bucket: string
  region: string
  prefix: string
  /** Access Key；编辑态留空 = 保留原值，填入 = 覆盖（后端可逆加密落库）。 */
  accessKey: string
  /** Secret Key；编辑态留空 = 保留原值，不回显明文。 */
  secretKey: string
  useSsl: boolean
  presignTtlSeconds: number
}

/** 草稿测试连接的结果（视图只需 ok 与 message 做内联回显，toast 文案归容器）。 */
export interface ArtifactStorageTestOutcome {
  ok: boolean
  message: string
}

/** 迁移任务（本视图只消费展示所需字段；终态判定复用 lib/task-status 的纯函数）。 */
export interface ArtifactMigrationTaskView {
  taskId: string
  state: TaskState
  /** 0~100。 */
  progress: number
  /** 已请求强制停止、尚未确认中断（为真时置灰「强制停止」）。 */
  cancelRequested: boolean
}

/** 迁移登记与实时计数（容器取自 GET /artifact-storages/migration 的 migration 段）。 */
export interface ArtifactMigrationInfoView {
  /** 重新发起（重试）时的目标渠道 id。 */
  targetChannelId: number
  /** 目标渠道当前名称（迁移后渠道被删时为空串）。 */
  targetName: string
  total: number
  migrated: number
  failed: number
  skipped: number
}

/** 迁移失败明细一条（sha256 + 原因；重试 = 对同目标重新发起，成功条自动跳过）。 */
export interface ArtifactMigrationFailureView {
  id: number
  sha256: string
  filename: string
  size: number
  reason: string
}

/** 新建时的空表单（仅 s3 渠道可建）。 */
const emptyForm: ArtifactStorageForm = {
  name: '', type: 's3', endpoint: '', bucket: '', region: '', prefix: '',
  accessKey: '', secretKey: '', useSsl: false, presignTtlSeconds: 600,
}

/** 字节数转人类可读，供迁移失败明细展示文件大小（保持原页 1 位小数口径）。 */
function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`
  return `${(bytes / 1024 / 1024 / 1024).toFixed(1)} GB`
}

/**
 * 受控边界（ADR-097 b 范式）：**不取数、不发请求、不弹 toast、不持鉴权状态**。
 * - 渠道列表、迁移任务与计数、失败明细经 props 注入（容器调对应 hook，并按任务终态启停轮询）；
 * - 写动作以回调上报，由容器执行 mutation 并决定成功/失败文案：提交表单与草稿测试回传
 *   `Promise`，视图据返回值决定是否复位本地编辑态（失败保留草稿与窗口便于重试）；
 * - 会触发重新取数的状态归容器：`failuresTaskId` 既是失败明细模态的打开状态、也是该查询的
 *   启用键（打开才拉取，同 `ArtifactReconcileSectionView` 的报告弹窗），故由容器持有；
 *   表单/设活跃/迁移/删除四个对话框的开合、表单草稿、字段门控（touched/submitted）与
 *   「待确认的渠道」是纯 UI 状态，留本组件内（待确认的渠道不触发取数）；
 * - 在途标志（`saving` / `rowTestingId` / `activating` / `migrationStarting` / `migrationCanceling`）
 *   只有容器知道具体在途对象，故全部注入，视图不自行判定 pending。
 */
export interface ArtifactStoragesPageViewProps {
  /** 存储渠道列表；容器取数后注入（缺省或空数组渲染空态）。 */
  channels?: ArtifactStorageChannelView[]
  /** 列表加载态（渲染骨架行）。 */
  isLoading?: boolean
  /** 创建/更新在途：禁用弹窗提交按钮。 */
  saving?: boolean
  /** 草稿测试连接在途：禁用「测试连接」按钮。 */
  testingDraft?: boolean
  /** 行内测试在途的目标 id：只禁用该行的测试按钮，其余行不受影响。 */
  rowTestingId?: number | null
  /** 设活跃在途：禁用设活跃确认按钮。 */
  activating?: boolean
  /** 最近一次迁移任务；null/缺省 = 从未迁移过（容器「任务与计数双 null」口径）。 */
  migrationTask?: ArtifactMigrationTaskView | null
  /** 最近一次迁移的登记与计数；任务存在但登记行缺失时也可能为 null。 */
  migrationInfo?: ArtifactMigrationInfoView | null
  /** 发起迁移在途：禁用各行迁移入口与「开始迁移 / 重新发起」。 */
  migrationStarting?: boolean
  /** 强制停止在途：禁用「强制停止」按钮。 */
  migrationCanceling?: boolean
  /** 失败明细模态的受控打开状态（同时是失败明细查询键）：非空即打开，由容器持有。 */
  failuresTaskId?: string | null
  /** 失败明细（容器按 `failuresTaskId` 拉取后注入；上限 500 条）。 */
  failures?: ArtifactMigrationFailureView[]
  /** 提交表单；`editingId` 为 null 表示创建。返回是否成功——成功才关窗并清空草稿。 */
  onSubmit: (values: ArtifactStorageForm, editingId: number | null) => Promise<boolean>
  /** 测试未保存的草稿（不落库）；`editingId` 非空时带 id 让后端复用存库凭证探测。 */
  onTestDraft: (values: ArtifactStorageForm, editingId: number | null) => Promise<ArtifactStorageTestOutcome>
  /** 行内测试已保存的渠道（结论提示由容器负责）。 */
  onTest: (id: number) => void
  /** 设活跃渠道（二次确认已在本视图内完成；影响后续上传落点）。 */
  onActivate: (id: number) => void
  /** 删除渠道（二次确认已在本视图内完成）。 */
  onDelete: (id: number) => void
  /**
   * 发起「迁移存量制品到渠道 :id」。
   * 原实现为「发起即关窗」（不等任务创建返回，成败由容器 toast），故本视图调用后立即复位
   * 迁移确认框与失败明细模态，不等待返回值——保持既有交互时序。
   */
  onStartMigration: (targetChannelId: number) => void
  /** 强制停止在途迁移（停止后的重取数与提示归容器；重新发起即从断点续跑）。 */
  onStopMigration: () => void
  /** 打开某次迁移的失败明细（容器据此拉取该任务的明细，并置 `failuresTaskId`）。 */
  onOpenFailures: (taskId: string) => void
  /** 关闭失败明细模态（容器清空 `failuresTaskId`，明细查询随之停用）。 */
  onCloseFailures: () => void
}

/**
 * 文件存储配置页（FR-347，见 ADR-073）：客户端分发制品的外置对象存储渠道管理。
 * 活跃渠道 = 新上传制品落点（存量制品按各自记录读取）；凭证直填、后端可逆加密，
 * 编辑不回显明文（留空 = 保留）。仅平台管理员可访问。
 * 表格外观为方案 A「精工卡片」（opt-in appearance="refined"）。
 */
export function ArtifactStoragesPageView({
  channels,
  isLoading = false,
  saving = false,
  testingDraft = false,
  rowTestingId = null,
  activating = false,
  migrationTask = null,
  migrationInfo = null,
  migrationStarting = false,
  migrationCanceling = false,
  failuresTaskId = null,
  failures,
  onSubmit,
  onTestDraft,
  onTest,
  onActivate,
  onDelete,
  onStartMigration,
  onStopMigration,
  onOpenFailures,
  onCloseFailures,
}: ArtifactStoragesPageViewProps) {
  const { t } = useTranslation()
  const [form, setForm] = useState<ArtifactStorageForm>(emptyForm)
  const [draftTestResult, setDraftTestResult] = useState<ArtifactStorageTestOutcome | null>(null)
  const [showForm, setShowForm] = useState(false)
  /** 编辑目标；null = 创建模式。纯 UI 状态（不触发取数）。 */
  const [editing, setEditing] = useState<ArtifactStorageChannelView | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<ArtifactStorageChannelView | null>(null)
  /** 设活跃确认目标（影响后续上传落点，需确认语义）。 */
  const [activateTarget, setActivateTarget] = useState<ArtifactStorageChannelView | null>(null)
  /** 迁移确认目标；null = 未在确认。 */
  const [migrateTarget, setMigrateTarget] = useState<ArtifactStorageChannelView | null>(null)
  const gate = useFieldGate()

  // 在途判定用包内纯函数（容器注入任务，本视图不引应用侧任务状态机）。
  const migrationActive = migrationTask !== null && !isTerminalTask(migrationTask)
  const migCountersText = migrationInfo
    ? t('artifactStorages.migrate.counters', '共 {{total}} · 已迁 {{migrated}} · 失败 {{failed}} · 跳过 {{skipped}}', {
        total: migrationInfo.total, migrated: migrationInfo.migrated,
        failed: migrationInfo.failed, skipped: migrationInfo.skipped,
      })
    : ''

  const activeChannelName = (channels ?? []).find((c) => c.active)?.name ?? '—'

  /** 发起迁移：立即关闭确认框与失败明细，结果提示与进度刷新归容器。 */
  const startMigration = (targetChannelId: number) => {
    onStartMigration(targetChannelId)
    setMigrateTarget(null)
    onCloseFailures()
  }

  /** 设活跃：确认后立即关窗（成败由容器 toast，活跃徽章随取数刷新）。 */
  const confirmActivate = () => {
    if (!activateTarget) return
    onActivate(activateTarget.id)
    setActivateTarget(null)
  }

  /** 删除：二次确认后立即关窗（守卫拒绝时由容器 toast 后端原因，行仍在）。 */
  const confirmDelete = () => {
    if (!deleteTarget) return
    onDelete(deleteTarget.id)
    setDeleteTarget(null)
  }

  const set = (k: keyof ArtifactStorageForm, v: string | boolean | number) => {
    // 任何键入都作废上一次草稿测试结论：结论只对应它那一刻的输入。
    setDraftTestResult(null)
    setForm((f) => ({ ...f, [k]: v }))
  }

  const errors = validateFields(
    { name: form.name, endpoint: form.endpoint ?? '', bucket: form.bucket ?? '' },
    {
      name: [validateRequired],
      endpoint: [validateRequired],
      bucket: [validateRequired],
    },
  )

  /** 编辑入口：回显非敏字段；凭证永不回显（hasSecretKey 时占位提示「留空保留」）。 */
  const openEdit = (ch: ArtifactStorageChannelView) => {
    setForm({
      name: ch.name, type: ch.type, endpoint: ch.endpoint, bucket: ch.bucket, region: ch.region,
      prefix: ch.prefix, accessKey: '', secretKey: '', useSsl: ch.useSsl,
      presignTtlSeconds: ch.presignTtlSeconds,
    })
    setEditing(ch)
    setDraftTestResult(null)
    gate.reset()
    setShowForm(true)
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    gate.submit()
    if (hasErrors(errors)) return
    // 失败（false，容器已弹 toast）时不关窗、不清草稿，便于修正后重试。
    const ok = await onSubmit(form, editing ? editing.id : null)
    if (!ok) return
    setForm(emptyForm)
    setEditing(null)
    gate.reset()
    setShowForm(false)
  }

  const handleTestDraft = async () => {
    if (hasErrors(errors)) return
    // 编辑态凭证留空：带 id 让后端复用存库凭证探测；结论成败都由容器落成结果对象回传。
    setDraftTestResult(await onTestDraft(form, editing ? editing.id : null))
  }

  return (
    // 全量对齐：外壳改用 PageShell；标题上移到页面级 PageHeader（页名只出现一次、且在页面
    // 最上面），卡片头只留计数与操作。原先也没有 data-page，迁移时补上。
    <PageShell data-page="artifact-storages">
      <Dialog open={showForm} onOpenChange={(o) => { setShowForm(o); if (!o) { setDraftTestResult(null); setEditing(null) } }}>
        <DialogContent className={`${scrollableDialogContentClass} sm:max-w-2xl`}>
          <DialogHeader>
            <DialogTitle>
              {editing ? t('artifactStorages.edit', '编辑存储渠道') : t('artifactStorages.add', '新增 S3 渠道')}
            </DialogTitle>
          </DialogHeader>
          <form id="artifact-storage-form" onSubmit={submit}>
            <ScrollableDialogBody className="grid grid-cols-1 md:grid-cols-2 gap-3">
              <div className="flex flex-col gap-1 text-sm">
                <FieldLabel required>{t('artifactStorages.name', '名称')}</FieldLabel>
                <input className="p-2 border rounded bg-background aria-invalid:border-destructive" value={form.name}
                  aria-invalid={!!gate.show('name', errors.name)}
                  onChange={(e) => set('name', e.target.value)}
                  onBlur={() => gate.touch('name')} />
                <FieldError error={gate.show('name', errors.name)} />
              </div>
              <div className="flex flex-col gap-1 text-sm">
                <FieldLabel required>{t('artifactStorages.bucket', 'Bucket')}</FieldLabel>
                <input className="p-2 border rounded bg-background aria-invalid:border-destructive" value={form.bucket}
                  aria-invalid={!!gate.show('bucket', errors.bucket)}
                  onChange={(e) => set('bucket', e.target.value)}
                  onBlur={() => gate.touch('bucket')} />
                <FieldError error={gate.show('bucket', errors.bucket)} />
              </div>
              <div className="flex flex-col gap-1 text-sm md:col-span-2">
                <FieldLabel required>{t('artifactStorages.endpoint', 'Endpoint')}</FieldLabel>
                <input className="p-2 border rounded bg-background aria-invalid:border-destructive"
                  placeholder={t('artifactStorages.endpointHint', '如 rustfs.example.com:9000（内网 http 常态；协议由「启用 TLS」决定）')}
                  aria-invalid={!!gate.show('endpoint', errors.endpoint)}
                  value={form.endpoint} onChange={(e) => set('endpoint', e.target.value)}
                  onBlur={() => gate.touch('endpoint')} />
                <FieldError error={gate.show('endpoint', errors.endpoint)} />
              </div>
              <label className="flex flex-col gap-1 text-sm">
                {t('artifactStorages.region', 'Region')}
                <input className="p-2 border rounded bg-background" placeholder="us-east-1" value={form.region}
                  onChange={(e) => set('region', e.target.value)} />
              </label>
              <label className="flex flex-col gap-1 text-sm">
                {t('artifactStorages.prefix', '对象键前缀')}
                <input className="p-2 border rounded bg-background" value={form.prefix}
                  onChange={(e) => set('prefix', e.target.value)} />
              </label>
              <label className="flex flex-col gap-1 text-sm">
                {t('artifactStorages.presignTtl', '预签名有效期（秒）')}
                <input type="number" min={60} max={3600} className="p-2 border rounded bg-background"
                  value={form.presignTtlSeconds ?? 600}
                  onChange={(e) => set('presignTtlSeconds', Number(e.target.value))} />
                <span className="text-xs text-muted-foreground">{t('artifactStorages.presignTtlHint', '玩家下载跳转链接的有效时长，60~3600 秒')}</span>
              </label>
              <label className="flex items-center gap-2 text-sm mt-6">
                <Checkbox checked={form.useSsl}
                  onCheckedChange={(v) => set('useSsl', v === true)} aria-label={t('artifactStorages.useSsl', '启用 TLS')} />
                {t('artifactStorages.useSsl', '启用 TLS')}
              </label>
              <label className="flex flex-col gap-1 text-sm">
                {t('artifactStorages.accessKey', 'Access Key')}
                <input className="p-2 border rounded bg-background font-mono"
                  placeholder={editing?.hasAccessKey ? t('artifactStorages.keyKeepHint', '已配置，留空保留') : ''}
                  value={form.accessKey} onChange={(e) => set('accessKey', e.target.value)} />
              </label>
              <label className="flex flex-col gap-1 text-sm">
                {t('artifactStorages.secretKey', 'Secret Key')}
                {/* 编辑不回显明文（SK 脱敏）：留空 = 保留原值，填入 = 覆盖。 */}
                <input type="password" autoComplete="new-password" className="p-2 border rounded bg-background font-mono"
                  placeholder={editing?.hasSecretKey ? t('artifactStorages.keyKeepHint', '已配置，留空保留') : ''}
                  value={form.secretKey} onChange={(e) => set('secretKey', e.target.value)} />
              </label>
            </ScrollableDialogBody>
          </form>
          {draftTestResult && (
            <p
              role="status"
              className={`text-sm ${draftTestResult.ok ? 'text-status-success' : 'text-status-danger'}`}
            >
              {draftTestResult.message}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setShowForm(false)}>
              {t('common.cancel', '取消')}
            </Button>
            <Button
              type="button"
              variant="outline"
              onClick={() => { void handleTestDraft() }}
              disabled={testingDraft || hasErrors(errors)}
            >
              {t('artifactStorages.testConnection', '测试连接')}
            </Button>
            <Button type="submit" form="artifact-storage-form" disabled={saving || hasErrors(errors)}>
              {editing ? t('common.save', '保存') : t('artifactStorages.create', '创建')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 标题按布局规范上移到页面级。表单 Dialog 是浮层（内容走 Portal、不占布局位置），
          故 PageHeader 仍排在视觉最前。 */}
      <PageHeader
        title={t('artifactStorages.title', '文件存储配置')}
        description={t('artifactStorages.subtitle', '配置客户端分发制品的存储渠道。活跃渠道决定新上传制品的落点；S3 兼容渠道（rustfs / MinIO 等）由对象存储直接分发下载流量，主控不中继大文件。')}
      />

      {/* 在途迁移进度卡（FR-348）：任务非终态时展示，轮询推进（启停规则归容器）；可强制停止（重新发起即续跑）。 */}
      {migrationActive && migrationTask && (
        <div className="rounded-lg border bg-card p-4 space-y-2">
          <div className="flex items-center justify-between gap-2 flex-wrap">
            <span className="text-sm font-medium">
              {t('artifactStorages.migrate.inProgress', '存量迁移进行中 → {{name}}', { name: migrationInfo?.targetName ?? '' })}
            </span>
            <Button
              variant="outline"
              size="xs"
              onClick={onStopMigration}
              disabled={migrationCanceling || migrationTask.cancelRequested}
            >
              {t('artifactStorages.migrate.stop', '强制停止')}
            </Button>
          </div>
          <div className="h-1.5 overflow-hidden rounded-full bg-muted">
            <div
              className="h-full rounded-full bg-primary transition-all"
              style={{ width: `${Math.max(0, Math.min(100, migrationTask.progress))}%` }}
            />
          </div>
          {migrationInfo && <p className="text-xs text-muted-foreground">{migCountersText}</p>}
        </div>
      )}

      {/* 上次迁移摘要（终态）：状态 + 四计数；有失败条时给失败明细入口（重试=重新发起）。 */}
      {migrationTask && isTerminalTask(migrationTask) && migrationInfo && (
        <div className="rounded-lg border bg-card px-4 py-3 flex items-center justify-between gap-3 flex-wrap text-sm">
          <div className="flex items-center gap-2 flex-wrap">
            <span className="font-medium">
              {t('artifactStorages.migrate.lastRun', '上次迁移 → {{name}}', { name: migrationInfo.targetName })}
            </span>
            <span className={migrationTask.state === 'succeeded' ? 'text-status-success' : 'text-status-danger'}>
              {migrationTask.state === 'succeeded'
                ? t('tasks.state.succeeded', '已完成')
                : migrationTask.state === 'canceled'
                  ? t('tasks.state.canceled', '已停止')
                  : t('tasks.state.failed', '已失败')}
            </span>
            <span className="text-xs text-muted-foreground">{migCountersText}</span>
          </div>
          {migrationInfo.failed > 0 && (
            <Button variant="outline" size="xs" onClick={() => onOpenFailures(migrationTask.taskId)}>
              {t('artifactStorages.migrate.failures', '失败明细')}
            </Button>
          )}
        </div>
      )}

      {/* 方案 A「精工卡片」：页面标题 + 功能说明 + 主操作全部收进卡片头。 */}
      <TableCard>
        <TableCardHeader
          count={t('artifactStorages.cardCount', { total: (channels ?? []).length })}
          actions={
            <Button
              onClick={() => { setForm(emptyForm); setEditing(null); setDraftTestResult(null); gate.reset(); setShowForm(true) }}
            >
              {t('artifactStorages.add', '新增 S3 渠道')}
            </Button>
          }
        />

        <Table appearance="refined">
          <TableHeader>
            <TableRow>
              <TableHead>{t('artifactStorages.name', '名称')}</TableHead>
              <TableHead>{t('artifactStorages.type', '类型')}</TableHead>
              <TableHead>{t('artifactStorages.endpoint', 'Endpoint')}</TableHead>
              <TableHead align="right">{t('artifactStorages.presignTtlShort', '签名时效')}</TableHead>
              <TableHead>{t('artifactStorages.lastTest', '最近测试')}</TableHead>
              <TableHead align="right">{t('artifactStorages.actions', '操作')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {(channels ?? []).map((ch) => {
              // 最近测试文案：优先后端 message，回落到「连接正常 / 测试连接失败」。
              const lastTestText =
                ch.lastTestMessage ||
                (ch.lastTestOk ? t('artifactStorages.testOk', '连接正常') : t('artifactStorages.testFailed', '测试连接失败'))
              const endpointText =
                ch.type === 's3' ? `${ch.endpoint} / ${ch.bucket}${ch.prefix ? ` / ${ch.prefix}` : ''}` : t('artifactStorages.localEndpoint', '主控数据根')
              return (
                <TableRow key={ch.id}>
                  <TableCell className="font-medium text-foreground">
                    <span className="inline-flex items-center gap-2">
                      {ch.name}
                      {ch.builtin && <Badge variant="outline">{t('artifactStorages.builtin', '内置')}</Badge>}
                      {ch.active && <Badge>{t('artifactStorages.active', '活跃')}</Badge>}
                    </span>
                  </TableCell>
                  <TableCell><Badge variant="chip-neutral">{ch.type.toUpperCase()}</Badge></TableCell>
                  <TableCell className="font-mono text-xs">
                    <span className="block max-w-[20rem] truncate" title={endpointText}>{endpointText}</span>
                  </TableCell>
                  <TableCell align="right" className="text-xs tabular-nums">{ch.type === 's3' ? `${ch.presignTtlSeconds}s` : '—'}</TableCell>
                  <TableCell className="text-xs">
                    {ch.lastTestAt ? (
                      <Badge
                        variant={ch.lastTestOk ? 'chip-ok' : 'chip-bad'}
                        title={lastTestText}
                        className="max-w-[14rem]"
                      >
                        <span className="min-w-0 truncate">{lastTestText}</span>
                      </Badge>
                    ) : (
                      <Badge variant="chip-idle">{t('artifactStorages.testNever', '未测试')}</Badge>
                    )}
                  </TableCell>
                  {/* 操作列：主操作（测试/迁移/设活跃）直显图标；低频（编辑/删除）收起进「更多」。
                      图标按钮走方案 A：28px 命中区 / 15px 图标 / 主操作弱底。 */}
                  <TableCell align="right">
                    <div className="flex items-center justify-end gap-1">
                      <TableIconButton
                        tone="primary"
                        aria-label={t('artifactStorages.test', '测试')}
                        title={t('artifactStorages.test', '测试')}
                        onClick={() => onTest(ch.id)}
                        disabled={rowTestingId === ch.id}
                      >
                        <Zap />
                      </TableIconButton>
                      {/* 迁移入口（FR-348）：任意渠道可为目标（含内置本机 = 回迁）；在途迁移时禁用。 */}
                      <TableIconButton
                        aria-label={t('artifactStorages.migrate.action', '迁移到此')}
                        title={t('artifactStorages.migrate.action', '迁移到此')}
                        onClick={() => setMigrateTarget(ch)}
                        disabled={migrationActive || migrationStarting}
                      >
                        <ArrowRightLeft />
                      </TableIconButton>
                      {!ch.active && (
                        <TableIconButton
                          aria-label={t('artifactStorages.setActive', '设活跃')}
                          title={t('artifactStorages.setActive', '设活跃')}
                          onClick={() => setActivateTarget(ch)}
                        >
                          <CircleCheck />
                        </TableIconButton>
                      )}
                      {/* 内置行不可编辑/删除；活跃行禁删（先切走活跃）。 */}
                      {!ch.builtin && (
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <TableIconButton
                              aria-label={t('common.moreActions', '更多操作')}
                              title={t('common.moreActions', '更多操作')}
                            >
                              <MoreHorizontal />
                            </TableIconButton>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem onSelect={() => openEdit(ch)}>
                              <Pencil className="size-3.5" />
                              {t('common.edit', '编辑')}
                            </DropdownMenuItem>
                            <DropdownMenuItem
                              variant="destructive"
                              disabled={ch.active}
                              onSelect={() => { if (!ch.active) setDeleteTarget(ch) }}
                            >
                              <Trash2 className="size-3.5" />
                              {t('common.delete', '删除')}
                            </DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      )}
                    </div>
                  </TableCell>
                </TableRow>
              )
            })}
            {isLoading && <TableSkeletonRows rows={3} cols={6} />}
            {(!channels || channels.length === 0) && !isLoading && (
              <TableEmptyRow
                colSpan={6}
                icon={<HardDrive />}
                title={t('artifactStorages.empty', '暂无存储渠道')}
                description={t('artifactStorages.emptyHint')}
              />
            )}
          </TableBody>
        </Table>

        <TableCardFooter>
          {t('artifactStorages.cardSummary', { total: (channels ?? []).length, active: activeChannelName })}
        </TableCardFooter>
      </TableCard>

      {/* 设活跃确认：影响后续上传落点（存量制品不迁移、按原渠道读取）。 */}
      <Dialog open={activateTarget !== null} onOpenChange={(o) => { if (!o) setActivateTarget(null) }}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t('artifactStorages.activateConfirmTitle', '切换活跃存储渠道？')}</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">
            {t('artifactStorages.activateConfirmDesc', '之后新上传的客户端分发制品将落入「{{name}}」；已上传的制品保持原位置、读取不受影响。', { name: activateTarget?.name ?? '' })}
          </p>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setActivateTarget(null)}>
              {t('common.cancel', '取消')}
            </Button>
            <Button type="button" onClick={confirmActivate} disabled={activating}>
              {t('artifactStorages.setActive', '设活跃')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 迁移确认（FR-348）：说明逐条搬运语义（校验→改记录→删源、已在目标跳过、可停可续跑）。 */}
      <Dialog open={migrateTarget !== null} onOpenChange={(o) => { if (!o) setMigrateTarget(null) }}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>
              {t('artifactStorages.migrate.confirmTitle', '迁移存量制品到「{{name}}」？', { name: migrateTarget?.name ?? '' })}
            </DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">
            {t('artifactStorages.migrate.confirmDesc', '将把全部存量客户端分发制品逐条搬运到该渠道：逐条校验、更新记录后再删除源副本；已在该渠道的自动跳过。任务可随时强制停止，重新发起即从断点续跑（已迁完成的不重传）。')}
          </p>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setMigrateTarget(null)}>
              {t('common.cancel', '取消')}
            </Button>
            <Button
              type="button"
              onClick={() => { if (migrateTarget) startMigration(migrateTarget.id) }}
              disabled={migrationStarting}
            >
              {t('artifactStorages.migrate.start', '开始迁移')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 失败明细（FR-348）：sha256+原因逐条；重试 = 对同目标重新发起（成功条自动跳过）。
          打开状态是容器的取数键（`failuresTaskId`），故本组件只上报开关意图。 */}
      <Dialog open={failuresTaskId !== null} onOpenChange={(o) => { if (!o) onCloseFailures() }}>
        <DialogContent className={`${scrollableDialogContentClass} sm:max-w-2xl`}>
          <DialogHeader>
            <DialogTitle>{t('artifactStorages.migrate.failuresTitle', '迁移失败明细')}</DialogTitle>
          </DialogHeader>
          <ScrollableDialogBody className="space-y-2">
            {(failures ?? []).map((f) => (
              <div key={f.id} className="rounded-md border p-2 text-xs space-y-1">
                <div className="flex items-center justify-between gap-2">
                  <span className="font-medium truncate">{f.filename || f.sha256}</span>
                  <span className="flex items-center gap-2 text-muted-foreground shrink-0">
                    <span className="font-mono">{f.sha256.slice(0, 12)}</span>
                    <span>{formatBytes(f.size)}</span>
                  </span>
                </div>
                <p className="text-status-danger break-all">{f.reason}</p>
              </div>
            ))}
            {failures && failures.length === 0 && (
              <p className="text-sm text-muted-foreground">{t('artifactStorages.migrate.noFailures', '暂无失败记录')}</p>
            )}
          </ScrollableDialogBody>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onCloseFailures}>
              {t('common.close', '关闭')}
            </Button>
            <Button
              type="button"
              onClick={() => { if (migrationInfo) startMigration(migrationInfo.targetChannelId) }}
              disabled={!migrationInfo || migrationActive || migrationStarting}
            >
              {t('artifactStorages.migrate.retry', '重新发起')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 删除二次确认：scope 保持原页的 platform、不新增 allowed 门禁（删除守卫由后端强制，
          此处仅二次确认）——迁移不改动门禁语义。 */}
      <DangerConfirm
        open={deleteTarget !== null}
        title={t('artifactStorages.deleteConfirm', '确定删除此存储渠道？')}
        scope="platform"
        confirmLabel={t('common.delete', '删除')}
        onConfirm={confirmDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </PageShell>
  )
}

export default ArtifactStoragesPageView
