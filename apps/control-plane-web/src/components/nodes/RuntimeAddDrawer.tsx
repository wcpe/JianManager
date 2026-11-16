import { useTranslation } from 'react-i18next'
import { Coffee, Pencil, Radar } from 'lucide-react'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@jianmanager/ui/components/sheet'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Segments, Segment } from '@jianmanager/ui/components/layout'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import NodeJDKTab from '@/components/nodes/NodeJDKTab'
import NodeRuntimeSection from '@/components/views/nodes/NodeRuntimeSection'
import { useScanRuntimes, useRegisterRuntime, useInstallRuntime } from '@/api/runtimes'
import { toast } from 'sonner'
import type { RuntimeInventoryRow } from '@/lib/nodes/runtime-inventory'
import { conflictText, parseTaskConflict, tasksHref } from '@/lib/nodes/task-conflict'
import { useState } from 'react'

/**
 * 「新增运行时」抽屉：把原先散在三个模态里的三段合并成一个入口。
 *
 * 【改造前的问题】装 JDK 要走「JDK 面板 → 一键下载 / 登记已有」两个子分段，
 * 装 Node 要到另一块「运行时」面板点「安装 Node.js」弹模态，扫描发现又是第三个模态。
 * 三个动作语义上都是「给这台机器添一个运行时」，却分散在两个面板 + 三个模态里。
 *
 * 现在是一个 Sheet（右侧抽屉），三段并列：
 *   JDK 下载安装 | JDK 登记已有 | 扫描发现（JDK + Node 一次扫）
 * 动作实现全部复用既有受控视图与 mutation，语义不变——变的只是「从哪进去」。
 */
export type RuntimeAddTab = 'install' | 'register' | 'scan'

export interface RuntimeAddDrawerProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  nodeId: number
  online: boolean
  tab: RuntimeAddTab
  onTab: (next: RuntimeAddTab) => void
  /** 正在编辑的运行时行（null=不编辑）。 */
  editing: RuntimeInventoryRow | null
  onCloseEdit: () => void
  onSaveEdit: (
    id: number,
    body: { vendor: string; majorVersion: number; version: string; arch: string; path: string },
  ) => Promise<boolean>
  /** 待删除行（null=不删除）。 */
  pendingDelete: RuntimeInventoryRow | null
  onCloseDelete: () => void
  onConfirmDelete: () => void
  deleting?: boolean
}

