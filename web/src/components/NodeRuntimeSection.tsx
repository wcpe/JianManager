import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Boxes, Coffee, Copy, Download, Hexagon, Loader2, Radar, Trash2 } from 'lucide-react'
import {
  useNodeRuntimes,
  useScanRuntimes,
  useRegisterRuntime,
  useDeleteRuntime,
  useInstallRuntime,
  type NodeRuntimeItem,
  type RuntimeCandidate,
} from '@/api/runtimes'
import { Button } from '@jianmanager/ui/components/button'
import { Badge } from '@jianmanager/ui/components/badge'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { Input } from '@jianmanager/ui/components/input'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { cn } from '@jianmanager/ui'
import DangerConfirm from '@/components/DangerConfirm'
import NodePMConfigSection from '@/components/NodePMConfigSection'
import { copyToClipboard } from '@/lib/clipboard'

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

interface NodeRuntimeSectionProps {
  nodeId: number
  /** 是否启用查询（所在分段打开时为 true）。 */
  active?: boolean
}

/**
 * 节点「运行时」分区（FR-298 节点运行时库）：
 * - 统一 Runtime 列表（node_jdks + node_runtimes 读侧拼装，类型徽章区分）；
 * - 「扫描发现」按钮开模态（scrollable-dialog 壳）列候选勾选入库，已在库项禁勾；
 * - 删除走 DangerConfirm（type=jdk 委托现链路托管连文件；其它只删记录）。
 * 挂在节点页 JDK 面板下方（NodeJDKPanel 扩展分区）。
 */
export default function NodeRuntimeSection({ nodeId, active = true }: NodeRuntimeSectionProps) {
  const { t } = useTranslation()
  const { data: runtimes, isLoading } = useNodeRuntimes(nodeId, { enabled: active })
  const scan = useScanRuntimes(nodeId)
  const register = useRegisterRuntime(nodeId)
  const del = useDeleteRuntime(nodeId)
  const install = useInstallRuntime(nodeId)

  const [scanOpen, setScanOpen] = useState(false)
  const [candidates, setCandidates] = useState<RuntimeCandidate[] | null>(null)
  const [checked, setChecked] = useState<Record<string, boolean>>({})
  const [pendingDel, setPendingDel] = useState<NodeRuntimeItem | null>(null)
  const [installOpen, setInstallOpen] = useState(false)
  const [installMajor, setInstallMajor] = useState(String(NODE_LTS_MAJORS[0]))

  const installMajorNum = Number.parseInt(installMajor, 10)
  const installMajorValid = Number.isInteger(installMajorNum) && installMajorNum > 0

  // 一键安装 Node.js（FR-299）：202 受理即提示跳任务中心，终态由心跳落库后列表自然出现。
  const onInstall = () => {
    if (!installMajorValid) return
    install.mutate(
      { type: 'nodejs', major: installMajorNum },
      {
        onSuccess: () => {
          toast.success(t('nodes.runtimeLib.installDispatched'))
          setInstallOpen(false)
        },
        onError: (err: Error & { response?: { data?: { message?: string } } }) =>
          toast.error(err.response?.data?.message || t('nodes.runtimeLib.installFailed')),
      },
    )
  }

  const rows = runtimes ?? []
  const selectedCount = useMemo(() => Object.values(checked).filter(Boolean).length, [checked])

  // 打开模态即扫描（重扫也走这里）：候选与勾选态清零重建。
  const runScan = () => {
    setCandidates(null)
    setChecked({})
    scan.mutate(undefined, {
      onSuccess: (list) => setCandidates(list),
      onError: (err: Error & { response?: { data?: { message?: string } } }) => {
        toast.error(err.response?.data?.message || t('nodes.runtimeLib.scanFailed'))
        setScanOpen(false)
      },
    })
  }

  const openScan = () => {
    setScanOpen(true)
    runScan()
  }

  // 勾选入库：逐条 POST（type=jdk 转发现有 JDK 登记链路，其它落 node_runtimes）。
  const onRegisterSelected = async () => {
    const picked = (candidates ?? []).filter((c) => checked[c.path])
    if (picked.length === 0) return
    let okCount = 0
    for (const c of picked) {
      try {
        await register.mutateAsync({
          type: c.type,
          vendor: c.type === 'jdk' ? c.vendor : undefined,
          name: c.type === 'jdk' ? undefined : `${c.vendor} ${c.majorVersion}`,
          majorVersion: c.majorVersion,
          version: c.version,
          arch: c.arch,
          path: c.path,
        })
        okCount++
      } catch (err) {
        const e = err as Error & { response?: { data?: { message?: string } } }
        toast.error(`${c.path}: ${e.response?.data?.message || t('nodes.runtimeLib.registerFailed')}`)
      }
    }
    if (okCount > 0) {
      toast.success(t('nodes.runtimeLib.registerSuccess', { count: okCount }))
      setScanOpen(false)
    }
  }

  const copyPath = async (p: string) => {
    const ok = await copyToClipboard(p)
    if (ok) toast.success(t('artifactCache.pathCopied'))
    else toast.error(t('common.copyFailed'))
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
        <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
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
                  onClick={() => copyPath(rt.path)}
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
            <Button onClick={onInstall} disabled={!installMajorValid || install.isPending}>
              {install.isPending && <Loader2 className="size-4 animate-spin" />}
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
            {scan.isPending ? (
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
            <Button onClick={onRegisterSelected} disabled={selectedCount === 0 || register.isPending}>
              {register.isPending && <Loader2 className="size-4 animate-spin" />}
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
        onConfirm={() => {
          const target = pendingDel!
          setPendingDel(null)
          del.mutate({ id: target.id, type: target.type }, {
            onSuccess: () => toast.success(t('nodes.runtimeLib.deleted')),
            onError: (err: Error & { response?: { data?: { message?: string; instances?: { name: string }[] } } }) => {
              const insts = err.response?.data?.instances
              if (insts && insts.length > 0) {
                toast.error(t('nodes.jdkInUse', { names: insts.map((i) => i.name).join(', ') }))
              } else {
                toast.error(err.response?.data?.message || t('nodes.runtimeLib.deleteFailed'))
              }
            },
          })
        }}
        onCancel={() => setPendingDel(null)}
      />

      {/* 包管理器与 registry 配置（FR-306） */}
      <NodePMConfigSection nodeId={nodeId} active={active} />
    </div>
  )
}
