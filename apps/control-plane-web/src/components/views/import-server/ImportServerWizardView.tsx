/**
 * @file ImportServerWizardView：导入现有服务器向导（选目录 → 探测结果 → 导入方式 → 实例配置）的受控视图，
 *       候选节点/JDK、目录探测、写权限预检、权限修复与导入写请求均由应用容器负责。
 * @input import-server-path（joinAbsPath 拼接绝对路径 / isPermissionErrorMessage 识别权限文案）、
 *        Dialog 与 scrollable-dialog 滚动壳原语、Combobox/Checkbox/Button/FieldLabel 原语、
 *        views/DangerConfirm（单路径 chmod 二次确认）、lucide-react 图标、翻译上下文
 * @output ImportServerWizardView、ImportServerWizardViewProps、ImportServerFormDraft、ImportServerQueryInput、
 *         ImportServerMode、ImportNodeTarget、ImportInspectResult、ImportJarCandidateView、ImportJdkCandidateView、
 *         ImportPathAccess、ImportInspectOutcome、ImportFixOutcome、ImportServerSubmitResult、
 *         makeImportServerQueryInit
 * @sync apps/control-plane-web/src/components/ImportServerWizard.tsx、
 *        apps/control-plane-web/src/components/ImportServerWizard.dom.test.tsx
 * @since FR-502（组件受控化迁包；原 FR-302 导入现有服务器向导、FR-374 权限失败诊断与就地写预检）
 */
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { HardDriveDownload, MapPin, Truck, ShieldAlert } from 'lucide-react'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { FieldLabel } from '@jianmanager/ui/components/field-label'
import { Button } from '@jianmanager/ui/components/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import { joinAbsPath, isPermissionErrorMessage } from '@/lib/import-server/import-server-path'
import { formatFileSize } from '@/lib/shared/format-file-size'

/** 向导步骤：目录 → 探测结果 → 导入方式 → 实例配置。 */
type Step = 'dir' | 'inspect' | 'mode' | 'config'

/** 导入方式（FR-302）：就地接管 / 搬进托管区。 */
export type ImportServerMode = 'in_place' | 'migrate'

/** 一个核心 jar 候选（`ImportJarCandidate` 结构兼容；视图只做单选与展示）。 */
export interface ImportJarCandidateView {
  /** 相对导入根、以「/」分隔的路径。 */
  path: string
  /** 字节数（视图格式化展示）。 */
  size: number
  /** MANIFEST Main-Class 嗅探结果（缺省不展示）。 */
  mainClassHint?: string
}

/** 一个内嵌 JDK 候选（`ImportJdkCandidate` 结构兼容；视图只做勾选与展示）。 */
export interface ImportJdkCandidateView {
  /** JDK home 绝对路径。 */
  path: string
  vendor: string
  version: string
  majorVersion: number
  arch: string
}

/** 目录探测结果（`ImportInspectResult` 结构兼容；由容器注入，视图只读展示）。 */
export interface ImportInspectResult {
  /** 核心 jar 候选（已知核心名排前，由后端排序）。 */
  jars: ImportJarCandidateView[]
  /** 内嵌 JDK 候选。 */
  jdks: ImportJdkCandidateView[]
  /** server.properties 的 server-port（0=未知）。 */
  serverPort: number
  /** EULA 是否已接受。 */
  eulaAccepted: boolean
  /** 是否发现 server.properties（决定就地写预检是否追加该文件的可写性检查）。 */
  propsFound: boolean
}

/** 节点绝对路径可读写探测结果（`NodePathAccess` 结构兼容；视图只用读/写与失败原因）。 */
export interface ImportPathAccess {
  /** 目标路径是否存在。 */
  exists: boolean
  /** 是否可读。 */
  readable: boolean
  /** 是否可写。 */
  writable: boolean
  /** 不可读/不可写的诊断文案（缺省用视图内回退文案）。 */
  reason?: string
}

/** 探测 / 预检 / 权限修复的目标：视图上报意图与参数，容器据此调节点侧接口。 */
export interface ImportNodeTarget {
  /** 目标节点 id。 */
  nodeId: number
  /** 节点上绝对路径。 */
  path: string
}

/**
 * 目录探测的结果：成功携带探测数据，失败携带文案（**探测请求失败由容器提示 toast**，
 * 此处只把文案交回视图做内联诊断区展示，视图不弹提示）。
 */
export type ImportInspectOutcome =
  | { ok: true; result: ImportInspectResult }
  | { ok: false; message: string }

/** 权限修复结果：成功=容器已提示可重新探测；失败携带文案供视图内联展示。 */
export type ImportFixOutcome = { ok: true } | { ok: false; message: string }

