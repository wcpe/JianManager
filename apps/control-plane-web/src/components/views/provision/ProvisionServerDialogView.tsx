/**
 * @file ProvisionServerDialogView：一键搭建后端子服向导（节点 / 核心 / 版本 / JDK / 分组 + 版本-JDK 兼容预检）的受控视图，
 *       候选取数与搭建写请求由应用容器负责。
 * @input form-validation（validateRequired/validatePositiveInt/validateFields/hasErrors）、use-field-gate（错误展示时机门控）、
 *        Combobox/Select/Checkbox/Dialog/滚动壳原语、FieldLabel/FieldError、翻译上下文
 * @output ProvisionServerDialogView、ProvisionServerDialogViewProps、ProvisionServerFormDraft、ProvisionServerQueryInput、
 *         ProvisionServerLinkArgs、ProvisionJdkOption、ProvisionResolvedCore、PROVISION_SERVER_QUERY_INIT
 * @sync apps/control-plane-web/src/components/ProvisionServerDialog.tsx、
 *        apps/control-plane-web/src/components/ProvisionServerDialog.dom.test.tsx、
 *        apps/control-plane-web/src/components/ProvisionServerDialog.fr316.dom.test.tsx、
 *        apps/control-plane-web/src/components/ProvisionServerDialog.fr328.dom.test.tsx
 * @since FR-502（组件受控化迁包；原 FR-034 一键搭建、FR-046 核心解析、FR-072 系统可获取项、FR-316 版本-JDK 预检、FR-328 下拉可滚动）
 */
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@jianmanager/ui/components/select'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import { validateRequired, validatePositiveInt, validateFields, hasErrors } from '@/lib/form-validation'
import { useFieldGate } from '@/lib/use-field-gate'

/** 可选 JDK 记录（`NodeJDK` 结构兼容）：视图按大版本做 FR-316 预检、按厂商/版本拼展示名。 */
export interface ProvisionJdkOption {
  id: number
  vendor: string
  majorVersion: number
  version: string
}

/** 解析出的核心下载信息（`CoreInfo` 结构兼容）：视图只用到文件名/构建号与所需最低 Java 大版本。 */
export interface ProvisionResolvedCore {
  filename: string
  build: number
  /** 该 MC 版本所需最低 Java 大版本（FR-316）；缺省=未知/不设需求，不据此拦截。 */
  javaMajorRequired?: number
}

/**
 * 取数输入（查询键）：**上提容器**——JDK 列表随 `nodeId` 变、版本列表随 `coreType` 变、
 * 解析预览随 `coreType`/`mcVersion`/`build` 变，一变就要重新取数。
 * 视图仍持有表单草稿，在这些字段变更或表单重置时上报快照，容器只保存查询键本身。
 */
export interface ProvisionServerQueryInput {
  /** 选中节点 id 原文（'' = 未选）。 */
  nodeId: string
  /** 核心类型（paper / spongevanilla / spongeforge）。 */
  coreType: string
  /** MC 版本原文（'' = 未选）。 */
  mcVersion: string
  /** 构建号原文（空或非数字 = 取最新构建）。 */
  build: string
}

/** 取数输入的初始值（= 表单初值/重置值）；容器初始化查询键时复用，避免两处默认值漂移。 */
// eslint-disable-next-line react-refresh/only-export-components -- 与组件同文件才保证初值单一真源（容器据此初始化查询键）
export const PROVISION_SERVER_QUERY_INIT: ProvisionServerQueryInput = {
  nodeId: '',
  coreType: 'paper',
  mcVersion: '',
  build: '',
}

/**
 * 表单草稿（**原文**，非请求体）：数值/数组字段保持用户输入的字符串，
 * 由容器组装请求体（Number 化、空值省略、jvmArgs 拆分）——「草稿 → 请求体」属请求语义，留在应用侧。
 */
