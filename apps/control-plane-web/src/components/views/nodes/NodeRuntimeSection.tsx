import { useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Boxes, Coffee, Copy, Download, Hexagon, Loader2, Radar, Trash2 } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Badge } from '@jianmanager/ui/components/badge'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { Input } from '@jianmanager/ui/components/input'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import { cn } from '@jianmanager/ui'
import DangerConfirm from '@/components/views/DangerConfirm'
import { copyToClipboard } from '@/lib/shared/clipboard'

/** 运行时类型展示名（专有名词，不进 i18n）。 */
const TYPE_LABEL: Record<string, string> = { jdk: 'JDK', nodejs: 'Node.js', python: 'Python' }

/** 常见 Node.js LTS 主版本快选（最小做法：静态列表 + 自定义输入，不代理 index.json）。 */
const NODE_LTS_MAJORS = [22, 20, 18]

/** 运行时类型图标：jdk=咖啡、nodejs=六边形、其它=通用盒。 */
function TypeIcon({ type, className }: { type: string; className?: string }) {
  if (type === 'jdk') return <Coffee className={className} />
  if (type === 'nodejs') return <Hexagon className={className} />
  return <Boxes className={className} />
}

/** 运行时列表行（本组件所需的最小结构；外壳传 API 返回项会结构兼容）。 */
export interface RuntimeRowView {
  id: number
  type: string
  name: string
  version: string
  arch: string
  path: string
  /** 是否由平台托管（托管项删除会连文件一起删）。 */
  managed?: boolean
  majorVersion: number
}

/** 扫描发现的候选运行时（同上）。 */
export interface RuntimeCandidateView {
  type: string
  vendor: string
  majorVersion: number
  version: string
  arch: string
  path: string
  /** 已在库中：候选禁勾并标注。 */
  alreadyRegistered?: boolean
}

/**
 * 节点「运行时」分区（FR-298 节点运行时库）：
 * - 统一 Runtime 列表（node_jdks + node_runtimes 读侧拼装，类型徽章区分）；
 * - 「扫描发现」按钮开模态（scrollable-dialog 壳）列候选勾选入库，已在库项禁勾；
 * - 删除走 DangerConfirm（type=jdk 委托现链路托管连文件；其它只删记录）。
 *
 * 受控视图（ADR-097 b 范式）：不取数、不发请求、不弹 toast。
 * - 扫描结果、登记成功数、安装与删除的结果都由回调的返回值交回（视图要据此
 *   关闭模态或复位勾选），故四个回调都返回结果而非仅上报；
 * - 分区尾部的两个子区块（包管理器配置、全局包管理）各自取数、不属本视图，
 *   经 `footer` 槽注入——这样视图不必认识它们，也就不会反向依赖应用层组件。
 */
export interface NodeRuntimeSectionProps {
  /** 运行时列表（含 jdk；本组件会过滤掉 jdk，理由见 render 内注释）。 */
  runtimes?: RuntimeRowView[]
  /** 列表加载态。 */
  isLoading?: boolean
  /** 尾部扩展区：外壳注入包管理器配置与全局包等子区块。 */
  footer?: ReactNode
  /** 扫描发现候选。成功回传列表；失败回 null（视图据此关闭模态）。 */
  onScan: () => Promise<RuntimeCandidateView[] | null>
  /** 登记选中的候选（逐条 POST）。回传成功条数，供视图决定提示与是否关闭模态。 */
  onRegister: (picked: RuntimeCandidateView[]) => Promise<number>
  /** 删除一条运行时。返回是否成功。 */
  onDelete: (item: RuntimeRowView) => Promise<boolean>
  /** 一键安装 Node.js 指定 LTS 主版本。返回是否成功。 */
  onInstall: (major: number) => Promise<boolean>
  /** 复制路径的结果上报（由外壳决定提示文案）。 */
  onCopyResult?: (ok: boolean) => void
}