/**
 * 导入提交结果：成功携带新实例 id——视图据此关窗并回调 `onImported`；
 * 失败（`ok: false`）时容器已提示，视图保留草稿与窗口便于重试。
 */
export type ImportServerSubmitResult = { ok: true; instanceId: number } | { ok: false }

/**
 * 取数输入（查询键）：**上提容器**——选中节点一变就要重新取该节点的 JDK 列表，是查询语义。
 * 视图仍持有节点草稿，在选中变更与复位时上报快照，容器只保存查询键本身。
 */
export interface ImportServerQueryInput {
  /** 选中节点 id 原文（'' = 未选）。 */
  nodeId: string
}

/**
 * 取数输入初值（= 表单初值 / 复位值）：预选节点时以其为初值。
 * 容器初始化查询键时复用同一函数，避免视图与容器两处默认值漂移。
 */
// eslint-disable-next-line react-refresh/only-export-components -- 与组件同文件才保证初值单一真源（容器据此初始化查询键）
export function makeImportServerQueryInit(initialNodeId?: number): ImportServerQueryInput {
  return { nodeId: initialNodeId ? String(initialNodeId) : '' }
}

/**
 * 表单草稿（**原文**，非请求体）：数值/数组字段保持用户输入的字符串与原始列表，
 * 由容器组装请求体（Number 化、空值省略）——「草稿 → 请求体」属请求语义，留在应用侧。
 */
export interface ImportServerFormDraft {
  /** 目标节点 id 原文（必填）。 */
  nodeId: string
  /** 导入根路径（探测成功后落定）。 */
  path: string
  /** 导入方式。 */
  mode: ImportServerMode
  /** 实例名原文（必填）。 */
  name: string
  /** 所选核心 jar 路径（必填；inspect 步的「下一步」即以此为门禁）。 */
  jarPath: string
  /** 绑定的节点 JDK id 原文（'' = 不绑定，用系统 Java）。 */
  jdkId: string
  /** 勾选登记进节点运行时库的内嵌 JDK 路径。 */
  jdkPaths: string[]
  /** 内存 MB 原文（空 = 由服务端取默认）。 */
  memoryMb: string
}

/**
 * 受控边界（ADR-097 b 范式）：**不取数、不发请求、不弹 toast、不读路由**。
 * - 候选经 props 注入：`nodeOptions`（容器调 `useNodes` 并拼在线状态文案）、
 *   `jdkOptions`（容器调 `useNodeJDKs(query.nodeId)`）；
 * - 取数输入经 `onQueryChange` 上报（仅节点）：容器据此重取 JDK 列表；
 * - 目录探测经 `onInspect` 上报：返回结果或失败文案——失败 toast 由容器发，视图只做内联诊断区；
 * - 写权限预检（FR-374）经 `onCheckAccess` 上报：地址拼装（根目录 + server.properties）与
 *   阻断/提示判定留本视图（它是 UI 状态：预检在途、未通过、提示文案）；探测失败由视图 catch 收口；
 * - 修复权限经 `onFixPermission` 上报：成功/失败提示由容器发，视图据返回值决定是否收起确认框并重新探测；
 * - 目录选择器经 `renderDirectoryPicker` 插槽注入（实现自带取数，属应用侧）；
 * - 提交经 `onSubmit` 上报：成功携带新实例 id——视图先关窗再回调 `onImported`（跳转由容器做，
 *   视图不引入 react-router），失败保留草稿与窗口；
 * - 提示经 `onNotify` 上报：仅「已建议搬进托管区」与「就地写预检未通过时提交」两处文案；
 * - **留本视图**的 UI 状态：步骤推进、全部表单草稿、探测结果、行内诊断（权限错误 / 预检提示）、
 *   三个在途标志（探测、预检、chmod）与 chmod 二次确认开合——都不触发取数；
 * - 开合：`open` 由容器持有；取消 / 遮罩 / Esc / 提交成功均走 `onClose`（视图已先复位草稿与步骤，
 *   并同步把查询键复位回 `initialNodeId` 对应的初值，原实现的 `inspect.reset()` 随之退化为本地复位）。
 */
