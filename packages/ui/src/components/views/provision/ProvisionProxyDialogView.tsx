/**
 * @file ProvisionProxyDialogView：搭建代理向导（代理类型 / 版本 / 资源 + forwarding secret 留存步骤）的受控视图，
 *       候选取数与搭建写请求由应用容器负责。
 * @input form-validation（validateRequired/validatePositiveInt/validateFields/hasErrors）、use-field-gate（错误展示时机门控）、
 *        Combobox/Button/Checkbox/Dialog/滚动壳原语、FieldLabel/FieldError、lib/clipboard、
 *        同目录 ProvisionServerDialogView 的共享契约类型（ProvisionJdkOption/ProvisionResolvedCore）、翻译上下文
 * @output ProvisionProxyDialogView、ProvisionProxyDialogViewProps、ProvisionProxyFormDraft、ProvisionProxyQueryInput、
 *         ProvisionProxySubmitResult、PROVISION_PROXY_QUERY_INIT、needsProxyVersion
 * @sync apps/control-plane-web/src/components/ProvisionProxyDialog.tsx、
 *        apps/control-plane-web/src/components/ProvisionProxyDialog.dom.test.tsx
 * @since FR-502（组件受控化迁包；原 FR-035 搭建代理、FR-072 系统可获取项、FR-202 供给/部署流程）
 */
import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Copy } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
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
import { copyToClipboard } from '@jianmanager/ui/lib/clipboard'
import { validateRequired, validatePositiveInt, validateFields, hasErrors } from '@jianmanager/ui/lib/form-validation'
import { useFieldGate } from '@jianmanager/ui/lib/use-field-gate'
import type { ProvisionJdkOption, ProvisionResolvedCore } from './ProvisionServerDialogView'

// eslint-disable-next-line react-refresh/only-export-components -- 判据与查询初值须与渲染同源导出：容器据此折算请求体与初始化查询键，两处各写一份会静默错位
export function needsProxyVersion(proxyType: string): boolean {
  return proxyType !== 'bungeecord'
}

/** 取数输入的初始值（= 表单初值/重置值）；容器初始化查询键时复用，避免两处默认值漂移。 */
// eslint-disable-next-line react-refresh/only-export-components -- 同上：与组件同文件才保证初值单一真源
export const PROVISION_PROXY_QUERY_INIT: ProvisionProxyQueryInput = {
  nodeId: '',
  proxyType: 'velocity',
  version: '',
}

/**
 * 取数输入（查询键）：**上提容器**——代理版本列表随 `proxyType` 变、解析预览随 `proxyType`/`version` 变、
 * JDK 列表随 `nodeId` 变，一变就要重新取数。视图仍持有表单草稿并在这些字段变更或表单重置时上报快照。
 */
export interface ProvisionProxyQueryInput {
  /** 选中节点 id 原文（'' = 未选）。 */
  nodeId: string
  /** 代理类型（velocity / waterfall / bungeecord）。 */
  proxyType: string
  /** 代理版本原文（bungeecord 下恒为无效；容器按类型折算为 `latest`）。 */
  version: string
}

/**
 * 表单草稿（**原文**，非请求体）：数值/数组字段保持用户输入的字符串，
 * 由容器组装请求体（Number 化、bungeecord 不下发 version、jvmArgs 拆分）——「草稿 → 请求体」属请求语义，留在应用侧。
 */
export interface ProvisionProxyFormDraft {
  /** 实例名（必填）。 */
  name: string
  /** 目标节点 id 原文（必填）。 */
  nodeId: string
  /** 代理类型。 */
  proxyType: string
  /** 代理版本原文（bungeecord 不需要；容器按 `needsProxyVersion` 决定是否下发）。 */
  version: string
  /** 绑定的 JDK id 原文（'' = 不指定）。 */
  jdkId: string
  /** 内存 MB 原文（必填正整数）。 */
  memoryMb: string
  /** JVM 参数原文（空白分隔）。 */
  jvmArgs: string
  /** 所属用户组 id 原文（'' = 不分组）。 */
  groupId: string
  /** 是否向 Mojang 校验正版（缺省 true = 正版网络）。 */
  onlineMode: boolean
}

