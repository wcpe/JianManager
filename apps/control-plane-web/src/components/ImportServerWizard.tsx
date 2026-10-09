// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只做候选取数、探测/预检/权限修复/导入写请求与提示文案。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { useNodes } from '@/api/nodes'
import { useNodeJDKs } from '@/api/jdks'
import { useInspectImportDir, useImportServer } from '@/api/importServer'
import { checkNodePathAccess, chmodNodePath } from '@/api/nodeRuntime'
import DirectoryPicker from '@/components/DirectoryPicker'
import type { ComboboxOption } from '@jianmanager/ui/components/combobox'
import ImportServerWizardView, {
  makeImportServerQueryInit,
  type ImportFixOutcome,
  type ImportInspectOutcome,
  type ImportServerQueryInput,
  type ImportServerSubmitResult,
} from '@/components/views/import-server/ImportServerWizardView'

interface ImportServerWizardProps {
  open: boolean
  onClose: () => void
  /** 预选节点（节点页入口传当前节点；实例列表入口不传，由向导内选择）。 */
  initialNodeId?: number
}

/** 取服务端 message，回退到本地文案（端点错误形态：`{ response: { data: { message } } }`）。 */
function apiMessage(err: unknown, fallback: string): string {
  const msg =
    (err as { response?: { data?: { message?: string } } })?.response?.data?.message ||
    (err as Error)?.message
  return msg || fallback
}

/**
 * 导入现有服务器向导接线层（ADR-097 b 范式，原 FR-302 导入现有服务器 / FR-374 权限诊断与就地写预检）。
 *
 * 视图本体是受控视图（见 components/views，不取数、不发请求、不弹 toast、不读路由）；本层负责：
 * - 候选取数：`useNodes()` 映射节点选项（含在线/启动中/离线状态文案）、
 *   `useNodeJDKs(query.nodeId)` 映射该节点的 JDK 选项（查询键由视图上报，选中节点变更或复位即重取）；
 * - 探测与预检：`useInspectImportDir()` 的 mutation 与 `checkNodePathAccess` 注入视图——
 *   探测失败文案在此组装（服务端 message 优先）并 toast，同时交回视图做内联诊断区；
 * - 权限修复：`chmodNodePath` 单路径 chmod，成功/失败提示在此 toast；
 * - 「草稿 → 请求体」组装与一次写请求（POST /instances/import）：Number 化、空值省略；
 * - 成功后的跳转：视图关窗后回调新实例 id，本层 `navigate` 到实例详情。
 *
 * 保留同路径默认导出与同名 props，调用点（InstancesPage）无需改动。
 */
export default function ImportServerWizard({ open, onClose, initialNodeId }: ImportServerWizardProps) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data: nodes } = useNodes()

  // 取数查询键由视图上报（节点草稿留在视图内），本层只保存查询键本身。
  const [query, setQuery] = useState<ImportServerQueryInput>(() => makeImportServerQueryInit(initialNodeId))
  const { data: jdks } = useNodeJDKs(query.nodeId ? Number(query.nodeId) : 0)

  const inspect = useInspectImportDir()
  const importServer = useImportServer()

  /** 节点状态文案（下拉展示；离线也可选，导入失败由服务端报错）。 */
  const nodeStatusLabel = (status: number): string => {
    if (status === 1) return t('importServer.nodeOnline')
    if (status === 2) return t('importServer.nodeStarting')
    return t('importServer.nodeOffline')
  }

  const nodeOptions: ComboboxOption[] = (nodes ?? []).map((n) => ({
    value: String(n.id),
    label: `${n.name} · ${nodeStatusLabel(n.status)}`,
  }))
  const jdkOptions: ComboboxOption[] = (jdks ?? []).map((j) => ({
    value: String(j.id),
    label: `${j.vendor} ${j.majorVersion} (${j.version})`,
  }))

  return (
    <ImportServerWizardView
      open={open}
      initialNodeId={initialNodeId}
      nodeOptions={nodeOptions}
      jdkOptions={jdkOptions}
      importing={importServer.isPending}
      onQueryChange={setQuery}
      onClose={onClose}
      onNotify={(kind, message) =>
        kind === 'success' ? toast.success(message) : kind === 'error' ? toast.error(message) : toast(message)
      }
      onInspect={(target) =>
        // mutation 化探测：以 Promise 收口成「结果 / 失败文案」，失败提示在本层 toast
        new Promise<ImportInspectOutcome>((resolve) => {
          inspect.mutate(
            { nodeId: target.nodeId, path: target.path },
            {
              onSuccess: (res) => resolve({ ok: true, result: res }),
              onError: (err) => {
                const msg = apiMessage(err, t('importServer.inspectFailed'))
                toast.error(msg)
                resolve({ ok: false, message: msg })
              },
            },
          )
        })
      }
      onCheckAccess={(target) => checkNodePathAccess(target.nodeId, target.path)}
      onFixPermission={async (target): Promise<ImportFixOutcome> => {
        try {
          await chmodNodePath(target.nodeId, target.path)
          toast.success(t('importServer.fixOk'))
          return { ok: true }
        } catch (err) {
          const msg = apiMessage(err, t('importServer.fixFailed'))
          toast.error(msg)
          return { ok: false, message: msg }
        }
      }}
      onSubmit={(draft) =>
        // 导入写请求：草稿原文在此组装为请求体（Number 化、空值省略），成功回传新实例 id
        new Promise<ImportServerSubmitResult>((resolve) => {
          importServer.mutate(
            {
              nodeId: Number(draft.nodeId),
              path: draft.path,
              mode: draft.mode,
              name: draft.name.trim(),
              jarPath: draft.jarPath,
              jdkId: draft.jdkId ? Number(draft.jdkId) : undefined,
              registerJdkPaths: draft.jdkPaths.length > 0 ? draft.jdkPaths : undefined,
              memoryMb: draft.memoryMb ? Number(draft.memoryMb) : undefined,
            },
            {
              onSuccess: (inst) => {
                toast.success(t('importServer.success', { name: inst.name }))
                resolve({ ok: true, instanceId: inst.id })
              },
              onError: (err) => {
                toast.error(apiMessage(err, t('importServer.failed')))
                resolve({ ok: false })
              },
            },
          )
        })
      }
      onImported={(instanceId) => navigate(`/instances/${instanceId}`)}
      renderDirectoryPicker={({ nodeId, onPick, onCancel }) => (
        // key=nodeId：换节点即重挂目录选择器（复位其内部浏览路径），与迁包前一致
        <DirectoryPicker key={nodeId} nodeId={nodeId} onPick={onPick} onCancel={onCancel} />
      )}
    />
  )
}