export interface ImportServerWizardViewProps {
  /** 是否展示对话框。 */
  open: boolean
  /** 预选节点（节点页入口传当前节点；实例列表入口不传，由向导内选择）。 */
  initialNodeId?: number
  /** 可选节点（容器已映射为选项，含在线/启动中/离线状态文案）。 */
  nodeOptions: ComboboxOption[]
  /** 选中节点上的可选 JDK（容器调 `useNodeJDKs(query.nodeId)` 映射）。 */
  jdkOptions: ComboboxOption[]
  /** 导入写请求在途：禁用提交并显示「导入中…」。 */
  importing?: boolean
  /** 目录探测（容器调 `useInspectImportDir()` 的 mutation）：返回结果或失败文案。 */
  onInspect: (target: ImportNodeTarget) => Promise<ImportInspectOutcome>
  /** 单路径可读写预检（容器调 `checkNodePathAccess`）；失败可 reject，文案由视图收口。 */
  onCheckAccess: (target: ImportNodeTarget) => Promise<ImportPathAccess>
  /** 尝试修复权限（容器调 `chmodNodePath` 并提示结果）；失败回传文案供视图内联展示。 */
  onFixPermission: (target: ImportNodeTarget) => Promise<ImportFixOutcome>
  /** 提交导入（容器组装请求体并调 `useImportServer()`）；成功回传新实例 id。 */
  onSubmit: (draft: ImportServerFormDraft) => Promise<ImportServerSubmitResult>
  /** 导入成功且已关窗后回调新实例 id；容器据此跳转实例详情（视图不引入路由）。 */
  onImported?: (instanceId: number) => void
  /** 轻提示通道：视图把提示文案与级别上报，toast 由容器负责。 */
  onNotify?: (kind: 'success' | 'error' | 'info', message: string) => void
  /** 取数输入变化上报（选中节点）；复位时会回传初值。 */
  onQueryChange: (query: ImportServerQueryInput) => void
  /** 关闭对话框（取消 / 遮罩 / Esc / 提交成功后由本视图调用）。 */
  onClose: () => void
  /** 目录选择器插槽（实现自带取数，属应用侧）；缺省不渲染选择器，仅保留手输路径探测。 */
  renderDirectoryPicker?: (args: {
    /** 目标节点 id（实现据此取目录树）。 */
    nodeId: number
    /** 选定目录回调（传回绝对路径），触发探测。 */
    onPick: (path: string) => void
    /** 取消/关闭（与本视图的关窗同义）。 */
    onCancel: () => void
  }) => ReactNode
}

/** 人类可读文件大小（jar 候选展示）。 */
/** 从目录路径提取默认实例名（最后一段）。 */
function dirBaseName(path: string): string {
  const parts = path.replace(/[\\/]+$/, '').split(/[\\/]/)
  return parts[parts.length - 1] || 'imported-server'
}

/** 从异常里取文案，回退到给定文案（与迁包前的取法一致：服务端 message 优先）。 */
function errorMessage(err: unknown, fallback: string): string {
  const msg =
    (err as { response?: { data?: { message?: string } } })?.response?.data?.message ||
    (err as Error)?.message
  return msg || fallback
}

/**
 * 导入现有服务器向导（FR-302 + FR-374）：选目录/手输绝对路径 → 探测 → 模式 → 配置 → 提交。
 * FR-374：权限失败诊断与尝试修复、就地写预检阻断。
 */
