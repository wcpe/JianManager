import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQueryClient } from '@tanstack/react-query'
import {
  AlertTriangle,
  ArrowRightCircle,
  Ban,
  CirclePlay,
  PauseCircle,
  Plug,
  RotateCcw,
  Save,
  ShieldAlert,
  SlidersHorizontal,
  Trash2,
} from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@jianmanager/ui/components/alert'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { NativeSelect, NativeSelectOption } from '@jianmanager/ui/components/native-select'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { PaginationBar } from '@jianmanager/ui/components/pagination'
import { Panel } from '@jianmanager/ui/components/panel'
import { Progress } from '@jianmanager/ui/components/progress'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@jianmanager/ui/components/tabs'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { EmptyState } from '@jianmanager/ui/components/empty-state'
import { Badge } from '@jianmanager/ui/components/badge'
import { ManagedConfigEditor } from '@/components/console/ManagedConfigEditor'
import { ManagedYamlEditor } from '@/components/console/ManagedYamlEditor'
import type { FieldValidation } from '@/components/console/ManagedConfigField'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import { InstancePicker } from '@/components/instances/InstancePicker'
import { useNodes } from '@/api/nodes'
import { useInstanceSearch } from '@/api/instances'
import { useDebounced } from '@/lib/hooks/use-debounced'
import { apiErrorMessage } from '@/lib/shared/api-error'
import { formatRelativeTime } from '@/lib/shared/relative-time'
import { stringifyManagedYaml, validateManagedYaml } from '@/lib/managed-config/managed-yaml'
import {
  MANAGED_PUSH_STATUSES,
  MANAGED_TARGET_STATES,
  flattenManagedConfig,
  isForbiddenTier,
  isPushTerminal,
  parseEffectiveJson,
  prettyJson,
  pushStatusLevel,
  releaseGuard,
  targetStateLevel,
  useCancelManagedPush,
  useCreateManagedPush,
  useDeleteManagedLayer,
  useManagedImpact,
  useManagedKeyCatalog,
  useManagedLayer,
  useManagedLayers,
  useManagedPushTargets,
  useManagedPushes,
  useManagedRollback,
  usePauseManagedPush,
  useReleaseManagedPush,
  useResumeManagedPush,
  useSaveManagedLayer,
  useStartManagedPush,
  useValidateManagedConfig,
  validateErrors,
  type ManagedConfigLayer,
  type ManagedLayerResult,
  type ManagedKeyMeta,
  type ManagedImpact,
  type ManagedPushSession,
  type ManagedPushTarget,
  type ManagedScope,
  type ManagedTargetState,
} from '@/api/managedConfig'

/**
 * 探针托管配置与下发页。
 *
 * ## 一页覆盖完整运营闭环
 * 配置层编辑 → 影响预演 → 保存 → 分批下发 / 放行 / 回滚。四段串在同一条动作链上：
 * 运维改完一个键，下一步必然是「这套改动会波及谁」再下发，分页反而要来回跳。
 *
 * ## 界面必须诚实的六处（每一处都直接决定运维会不会误判）
 * 1. **禁档键是整体拒绝**，不是「跳过那个键」。后端 message 带具体键名，界面原样展示，
 *    让运维能照着键名去改；二次包装会让「哪个键不能改」这条信息消失。
 * 2. **409 是「别人先改过」**，与「你写错了」完全不同：给「重新加载」而不是「重试」——
 *    重试只会把别人的修改覆盖掉。
 * 3. **`awaiting_release` 与 `running` 分开显示**：前者是「本批已推完，等人点继续」，
 *    用运行中的进度条描述它，运维会一直等一个不会动的进度。
 * 4. **放行按钮的可点性**由后端两条显式校验反推（状态必须是 awaiting_release、且不能是
 *    最后一批）；点不动时必须把原因写在页面上，只灰掉按钮等于没说。
 * 5. **回滚是重推冻结快照**，不是「按现在的层重新合并一遍」。文案与二次确认都要说清，
 *    否则运维会以为回滚只是把层回退，实际推出去的是一份历史快照。
 * 6. **`timeout` 不是 `failed`**：前者是「没回执（不知道）」，后者是「探针说不行了」。
 *
 * ## 列表端点的缺口
 * 后端 `ManagedPushService.List` 已实现，但路由器未注册 `GET /managed-pushes`，
 * 因此本页在列表请求失败时降级为「本次访问内创建/推进过的会话」并在页面上明确提示。
 * 闭环本身不受影响：创建返回完整会话，每个动作也返回完整会话。
 */

/** 服务端分页每页条数。 */
const PAGE_SIZE = 20

/** 作用域选项顺序 = 合并优先级（低 → 高），与后端 `ManagedScopePriority` 一致。 */
const SCOPE_OPTIONS: ManagedScope[] = ['global', 'node', 'instance']

/**
 * 把后端存的增量文本（历史数据是 JSON，新数据也可能是 YAML）解析成对象。
 *
 * 不能直接 `JSON.parse`：早期层数据是 JSON，而编辑器现在产出 YAML 文本，
 * 同一列里两种形态会共存。先按 YAML 解，YAML 是 JSON 的超集，两种都能吃。
 */
function parseLayerConfig(raw: string | undefined): Record<string, unknown> {
  if (!raw || !raw.trim()) return {}
  const result = validateManagedYaml(raw)
  return result.value ?? {}
}

/**
 * 可视化表单改了若干项之后，把改动合并回 YAML 文本。
 *
 * ## 为什么不能直接 `stringifyManagedYaml(next)` 了事
 * 那样做会把用户在 YAML 里手写的注释、键顺序、空行全部抹掉——用户切到可视化
 * 改一个数字，回来发现自己写的注释没了，这是不可接受的破坏。
 *
 * 这里的做法：只重写**值确实变了**的那些行。逐行匹配 `key:` 前缀（含缩进），
 * 命中就替换该行的标量部分；找不到就按目录注释追加到文末。
 * 结构变化（数组增删项、对象增删子键）走整段重建，因为逐行改不现实。
 */
function mergeManagedYaml(
  prevText: string,
  nextDoc: Record<string, unknown>,
  items: ManagedKeyMeta[],
): string {
  const prevDoc = parseLayerConfig(prevText)
  const prevFlat = flattenManagedConfig(prevDoc)
  const nextFlat = flattenManagedConfig(nextDoc)
  const changed = new Set<string>()
  for (const key of new Set([...Object.keys(prevFlat), ...Object.keys(nextFlat)])) {
    if (prevFlat[key] !== nextFlat[key]) changed.add(key)
  }
  if (changed.size === 0) return prevText

  // 结构变化（数组长度变了 / 对象键集合变了）无法逐行替换，整体重建。
  const structural = [...changed].some((key) => {
    const before = prevFlat[key]
    const after = nextFlat[key]
    const shape = (v: unknown) =>
      Array.isArray(v) ? `arr:${v.length}` : v !== null && typeof v === 'object' ? 'obj' : 'scalar'
    return shape(before) !== shape(after)
  })
  if (structural) return stringifyManagedYaml(nextDoc, items)

  const lines = prevText.split('\n')
  for (const key of changed) {
    const leaf = key.split('.').pop() ?? key
    const after = nextFlat[key]
    // 只在没有嵌套子的行上替换：`collect:` 这种父行必须跳过，否则会把整个块压成标量。
    const index = lines.findIndex((line) => {
      const trimmed = line.trimStart()
      if (trimmed.startsWith('#')) return false
      if (!trimmed.startsWith(`${leaf}:`)) return false
      const inline = trimmed.slice(leaf.length + 1).trim()
      return !line.startsWith('-') && (inline === '' ? false : true)
    })
    if (index < 0) {
      // 找不到就按目录追加，不丢这次改动。
      return stringifyManagedYaml(nextDoc, items)
    }
    if (after === undefined) {
      lines.splice(index, 1)
      continue
    }
    const indent = lines[index]!.slice(0, lines[index]!.length - lines[index]!.trimStart().length)
    lines[index] = `${indent}${leaf}: ${formatYamlScalar(after)}`
  }
  return lines.join('\n')
}

/** 标量转 YAML 字面量（与 managed-yaml 的口径一致：字符串一律加引号）。 */
function formatYamlScalar(value: unknown): string {
  if (value === null || value === undefined) return 'null'
  if (typeof value === 'boolean') return value ? 'true' : 'false'
  if (typeof value === 'number') return Number.isFinite(value) ? String(value) : 'null'
  return JSON.stringify(String(value))
}