export function RuntimeAddDrawer({
  open,
  onOpenChange,
  nodeId,
  online,
  tab,
  onTab,
  editing,
  onCloseEdit,
  onSaveEdit,
  pendingDelete,
  onCloseDelete,
  onConfirmDelete,
  deleting = false,
}: RuntimeAddDrawerProps) {
  const { t } = useTranslation()
  const scan = useScanRuntimes(nodeId)
  const register = useRegisterRuntime(nodeId)
  const install = useInstallRuntime(nodeId)

  // 编辑草稿：厂商 / 大版本 / 版本 / 架构 / 路径（与后端 PUT 字段一致）。
  const [draft, setDraft] = useState<{ vendor: string; majorVersion: string; version: string; arch: string; path: string } | null>(null)

  const startEdit = () => {
    if (!editing) return
    setDraft({
      vendor: editing.name,
      majorVersion: String(editing.majorVersion),
      version: editing.version,
      arch: editing.arch,
      path: editing.path,
    })
  }

  /** JDK 段：直接把受控视图挂进来（下载 + 登记两件事都在它的内部分段里）。 */
  const jdkSection = (
    <NodeJDKTab
      nodeId={nodeId}
      initialTab={tab === 'register' ? 'register' : 'install'}
      // key 带上 tab：从「登记」切到「下载」时重新挂载，让 initialTab 生效。
      key={`jdk-${tab}`}
    />
  )

  return (
    <>
      <Sheet open={open} onOpenChange={onOpenChange}>
        <SheetContent side="right" className="flex w-full flex-col gap-0 p-0 sm:max-w-2xl">
          <SheetHeader className="border-b px-4 py-3">
            <SheetTitle className="flex items-center gap-2 text-base">
              <Coffee className="size-4" />
              {t('nodes.runtime.addRuntime', '新增运行时')}
            </SheetTitle>
            <SheetDescription className="text-xs">
              {t(
                'nodes.runtime.addRuntimeHint',
                '给这台节点添一个可执行运行时。JDK 用于 Java 实例；Node.js 用于依赖 Node 的插件与工具链。',
              )}
            </SheetDescription>
          </SheetHeader>

          <div className="border-b px-4 py-2">
            <Segments aria-label={t('nodes.runtime.addTabs', '新增方式')}>
              <Segment active={tab === 'install'} onClick={() => onTab('install')} data-testid="runtime-add-install">
                {t('nodes.runtime.add.install', '下载安装 JDK')}
              </Segment>
              <Segment active={tab === 'register'} onClick={() => onTab('register')} data-testid="runtime-add-register">
                {t('nodes.runtime.add.register', '登记已有 JDK')}
              </Segment>
              <Segment active={tab === 'scan'} onClick={() => onTab('scan')} data-testid="runtime-add-scan">
                <Radar className="mr-1 inline size-3.5" aria-hidden />
                {t('nodes.runtime.add.scan', '扫描发现')}
              </Segment>
            </Segments>
          </div>

          <div className="min-h-0 flex-1 overflow-y-auto px-4 py-3">
            {!online && (
              <p role="alert" className="mb-2 rounded-md border border-status-warning/40 bg-status-warning/10 px-2.5 py-1.5 text-[11px] text-status-warning">
                {t('nodes.runtime.offlineHint', '节点离线：安装与扫描需要节点在线才能执行。')}
              </p>
            )}

            {tab === 'install' || tab === 'register' ? (
              jdkSection
            ) : (
              /* 扫描发现：复用既有的受控视图（候选勾选入库 + Node.js 一键安装）。 */
              <NodeRuntimeSection
                runtimes={[]}
                /* 在途态来自 mutation（唯一真相），组件内部只做本组件内的连点闸门。 */
                installing={install.isPending}
                onScan={async () => {
                  try {
                    return await scan.mutateAsync(undefined)
                  } catch (err) {
                    toast.error(
                      (err as { response?: { data?: { message?: string } } })?.response?.data?.message ||
                        t('nodes.runtimeLib.scanFailed', '扫描失败'),
                    )
                    return null
                  }
                }}
                onRegister={async (picked) => {
                  let ok = 0
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
                      ok++
                    } catch (err) {
                      toast.error(
                        `${c.path}: ${(err as { response?: { data?: { message?: string } } })?.response?.data?.message || t('nodes.runtimeLib.registerFailed', '入库失败')}`,
                      )
                    }
                  }
                  if (ok > 0) toast.success(t('nodes.runtimeLib.registerSuccess', { count: ok, defaultValue: '已入库 {{count}} 条运行时' }))
                  return ok
                }}
                onDelete={async () => false}
                onInstall={async (major) => {
                  try {
                    await install.mutateAsync({ type: 'nodejs', major })
                    toast.success(t('nodes.runtimeLib.installDispatched', 'Node.js 安装任务已下发'))
                    return true
                  } catch (err) {
                    /*
                      被 409 拦（同节点已有同类任务在跑）与"安装失败"是两回事：
                      后者重试有意义，前者要先去处置那个在跑的任务。
                      所以这里给一条带跳转的提示，而不是让用户对着一句后端 message 干点。
                    */
                    const conflict = parseTaskConflict(err)
                    if (conflict) {
                      toast.warning(conflictText(conflict), {
                        action: { label: t('nodes.runtime.goTasks', '去任务中心'), onClick: () => window.location.assign(tasksHref(conflict)) },
                        duration: 8000,
                      })
                      return false
                    }
                    toast.error(
                      (err as { response?: { data?: { message?: string } } })?.response?.data?.message ||
                        t('nodes.runtimeLib.installFailed', '安装失败'),
                    )
                    return false
                  }
                }}
              />
            )}
          </div>
        </SheetContent>
      </Sheet>

      {/* 编辑登记信息：只在 JDK 行出现（其它类型没有厂商/架构这些可编辑字段）。 */}
      <Dialog
        open={editing !== null}
        onOpenChange={(o) => {
          if (!o) {
            setDraft(null)
            onCloseEdit()
          }
        }}
      >
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Pencil className="size-4" />
              {t('nodes.jdkEdit', '编辑 JDK 登记信息')}
            </DialogTitle>
          </DialogHeader>
          {editing && draft === null && (
            // 首次打开时用当前值初始化草稿（放在渲染里而不是 effect，避免一帧空表单）。
            <button type="button" className="hidden" onClick={startEdit} />
          )}
          {editing && (
            <div className="space-y-2">
              {(
                [
                  ['vendor', t('nodes.jdkVendor', '厂商')],
                  ['majorVersion', t('nodes.jdkMajor', '大版本')],
                  ['version', t('artifactCache.jdkVersionPick', '具体版本')],
                  ['arch', t('nodes.jdkArch', '架构')],
                  ['path', t('nodes.jdkPath', '路径')],
                ] as const
              ).map(([key, label]) => (
                <label key={key} className="block space-y-1">
                  <span className="text-xs font-medium">{label}</span>
                  <Input
                    value={draft?.[key] ?? (key === 'vendor' ? editing.name : key === 'majorVersion' ? String(editing.majorVersion) : (editing as unknown as Record<string, string>)[key] ?? '')}
                    onChange={(e) => setDraft({ ...(draft ?? { vendor: editing.name, majorVersion: String(editing.majorVersion), version: editing.version, arch: editing.arch, path: editing.path }), [key]: e.target.value })}
                    className="h-8"
                  />
                </label>
              ))}
            </div>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={onCloseEdit}>
              {t('common.cancel', '取消')}
            </Button>
            <Button
              onClick={() => {
                if (!editing) return
                const body = draft ?? {
                  vendor: editing.name,
                  majorVersion: String(editing.majorVersion),
                  version: editing.version,
                  arch: editing.arch,
                  path: editing.path,
                }
                void onSaveEdit(editing.id, {
                  vendor: body.vendor.trim(),
                  majorVersion: Number(body.majorVersion) || editing.majorVersion,
                  version: body.version.trim(),
                  arch: body.arch.trim(),
                  path: body.path.trim(),
                })
              }}
            >
              {t('common.save', '保存')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 删除确认：托管项会连同文件一起删，所以要逐字输入名称。 */}
      <DangerConfirm
        open={pendingDelete !== null}
        title={
          pendingDelete?.managed
            ? t('nodes.runtimeLib.deleteManagedTitle', '删除托管运行时及其文件?')
            : t('nodes.runtimeLib.deleteRecordTitle', '删除运行时登记记录?')
        }
        description={
          pendingDelete?.managed
            ? t('nodes.runtimeLib.deleteManagedDesc', '这是平台一键安装的托管运行时，删除将同时清除节点上的整个安装目录，不可恢复。')
            : t('nodes.runtimeLib.deleteRecordDesc', '外部登记的运行时仅删除平台记录，不影响磁盘上的文件。')
        }
        confirmLabel={t('common.delete', '删除')}
        confirmText={pendingDelete?.managed ? `${pendingDelete.name} ${pendingDelete.majorVersion}` : undefined}
        pending={deleting}
        onConfirm={onConfirmDelete}
        onCancel={onCloseDelete}
      />
    </>
  )
}

export default RuntimeAddDrawer