export default function ImportServerWizardView({
  open,
  initialNodeId,
  nodeOptions,
  jdkOptions,
  importing = false,
  onInspect,
  onCheckAccess,
  onFixPermission,
  onSubmit,
  onImported,
  onNotify,
  onQueryChange,
  onClose,
  renderDirectoryPicker,
}: ImportServerWizardViewProps) {
  const { t } = useTranslation()

  /** 节点草稿的初值与复位值同源（容器据同一构造初始化查询键）。 */
  const nodeIdInit = makeImportServerQueryInit(initialNodeId).nodeId

  const [step, setStep] = useState<Step>('dir')
  const [nodeId, setNodeId] = useState(nodeIdInit)
  const [path, setPath] = useState('')
  const [pathDraft, setPathDraft] = useState('')
  const [result, setResult] = useState<ImportInspectResult | null>(null)
  const [jarPath, setJarPath] = useState('')
  const [jdkPaths, setJdkPaths] = useState<string[]>([])
  const [mode, setMode] = useState<ImportServerMode>('in_place')
  const [name, setName] = useState('')
  const [memoryMb, setMemoryMb] = useState('2048')
  const [jdkId, setJdkId] = useState('')
  /** FR-374：最近一次权限/预检失败文案（内联展示）。 */
  const [permError, setPermError] = useState('')
  /** FR-374：就地写预检是否未通过。 */
  const [writePrecheckFailed, setWritePrecheckFailed] = useState(false)
  const [precheckHint, setPrecheckHint] = useState('')
  const [chmodOpen, setChmodOpen] = useState(false)
  const [chmodBusy, setChmodBusy] = useState(false)
  const [precheckBusy, setPrecheckBusy] = useState(false)
  /** 探测在途（迁包前取 react-query 的 `inspect.isPending`，现由本视图自持）。 */
  const [inspecting, setInspecting] = useState(false)

  const reset = () => {
    setStep('dir')
    setNodeId(nodeIdInit)
    setPath('')
    setPathDraft('')
    setResult(null)
    setJarPath('')
    setJdkPaths([])
    setMode('in_place')
    setName('')
    setMemoryMb('2048')
    setJdkId('')
    setPermError('')
    setWritePrecheckFailed(false)
    setPrecheckHint('')
    setChmodOpen(false)
    setInspecting(false)
    // 查询键同步复位：容器随之中止该节点的 JDK 取数（原实现此处调 inspect.reset()）
    onQueryChange(makeImportServerQueryInit(initialNodeId))
  }

  const close = () => {
    onClose()
    reset()
  }

  /** FR-374：就地模式写预检（根目录 + server.properties）。不阻断探测流程，只标记未通过。 */
  const runWritePrecheck = async (target: ImportNodeTarget, res: ImportInspectResult) => {
    setPrecheckBusy(true)
    setWritePrecheckFailed(false)
    setPrecheckHint('')
    try {
      const rootAccess = await onCheckAccess(target)
      if (!rootAccess.readable) {
        setWritePrecheckFailed(true)
        setPrecheckHint(rootAccess.reason || t('importServer.precheckNotReadable'))
        return
      }
      // 预检时默认按就地假设；migrate 在 mode 步再放宽
      if (!rootAccess.writable) {
        setWritePrecheckFailed(true)
        setPrecheckHint(rootAccess.reason || t('importServer.precheckNotWritable'))
        return
      }
      if (res.propsFound) {
        const propsPath = joinAbsPath(target.path, 'server.properties')
        const fa = await onCheckAccess({ nodeId: target.nodeId, path: propsPath })
        if (fa.exists && !fa.writable) {
          setWritePrecheckFailed(true)
          setPrecheckHint(fa.reason || t('importServer.precheckPropsNotWritable'))
          return
        }
      }
    } catch (err: unknown) {
      setWritePrecheckFailed(true)
      setPrecheckHint(errorMessage(err, t('importServer.precheckFailed')))
    } finally {
      setPrecheckBusy(false)
    }
  }

  /**
   * 目录探测：先落路径并清掉上一轮诊断态，再上报容器（探测请求与失败 toast 由容器负责）；
   * 成功后展示探测结果并触发就地写预检。
   */
  const runInspect = async (picked: string) => {
    const target = picked.trim()
    if (!target || !nodeId) return
    setPath(target)
    setPathDraft(target)
    setPermError('')
    setWritePrecheckFailed(false)
    setPrecheckHint('')
    const nodeTarget: ImportNodeTarget = { nodeId: Number(nodeId), path: target }
    setInspecting(true)
    let outcome: ImportInspectOutcome
    try {
      outcome = await onInspect(nodeTarget)
    } catch (err: unknown) {
      // 容器契约是「以 ok:false 落定」，此处仅兜底契约外的异常
      outcome = { ok: false, message: errorMessage(err, t('importServer.inspectFailed')) }
    } finally {
      setInspecting(false)
    }
    if (!outcome.ok) {
      setPermError(outcome.message)
      return
    }
    const res = outcome.result
    setResult(res)
    setJarPath(res.jars[0]?.path ?? '')
    setJdkPaths([])
    if (!name) setName(dirBaseName(target))
    setStep('inspect')
    await runWritePrecheck(nodeTarget, res)
  }

  /** FR-374：请求容器对当前路径做单路径 chmod，成功后重新探测。 */
  const tryFixPerm = async () => {
    const target = pathDraft.trim()
    if (!nodeId || !target) return
    setChmodBusy(true)
    let outcome: ImportFixOutcome
    try {
      outcome = await onFixPermission({ nodeId: Number(nodeId), path: target })
    } catch (err: unknown) {
      outcome = { ok: false, message: errorMessage(err, t('importServer.fixFailed')) }
    } finally {
      setChmodBusy(false)
    }
    if (!outcome.ok) {
      setPermError(outcome.message)
      return
    }
    setChmodOpen(false)
    setPermError('')
    await runInspect(target)
  }

  const toggleJdk = (p: string) =>
    setJdkPaths((prev) => (prev.includes(p) ? prev.filter((x) => x !== p) : [...prev, p]))

  /** 就地且写预检失败时禁止提交。migrate 不要求源可写。 */
  const submitBlocked = mode === 'in_place' && writePrecheckFailed

  /** 当前草稿（原文）；请求体组装在容器完成。 */
  const draft = (): ImportServerFormDraft => ({
    nodeId,
    path,
    mode,
    name,
    jarPath,
    jdkId,
    jdkPaths,
    memoryMb,
  })

  /** 提交：成功才关窗并回调新实例 id；失败保留草稿与窗口便于重试（提示由容器负责）。 */
  const submit = async () => {
    if (submitBlocked) {
      onNotify?.('error', precheckHint || t('importServer.precheckBlocked'))
      return
    }
    const res = await onSubmit(draft())
    if (!res.ok) return
    close()
    onImported?.(res.instanceId)
  }

  /** 权限诊断区/预检区的「改用搬进托管区」：切模式并提示；预检未通过同时清掉阻断态。 */
  const suggestMigrate = (clearPrecheck: boolean) => {
    setMode('migrate')
    if (clearPrecheck) setWritePrecheckFailed(false)
    onNotify?.('info', t('importServer.switchMigrateHint'))
  }

  if (!open) return null

  const stepTitle: Record<Step, string> = {
    dir: t('importServer.stepDir'),
    inspect: t('importServer.stepInspect'),
    mode: t('importServer.stepMode'),
    config: t('importServer.stepConfig'),
  }

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) close() }}>
      <DialogContent
        showCloseButton={false}
        onPointerDownOutside={(event) => event.preventDefault()}
        className={`${scrollableDialogContentClass} sm:max-w-lg`}
      >
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <HardDriveDownload className="size-4" />
            {t('importServer.title')}
          </DialogTitle>
          <DialogDescription className="text-xs">{stepTitle[step]}</DialogDescription>
        </DialogHeader>

        <ScrollableDialogBody className="space-y-3 py-2">
          {step === 'dir' && (
            <ImportDirStep
              nodeId={nodeId}
              nodeOptions={nodeOptions}
              pathDraft={pathDraft}
              permError={permError}
              inspecting={inspecting}
              onNodeChange={(v) => {
                setNodeId(v)
                setPath('')
                setPathDraft('')
                setPermError('')
                onQueryChange({ nodeId: v })
              }}
              onPathDraftChange={setPathDraft}
              onInspect={(picked) => { void runInspect(picked) }}
              onTryFix={() => setChmodOpen(true)}
              onSuggestMigrate={() => suggestMigrate(false)}
              onCancel={close}
              renderDirectoryPicker={renderDirectoryPicker}
            />
          )}

          {step === 'inspect' && result && (
            <ImportInspectStep
              path={path}
              result={result}
              jarPath={jarPath}
              jdkPaths={jdkPaths}
              precheckBusy={precheckBusy}
              writePrecheckFailed={writePrecheckFailed}
              precheckHint={precheckHint}
              onPickJar={setJarPath}
              onToggleJdk={toggleJdk}
              onTryFix={() => setChmodOpen(true)}
              onSuggestMigrate={() => suggestMigrate(true)}
            />
          )}

          {step === 'mode' && (
            <ImportModeStep mode={mode} writePrecheckFailed={writePrecheckFailed} onModeChange={setMode} />
          )}

          {step === 'config' && (
            <ImportConfigStep
              name={name}
              memoryMb={memoryMb}
              jdkId={jdkId}
              jdkOptions={jdkOptions}
              mode={mode}
              submitBlocked={submitBlocked}
              onNameChange={setName}
              onMemoryChange={setMemoryMb}
              onJdkChange={setJdkId}
            />
          )}
        </ScrollableDialogBody>

        <DialogFooter className="flex-row justify-end gap-2 pt-2">
          <Button type="button" variant="outline" onClick={close}>
            {t('common.cancel')}
          </Button>
          {step !== 'dir' && (
            <Button
              type="button"
              variant="outline"
              onClick={() => setStep(step === 'config' ? 'mode' : step === 'mode' ? 'inspect' : 'dir')}
            >
              {t('importServer.back')}
            </Button>
          )}
          {(step === 'inspect' || step === 'mode') && (
            <Button
              type="button"
              disabled={
                (step === 'inspect' && !jarPath) ||
                (step === 'mode' && mode === 'in_place' && writePrecheckFailed)
              }
              onClick={() => setStep(step === 'inspect' ? 'mode' : 'config')}
            >
              {t('importServer.next')}
            </Button>
          )}
          {step === 'config' && (
            <Button
              type="button"
              disabled={importing || !name.trim() || !jarPath || submitBlocked}
              onClick={() => { void submit() }}
            >
              {importing ? t('importServer.importing') : t('importServer.submit')}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>

      {/* chmod 二次确认沿用迁包前的门禁语义：原调用点未声明 scope，受控版据此不做角色门禁（不新增不放宽） */}
      <DangerConfirm
        open={chmodOpen}
        onCancel={() => setChmodOpen(false)}
        title={t('importServer.tryFixPerm')}
        description={t('importServer.fixConfirmDesc', { path: pathDraft || path })}
        confirmLabel={t('importServer.tryFixPerm')}
        pending={chmodBusy}
        onConfirm={() => { void tryFixPerm() }}
      />
    </Dialog>
  )
}