/** 时刻占位：空值显示破折号，与「刚刚」区分开。 */
function safeRelative(value: string | null | undefined): string {
  return formatRelativeTime(value) || '—'
}

// ---------------------------------------------------------------------------
// 配置层编辑
// ---------------------------------------------------------------------------

interface LayerEditorProps {
  scope: ManagedScope
  scopeRefId: number
  /** 已存在的层；null 表示该层尚未配置（版本按 0 处理）。 */
  layer: ManagedConfigLayer | null
  /**
   * 层数据是否已就绪（query 已成功返回，无论 found 与否）。
   *
   * 未就绪时 `layer` 与「该层确实不存在」都是 null，无法区分：此时放行保存会把
   * expectedVersion 当 0 送出去，撞上别人刚写的版本只会报一个说不清的 409。
   * 故未就绪时禁用保存，等查询落地后再放行。
   */
  layerReady: boolean
  /** 保存成功回调：父级把返回层回写详情缓存并刷新列表，不重取层。 */
  onSaved: (layer: ManagedConfigLayer) => void
  /** 请求删除该层（父级负责二次确认）。 */
  onRequestDelete: () => void
  /** 冲突后重新加载（父级换 key 重挂本组件）。 */
  onRequestReload: () => void
}

function LayerEditor({ scope, scopeRefId, layer, layerReady, onSaved, onRequestDelete, onRequestReload }: LayerEditorProps) {
  const { t } = useTranslation()
  /**
   * 用户手改过的 YAML 文本；null 表示「还没动过，跟随服务端真值」。
   *
   * ## 为什么不用 useState 初值 + effect 重建
   * catalog 与 layer 是两个独立请求，到达顺序不确定。若在 catalog 到达时
   * 用 `layer?.config` 重建一次并写进 state，而此时 layer 还没到（空配置），
   * 草稿就被永久锁成空串——随后 layer 数据到了也不会再更新，界面表现为
   * 「明明有配置，输入框却是空的」。
   *
   * 这里改成「派生值 + 覆盖值」：没编辑时永远由 (layer.config, catalog) 现算，
   * 两者谁后到都能得到正确结果；一旦用户编辑，才切换到他自己的文本。
   */
  const [editedText, setEditedText] = useState<string | null>(null)
  const [remark, setRemark] = useState('')
  const [conflict, setConflict] = useState(false)
  const [saveError, setSaveError] = useState('')
  const [impact, setImpact] = useState<ManagedImpact | null>(null)
  const [yamlValid, setYamlValid] = useState(true)
  /** 已经写进服务端的键快照：用于区分「草稿里改过」与「已落盘」。 */
  const [committed, setCommitted] = useState<Record<string, unknown>>(() =>
    flattenManagedConfig(parseLayerConfig(layer?.config)),
  )
  /** 服务端当前层版本：单键保存后本地推进，不依赖重取层（重取会令编辑器重挂丢草稿）。 */
  const [serverVersion, setServerVersion] = useState(layer?.version ?? 0)
  /** 服务端层身份令牌：随版本基线一起钉住，删层重建后旧草稿写入由后端 409 拦截。 */
  const [serverIdentity, setServerIdentity] = useState(layer?.identity ?? '')

  /**
   * 层数据常在编辑器挂载后才返回（layer 从 null → 实层），「重新加载」或后台重取
   * 也会换成新对象。editorKey 已不含版本号，靠渲染期状态对齐把「真值变了」接进来：
   * prop 引用一变就同步已存快照与版本基线（React 官方认可的 adjust-state-during-
   * render 写法，替代旧「版本变 → 重挂」的同步职责）。不碰 editedText——草稿是
   * 用户的，committed 只是「已落盘」的参照系。
   */
  const [syncedLayer, setSyncedLayer] = useState(layer)
  if (layer !== syncedLayer) {
    setSyncedLayer(layer)
    // 草稿绑定在生成它的那版基线上：有草稿时又收到「非首次」的真值变化
    //（他人写入 / 后台重取），不能推进 committed 与 serverVersion——
    // 推进了，整层保存就会拿着新版本号把草稿整包静默盖掉别人的改动。
    // 保持旧基线，让后端 409 触发「重新加载」。自己的保存不经过这里
    //（保存成功时已手动推进快照与版本）。syncedLayer 为 null 的首次
    // 载入不受限：没有旧基线可保，必须先对齐才能提交。
    if (syncedLayer === null || editedText === null) {
      setCommitted(flattenManagedConfig(parseLayerConfig(layer?.config)))
      setServerVersion(layer?.version ?? 0)
      setServerIdentity(layer?.identity ?? '')
    }
  }

  const validateMut = useValidateManagedConfig()
  const impactMut = useManagedImpact()
  const saveMut = useSaveManagedLayer()
  const { mutate: runValidate, reset: resetValidate } = validateMut

  // 配置键目录：可视化的数据源。取不到就回退纯 YAML 编辑器，不阻塞用户。
  const catalogQuery = useManagedKeyCatalog()
  const catalog = catalogQuery.data

  const draftText = useMemo(
    () => editedText ?? stringifyManagedYaml(parseLayerConfig(layer?.config), catalog?.items ?? []),
    [editedText, layer?.config, catalog?.items],
  )
  const setDraftText = (next: string | ((prev: string) => string)) => {
    setEditedText((prev) => {
      const base = prev ?? stringifyManagedYaml(parseLayerConfig(layer?.config), catalog?.items ?? [])
      return typeof next === 'function' ? next(base) : next
    })
  }

  /**
   * 草稿文本 → 对象（供可视化编辑器用）。
   *
   * 解析失败时返回空对象并记录错误：编辑器要能渲染出来（让用户看到「YAML 坏了」），
   * 而不是整块消失——那样用户只会在空白页面上不知所措。
   */
  const { draftDoc, draftParseError } = useMemo(() => {
    const result = validateManagedYaml(draftText)
    if (result.error) return { draftDoc: {} as Record<string, unknown>, draftParseError: result.error }
    return { draftDoc: result.value ?? {}, draftParseError: '' }
  }, [draftText])

  /**
   * 低层生效值（仅实例 / 节点作用域有意义）。
   *
   * 用途是把「你没设这个键，但低层设了，所以它实际生效」讲出来。
   * 全局层没有更低层，故不传。
   */
  const inheritedFlat = useMemo<Record<string, unknown> | undefined>(() => {
    if (scope === 'global') return undefined
    return undefined // 预留：待后端暴露「低层合并结果」后接入
  }, [scope])

  // 键入防抖：不防抖会在每次按键后打一个 POST，大段配置时非常浪费。
  const debounced = useDebounced(draftText, 500)

  useEffect(() => {
    // 空文本是「清空该层」的合法意图，不必先校验（后端按空配置处理）。
    if (!debounced.trim()) {
      resetValidate()
      return
    }
    runValidate({ scope, scopeRefId, config: debounced })
  }, [debounced, scope, scopeRefId, runValidate, resetValidate])

  const errors = validateErrors(validateMut.data)
  const forbiddenKeys = impact?.forbiddenKeys ?? []
  const blockedByValidate = validateMut.isSuccess && errors.length > 0
  const blockedByForbidden = forbiddenKeys.length > 0
  // 保存门槛：只拦「正在保存」「数据未就绪」「已知会失败」三类情况。
  //
  // 空草稿**不是**非法状态——它就是「清空该层」的合法意图（`{}` 是有效增量），
  // 后端也接受空对象。曾经这里要求 draft 非空，导致删空内容后无法保存，
  // 用户只能删整层再重建，且空内容下保存按钮永久置灰毫无提示。
  //
  // `layerReady`：层数据到达前不能保存。此时父级 `editorKey` 仍含 version=0，
  // 数据一到就会重挂本组件；若用户恰在此刻点击并拿到后端报错，`saveError` 会随重挂
  // 一起消失，表现为「点了保存什么都没有」。与其让报错凭空蒸发，不如先把按钮关掉。
  // 注：canSave 移到 fieldValidations 之后定义——本地边界错误也要拦保存。

  /**
   * 逐字段校验态：把「后端逐键拒绝」与「本地边界检查」合并成本字段的状态。
   *
   * 后端 `Validate` 返回的是**整包拒绝**的逐条原因（禁档 / 未登记档位），
   * 文本里带键名。这里按键名把原因摘出来挂到对应字段上，用户不必去底部列表对照。
   * 本地边界（min/max）由目录元数据直接判定，无需等后端。
   */
  const fieldValidations = useMemo<Record<string, FieldValidation>>(() => {
    const out: Record<string, FieldValidation> = {}
    const flatDraft = flattenManagedConfig(draftDoc)
    // 1) 本地边界检查：填了值且越界就是错，这类错误后端不报（它只管档位）。
    for (const meta of catalog?.items ?? []) {
      if (!isForbiddenTier(meta)) continue
      // 禁止档不参与本地判定：它本来就不可写，状态由只读文案说明。
      delete out[meta.key]
    }
    for (const [key, value] of Object.entries(flatDraft)) {
      const meta = catalog?.items.find((item) => item.key === key)
      if (!meta) continue
      // 服务端已存的越界值只提示不拦截：值是历史落盘的，拦下保存会让用户
      // 改别的东西也保存不了；只有草稿把值改成越界（或新写出越界）才拦。
      const dirty = value !== committed[key]
      if (!dirty) {
        out[key] = { state: 'ok' }
        continue
      }
      if (typeof value === 'number' && Number.isFinite(value)) {
        if (meta.min !== undefined && value < meta.min) {
          out[key] = { state: 'error', message: meta.boundsHint || `不能小于 ${meta.min}` }
          continue
        }
        if (meta.max !== undefined && value > meta.max) {
          out[key] = { state: 'error', message: meta.boundsHint || `不能大于 ${meta.max}` }
          continue
        }
      }
      out[key] = { state: 'ok' }
    }
    // 2) 后端逐键拒绝原因覆盖本地结论（它是权威，且能识别未登记键）。
    for (const message of errors) {
      for (const key of Object.keys(flatDraft)) {
        // 后端文案形如 `键 "collect-period-ticks" 属于…`，直接做子串匹配即可。
        if (!message.includes(`"${key}"`)) continue
        out[key] = { state: 'error', message }
      }
    }
    return out
  }, [catalog, draftDoc, errors, committed])

  // 任何字段处于错误态（草稿改出的本地越界，或后端逐键拒绝）都不允许整层保存：
  // 后端不管数值边界，这里不拦就会把界面已标红的值静默写库。
  // 服务端已存的越界值不算错误——那是历史数据，不该把用户拦在「保存别的改动」之外。
  const hasFieldErrors = Object.values(fieldValidations).some((v) => v.state === 'error')
  const canSave =
    layerReady &&
    !saveMut.isPending &&
    !blockedByValidate &&
    !blockedByForbidden &&
    yamlValid &&
    !draftParseError &&
    !hasFieldErrors

  /**
   * 有未落盘改动的键。
   *
   * 判定口径是「草稿里这一项的取值与已提交快照不同」，包括「草稿里删掉了」。
   * 这样 `重置` 也能正确点亮它的「写入」按钮——重置本身也是一次未落盘的改动。
   */
  const dirtyKeys = useMemo<Set<string>>(() => {
    const flatDraft = flattenManagedConfig(draftDoc)
    const keys = new Set<string>()
    for (const key of new Set([...Object.keys(flatDraft), ...Object.keys(committed)])) {
      if (flatDraft[key] !== committed[key]) keys.add(key)
    }
    return keys
  }, [draftDoc, committed])

  const [committingKey, setCommittingKey] = useState<string | null>(null)

  /**
   * 写入单个键。
   *
   * 语义是「把这一个键的当前值合并进该层并落盘」——不是保存整层。
   * 因此实现上是把草稿里的这个键，配上**服务端已有版本**做一次保存；
   * 保存成功后该键进入已提交快照，其它键的未落盘改动保持不动。
   */
  const commitKey = async (key: string) => {
    setCommittingKey(key)
    setSaveError('')
    try {
      // PUT 语义是整层替换：只发单键会把层里其他键全部抹掉，发整份草稿又会
      // 把其它键的未落盘改动一并提交——两种都不是「只写这个键」。
      // 正确口径：已存层快照（committed）+ 这个键的草稿值。committed 而非
      // layer?.config：单键保存后父级不重取层（防重挂丢草稿），layer 会滞后。
      const flatDraft = flattenManagedConfig(draftDoc)
      const next: Record<string, unknown> = { ...committed }
      // 先剔除该键及所有 `key.` 前缀点分旧键：已存的 alert.rules.tps-warn.threshold
      // 这类旧子键会与草稿展开的新对象在服务端展平时撞同一路径，排序后旧键反而
      // 覆盖新值（保存成功但内容没变）。对组重置与删规则同一条路径生效。
      for (const k of Object.keys(next)) {
        if (k === key || k.startsWith(`${key}.`)) delete next[k]
      }
      // 草稿侧同样按前缀收集：object 键展平后是多个 `key.*` 子键；草稿里删掉
      // 该键（前缀下一条都没有）＝提交「删除这个键」。
      for (const [k, v] of Object.entries(flatDraft)) {
        if (k === key || k.startsWith(`${key}.`)) next[k] = v
      }
      const saved = await saveMut.mutateAsync({
        scope,
        scopeRefId,
        config: JSON.stringify(next),
        expectedVersion: serverVersion,
        expectedLayerIdentity: serverIdentity,
        remark: remark.trim() || undefined,
      })
      // 用返回层更新本地已存快照与乐观并发基线；父级把返回体回写层详情缓存，
      // 不重取层（重取交给「重新加载」），草稿靠 editorKey 不含版本号保住。
      setCommitted(flattenManagedConfig(parseLayerConfig(saved.config)))
      setServerVersion(saved.version)
      setServerIdentity(saved.identity)
      onSaved(saved)
    } catch (err) {
      const status = (err as { response?: { status?: number } }).response?.status
      if (status === 409) {
        setConflict(true)
        return
      }
      setSaveError(apiErrorMessage(err, t('managedConfig.layers.editor.saveFailed')))
    } finally {
      setCommittingKey(null)
    }
  }

  const handleSave = async () => {
    setConflict(false)
    setSaveError('')
    try {
      const saved = await saveMut.mutateAsync({
        scope,
        scopeRefId,
        config: draftText,
        expectedVersion: serverVersion,
        expectedLayerIdentity: serverIdentity,
        remark: remark.trim() || undefined,
      })
      setCommitted(flattenManagedConfig(parseLayerConfig(saved.config)))
      setServerVersion(saved.version)
      setServerIdentity(saved.identity)
      onSaved(saved)
    } catch (err) {
      const status = (err as { response?: { status?: number } }).response?.status
      const msg = apiErrorMessage(err, t('managedConfig.layers.editor.saveFailed'))
      if (status === 409) {
        setConflict(true)
        return
      }
      // 定向拒绝（禁档键 / 未登记键 / 目标不存在）必须留在页面上可读，不能只闪一下 toast。
      setSaveError(msg)
    }
  }

  /** 触发影响预演：用户改完想知道「这一改会波及多少台、指标翻几倍」。 */
  const runImpact = () => {
    impactMut.mutate({ scope, scopeRefId, config: draftText }, { onSuccess: setImpact })
  }

  const dirtyCount = dirtyKeys.size

  return (
    <Panel
      title={t('managedConfig.layers.editor.title')}
      icon={<SlidersHorizontal className="size-4" />}
      data-testid="managed-layer-editor"
      /*
        面板底部只留「版本号 + 删除 + 保存整层」三样。
        逐字段的「写入」已经在行内，这里再放一遍会让用户以为是两套保存。
      */
      footer={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-xs text-muted-foreground" data-testid="managed-layer-version">
              {layer
                ? t('managedConfig.layers.editor.version', { version: layer.version })
                : t('managedConfig.layers.editor.notConfigured')}
            </span>
            {dirtyCount > 0 ? (
              <Badge variant="secondary" className="text-[10px]" data-testid="managed-layer-dirty-count">
                {dirtyCount} 项未落盘
              </Badge>
            ) : null}
            {layer ? (
              <Button
                type="button"
                size="xs"
                variant="ghost"
                className="text-destructive"
                onClick={onRequestDelete}
                data-testid="managed-layer-delete"
              >
                <Trash2 />
                {t('managedConfig.layers.editor.delete')}
              </Button>
            ) : null}
          </div>
          <div className="flex items-center gap-2">
            <Button
              type="button"
              size="xs"
              variant="outline"
              disabled={!draftText.trim() || impactMut.isPending}
              onClick={runImpact}
              data-testid="managed-impact-run"
            >
              {impactMut.isPending
                ? t('managedConfig.layers.editor.impactRunning')
                : t('managedConfig.layers.editor.impactRun')}
            </Button>
            <Button
              type="button"
              size="sm"
              disabled={!canSave}
              onClick={() => {
                void handleSave()
              }}
              data-testid="managed-layer-save"
            >
              <Save />
              {saveMut.isPending
                ? t('managedConfig.layers.editor.saving')
                : t('managedConfig.layers.editor.save')}
            </Button>
          </div>
        </div>
      }
    >
      <div className="flex flex-col gap-3">
        {/* 冲突优先于一切：此时任何保存都会覆盖别人的修改，必须先重新加载。 */}
        {conflict ? (
          <Alert variant="destructive" data-testid="managed-layer-conflict">
            <ShieldAlert aria-hidden />
            <AlertTitle>{t('managedConfig.layers.editor.conflictTitle')}</AlertTitle>
            <AlertDescription>
              <span className="text-xs">{t('managedConfig.layers.editor.conflict')}</span>
              <Button
                type="button"
                size="xs"
                variant="outline"
                className="mt-2"
                onClick={() => {
                  setConflict(false)
                  onRequestReload()
                }}
                data-testid="managed-layer-reload"
              >
                {t('managedConfig.layers.editor.reload')}
              </Button>
            </AlertDescription>
          </Alert>
        ) : null}

        {/* 后端拒绝原因原样展示（含具体键名）。 */}
        {saveError ? (
          <Alert variant="destructive" data-testid="managed-layer-save-error">
            <AlertTriangle aria-hidden />
            <AlertTitle>{t('managedConfig.layers.editor.saveFailed')}</AlertTitle>
            <AlertDescription className="text-xs">{saveError}</AlertDescription>
          </Alert>
        ) : null}

        {/*
          编辑器占满整行，校验/预演移到下方折叠区。
          早先是 `lg:grid-cols-2` 两栏：编辑器被压进半栏（实测仅 540px），
          左分类导航再吃掉 160px，右侧字段只剩 368px，字段全部横向溢出重叠。
          配置页的主任务是「改配置」，编辑区应当优先拿到全部宽度。
        */}
        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-1.5 sm:flex-row sm:items-end sm:justify-between">
            <div className="flex flex-col gap-1">
              <label className="text-sm font-medium" htmlFor="managed-layer-config">
                {t('managedConfig.layers.editor.configLabel')}
              </label>
              <p className="text-xs text-muted-foreground">
                每项下方的「写入」只提交这一项；改完多处后可一次性保存整层。
              </p>
            </div>
            <Input
              className="h-8 sm:w-64"
              value={remark}
              placeholder={t('managedConfig.layers.editor.remark')}
              onChange={(e) => setRemark(e.target.value)}
              data-testid="managed-layer-remark"
            />
          </div>

          {/*
            可视化编辑器：按 catalog 元数据渲染开关/数字/下拉/标签/规则编辑器。
            禁止档键（bridge.* 等）也会列出但只读——藏起来会让人以为「没有这个配置项」。
            未取到 catalog 时回退到纯 YAML 编辑器（不阻塞使用）。
          */}
          {catalog ? (
            <ManagedConfigEditor
              items={catalog.items}
              groups={catalog.groups}
              doc={draftDoc}
              onChange={(next) => {
                setDraftText((prev) => mergeManagedYaml(prev, next, catalog.items))
                setImpact(null)
                setSaveError('')
              }}
              yamlText={draftText}
              onYamlChange={(text) => {
                setDraftText(text)
                setImpact(null)
                setSaveError('')
              }}
              onRegenerateYaml={() => {
                setDraftText(stringifyManagedYaml(draftDoc, catalog.items))
              }}
              inherited={inheritedFlat}
              parseError={draftParseError}
              fieldValidations={fieldValidations}
              dirtyKeys={dirtyKeys}
              committingKey={committingKey}
              onCommitKey={(key) => {
                void commitKey(key)
              }}
            />
          ) : (
            <ManagedYamlEditor
              items={[]}
              value={draftText}
              onChange={(text) => {
                setDraftText(text)
                setImpact(null)
                setSaveError('')
              }}
              onValidityChange={setYamlValid}
            />
          )}
        </div>

        {/* 汇总条：只在真的有错误或预演结果时出现，平时不占版面。 */}
        {errors.length > 0 ? (
          <div className="rounded-md border border-destructive/40 bg-destructive/5 p-3" data-testid="managed-validate-result">
            <p className="text-xs font-medium text-destructive">
              {t('managedConfig.layers.editor.validateFailed', { count: errors.length })}
            </p>
            <ul className="mt-1 list-disc space-y-0.5 pl-4 text-xs text-destructive" data-testid="managed-validate-errors">
              {errors.map((message) => (
                <li key={message}>{message}</li>
              ))}
            </ul>
          </div>
        ) : validateMut.isSuccess ? (
          <p className="text-xs text-status-success" data-testid="managed-validate-ok">
            {t('managedConfig.layers.editor.validateOk')}
          </p>
        ) : null}

        {impact ? (
          <div className="rounded-md border p-3" data-testid="managed-impact">
            <p className="text-xs font-medium">{t('managedConfig.layers.editor.impact')}</p>
            <div className="mt-2 grid grid-cols-2 gap-2 text-xs">
              <div>
                <p className="text-muted-foreground">{t('managedConfig.layers.editor.impactAffected')}</p>
                <p className="font-medium tabular-nums" data-testid="managed-impact-affected">
                  {t('managedConfig.layers.editor.impactAffectedValue', {
                    affected: impact.affectedInstances,
                    total: impact.totalInstances,
                  })}
                </p>
              </div>
              <div>
                <p className="text-muted-foreground">{t('managedConfig.layers.editor.impactMultiplier')}</p>
                <p className="font-medium tabular-nums" data-testid="managed-impact-multiplier">
                  {t('managedConfig.layers.editor.impactMultiplierValue', {
                    // 后端给的是原始浮点（如 0.3333333333333333），直接插值会让界面出现
                    // 一串无意义的小数位。保留两位并去掉末尾的 0，读数才像人写的。
                    multiplier: Number(impact.metricMultiplier.toFixed(2)),
                  })}
                </p>
              </div>
              <div className="col-span-2">
                <p className="text-muted-foreground">{t('managedConfig.layers.editor.impactPeriod')}</p>
                <p className="font-medium tabular-nums">
                  {t('managedConfig.layers.editor.impactPeriodValue', {
                    effective: impact.effectiveCollectPeriodTicks,
                    default: impact.defaultCollectPeriodTicks,
                  })}
                </p>
              </div>
            </div>

            {/* 禁档键：非空即不可下发，最醒目。 */}
            {forbiddenKeys.length > 0 ? (
              <Alert variant="destructive" className="mt-2 px-2 py-1.5" data-testid="managed-impact-forbidden">
                <ShieldAlert aria-hidden />
                <AlertDescription className="text-xs">
                  <span className="font-medium">{t('managedConfig.layers.editor.forbiddenTitle')}</span>
                  <span className="mt-1 block font-mono">{forbiddenKeys.join('、')}</span>
                </AlertDescription>
              </Alert>
            ) : null}

            {/* 危险档键：能存能下发，但会改变数据量/资源占用。 */}
            {impact.guardedKeys.length > 0 ? (
              <Alert variant="warning" className="mt-2 px-2 py-1.5" data-testid="managed-impact-guarded">
                <AlertTriangle aria-hidden />
                <AlertDescription className="text-xs">
                  <span className="font-medium">{t('managedConfig.layers.editor.guardedTitle')}</span>
                  <span className="mt-1 block font-mono">{impact.guardedKeys.join('、')}</span>
                </AlertDescription>
              </Alert>
            ) : null}

            {impact.notes.length > 0 ? (
              <ul className="mt-2 list-disc space-y-0.5 pl-4 text-xs text-muted-foreground">
                {impact.notes.map((note) => (
                  <li key={note}>{note}</li>
                ))}
              </ul>
            ) : null}
          </div>
        ) : null}

        {/* 保存被拦下时说明原因，否则用户只看到一个灰按钮。 */}
        {blockedByForbidden ? (
          <p className="text-xs text-destructive" data-testid="managed-save-blocked">
            {t('managedConfig.layers.editor.saveBlockedForbidden')}
          </p>
        ) : blockedByValidate ? (
          <p className="text-xs text-destructive" data-testid="managed-save-blocked">
            {t('managedConfig.layers.editor.saveBlockedInvalid')}
          </p>
        ) : null}
      </div>
    </Panel>
  )
}