/** 提交结果：命中 Velocity 时带回 forwarding secret，决定「切 secret 步骤」还是「关窗」。 */
export interface ProvisionProxySubmitResult {
  /** 本次搭建返回的 forwarding secret；有值时视图切到 secret 步骤留待用户复制留存。 */
  forwardingSecret?: string
}

/**
 * 受控边界（ADR-097 b 范式）：**不取数、不发请求、不弹 toast**。
 * - 四份候选经 props 注入：`nodeOptions` / `groupOptions`（容器映射为选项）、`versions`（容器调
 *   `useCoreVersions`，按 `needsProxyVersion` 折算）、`jdks`（容器调 `useNodeJDKs(nodeId)`；undefined = 尚未取到）；
 *   解析预览经 `resolved` 注入；
 * - 取数输入经 `onQueryChange` 上报（节点 / 代理类型 / 版本）；
 * - 提交经 `onSubmit` 上报：**返回结果对象 = 成功**（带 `forwardingSecret` 时切 secret 步骤、否则关窗），
 *   **返回 null = 失败**（容器已提示，保留草稿与窗口便于重试）；
 * - 复制 secret 走包内 `lib/clipboard`，结果经 `onCopyResult` 上报（提示文案由容器决定）；
 * - **留本视图**的状态：全部表单草稿、字段错误门控（`useFieldGate`）、步骤推进（表单 → secret）、
 *   每节点只默认一次的 JDK 自动绑定标记；
 * - 开合：`open` 由容器持有；取消 / 遮罩 / Esc / 「完成」/ 无 secret 的成功提交均走 `onClose`（视图已先复位草稿与步骤）。
 */
export interface ProvisionProxyDialogViewProps {
  /** 是否展示对话框。 */
  open: boolean
  /** 可选节点（容器已过滤为启用态）。 */
  nodeOptions: ComboboxOption[]
  /** 该代理类型的可用版本（bungeecord 下不被消费）；加载态由 `versionsLoading` 区分。 */
  versions: string[]
  /** 版本列表加载中：禁用版本选择并显示「加载版本中…」。 */
  versionsLoading?: boolean
  /** 选中节点上的 JDK 记录；**undefined = 尚未取到**。 */
  jdks?: ProvisionJdkOption[]
  /** 核心解析结果（预览行数据源）；缺省 = 未解析或在解析中。 */
  resolved?: ProvisionResolvedCore | null
  /** 可选用户组。 */
  groupOptions: ComboboxOption[]
  /** 搭建在途：禁用提交并显示「搭建中…」。 */
  submitting?: boolean
  /** 取数输入变化上报（节点 / 代理类型 / 版本原文）。 */
  onQueryChange: (query: ProvisionProxyQueryInput) => void
  /** 关闭对话框（取消 / 遮罩 / Esc / 「完成」/ 无 secret 的成功提交）。 */
  onClose: () => void
  /** 提交搭建；返回结果对象=成功（含可选 forwarding secret），返回 null=失败（容器已提示，保留草稿与窗口）。 */
  onSubmit: (draft: ProvisionProxyFormDraft) => Promise<ProvisionProxySubmitResult | null>
  /** 复制 forwarding secret 的结果上报（由容器决定成功/失败提示文案）。 */
  onCopyResult?: (ok: boolean) => void
}

/**
 * 搭建代理向导（FR-035）：选代理类型/版本/资源，系统分配监听端口与工作目录，
 * 后端下载核心、生成转发配置；Velocity 生成 forwarding secret 并在成功后就地展示一次供留存。
 * 注册后端在创建后经「管理后端」完成。
 */