/** dir 步：选目标节点 + 手输绝对路径 / 插槽目录浏览 → 触发探测，并展示权限失败诊断区。 */
interface ImportDirStepProps {
  /** 节点草稿原文（'' = 未选，此时只提示先选节点）。 */
  nodeId: string
  nodeOptions: ComboboxOption[]
  /** 绝对路径输入框内容（未探测落定前的草稿）。 */
  pathDraft: string
  /** 上一轮探测的失败文案（缺省不渲染诊断区）。 */
  permError: string
  /** 探测在途：禁用「探测」按钮并显示进度文案。 */
  inspecting: boolean
  /** 选中节点（父视图同时复位路径与诊断态，并上报查询键）。 */
  onNodeChange: (nodeId: string) => void
  onPathDraftChange: (value: string) => void
  /** 触发对某路径的探测（手输回车 / 点按钮 / 插槽选定目录共用）。 */
  onInspect: (path: string) => void
  /** 打开权限修复二次确认。 */
  onTryFix: () => void
  /** 建议改用搬迁模式（权限诊断区入口）。 */
  onSuggestMigrate: () => void
  /** 插槽目录选择器的取消回调（与本视图关窗同义）。 */
  onCancel: () => void
  renderDirectoryPicker?: ImportServerWizardViewProps['renderDirectoryPicker']
}