// ---------------------------------------------------------------------------
// 逐台目标
// ---------------------------------------------------------------------------

/** 目标列表：服务端分页 + 状态筛选 + 生效值展开。 */
function TargetTable({ sessionId, live }: { sessionId: number; live: boolean }) {
  const { t } = useTranslation()
  const [state, setState] = useState<ManagedTargetState | ''>('')
  const [page, setPage] = useState(1)
  const [expanded, setExpanded] = useState<number[]>([])

  const query = useManagedPushTargets(sessionId, { ...(state ? { state } : {}), page, pageSize: PAGE_SIZE }, live)
  const items = query.data?.items ?? []
  const total = query.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / (query.data?.pageSize ?? PAGE_SIZE)))

  return (
    <Panel
      title={t('managedConfig.targets.title')}
      icon={<Plug className="size-4" />}
      actions={
        <NativeSelect
          size="sm"
          aria-label={t('common.status')}
          data-testid="managed-target-state-filter"
          value={state}
          onChange={(e) => {
            setState(e.target.value as ManagedTargetState | '')
            setPage(1)
          }}
        >
          <NativeSelectOption value="">{t('common.all')}</NativeSelectOption>
          {MANAGED_TARGET_STATES.map((s) => (
            <NativeSelectOption key={s} value={s}>
              {t(`managedConfig.targetState.${s}`)}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      }
      footer={
        total > 0 ? (
          <PaginationBar
            page={page}
            totalPages={totalPages}
            total={total}
            onChange={setPage}
            totalLabel={t('managedConfig.targets.totalLabel', { total })}
            prevLabel={t('common.prevPage')}
            nextLabel={t('common.nextPage')}
          />
        ) : undefined
      }
    >
      {query.isError ? (
        <p className="text-sm text-destructive">{t('managedConfig.targets.loadFailed')}</p>
      ) : query.isLoading ? (
        <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
      ) : items.length === 0 ? (
        <EmptyState title={t('managedConfig.targets.empty')} description={t('managedConfig.targets.emptyHint')} />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('managedConfig.targets.colInstance')}</TableHead>
              <TableHead>{t('managedConfig.targets.colBatch')}</TableHead>
              <TableHead>{t('common.status')}</TableHead>
              <TableHead>{t('managedConfig.targets.colError')}</TableHead>
              <TableHead>{t('managedConfig.targets.colEffective')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((target: ManagedPushTarget) => {
              const open = expanded.includes(target.id)
              return [
                <TableRow key={target.id} data-testid="managed-target-row">
                  <TableCell className="font-medium">{target.instanceName}</TableCell>
                  <TableCell className="tabular-nums">{target.batchNo}</TableCell>
                  <TableCell>
                    <StatusBadge
                      level={targetStateLevel(target.state)}
                      label={t(`managedConfig.targetState.${target.state}`)}
                      pulse={target.state === 'pushing'}
                    />
                  </TableCell>
                  <TableCell className="max-w-[280px] truncate text-xs text-destructive" title={target.error}>
                    {target.error || '—'}
                  </TableCell>
                  <TableCell>
                    {target.effectiveJson ? (
                      <Button
                        type="button"
                        size="xs"
                        variant="ghost"
                        onClick={() =>
                          setExpanded((prev) =>
                            prev.includes(target.id) ? prev.filter((id) => id !== target.id) : [...prev, target.id],
                          )
                        }
                      >
                        {open ? t('common.close') : t('common.details')}
                      </Button>
                    ) : (
                      <span className="text-xs text-muted-foreground">—</span>
                    )}
                  </TableCell>
                </TableRow>,
                open ? (
                  <TableRow key={`${target.id}-effective`}>
                    <TableCell colSpan={5}>
                      <pre className="max-h-64 overflow-auto rounded-md bg-muted p-2 text-xs">
                        {prettyJson(parseEffectiveJson(target.effectiveJson))}
                      </pre>
                    </TableCell>
                  </TableRow>
                ) : null,
              ]
            })}
          </TableBody>
        </Table>
      )}
    </Panel>
  )
}

// ---------------------------------------------------------------------------
// 会话详情
// ---------------------------------------------------------------------------

/** 会话详情：批次进度、可用动作（含后果说明）、逐台目标与回滚。 */
function SessionDetail({
  session,
  live,
  onSessionUpdated,
}: {
  session: ManagedPushSession
  live: boolean
  onSessionUpdated: (session: ManagedPushSession) => void
}) {
  const { t } = useTranslation()
  const startMut = useStartManagedPush()
  const releaseMut = useReleaseManagedPush()
  const pauseMut = usePauseManagedPush()
  const resumeMut = useResumeManagedPush()
  const cancelMut = useCancelManagedPush()
  const rollbackMut = useManagedRollback()

  const guard = releaseGuard(session)
  const terminal = isPushTerminal(session.status)
  const progress = session.plannedCount > 0 ? Math.round((session.appliedCount / session.plannedCount) * 100) : 0

  const [rollbackReason, setRollbackReason] = useState('')
  const [rollbackOpen, setRollbackOpen] = useState(false)
  const [cancelOpen, setCancelOpen] = useState(false)

  return (
    <div className="flex flex-col gap-3" data-testid="managed-push-detail">
      <Panel title={t('managedConfig.pushes.detailTitle', { id: session.id })} icon={<ArrowRightCircle className="size-4" />}>
        <div className="flex flex-col gap-3">
          <div className="flex flex-wrap items-center gap-2 text-xs">
            <StatusBadge
              level={pushStatusLevel(session.status)}
              label={t(`managedConfig.pushStatus.${session.status}`)}
              pulse={session.status === 'running'}
            />
            <span className="text-muted-foreground" data-testid="managed-push-batch">
              {t('managedConfig.pushes.batchProgress', {
                current: session.currentBatch,
                total: session.totalBatches,
              })}
            </span>
            <span className="text-muted-foreground" data-testid="managed-push-counters">
              {t('managedConfig.pushes.counters', {
                applied: session.appliedCount,
                failed: session.failedCount,
                timeout: session.timeoutCount,
                skipped: session.skippedCount,
                planned: session.plannedCount,
              })}
            </span>
          </div>

          <Progress value={progress} aria-label={t('managedConfig.pushes.progressLabel')} />

          {session.status === 'paused' ? (
            <Alert variant="warning" className="px-2 py-1.5" data-testid="managed-push-paused">
              <PauseCircle aria-hidden />
              <AlertDescription className="text-xs">
                {session.pauseKind === 'circuit_break'
                  ? t('managedConfig.pushes.pausedByCircuit', {
                      threshold: session.breakThreshold ?? 0,
                      observed: session.breakObserved ?? 0,
                    })
                  : t('managedConfig.pushes.pausedManual', { reason: session.pauseReason || '—' })}
              </AlertDescription>
            </Alert>
          ) : null}

          <div className="flex flex-wrap items-center gap-2">
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={startMut.isPending || terminal || session.status !== 'running'}
              onClick={() =>
                startMut.mutate(session.id, {
                  onSuccess: () => onSessionUpdated({ ...session, status: 'running' }),
                })
              }
              data-testid="managed-push-start"
            >
              <CirclePlay />
              {t('managedConfig.pushes.actionStart')}
            </Button>
            <Button
              type="button"
              size="sm"
              disabled={releaseMut.isPending || !guard.allowed}
              title={guard.allowed ? undefined : t(`managedConfig.pushes.${guard.reasonKey}`)}
              onClick={() => releaseMut.mutate(session.id, { onSuccess: onSessionUpdated })}
              data-testid="managed-release"
            >
              {t('managedConfig.pushes.releaseLabel', {
                current: session.currentBatch,
                total: session.totalBatches,
                applied: session.appliedCount,
              })}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={pauseMut.isPending || terminal || session.status === 'paused'}
              onClick={() =>
                pauseMut.mutate(
                  { id: session.id, reason: t('managedConfig.pushes.pauseDefaultReason') },
                  { onSuccess: onSessionUpdated },
                )
              }
              data-testid="managed-push-pause"
            >
              <PauseCircle />
              {t('managedConfig.pushes.actionPause')}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={resumeMut.isPending || session.status !== 'paused'}
              onClick={() => resumeMut.mutate(session.id, { onSuccess: onSessionUpdated })}
              data-testid="managed-push-resume"
            >
              <CirclePlay />
              {t('managedConfig.pushes.actionResume')}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="destructive"
              disabled={cancelMut.isPending || terminal}
              onClick={() => setCancelOpen(true)}
              data-testid="managed-push-cancel"
            >
              <Ban />
              {t('managedConfig.pushes.actionCancel')}
            </Button>
          </div>

          {/* 放行不可点的原因必须写在页面上。 */}
          {!guard.allowed ? (
            <p className="text-xs text-muted-foreground" data-testid="managed-release-blocked">
              {t(`managedConfig.pushes.${guard.reasonKey}`)}
            </p>
          ) : null}

          <ul className="space-y-1 text-xs text-muted-foreground" data-testid="managed-push-consequences">
            <li>{t('managedConfig.pushes.effectStart')}</li>
            <li>{t('managedConfig.pushes.effectRelease')}</li>
            <li>{t('managedConfig.pushes.effectPause')}</li>
            <li>{t('managedConfig.pushes.effectResume')}</li>
            <li>{t('managedConfig.pushes.effectCancel')}</li>
          </ul>
        </div>
      </Panel>

      <TargetTable sessionId={session.id} live={live} />

      {/* 回滚只对「已完成的会话」成立：后端要求源会话 status=completed。 */}
      <Panel title={t('managedConfig.rollback.title')} icon={<RotateCcw className="size-4" />}>
        <div className="flex flex-col gap-2">
          <p className="text-xs text-muted-foreground">{t('managedConfig.rollback.description')}</p>
          <Input
            value={rollbackReason}
            placeholder={t('managedConfig.rollback.reasonPlaceholder')}
            onChange={(e) => setRollbackReason(e.target.value)}
            data-testid="managed-rollback-reason"
          />
          <div className="flex flex-wrap items-center gap-2">
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={session.status !== 'completed' || !rollbackReason.trim() || rollbackMut.isPending}
              onClick={() => setRollbackOpen(true)}
              data-testid="managed-rollback"
            >
              <RotateCcw />
              {t('managedConfig.rollback.action')}
            </Button>
            {session.status !== 'completed' ? (
              <span className="text-xs text-muted-foreground" data-testid="managed-rollback-blocked">
                {t('managedConfig.rollback.onlyCompleted')}
              </span>
            ) : null}
          </div>
        </div>
      </Panel>

      <DangerConfirm
        open={rollbackOpen}
        title={t('managedConfig.rollback.confirmTitle')}
        description={t('managedConfig.rollback.confirmDescription')}
        confirmLabel={t('managedConfig.rollback.action')}
        pending={rollbackMut.isPending}
        onCancel={() => setRollbackOpen(false)}
        onConfirm={() => {
          rollbackMut.mutate(
            { sourceSessionId: session.id, reason: rollbackReason.trim(), confirmed: true },
            {
              onSuccess: (created) => {
                setRollbackOpen(false)
                setRollbackReason('')
                onSessionUpdated(created)
              },
            },
          )
        }}
      />

      <DangerConfirm
        open={cancelOpen}
        title={t('managedConfig.pushes.cancelConfirmTitle')}
        description={t('managedConfig.pushes.cancelConfirmDescription')}
        confirmLabel={t('managedConfig.pushes.actionCancel')}
        pending={cancelMut.isPending}
        onCancel={() => setCancelOpen(false)}
        onConfirm={() => {
          cancelMut.mutate(
            { id: session.id, reason: t('managedConfig.pushes.cancelDefaultReason') },
            {
              onSuccess: (updated) => {
                setCancelOpen(false)
                onSessionUpdated(updated)
              },
            },
          )
        }}
      />
    </div>
  )
}

// ---------------------------------------------------------------------------
// 新建会话
// ---------------------------------------------------------------------------

/** 新建会话对话框：作用域、目标、批次与熔断参数。 */
function CreatePushDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: (session: ManagedPushSession) => void
}) {
  const { t } = useTranslation()
  const createMut = useCreateManagedPush()
  const [scope, setScope] = useState<ManagedScope>('global')
  const [scopeRefId, setScopeRefId] = useState(0)
  const [batchSize, setBatchSize] = useState(0)
  const [threshold, setThreshold] = useState(20)
  const [ackTimeout, setAckTimeout] = useState(30)
  const [reason, setReason] = useState('')

  const nodesQuery = useNodes({ enabled: open && scope === 'node' })
  const canSubmit = scope === 'global' || scopeRefId > 0

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('managedConfig.pushes.createTitle')}</DialogTitle>
          <DialogDescription>{t('managedConfig.pushes.createDescription')}</DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-1.5">
            <label className="text-sm font-medium" htmlFor="managed-create-scope">
              {t('managedConfig.layers.scope')}
            </label>
            <NativeSelect
              id="managed-create-scope"
              value={scope}
              onChange={(e) => {
                setScope(e.target.value as ManagedScope)
                setScopeRefId(0)
              }}
              data-testid="managed-create-scope"
            >
              {SCOPE_OPTIONS.map((s) => (
                <NativeSelectOption key={s} value={s}>
                  {t(`managedConfig.scope.${s}`)}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </div>

          {scope === 'node' ? (
            <div className="flex flex-col gap-1.5">
              <label className="text-sm font-medium" htmlFor="managed-create-node">
                {t('managedConfig.layers.target')}
              </label>
              <NativeSelect
                id="managed-create-node"
                value={scopeRefId || ''}
                onChange={(e) => setScopeRefId(Number(e.target.value))}
                data-testid="managed-create-node"
              >
                <NativeSelectOption value="">{t('managedConfig.layers.selectNode')}</NativeSelectOption>
                {(nodesQuery.data ?? []).map((node) => (
                  <NativeSelectOption key={node.id} value={node.id}>
                    {node.name}
                  </NativeSelectOption>
                ))}
              </NativeSelect>
            </div>
          ) : null}

          {scope === 'instance' ? (
            <div className="flex flex-col gap-1.5">
              <label className="text-sm font-medium">{t('managedConfig.layers.target')}</label>
              <InstancePicker
                value={scopeRefId || null}
                onChange={(value) => setScopeRefId(value ?? 0)}
                ariaLabel={t('managedConfig.layers.selectInstance')}
                enabled={open}
              />
            </div>
          ) : null}

          <div className="grid grid-cols-3 gap-2">
            <div className="flex flex-col gap-1.5">
              <label className="text-xs font-medium" htmlFor="managed-create-batch">
                {t('managedConfig.pushes.batchSize')}
              </label>
              <Input
                id="managed-create-batch"
                type="number"
                min={0}
                value={batchSize}
                onChange={(e) => setBatchSize(Number(e.target.value))}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <label className="text-xs font-medium" htmlFor="managed-create-threshold">
                {t('managedConfig.pushes.threshold')}
              </label>
              <Input
                id="managed-create-threshold"
                type="number"
                min={0}
                max={100}
                value={threshold}
                onChange={(e) => setThreshold(Number(e.target.value))}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <label className="text-xs font-medium" htmlFor="managed-create-ack">
                {t('managedConfig.pushes.ackTimeout')}
              </label>
              <Input
                id="managed-create-ack"
                type="number"
                min={5}
                value={ackTimeout}
                onChange={(e) => setAckTimeout(Number(e.target.value))}
              />
            </div>
          </div>
          <p className="text-xs text-muted-foreground">{t('managedConfig.pushes.paramsHint')}</p>

          <div className="flex flex-col gap-1.5">
            <label className="text-xs font-medium" htmlFor="managed-create-reason">
              {t('managedConfig.pushes.reason')}
            </label>
            <Input
              id="managed-create-reason"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder={t('managedConfig.pushes.reasonPlaceholder')}
            />
          </div>
        </div>

        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel')}
          </Button>
          <Button
            type="button"
            disabled={!canSubmit || createMut.isPending}
            onClick={() =>
              createMut.mutate(
                {
                  scope,
                  scopeRefId: scope === 'global' ? 0 : scopeRefId,
                  batchSize,
                  failureRateThreshold: threshold,
                  ackTimeoutSec: ackTimeout,
                  reason: reason.trim() || undefined,
                },
                {
                  onSuccess: (session) => {
                    onCreated(session)
                    onOpenChange(false)
                  },
                },
              )
            }
            data-testid="managed-create-submit"
          >
            {t('managedConfig.pushes.createSubmit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ---------------------------------------------------------------------------
// 页面
// ---------------------------------------------------------------------------

/**
 * 托管配置与下发页（配置层 + 下发会话两个签页）。
 *
 * `defaultTab` 供外层容器（探针托管总控页）指定初始签页：
 * 它把"配置"与"下发历史"拆成两个外层 Tab，内层仍用这两个签页承载，避免重写已验证的代码。
 */
export default function ManagedConfigPage({ defaultTab = 'layers' }: { defaultTab?: 'layers' | 'pushes' } = {}) {
  const { t } = useTranslation()
  const [tab, setTab] = useState<'layers' | 'pushes'>(defaultTab)

  // ---- 配置层 ----
  const [scope, setScope] = useState<ManagedScope>('global')
  const [scopeRefId, setScopeRefId] = useState(0)
  // 重新加载计数：并进编辑器的 key，让「重新加载」能把草稿重置为服务端真值。
  const [reloadNonce, setReloadNonce] = useState(0)
  const [deleteOpen, setDeleteOpen] = useState(false)

  const qc = useQueryClient()
  const layersQuery = useManagedLayers()
  const layerQuery = useManagedLayer(scope, scopeRefId)
  const nodesQuery = useNodes({ enabled: scope === 'node' })
  const deleteMut = useDeleteManagedLayer()

  /**
   * 实例作用域下"只有一台实例"时自动选中它。
   *
   * ## 为什么需要
   * 切到「实例」作用域后还要再点一次目标下拉——单机运维时这一步纯属多余，
   * 用户看着一个已经确定的选择却必须再点一下才能开始编辑。
   *
   * ## 为什么只在「恰好一台」时做
   * 多台时自动选谁都可能选错，而改错机器的配置代价不低（配置会真的下发到那台探针）。
   * 所以自动选中只在"选了也不可能有歧义"时发生；两台上以一律留空，把选择权交回用户。
   *
   * ## 为什么用 pageSize=2
   * 只需要区分 0 / 1 / 多台三种情况，取 2 条就够。用 2 而不是 1 是为了不把"多台"误判成"一台"。
   */
  const singleInstanceQuery = useInstanceSearch(
    { page: 1, pageSize: 2, sort: 'name', order: 'asc' },
    scope === 'instance' && scopeRefId === 0,
  )
  useEffect(() => {
    // 只在一个明确的时刻自动选：实例作用域 + 尚未选目标 + 总数恰好 1。
    if (scope !== 'instance' || scopeRefId !== 0) return
    if (singleInstanceQuery.data?.total !== 1) return
    const only = singleInstanceQuery.data.items?.[0]
    // 微任务推迟到渲染提交后再写状态，避免在 effect 里同步 setState 触发级联渲染。
    if (only) {
      const id = only.id
      queueMicrotask(() => setScopeRefId(id))
    }
  }, [scope, scopeRefId, singleInstanceQuery.data])

  // 逐成员收窄：`data?.found` 只在同一次访问里成立，跨两次访问 TS 无法保证。
  const layerResult = layerQuery.data
  const layer = layerResult && layerResult.found ? layerResult.layer : null
  const layers = layersQuery.data ?? []
  const targetId = scope === 'global' ? 0 : scopeRefId

  // 编辑器重挂键：作用域切换 / 目标切换 / 手动重新加载才重挂。
  //
  // 刻意不含 `layer?.version`：版本号变化只代表「服务端那层被写过」，把它并进 key
  // 会在每次保存（含单键写入）后重挂编辑器、销毁其它键的未落盘草稿。保存成功后
  // 层数据用 PUT 返回体直接回写查询缓存（见 onSaved），编辑器靠本地 `committed`
  // 快照跟进真值；想要服务端最新版时走「重新加载」按钮（reloadNonce 换 key）。
  const editorKey = useMemo(
    () => `${scope}:${targetId}:${reloadNonce}`,
    [scope, targetId, reloadNonce],
  )

  // ---- 下发会话 ----
  const [status, setStatus] = useState('')
  const [page, setPage] = useState(1)
  const [createOpen, setCreateOpen] = useState(false)
  // 本次访问内见过的会话：列表端点缺失时它就是唯一数据源。
  const [localSessions, setLocalSessions] = useState<ManagedPushSession[]>([])
  const [activeId, setActiveId] = useState<number | null>(null)
  const pushesQuery = useManagedPushes({ ...(status ? { status } : {}), page, pageSize: PAGE_SIZE })

  const upsertSession = (session: ManagedPushSession) => {
    setLocalSessions((prev) => [session, ...prev.filter((item) => item.id !== session.id)])
  }

  // 列表端点缺失（后端未注册 GET /managed-pushes）时降级为本地已知会话。
  const listMissing = pushesQuery.isError
  const remoteSessions = pushesQuery.data?.items ?? []
  const sessions = listMissing ? localSessions : remoteSessions
  const total = listMissing ? sessions.length : (pushesQuery.data?.total ?? 0)
  const totalPages = Math.max(1, Math.ceil(total / (pushesQuery.data?.pageSize ?? PAGE_SIZE)))
  const active = sessions.find((item) => item.id === activeId) ?? null
  const activeLive = !!active && (active.status === 'running' || active.status === 'awaiting_release')

  return (
    <PageShell>
      <PageHeader title={t('managedConfig.title')} description={t('managedConfig.description')} />

      <Tabs value={tab} onValueChange={(value) => setTab(value as 'layers' | 'pushes')}>
        <TabsList>
          <TabsTrigger value="layers">{t('managedConfig.tabLayers')}</TabsTrigger>
          <TabsTrigger value="pushes">{t('managedConfig.tabPushes')}</TabsTrigger>
        </TabsList>

        <TabsContent value="layers" className="flex flex-col gap-3">
          <Panel title={t('managedConfig.layers.title')} icon={<SlidersHorizontal className="size-4" />}>
            <div className="flex flex-col gap-3">
              <p className="text-xs text-muted-foreground">{t('managedConfig.layers.hint')}</p>
              <div className="flex flex-wrap items-end gap-2">
                <div className="flex flex-col gap-1.5">
                  <label className="text-xs font-medium" htmlFor="managed-scope">
                    {t('managedConfig.layers.scope')}
                  </label>
                  <NativeSelect
                    id="managed-scope"
                    value={scope}
                    onChange={(e) => {
                      setScope(e.target.value as ManagedScope)
                      setScopeRefId(0)
                    }}
                    data-testid="managed-scope"
                  >
                    {SCOPE_OPTIONS.map((s) => (
                      <NativeSelectOption key={s} value={s}>
                        {t(`managedConfig.scope.${s}`)}
                      </NativeSelectOption>
                    ))}
                  </NativeSelect>
                </div>

                {scope === 'node' ? (
                  <div className="flex flex-col gap-1.5">
                    <label className="text-xs font-medium" htmlFor="managed-node">
                      {t('managedConfig.layers.target')}
                    </label>
                    <NativeSelect
                      id="managed-node"
                      value={scopeRefId || ''}
                      onChange={(e) => setScopeRefId(Number(e.target.value))}
                      data-testid="managed-node"
                    >
                      <NativeSelectOption value="">{t('managedConfig.layers.selectNode')}</NativeSelectOption>
                      {(nodesQuery.data ?? []).map((node) => (
                        <NativeSelectOption key={node.id} value={node.id}>
                          {node.name}
                        </NativeSelectOption>
                      ))}
                    </NativeSelect>
                  </div>
                ) : null}

                {scope === 'instance' ? (
                  <div className="flex min-w-[240px] flex-col gap-1.5">
                    <label className="text-xs font-medium">{t('managedConfig.layers.target')}</label>
                    <InstancePicker
                      value={scopeRefId || null}
                      onChange={(value) => setScopeRefId(value ?? 0)}
                      ariaLabel={t('managedConfig.layers.selectInstance')}
                    />
                  </div>
                ) : null}
              </div>

              {/* 已有层清单：一眼看清「哪些层配过、配到第几版」。 */}
              {layers.length === 0 ? (
                <p className="text-xs text-muted-foreground" data-testid="managed-layers-empty">
                  {t('managedConfig.layers.empty')}
                </p>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t('managedConfig.layers.colScope')}</TableHead>
                      <TableHead>{t('managedConfig.layers.colTarget')}</TableHead>
                      <TableHead>{t('managedConfig.layers.colVersion')}</TableHead>
                      <TableHead>{t('managedConfig.layers.colUpdatedAt')}</TableHead>
                      <TableHead>{t('common.actions')}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {layers.map((row) => (
                      <TableRow key={row.id} data-testid="managed-layer-row">
                        <TableCell>{t(`managedConfig.scope.${row.scope}`)}</TableCell>
                        <TableCell>
                          {row.scope === 'global'
                            ? t('managedConfig.scope.globalTarget')
                            : row.scopeName || `#${row.scopeRefId}`}
                        </TableCell>
                        <TableCell className="tabular-nums">v{row.version}</TableCell>
                        <TableCell className="text-xs text-muted-foreground">{safeRelative(row.updatedAt)}</TableCell>
                        <TableCell>
                          <Button
                            type="button"
                            size="xs"
                            variant="ghost"
                            onClick={() => {
                              setScope(row.scope)
                              setScopeRefId(row.scopeRefId)
                            }}
                          >
                            {t('managedConfig.layers.load')}
                          </Button>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
            </div>
          </Panel>

          {/* 目标未选定时不挂编辑器：没有目标就没有「该层」，先选再编。 */}
          {scope !== 'global' && scopeRefId === 0 ? (
            <Panel>
              <p className="text-sm text-muted-foreground" data-testid="managed-layer-need-target">
                {t('managedConfig.layers.needTarget')}
              </p>
            </Panel>
          ) : (
            <LayerEditor
              key={editorKey}
              scope={scope}
              scopeRefId={targetId}
              layer={layer}
              // 「拿到过结果」就算就绪：found=false（该层确实没配过）也是就绪，
              // 此时允许首次保存。只有仍在 loading/error 时才算未就绪。
              layerReady={layerQuery.isSuccess}
              onSaved={(saved) => {
                // 用 PUT 返回体直接回写层详情缓存：单键写入新建层时若任由 404
                // 缓存挂着，编辑器拿不到 layer → 删除按钮缺失，且 30s 内切走再
                // 切回会以版本 0 打开、再写入撞 409。整层 / 单键统一回写，
                // 不再重取层——重取交给「重新加载」按钮，保存路径是权威的。
                qc.setQueryData(
                  ['managed-configs', 'layer', scope, targetId],
                  { found: true, layer: saved } as ManagedLayerResult,
                )
                void layersQuery.refetch()
              }}
              onRequestDelete={() => setDeleteOpen(true)}
              onRequestReload={() => {
                void layerQuery.refetch()
                setReloadNonce((n) => n + 1)
              }}
            />
          )}

          <DangerConfirm
            open={deleteOpen}
            title={t('managedConfig.layers.editor.deleteTitle')}
            description={t('managedConfig.layers.editor.deleteDesc')}
            confirmLabel={t('managedConfig.layers.editor.delete')}
            pending={deleteMut.isPending}
            onCancel={() => setDeleteOpen(false)}
            onConfirm={() => {
              deleteMut.mutate(
                { scope, scopeRefId: targetId },
                {
                  onSuccess: () => {
                    setDeleteOpen(false)
                    // 层详情回写「未配置」并重挂编辑器：若只重取，
                    // 已编辑过的草稿会留在版本 0 的编辑器里，整层保存
                    // 会把刚删掉的键整包写回去。
                    qc.setQueryData(
                      ['managed-configs', 'layer', scope, targetId],
                      { found: false } as ManagedLayerResult,
                    )
                    void layersQuery.refetch()
                    setReloadNonce((n) => n + 1)
                  },
                },
              )
            }}
          />
        </TabsContent>

        <TabsContent value="pushes" className="flex flex-col gap-3">
          <Panel
            title={t('managedConfig.pushes.listTitle')}
            icon={<ArrowRightCircle className="size-4" />}
            actions={
              <div className="flex items-center gap-2">
                <NativeSelect
                  size="sm"
                  aria-label={t('common.status')}
                  data-testid="managed-push-status-filter"
                  value={status}
                  onChange={(e) => {
                    setStatus(e.target.value)
                    setPage(1)
                  }}
                >
                  <NativeSelectOption value="">{t('common.all')}</NativeSelectOption>
                  {MANAGED_PUSH_STATUSES.map((s) => (
                    <NativeSelectOption key={s} value={s}>
                      {t(`managedConfig.pushStatus.${s}`)}
                    </NativeSelectOption>
                  ))}
                </NativeSelect>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() => setCreateOpen(true)}
                  data-testid="managed-push-create"
                >
                  {t('managedConfig.pushes.create')}
                </Button>
              </div>
            }
            footer={
              total > 0 && !listMissing ? (
                <PaginationBar
                  page={page}
                  totalPages={totalPages}
                  total={total}
                  onChange={setPage}
                  totalLabel={t('managedConfig.pushes.totalLabel', { total })}
                  prevLabel={t('common.prevPage')}
                  nextLabel={t('common.nextPage')}
                />
              ) : undefined
            }
          >
            <div className="flex flex-col gap-2">
              {/* 列表端点缺失必须说清，否则「空列表」会被读成「没下发过」。 */}
              {listMissing ? (
                <Alert variant="warning" className="px-2 py-1.5" data-testid="managed-push-list-missing">
                  <AlertTriangle aria-hidden />
                  <AlertDescription className="text-xs">{t('managedConfig.pushes.listEndpointMissing')}</AlertDescription>
                </Alert>
              ) : null}

              {sessions.length === 0 ? (
                <EmptyState title={t('managedConfig.pushes.empty')} description={t('managedConfig.pushes.emptyHint')} />
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t('managedConfig.pushes.colId')}</TableHead>
                      <TableHead>{t('common.status')}</TableHead>
                      <TableHead>{t('managedConfig.pushes.colBatch')}</TableHead>
                      <TableHead>{t('managedConfig.pushes.colApplied')}</TableHead>
                      <TableHead>{t('managedConfig.pushes.colFailed')}</TableHead>
                      <TableHead>{t('managedConfig.pushes.colCreatedAt')}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {sessions.map((row) => (
                      <TableRow key={row.id} data-testid="managed-push-row">
                        <TableCell>
                          <Button
                            type="button"
                            size="xs"
                            variant="ghost"
                            onClick={() => setActiveId(row.id)}
                            data-testid={`managed-push-open-${row.id}`}
                          >
                            #{row.id}
                          </Button>
                        </TableCell>
                        <TableCell>
                          <StatusBadge
                            level={pushStatusLevel(row.status)}
                            label={t(`managedConfig.pushStatus.${row.status}`)}
                            pulse={row.status === 'running'}
                          />
                        </TableCell>
                        <TableCell className="tabular-nums text-xs">
                          {row.currentBatch} / {row.totalBatches}
                        </TableCell>
                        <TableCell className="tabular-nums">{row.appliedCount}</TableCell>
                        <TableCell className="tabular-nums" data-testid="managed-push-failed">
                          {row.failedCount}
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground">{safeRelative(row.createdAt)}</TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
            </div>
          </Panel>

          {active ? (
            <SessionDetail session={active} live={activeLive} onSessionUpdated={upsertSession} />
          ) : (
            <Panel>
              <p className="text-sm text-muted-foreground" data-testid="managed-push-select-hint">
                {t('managedConfig.pushes.selectHint')}
              </p>
            </Panel>
          )}

          <CreatePushDialog
            open={createOpen}
            onOpenChange={setCreateOpen}
            onCreated={(session) => {
              upsertSession(session)
              setActiveId(session.id)
            }}
          />
        </TabsContent>
      </Tabs>
    </PageShell>
  )
}