export interface ProvisionServerFormDraft {
  /** 实例名（必填）。 */
  name: string
  /** 目标节点 id 原文（必填）。 */
  nodeId: string
  /** 核心类型。 */
  coreType: string
  /** MC 版本（必填）。 */
  mcVersion: string
  /** 构建号原文（空/非数字 = 最新构建）。 */
  build: string
  /** 绑定的 JDK id 原文（'' = 不指定）。 */
  jdkId: string
  /** 内存 MB 原文（必填正整数）。 */
  memoryMb: string
  /** JVM 参数原文（空白分隔）。 */
  jvmArgs: string
  /** 所属用户组 id 原文（'' = 不分组）。 */
  groupId: string
  /** 是否向 Mojang 校验正版（缺省 false = 代理就绪/离线）。 */
  onlineMode: boolean
}

/** 外链插槽入参：`to` 为路由路径，样式由视图给定，节点由容器注入（react-router 不进包）。 */
export interface ProvisionServerLinkArgs {
  /** 目标路由路径。 */
  to: string
  /** 视图给定的类名（保证样式不漂移）。 */
  className: string
  /** 链接文案。 */
  children: ReactNode
}

/**
 * 受控边界（ADR-097 b 范式）：**不取数、不发请求、不弹 toast**。
 * - 四份候选经 props 注入：`nodeOptions` / `groupOptions`（容器映射为选项）、`versions`（容器调
 *   `useCoreVersions`）、`jdks`（容器调 `useNodeJDKs(nodeId)`）；解析预览经 `resolved` 注入；
 * - `jdks` 以 **undefined 表示「还没取到」**：这是原实现的语义（`jdks !== undefined` 才做无 JDK 阻断），
 *   「已加载但为空」才阻断并引导安装；
 * - 取数输入经 `onQueryChange` 上报（节点 / 核心类型 / MC 版本 / 构建号）；
 * - 提交经 `onSubmit` 上报：返回是否成功——成功才关窗（失败保留草稿与窗口便于重试，提示与成功文案由容器负责）；
 * - **留本视图**的状态：全部表单草稿、字段错误门控（`useFieldGate`）、每节点只默认一次的 JDK 自动绑定标记；
 * - 开合：`open` 由容器持有；取消 / 遮罩 / Esc / 提交成功均走 `onClose`（视图已先复位草稿与门控）。
 */
export interface ProvisionServerDialogViewProps {
  /** 是否展示对话框。 */
  open: boolean
  /** 可选节点（容器已过滤为启用态）。 */
  nodeOptions: ComboboxOption[]
  /** 该核心类型的可用 MC 版本（新→旧）；加载/失败态由下方两个标志区分。 */
  versions: string[]
  /** 版本列表加载中：禁用版本选择并显示「加载版本中…」。 */
  versionsLoading?: boolean
  /** 版本列表取数失败：禁用版本选择并显示错误占位。 */
  versionsError?: boolean
  /** 选中节点上的 JDK 记录；**undefined = 尚未取到**（不参与阻断判定）。 */
  jdks?: ProvisionJdkOption[]
  /** 核心解析结果（预览行与 Java 需求预检的数据源）；缺省 = 未解析或在解析中。 */
  resolved?: ProvisionResolvedCore | null
  /** 解析在途（预览行显示「加载中…」）。 */
  resolving?: boolean
  /** 可选用户组。 */
  groupOptions: ComboboxOption[]
  /** 搭建在途：禁用提交并显示「搭建中…」。 */
  submitting?: boolean
  /** 取数输入变化上报（节点 / 核心类型 / MC 版本 / 构建号原文）。 */
  onQueryChange: (query: ProvisionServerQueryInput) => void
  /** 关闭对话框（取消 / 遮罩 / Esc / 提交成功后由本视图调用）。 */
  onClose: () => void
  /** 提交搭建；返回是否成功——成功才关窗（失败保留草稿与窗口，提示由容器负责）。 */
  onSubmit: (draft: ProvisionServerFormDraft) => Promise<boolean>
  /** 外链插槽；缺省退化为原生 `<a href>`（样式一致）。 */
  renderLink?: (args: ProvisionServerLinkArgs) => ReactNode
}