function ImportDirStep({
  nodeId,
  nodeOptions,
  pathDraft,
  permError,
  inspecting,
  onNodeChange,
  onPathDraftChange,
  onInspect,
  onTryFix,
  onSuggestMigrate,
  onCancel,
  renderDirectoryPicker,
}: ImportDirStepProps) {
  const { t } = useTranslation()
  const showPermActions = !!permError && isPermissionErrorMessage(permError)

  return (
    <>
      <div>
        <FieldLabel required>{t('importServer.nodeLabel')}</FieldLabel>
        <div className="mt-1">
          <Combobox
            options={nodeOptions}
            value={nodeId}
            onChange={onNodeChange}
            allowCustom={false}
            placeholder={t('importServer.selectNode')}
          />
        </div>
      </div>
      {nodeId ? (
        <div className="space-y-2">
          <div>
            <FieldLabel>{t('importServer.pathInputLabel')}</FieldLabel>
            <div className="mt-1 flex gap-2">
              <input
                value={pathDraft}
                onChange={(e) => onPathDraftChange(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault()
                    onInspect(pathDraft)
                  }
                }}
                className="min-w-0 flex-1 rounded-md border bg-background px-3 py-2 font-mono text-xs"
                placeholder={t('importServer.pathPlaceholder')}
                aria-label={t('importServer.pathInputLabel')}
              />
              <Button
                type="button"
                size="sm"
                disabled={!pathDraft.trim() || inspecting}
                onClick={() => onInspect(pathDraft)}
              >
                {t('importServer.pathProbe')}
              </Button>
            </div>
            <p className="mt-1 text-xs text-muted-foreground">{t('importServer.pathHint')}</p>
          </div>

          {permError && (
            <div
              className="space-y-2 rounded-md border border-destructive/40 bg-destructive/5 p-2 text-xs"
              role="alert"
              data-testid="import-perm-error"
            >
              <p className="flex items-start gap-1.5 text-destructive">
                <ShieldAlert className="mt-0.5 size-3.5 shrink-0" />
                <span>{permError}</span>
              </p>
              {showPermActions && (
                <div className="flex flex-wrap gap-2">
                  <Button type="button" size="sm" variant="outline" onClick={onTryFix}>
                    {t('importServer.tryFixPerm')}
                  </Button>
                  <Button type="button" size="sm" variant="outline" onClick={onSuggestMigrate}>
                    {t('importServer.suggestMigrate')}
                  </Button>
                </div>
              )}
            </div>
          )}

          <div>
            <FieldLabel>{t('importServer.dirLabel')}</FieldLabel>
            <div className="mt-1">
              {renderDirectoryPicker?.({ nodeId: Number(nodeId), onPick: onInspect, onCancel })}
            </div>
            {inspecting && (
              <p className="mt-1 text-xs text-muted-foreground">{t('importServer.inspecting')}</p>
            )}
          </div>
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">{t('importServer.selectNodeFirst')}</p>
      )}
    </>
  )
}