export default function ProvisionProxyDialogView({
  open,
  nodeOptions,
  versions,
  versionsLoading = false,
  jdks,
  resolved,
  groupOptions,
  submitting = false,
  onQueryChange,
  onClose,
  onSubmit,
  onCopyResult,
}: ProvisionProxyDialogViewProps) {
  const { t } = useTranslation()

  const [name, setName] = useState('')
  const [nodeId, setNodeId] = useState('')
  const [proxyType, setProxyType] = useState('velocity')
  const [version, setVersion] = useState('')
  const [jdkId, setJdkId] = useState('')
  const [memoryMb, setMemoryMb] = useState('1024')
  const [jvmArgs, setJvmArgs] = useState('')
  const [groupId, setGroupId] = useState('')
  const [onlineMode, setOnlineMode] = useState(true) // 默认正版网络
  const [forwardingSecret, setForwardingSecret] = useState('')
  const gate = useFieldGate()

  // bungeecord 无版本选择（仅 latest）；velocity/waterfall 走 PaperMC 版本列表。
  const needsVersion = needsProxyVersion(proxyType)

  // 代理类型是静态枚举（不取数），留在视图。
  const proxyTypeOptions: ComboboxOption[] = [
    { value: 'velocity', label: 'Velocity (modern)' },
    { value: 'waterfall', label: 'Waterfall' },
    { value: 'bungeecord', label: 'BungeeCord' },
  ]
  const versionOptions: ComboboxOption[] = versions.map((v) => ({ value: v }))
  const jdkOptions: ComboboxOption[] = (jdks ?? []).map((j) => ({
    value: String(j.id),
    label: `${j.vendor} ${j.majorVersion} (${j.version})`,
  }))

  const errors = validateFields(
    { name, nodeId, version, memoryMb },
    {
      name: [validateRequired],
      nodeId: [validateRequired],
      // 仅当该代理类型需要版本时才把版本设为必填
      version: needsVersion ? [validateRequired] : [],
      memoryMb: [validatePositiveInt],
    },
  )

  // 选节点后默认绑定该节点最高版本的已装 JDK（与后端子服向导同一策略，避免默认落到系统 Java）。每节点只默认一次。
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
  const emitQuery = (patch: Partial<ProvisionProxyQueryInput>) => {
    onQueryChange({ nodeId, proxyType, version, ...patch })
  }

  /** 复位表单、门控与步骤（关闭时调用）；查询键同时回到初始态。 */
  const reset = () => {
    setName(''); setNodeId(''); setProxyType('velocity'); setVersion('')
    setJdkId(''); setMemoryMb('1024'); setJvmArgs(''); setGroupId(''); setOnlineMode(true); setForwardingSecret('')
    jdkDefaultNodeRef.current = ''
    gate.reset()
    onQueryChange(PROVISION_PROXY_QUERY_INIT)
  }
  const close = () => { reset(); onClose() }
  /** 复制 secret：写剪贴板在视图（包内工具），成功/失败提示由容器出。 */
  const copySecret = async () => {
    const ok = await copyToClipboard(forwardingSecret)
    onCopyResult?.(ok)
  }

  /** 当前草稿（原文）；请求体组装在容器完成。 */
  const draft = (): ProvisionProxyFormDraft => ({
    name,
    nodeId,
    proxyType,
    version,
    jdkId,
    memoryMb,
    jvmArgs,
    groupId,
    onlineMode,
  })

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    gate.submit()
    if (hasErrors(errors)) return
    const res = await onSubmit(draft())
    // 失败（null，容器已弹 toast）时不关窗、不清草稿；成功且无 secret 才关窗，有 secret 则切到留存步骤。
    if (!res) return
    if (res.forwardingSecret) setForwardingSecret(res.forwardingSecret)
    else close()
  }

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) close() }}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-md`}>
        {forwardingSecret ? (
          <>
            <DialogHeader>
              <DialogTitle>{t('proxy.secretTitle')}</DialogTitle>
              <DialogDescription>{t('proxy.secretDesc')}</DialogDescription>
            </DialogHeader>
            <ScrollableDialogBody>
              <div className="rounded-md border bg-muted/40 p-3 font-mono text-sm break-all">
                {forwardingSecret}
              </div>
            </ScrollableDialogBody>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={copySecret}>
                <Copy className="size-4" /> {t('proxy.copySecret')}
              </Button>
              <Button type="button" onClick={close}>{t('common.done')}</Button>
            </DialogFooter>
          </>
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>{t('proxy.title')}</DialogTitle>
              <DialogDescription>{t('provision.systemAssigned')}</DialogDescription>
            </DialogHeader>

            <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
              <ScrollableDialogBody className="space-y-3 py-2">
                <div>
                  <FieldLabel required>{t('instances.instanceName')}</FieldLabel>
                  <input value={name} onChange={(e) => setName(e.target.value)}
                    onBlur={() => gate.touch('name')}
                    aria-invalid={!!gate.show('name', errors.name)}
                    className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive" placeholder="velocity-main" />
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
                    <FieldLabel>{t('proxy.type')}</FieldLabel>
                    <div className="mt-1">
                      <Combobox
                        options={proxyTypeOptions}
                        value={proxyType}
                        onChange={(v) => {
                          setProxyType(v)
                          setVersion('')
                          emitQuery({ proxyType: v, version: '' })
                        }}
                      />
                    </div>
                  </div>
                  <div>
                    <FieldLabel required={needsVersion}>{t('proxy.version')}</FieldLabel>
                    <div className="mt-1">
                      <Combobox
                        options={versionOptions}
                        value={needsVersion ? version : ''}
                        onChange={(v) => { gate.touch('version'); setVersion(v); emitQuery({ version: v }) }}
                        disabled={!needsVersion || versionsLoading}
                        invalid={!!gate.show('version', errors.version)}
                        placeholder={needsVersion ? (versionsLoading ? t('provision.loadingVersions') : t('provision.selectVersion')) : t('proxy.latestOnly')}
                      />
                    </div>
                    <FieldError error={gate.show('version', errors.version)} />
                  </div>
                </div>
                {resolved && (
                  <p className="text-xs text-muted-foreground">{t('provision.willDownload')}: {resolved.filename}</p>
                )}

                <div className="grid grid-cols-2 gap-3">
                  <div>
                    <FieldLabel>{t('provision.memory')}</FieldLabel>
                    <input value={memoryMb} onChange={(e) => setMemoryMb(e.target.value)} inputMode="numeric"
                      onBlur={() => gate.touch('memoryMb')}
                      aria-invalid={!!gate.show('memoryMb', errors.memoryMb)}
                      className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive" placeholder="1024" />
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
                      />
                    </div>
                  </div>
                </div>

                <div>
                  <FieldLabel>{t('provision.jvmArgs')}</FieldLabel>
                  <input value={jvmArgs} onChange={(e) => setJvmArgs(e.target.value)}
                    className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm font-mono" placeholder="-XX:+UseG1GC" />
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
                    <Checkbox checked={onlineMode} onCheckedChange={(v) => setOnlineMode(v === true)} aria-label={t('proxy.onlineMode')} />
                    {t('proxy.onlineMode')}
                  </label>
                  <p className="mt-1 text-xs text-muted-foreground">{t('proxy.onlineModeHint')}</p>
                </div>
              </ScrollableDialogBody>

              <DialogFooter className="flex-row justify-end pt-2">
                <Button type="button" variant="outline" onClick={close}>
                  {t('common.cancel')}
                </Button>
                <Button type="submit" disabled={submitting || hasErrors(errors)}>
                  {submitting ? t('proxy.provisioning') : t('proxy.submit')}
                </Button>
              </DialogFooter>
            </form>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}