/**
 * 一键搭建后端子服向导（FR-034）：用户只需选核心/版本/资源，端口与工作目录由系统分配，
 * 核心由后端解析并写入基础配置（FR-046）。含 FR-316 版本-JDK 兼容预检。
 */
export default function ProvisionServerDialogView({
  open,
  nodeOptions,
  versions,
  versionsLoading = false,
  versionsError = false,
  jdks,
  resolved,
  resolving = false,
  groupOptions,
  submitting = false,
  onQueryChange,
  onClose,
  onSubmit,
  renderLink,
}: ProvisionServerDialogViewProps) {
  const { t } = useTranslation()

  const [name, setName] = useState('')
  const [nodeId, setNodeId] = useState('')
  const [coreType, setCoreType] = useState('paper')
  const [mcVersion, setMcVersion] = useState('')
  const [build, setBuild] = useState('')
  const [jdkId, setJdkId] = useState('')
  const [memoryMb, setMemoryMb] = useState('2048')
  const [jvmArgs, setJvmArgs] = useState('')
  const [groupId, setGroupId] = useState('')
  const [onlineMode, setOnlineMode] = useState(false) // 默认代理就绪（离线）
  const gate = useFieldGate()

  const versionOptions: ComboboxOption[] = versions.map((v) => ({ value: v }))
  const jdkOptions: ComboboxOption[] = (jdks ?? []).map((j) => ({
    value: String(j.id),
    label: `${j.vendor} ${j.majorVersion} (${j.version})`,
  }))

  const errors = validateFields(
    { name, nodeId, mcVersion, memoryMb },
    {
      name: [validateRequired],
      nodeId: [validateRequired],
      mcVersion: [validateRequired],
      memoryMb: [validatePositiveInt],
    },
  )

  // FR-316 版本-JDK 兼容预检：解析响应携带该 MC 版本所需最低 Java 大版本（CP 单一真值），
  // 对所选/默认 JDK 表单级阻断，与 FR-314 启动预检互补（搭建时拦 vs 启动时拦）。
  // 阻断：节点无任何 JDK；或所选 JDK 大版本低于需求（真机事故：MC 26.1 + 最高 Temurin 21 必崩）。
  // 仅警示不阻断：节点有 JDK 但选了「不指定」——系统 Java 版本未知，宁漏勿误伤，交 FR-314 兜底。
  const requiredJava = resolved?.javaMajorRequired ?? 0
  const selectedJdk = (jdks ?? []).find((j) => String(j.id) === jdkId)
  let jdkBlockText: string | null = null
  let jdkWarnText: string | null = null
  if (nodeId && mcVersion && requiredJava > 0 && jdks !== undefined) {
    if (jdks.length === 0) {
      jdkBlockText = t('provision.javaReqNoJdk', { version: mcVersion, java: requiredJava })
    } else if (!selectedJdk) {
      jdkWarnText = t('provision.javaReqUnverified', { version: mcVersion, java: requiredJava })
    } else if (selectedJdk.majorVersion < requiredJava) {
      jdkBlockText = t('provision.javaReqTooLow', {
        version: mcVersion,
        java: requiredJava,
        current: selectedJdk.majorVersion,
        gap: requiredJava - selectedJdk.majorVersion,
      })
    }
  }

  // 选节点后默认绑定该节点最高版本的已装 JDK：现代 Paper 需 Java 17/21，
  // 默认「不指定」会用系统 Java（常为 8）导致一键搭建出的服跑不起来。每节点只默认一次，用户仍可改。
  const jdkDefaultNodeRef = useRef('')
  useEffect(() => {
    if (nodeId && jdks && jdks.length > 0 && jdkDefaultNodeRef.current !== nodeId) {
      jdkDefaultNodeRef.current = nodeId
      const best = [...jdks].sort((a, b) => b.majorVersion - a.majorVersion)[0]
      // 节点 JDK 列表到达后一次性默认绑定（经 ref 守卫，不会重复触发），非渲染期联动。
      setJdkId(String(best.id))
    }
  }, [nodeId, jdks])

  /** 上报取数输入快照（单字段变更时用 patch 覆盖，其余字段取当前值）。 */
  const emitQuery = (patch: Partial<ProvisionServerQueryInput>) => {
    onQueryChange({ nodeId, coreType, mcVersion, build, ...patch })
  }

  /** 复位表单与门控（关闭时调用）；取数查询键同时回到初始态（容器随之停掉版本/解析取数）。 */
  const resetForm = () => {
    setName('')
    setNodeId('')
    setCoreType('paper')
    setMcVersion('')
    setBuild('')
    setJdkId('')
    setMemoryMb('2048')
    setJvmArgs('')
    setGroupId('')
    setOnlineMode(false)
    jdkDefaultNodeRef.current = ''
    gate.reset()
    onQueryChange(PROVISION_SERVER_QUERY_INIT)
  }

  const changeCoreType = (next: string) => {
    setCoreType(next)
    setMcVersion('')
    setBuild('')
    emitQuery({ coreType: next, mcVersion: '', build: '' })
  }

  const close = () => {
    resetForm()
    onClose()
  }

  /** 当前草稿（原文）；请求体组装在容器完成。 */
  const draft = (): ProvisionServerFormDraft => ({
    name,
    nodeId,
    coreType,
    mcVersion,
    build,
    jdkId,
    memoryMb,
    jvmArgs,
    groupId,
    onlineMode,
  })

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    gate.submit()
    if (hasErrors(errors) || jdkBlockText) return
    // 失败（false，容器已弹 toast）时不关窗、不清草稿，便于修正后重试。
    if (await onSubmit(draft())) close()
  }

  /** 外链：优先走容器注入的路由链接，缺省退化为原生锚点（样式一致）。 */
  const link = (to: string, className: string, children: ReactNode) =>
    renderLink ? renderLink({ to, className, children }) : <a href={to} className={className}>{children}</a>

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) close() }}>
      <DialogContent
        showCloseButton={false}
        onPointerDownOutside={(event) => event.preventDefault()}
        className={`${scrollableDialogContentClass} sm:max-w-md`}
      >
        <DialogHeader>
          <DialogTitle>{t('provision.title')}</DialogTitle>
          <DialogDescription className="text-xs">{t('provision.systemAssigned')}</DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <ScrollableDialogBody className="space-y-3 py-2">
          <div>
            <FieldLabel required>{t('instances.instanceName')}</FieldLabel>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              onBlur={() => gate.touch('name')}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive"
              placeholder="lobby"
              aria-invalid={!!gate.show('name', errors.name)}
            />
            <FieldError error={gate.show('name', errors.name)} />
          </div>

          <div>
            <FieldLabel required>{t('instances.node')}</FieldLabel>
            <div className="mt-1">
              <Combobox
                options={nodeOptions}
                value={nodeId}
                onChange={(v) => { gate.touch('nodeId'); setNodeId(v); emitQuery({ nodeId: v }) }}
                allowCustom={false}
                placeholder={t('instances.selectNode')}
                invalid={!!gate.show('nodeId', errors.nodeId)}
              />
            </div>
            <FieldError error={gate.show('nodeId', errors.nodeId)} />
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div>
              <FieldLabel>{t('provision.coreType')}</FieldLabel>
              <Select value={coreType} onValueChange={changeCoreType}>
                <SelectTrigger className="w-full mt-1">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="paper">Paper</SelectItem>
                  <SelectItem value="spongevanilla">{t('provision.coreTypeSpongeVanilla')}</SelectItem>
                  <SelectItem value="spongeforge">{t('provision.coreTypeSpongeForge')}</SelectItem>
                </SelectContent>
              </Select>
              {coreType === 'spongeforge' && (
                <p className="mt-1 text-xs text-muted-foreground">{t('provision.spongeForgeHint')}</p>
              )}
            </div>
            <div>
              <FieldLabel required>{t('provision.mcVersion')}</FieldLabel>
              <div className="mt-1">
                <Combobox
                  options={versionOptions}
                  value={mcVersion}
                  onChange={(v) => { gate.touch('mcVersion'); setMcVersion(v); emitQuery({ mcVersion: v }) }}
                  disabled={versionsLoading || versionsError}
                  invalid={!!gate.show('mcVersion', errors.mcVersion)}
                  placeholder={
                    versionsLoading
                      ? t('provision.loadingVersions')
                      : versionsError
                        ? t('provision.versionsError')
                        : t('provision.selectVersion')
                  }
                />
              </div>
              <FieldError error={gate.show('mcVersion', errors.mcVersion)} />
            </div>
          </div>

          <div>
            <FieldLabel>{t('provision.build')}</FieldLabel>
            <input
              value={build}
              onChange={(e) => { setBuild(e.target.value); emitQuery({ build: e.target.value }) }}
              inputMode="numeric"
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
              placeholder={t('provision.latestBuild')}
            />
            {mcVersion && (
              <p className="mt-1 text-xs text-muted-foreground">
                {resolving
                  ? t('common.loading')
                  : resolved
                    ? `${t('provision.willDownload')}: ${resolved.filename} (build #${resolved.build})`
                    : t('provision.versionsError')}
              </p>
            )}
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div>
              <FieldLabel>{t('provision.memory')}</FieldLabel>
              <input
                value={memoryMb}
                onChange={(e) => setMemoryMb(e.target.value)}
                onBlur={() => gate.touch('memoryMb')}
                inputMode="numeric"
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive"
                placeholder="2048"
                aria-invalid={!!gate.show('memoryMb', errors.memoryMb)}
              />
              <FieldError error={gate.show('memoryMb', errors.memoryMb)} />
            </div>
            <div>
              <FieldLabel>JDK</FieldLabel>
              <div className="mt-1">
                <Combobox
                  options={jdkOptions}
                  value={jdkId}
                  onChange={setJdkId}
                  allowCustom={false}
                  placeholder={t('provision.noJdk')}
                  invalid={!!jdkBlockText}
                />
              </div>
            </div>
          </div>

          {jdkBlockText && (
            <div className="rounded-md border border-destructive/40 bg-destructive/10 px-2.5 py-2 text-xs text-destructive">
              <span>{jdkBlockText}</span>{' '}
              {link(
                '/runtime-assets',
                'font-medium underline underline-offset-2',
                t('provision.goInstallJdk'),
              )}
            </div>
          )}
          {!jdkBlockText && jdkWarnText && (
            <p className="text-xs text-amber-600 dark:text-amber-400">{jdkWarnText}</p>
          )}

          <div>
            <FieldLabel>{t('provision.jvmArgs')}</FieldLabel>
            <input
              value={jvmArgs}
              onChange={(e) => setJvmArgs(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm font-mono"
              placeholder="-XX:+UseG1GC"
            />
            <p className="mt-1 text-xs text-muted-foreground">{t('provision.jvmArgsHint')}</p>
          </div>

          <div>
            <FieldLabel>{t('instances.group')}</FieldLabel>
            <div className="mt-1">
              <Combobox
                options={groupOptions}
                value={groupId}
                onChange={setGroupId}
                allowCustom={false}
                placeholder={t('instances.noGroup')}
              />
            </div>
          </div>

          <div>
            <label className="flex items-center gap-2 text-sm">
              <Checkbox
                checked={onlineMode}
                onCheckedChange={(v) => setOnlineMode(v === true)}
                aria-label={t('provision.onlineMode')}
              />
              {t('provision.onlineMode')}
            </label>
            <p className="mt-1 text-xs text-muted-foreground">{t('provision.onlineModeHint')}</p>
          </div>
          </ScrollableDialogBody>

          <DialogFooter className="flex-row justify-end pt-2">
            <button
              type="button"
              onClick={close}
              className="px-4 py-2 text-sm border rounded-md hover:bg-accent"
            >
              {t('common.cancel')}
            </button>
            <button
              type="submit"
              disabled={submitting || hasErrors(errors) || !!jdkBlockText}
              className="px-4 py-2 text-sm bg-primary text-primary-foreground rounded-md disabled:pointer-events-none disabled:opacity-50 hover:bg-primary/90"
            >
              {submitting ? t('provision.provisioning') : t('provision.submit')}
            </button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