/** 探测结果步：就地写预检提示 + jar 单选 + 内嵌 JDK 勾选 + 端口/EULA 概览。 */
interface ImportInspectStepProps {
  /** 已探测落定的根路径。 */
  path: string
  result: ImportInspectResult
  /** 所选 jar 路径（空 = 未选，下一步被门禁）。 */
  jarPath: string
  /** 勾选登记的内嵌 JDK 路径。 */
  jdkPaths: string[]
  /** 预检在途（展示「正在检查写权限…」）。 */
  precheckBusy: boolean
  /** 就地写预检未通过（展示黄色阻断区）。 */
  writePrecheckFailed: boolean
  /** 预检未通过的诊断文案。 */
  precheckHint: string
  onPickJar: (path: string) => void
  onToggleJdk: (path: string) => void
  /** 打开权限修复二次确认。 */
  onTryFix: () => void
  /** 建议改用搬迁模式（预检阻断区入口）。 */
  onSuggestMigrate: () => void
}

function ImportInspectStep({
  path,
  result,
  jarPath,
  jdkPaths,
  precheckBusy,
  writePrecheckFailed,
  precheckHint,
  onPickJar,
  onToggleJdk,
  onTryFix,
  onSuggestMigrate,
}: ImportInspectStepProps) {
  const { t } = useTranslation()

  return (
    <>
      <p className="break-all rounded bg-muted/40 px-2 py-1 font-mono text-xs" title={path}>{path}</p>

      {(writePrecheckFailed || precheckBusy) && (
        <div
          className="space-y-2 rounded-md border border-yellow-600/40 bg-yellow-500/5 p-2 text-xs"
          data-testid="import-write-precheck"
        >
          {precheckBusy ? (
            <p className="text-muted-foreground">{t('importServer.prechecking')}</p>
          ) : (
            <>
              <p className="text-yellow-700 dark:text-yellow-500">
                {precheckHint || t('importServer.precheckBlocked')}
              </p>
              <div className="flex flex-wrap gap-2">
                <Button type="button" size="sm" variant="outline" onClick={onTryFix}>
                  {t('importServer.tryFixPerm')}
                </Button>
                <Button type="button" size="sm" variant="outline" onClick={onSuggestMigrate}>
                  {t('importServer.suggestMigrate')}
                </Button>
              </div>
            </>
          )}
        </div>
      )}

      <div>
        <FieldLabel required>{t('importServer.jarSection')}</FieldLabel>
        {result.jars.length === 0 ? (
          <p className="mt-1 text-sm text-destructive">{t('importServer.noJars')}</p>
        ) : (
          <div className="mt-1 max-h-48 space-y-1 overflow-y-auto rounded border p-1.5">
            {result.jars.map((j) => (
              <label key={j.path} className="flex cursor-pointer items-start gap-2 rounded px-1.5 py-1 text-sm hover:bg-accent">
                <input
                  type="radio"
                  name="import-jar"
                  className="mt-1"
                  checked={jarPath === j.path}
                  onChange={() => onPickJar(j.path)}
                />
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-mono text-xs">{j.path}</span>
                  <span className="block text-xs text-muted-foreground">
                    {formatFileSize(j.size)}
                    {j.mainClassHint ? ` · Main-Class: ${j.mainClassHint}` : ''}
                  </span>
                </span>
              </label>
            ))}
          </div>
        )}
      </div>

      <div>
        <FieldLabel>{t('importServer.jdkSection')}</FieldLabel>
        {result.jdks.length === 0 ? (
          <p className="mt-1 text-xs text-muted-foreground">{t('importServer.noJdks')}</p>
        ) : (
          <div className="mt-1 space-y-1 rounded border p-1.5">
            {result.jdks.map((j) => (
              <label key={j.path} className="flex cursor-pointer items-start gap-2 rounded px-1.5 py-1 text-sm hover:bg-accent">
                <Checkbox
                  checked={jdkPaths.includes(j.path)}
                  onCheckedChange={() => onToggleJdk(j.path)}
                  aria-label={j.path}
                />
                <span className="min-w-0 flex-1">
                  <span className="block text-sm">{j.vendor} {j.majorVersion} ({j.version}, {j.arch})</span>
                  <span className="block truncate font-mono text-xs text-muted-foreground">{j.path}</span>
                </span>
              </label>
            ))}
          </div>
        )}
      </div>

      <div className="grid grid-cols-2 gap-3 text-sm">
        <div>
          <span className="text-xs text-muted-foreground">{t('importServer.portLabel')}</span>
          <p>{result.propsFound && result.serverPort > 0 ? result.serverPort : t('importServer.portUnknown')}</p>
        </div>
        <div>
          <span className="text-xs text-muted-foreground">{t('importServer.eulaLabel')}</span>
          <p className={result.eulaAccepted ? '' : 'text-yellow-600'}>
            {result.eulaAccepted ? t('importServer.eulaAccepted') : t('importServer.eulaMissing')}
          </p>
        </div>
      </div>
    </>
  )
}