export default function NodeRuntimeSection({
  runtimes,
  isLoading,
  footer,
  onScan,
  onRegister,
  onDelete,
  onInstall,
  onCopyResult,
}: NodeRuntimeSectionProps) {
  const { t } = useTranslation()

  const [scanOpen, setScanOpen] = useState(false)
  const [scanning, setScanning] = useState(false)
  const [candidates, setCandidates] = useState<RuntimeCandidateView[] | null>(null)
  const [checked, setChecked] = useState<Record<string, boolean>>({})
  const [pendingDel, setPendingDel] = useState<RuntimeRowView | null>(null)
  const [deleting, setDeleting] = useState(false)
  const [registering, setRegistering] = useState(false)
  const [installOpen, setInstallOpen] = useState(false)
  const [installing, setInstalling] = useState(false)
  const [installMajor, setInstallMajor] = useState(String(NODE_LTS_MAJORS[0]))

  const installMajorNum = Number.parseInt(installMajor, 10)
  const installMajorValid = Number.isInteger(installMajorNum) && installMajorNum > 0

  // 一键安装 Node.js（FR-299）：202 受理即提示跳任务中心，终态由心跳落库后列表自然出现。
  const submitInstall = async () => {
    if (!installMajorValid) return
    setInstalling(true)
    const ok = await onInstall(installMajorNum)
    setInstalling(false)
    if (ok) setInstallOpen(false)
  }

  // 列表只承载非 JDK 类型（v0.15.0 验收 e2e 抓出的双列重复修复）：JDK 已由上方 JDK 面板
  // 富列表（FR-195 行卡片/筛选/复制/删除守卫）唯一呈现，此处再列 type=jdk 即整页重复；
  // 统一视图保留在 API 层（GET /nodes/:id/runtimes 仍含 jdk），扫描/登记链路不变。
  const rows = (runtimes ?? []).filter((rt) => rt.type !== 'jdk')
  const selectedCount = useMemo(() => Object.values(checked).filter(Boolean).length, [checked])

  // 打开模态即扫描（重扫也走这里）：候选与勾选态清零重建。
  const runScan = async () => {
    setCandidates(null)
    setChecked({})
    setScanning(true)
    const list = await onScan()
    setScanning(false)
    if (list) setCandidates(list)
    else setScanOpen(false)
  }

  const openScan = () => {
    setScanOpen(true)
    void runScan()
  }

  // 勾选入库：逐条 POST（type=jdk 转发现有 JDK 登记链路，其它落 node_runtimes）。
  const onRegisterSelected = async () => {
    const picked = (candidates ?? []).filter((c) => checked[c.path])
    if (picked.length === 0) return
    setRegistering(true)
    const okCount = await onRegister(picked)
    setRegistering(false)
    if (okCount > 0) setScanOpen(false)
  }

  const copyPath = async (p: string) => {
    const ok = await copyToClipboard(p)
    onCopyResult?.(ok)
  }

  return (
    <div className="space-y-3 border-t pt-4">
      {/* 分区头：标题 + 扫描发现 */}
      <div className="flex items-center justify-between gap-2">
        <div>
          <h3 className="text-sm font-semibold">{t('nodes.runtimeLib.title')}</h3>
          <p className="text-xs text-muted-foreground">{t('nodes.runtimeLib.subtitle')}</p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" onClick={() => setInstallOpen(true)}>
            <Download className="size-4" />
            {t('nodes.runtimeLib.installNode')}
          </Button>
          <Button variant="outline" size="sm" onClick={openScan}>
            <Radar className="size-4" />
            {t('nodes.runtimeLib.scan')}
          </Button>
        </div>
      </div>

      {/* 统一列表：类型徽章区分 */}
      {isLoading ? (
        /* 骨架占位：按运行时行卡片（size-9 图标 + 双行文本 ≈ h-14）轮廓，替代裸文字。 */
        <div className="space-y-2">
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-14 w-full" />
        </div>
      ) : rows.length === 0 ? (
        <p className="py-4 text-center text-sm text-muted-foreground">{t('nodes.runtimeLib.empty')}</p>
      ) : (
        <div className="space-y-2">
          {rows.map((rt) => (
            <div
              key={`${rt.type}-${rt.id}`}
              className="flex items-center gap-3 rounded-lg border bg-card px-3 py-2.5 transition-colors hover:bg-muted/40"
            >
              <div
                className={cn(
                  'flex size-9 shrink-0 items-center justify-center rounded-md',
                  rt.managed ? 'bg-accent text-primary' : 'bg-muted text-muted-foreground',
                )}
              >
                <TypeIcon type={rt.type} className="size-[18px]" />
              </div>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <Badge variant="secondary" className="bg-accent font-medium text-primary">
                    {TYPE_LABEL[rt.type] ?? rt.type}
                  </Badge>
                  <span className="font-medium">{rt.name}</span>
                  <span className="text-xs text-muted-foreground">
                    {[rt.version, rt.arch].filter(Boolean).join(' · ')}
                  </span>
                </div>
                <button
                  type="button"
                  className="mt-0.5 flex max-w-full items-center gap-1 font-mono text-xs text-muted-foreground transition-colors hover:text-foreground"
                  title={rt.path}
                  onClick={() => void copyPath(rt.path)}
                >
                  <span className="truncate">{rt.path}</span>
                  <Copy className="size-3 shrink-0" />
                </button>
              </div>
              <span className={cn('whitespace-nowrap text-xs', rt.managed ? 'font-medium text-primary' : 'text-muted-foreground')}>
                {rt.managed ? t('nodes.jdkManaged') : t('nodes.jdkExternal')}
              </span>
              <button
                type="button"
                aria-label={t('common.delete')}
                className="shrink-0 text-muted-foreground transition-colors hover:text-status-danger"
                onClick={() => setPendingDel(rt)}
              >
                <Trash2 className="size-4" />
              </button>
            </div>
          ))}
        </div>
      )}

      {/* 安装 Node.js 模态（FR-299）：LTS 主版本快选 + 自定义输入，202 受理后进度跳任务中心 */}
      <Dialog open={installOpen} onOpenChange={setInstallOpen}>
        <DialogContent className={scrollableDialogContentClass}>
          <DialogHeader>
            <DialogTitle>{t('nodes.runtimeLib.installTitle')}</DialogTitle>
          </DialogHeader>
          <ScrollableDialogBody className="space-y-3">
            <div>
              <label className="mb-1.5 block text-sm font-medium" htmlFor="node-install-major">
                {t('nodes.runtimeLib.installMajor')}
              </label>
              <div className="flex items-center gap-2">
                {NODE_LTS_MAJORS.map((m) => (
                  <Button
                    key={m}
                    type="button"
                    size="sm"
                    variant={installMajorNum === m ? 'default' : 'outline'}
                    onClick={() => setInstallMajor(String(m))}
                  >
                    {m} LTS
                  </Button>
                ))}
                <Input
                  id="node-install-major"
                  className="w-24"
                  inputMode="numeric"
                  value={installMajor}
                  onChange={(e) => setInstallMajor(e.target.value)}
                  aria-label={t('nodes.runtimeLib.installMajor')}
                />
              </div>
            </div>
            <p className="text-xs text-muted-foreground">{t('nodes.runtimeLib.installHint')}</p>
          </ScrollableDialogBody>
          <DialogFooter>
            <Button variant="outline" onClick={() => setInstallOpen(false)}>
              {t('common.cancel')}
            </Button>
            <Button onClick={() => void submitInstall()} disabled={!installMajorValid || installing}>
              {installing && <Loader2 className="size-4 animate-spin" />}
              {t('nodes.runtimeLib.installConfirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 扫描发现模态：scrollable-dialog 壳（头/脚固定、候选超高内部滚动） */}
      <Dialog open={scanOpen} onOpenChange={setScanOpen}>
        <DialogContent className={cn(scrollableDialogContentClass, 'sm:max-w-xl')}>
          <DialogHeader>
            <DialogTitle>{t('nodes.runtimeLib.scanTitle')}</DialogTitle>
          </DialogHeader>
          <ScrollableDialogBody className="space-y-2">
            {scanning ? (
              <p className="flex items-center gap-2 py-6 text-sm text-muted-foreground">
                <Loader2 className="size-4 animate-spin" />
                {t('nodes.runtimeLib.scanning')}
              </p>
            ) : !candidates || candidates.length === 0 ? (
              <p className="py-6 text-center text-sm text-muted-foreground">{t('nodes.runtimeLib.scanEmpty')}</p>
            ) : (
              candidates.map((c) => (
                <label
                  key={`${c.type}-${c.path}`}
                  className={cn(
                    'flex items-center gap-3 rounded-md border px-3 py-2',
                    c.alreadyRegistered ? 'opacity-60' : 'cursor-pointer hover:bg-muted/40',
                  )}
                >
                  <Checkbox
                    checked={!!checked[c.path]}
                    disabled={c.alreadyRegistered}
                    onCheckedChange={(v) => setChecked((prev) => ({ ...prev, [c.path]: v === true }))}
                    aria-label={c.path}
                  />
                  <TypeIcon type={c.type} className="size-4 shrink-0 text-muted-foreground" />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <Badge variant="secondary" className="bg-accent font-medium text-primary">
                        {TYPE_LABEL[c.type] ?? c.type}
                      </Badge>
                      <span className="text-sm font-medium">
                        {c.vendor} {c.majorVersion}
                      </span>
                      <span className="text-xs text-muted-foreground">
                        {[c.version, c.arch].filter(Boolean).join(' · ')}
                      </span>
                    </div>
                    <p className="truncate font-mono text-xs text-muted-foreground" title={c.path}>
                      {c.path}
                    </p>
                  </div>
                  {c.alreadyRegistered && (
                    <span className="whitespace-nowrap rounded-full border px-2 py-0.5 text-xs text-muted-foreground">
                      {t('nodes.runtimeLib.alreadyRegistered')}
                    </span>
                  )}
                </label>
              ))
            )}
          </ScrollableDialogBody>
          <DialogFooter>
            <Button variant="outline" onClick={() => setScanOpen(false)}>
              {t('common.cancel')}
            </Button>
            <Button onClick={() => void onRegisterSelected()} disabled={selectedCount === 0 || registering}>
              {registering && <Loader2 className="size-4 animate-spin" />}
              {t('nodes.runtimeLib.register', { count: selectedCount })}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 删除确认：托管的连文件（jdk 委托现链路 / nodejs 经 RemoveRuntime，FR-299），外部登记只删记录 */}
      <DangerConfirm
        open={pendingDel !== null}
        title={pendingDel?.managed
          ? (pendingDel.type === 'jdk' ? t('nodes.jdkDeleteFilesTitle') : t('nodes.runtimeLib.deleteManagedTitle'))
          : t('nodes.runtimeLib.deleteRecordTitle')}
        description={pendingDel?.managed
          ? (pendingDel.type === 'jdk' ? t('nodes.jdkDeleteFilesDesc') : t('nodes.runtimeLib.deleteManagedDesc'))
          : t('nodes.runtimeLib.deleteRecordDesc')}
        confirmLabel={t('common.delete')}
        confirmText={pendingDel?.managed
          ? (pendingDel.type === 'jdk' ? `${pendingDel.name} ${pendingDel.majorVersion}` : pendingDel.name)
          : undefined}
        pending={deleting}
        onConfirm={() => {
          const target = pendingDel!
          setPendingDel(null)
          setDeleting(true)
          void onDelete(target).finally(() => setDeleting(false))
        }}
        onCancel={() => setPendingDel(null)}
      />

      {footer}
    </div>
  )
}