/** 导入方式步：就地接管 / 搬进托管区二选一（就地且预检未通过时提示阻断）。 */
interface ImportModeStepProps {
  mode: ImportServerMode
  /** 就地写预检未通过（就地选项下展示阻断提示）。 */
  writePrecheckFailed: boolean
  onModeChange: (mode: ImportServerMode) => void
}

function ImportModeStep({ mode, writePrecheckFailed, onModeChange }: ImportModeStepProps) {
  const { t } = useTranslation()

  return (
    <div className="space-y-2">
      {([
        { value: 'in_place' as const, icon: MapPin, title: t('importServer.modeInPlace'), desc: t('importServer.modeInPlaceDesc') },
        { value: 'migrate' as const, icon: Truck, title: t('importServer.modeMigrate'), desc: t('importServer.modeMigrateDesc') },
      ]).map((opt) => (
        <label
          key={opt.value}
          className={`flex cursor-pointer items-start gap-3 rounded-lg border p-3 transition-colors ${
            mode === opt.value ? 'border-primary bg-primary/5' : 'hover:bg-accent/50'
          }`}
        >
          <input
            type="radio"
            name="import-mode"
            className="mt-1"
            checked={mode === opt.value}
            onChange={() => onModeChange(opt.value)}
          />
          <span className="min-w-0 flex-1">
            <span className="flex items-center gap-1.5 text-sm font-medium">
              <opt.icon className="size-3.5" /> {opt.title}
            </span>
            <span className="mt-0.5 block text-xs text-muted-foreground">{opt.desc}</span>
          </span>
        </label>
      ))}
      {mode === 'in_place' && writePrecheckFailed && (
        <p className="text-xs text-destructive" data-testid="import-inplace-blocked">
          {t('importServer.precheckBlocked')}
        </p>
      )}
    </div>
  )
}

/** 配置步：实例名 / 内存 / 绑定 JDK + 当前模式说明（预检未通过时二次提示阻断）。 */
interface ImportConfigStepProps {
  name: string
  memoryMb: string
  /** 绑定 JDK id 原文（'' = 不绑定）。 */
  jdkId: string
  jdkOptions: ComboboxOption[]
  mode: ImportServerMode
  /** 就地且预检未通过（提交被门禁）。 */
  submitBlocked: boolean
  onNameChange: (value: string) => void
  onMemoryChange: (value: string) => void
  onJdkChange: (value: string) => void
}

function ImportConfigStep({
  name,
  memoryMb,
  jdkId,
  jdkOptions,
  mode,
  submitBlocked,
  onNameChange,
  onMemoryChange,
  onJdkChange,
}: ImportConfigStepProps) {
  const { t } = useTranslation()

  return (
    <>
      <div>
        <FieldLabel required>{t('importServer.nameLabel')}</FieldLabel>
        <input
          value={name}
          onChange={(e) => onNameChange(e.target.value)}
          className="mt-1 w-full rounded-md border bg-background px-3 py-2 text-sm"
          placeholder="old-server"
          aria-label={t('importServer.nameLabel')}
        />
      </div>
      <div className="grid grid-cols-2 gap-3">
        <div>
          <FieldLabel>{t('importServer.memoryLabel')}</FieldLabel>
          <input
            value={memoryMb}
            onChange={(e) => onMemoryChange(e.target.value)}
            inputMode="numeric"
            className="mt-1 w-full rounded-md border bg-background px-3 py-2 text-sm"
            placeholder="2048"
            aria-label={t('importServer.memoryLabel')}
          />
        </div>
        <div>
          <FieldLabel>{t('importServer.jdkBindLabel')}</FieldLabel>
          <div className="mt-1">
            <Combobox
              options={jdkOptions}
              value={jdkId}
              onChange={onJdkChange}
              allowCustom={false}
              placeholder={t('importServer.noJdkBind')}
            />
          </div>
        </div>
      </div>
      <p className="rounded bg-muted/40 px-2 py-1.5 text-xs text-muted-foreground">
        {mode === 'in_place' ? t('importServer.modeInPlaceDesc') : t('importServer.modeMigrateDesc')}
      </p>
      {submitBlocked && (
        <p className="text-xs text-destructive">{t('importServer.precheckBlocked')}</p>
      )}
    </>
  )
}
