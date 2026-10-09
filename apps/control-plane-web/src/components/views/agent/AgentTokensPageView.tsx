/* eslint-disable react-refresh/only-export-components -- 纯函数 parseIdInput/mergeIds/formatScopeSummary 与渲染它们的组件同文件导出，供应用侧 DOM 测同源引用 */
/**
 * @file AgentTokensPageView：Agent Token 管理页的受控视图，列表取数、签发/吊销 mutation、候选数据取数与 toast 由应用容器负责。
 * @input Panel/Button/Input/Label/Badge/Checkbox/StatusBadge/Table/Dialog 原语、layout 页壳（PageShell/PageHeader）、
 *        lib/virtual-list（useVirtualRows：候选项千级时只渲染窗口内的行）、
 *        视图层 DangerConfirm（吊销二次确认）、lib/clipboard（复制）、lucide-react 图标、翻译上下文
 * @output AgentTokensPageView、AgentTokensPageViewProps、AgentTokenRow、AgentTokenStatus、AgentTokenOption、
 *         AgentTokenInstanceOption、AgentTokenNodeOption、AgentTokenIssuePayload、IssuedAgentTokenPlain、
 *         AgentTokensNotice、AgentTokenInstanceCandidateView、AgentTokenInstanceCandidateViewProps、
 *         parseIdInput、mergeIds、formatScopeSummary
 * @sync apps/control-plane-web/src/pages/AgentTokensPage.tsx（容器，再导出三个纯函数）、
 *        apps/control-plane-web/src/pages/AgentTokensPage.dom.test.tsx
 * @since FR-502（组件受控化迁包；原页 FR-387 Agent Token 管理，消费 FR-384 API）
 */
import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Copy, KeyRound, Plus } from 'lucide-react'
import { copyToClipboard } from '@/lib/shared/clipboard'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import { Panel } from '@jianmanager/ui/components/panel'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Label } from '@jianmanager/ui/components/label'
import { Badge } from '@jianmanager/ui/components/badge'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { useVirtualRows } from '@/lib/shared/virtual-list'

/** 解析逗号/空格分隔的正整数 ID 列表。 */
export function parseIdInput(raw: string): number[] {
  if (!raw.trim()) return []
  const parts = raw.split(/[\s,;]+/).filter(Boolean)
  const ids: number[] = []
  const seen = new Set<number>()
  for (const p of parts) {
    const n = Number(p)
    if (!Number.isFinite(n) || n <= 0 || !Number.isInteger(n)) continue
    if (seen.has(n)) continue
    seen.add(n)
    ids.push(n)
  }
  return ids
}

/** 合并多选 ID 与手动输入 ID（去重保序）。 */
export function mergeIds(selected: number[], typed: string): number[] {
  const out: number[] = []
  const seen = new Set<number>()
  for (const id of [...selected, ...parseIdInput(typed)]) {
    if (seen.has(id)) continue
    seen.add(id)
    out.push(id)
  }
  return out
}

/** scope 摘要：如「实例 1,2 · 节点 3」；空则「未授权」。 */
export function formatScopeSummary(
  instIds: number[],
  nodeIds: number[],
  labels: { instances: string; nodes: string; none: string },
): string {
  const parts: string[] = []
  if (instIds.length) parts.push(`${labels.instances} ${instIds.join(',')}`)
  if (nodeIds.length) parts.push(`${labels.nodes} ${nodeIds.join(',')}`)
  return parts.length ? parts.join(' · ') : labels.none
}

/**
 * Token 展示状态（active / expired / revoked）。
 *
 * 状态**判定**不在这里：它沿用应用侧 `agentTokenStatus`（已被 `agentTokens.test.ts` 覆盖），
 * 由容器判定后随行注入——包内不复制一份规则副本，避免两侧漂移。
 */
export type AgentTokenStatus = 'active' | 'expired' | 'revoked'

/**
 * Token 行（本视图渲染所需的最小字段集）。
 *
 * 刻意只声明用到的字段，而非照搬应用侧 `@/api/agentTokens` 的 `AgentTokenInfo`：
 * 容器把 API 对象补上 `status` 后即结构兼容，无需把该 API 类型迁进包（同 `AgentCallLogRow` 的取舍）。
 *
 * 脱敏边界：`tokenPrefix` 是后端给出的**前缀**，后端从不返回明文；
 * 列表只能展示它，本视图任何位置都不得补全、推断或回显完整密钥。
 */
export interface AgentTokenRow {
  /** 行主键。 */
  id: number
  /** Token 名称。 */
  name: string
  /** Token 前缀（非明文，仅用于人工对号）。 */
  tokenPrefix: string
  /** 展示状态（容器注入判定结果）。 */
  status: AgentTokenStatus
  /** 策略版本：2 = V2 能力策略，其余一律按 V1 兼容展示。 */
  policyVersion: number
  /** V2 能力值（未命中候选表时原样展示）。 */
  capabilities: string[]
  /** V1 写白名单值（仅 V1 行展示）。 */
  writeAllowlist: string[]
  /** 限定的实例 ID 列表。 */
  scopedInstanceIds: number[]
  /** 限定的节点 ID 列表。 */
  scopedNodeIds: number[]
  /** 过期时间（RFC3339）；空显示 `—`。 */
  expiresAt: string
  /** 最近使用时间（RFC3339）；空显示 `—`。 */
  lastUsedAt?: string | null
  /** 近 24h 调用次数；缺失按 0 展示。 */
  callCount24h?: number
}

/**
 * 候选项（值 → i18n 标签键）。
 *
 * 能力清单与 V1 写白名单是**与后端枚举对齐的映射表**（应用侧 `CAPABILITY_OPTIONS` /
 * `WRITE_ALLOWLIST_OPTIONS`，含 labelKey），由容器注入而非在包内复制一份：
 * 后端新增/改能力时只改应用侧一处，包内不会留下过期副本。
 */
export interface AgentTokenOption {
  /** 提交给后端的值（如 `instance.life`）。 */
  value: string
  /** 展示用 i18n 标签键（如 `agentTokens.capability.instanceLife`）。 */
  labelKey: string
}

/** 实例候选（容器取数注入；视图只做勾选与手输 ID 的合并）。 */
export interface AgentTokenInstanceOption {
  id: number
  name: string
  /** 实例状态，以次级徽章展示。 */
  status: string
}

/** 节点候选（容器取数注入；容器按原页时机——签发对话框打开时——才发起查询）。 */
export interface AgentTokenNodeOption {
  id: number
  name: string
}

/** 签发请求体：视图完成校验后上报，mutation 由容器执行。 */
export interface AgentTokenIssuePayload {
  /** 已 trim 的名称。 */
  name: string
  /** 实例范围（多选与手输合并去重后的结果）。 */
  scopedInstanceIds: number[]
  /** 节点范围（多选与手输合并去重后的结果）。 */
  scopedNodeIds: number[]
  /** 策略版本：固定 2（本页只签发 V2 能力策略 Token）。 */
  policyVersion: number
  /** 勾选的能力值。 */
  capabilities: string[]
  /** 有效天数（已校验为 1..365 的整数）。 */
  ttlDays: number
}

/**
 * 签发成功结果：`plaintext` 是**唯一一次**明文。
 *
 * 容器只在 `POST /agent/tokens` 的响应里取到它并随本次回调返回；视图把它写进一次性展示对话框，
 * 关闭即清空，不落任何其它状态、不提供任何「再取/重放」入口。
 */
export interface IssuedAgentTokenPlain {
  /** Token 名称（展示在提示文案里）。 */
  name: string
  /** 一次性明文密钥。 */
  plaintext: string
}

/** 提示通道：视图算好文案交容器展示（本包不弹 toast）。 */
export type AgentTokensNotice = (kind: 'success' | 'error', message: string) => void

/**
 * 受控边界（ADR-097 b 范式）：**不取数、不发请求、不弹 toast**。
 *
 * - 列表与三态经 props 注入（容器调 `useAgentTokens`）；失败文案含后端 message，故由容器算好经
 *   `errorText` 注入，视图只决定「加载 / 失败 / 空 / 表格」的判定优先级；
 * - 实例与节点候选、能力与写白名单映射表、MCP 基址（容器读 `window.location`）都是数据，全部注入；
 * - 签发与吊销以回调上报，由容器执行 mutation 并决定成功/失败文案：两个回调都回传成功与否，
 *   视图据此决定「关窗」还是「保留窗口与草稿」——失败时**不关窗**是原页既有语义；
 * - 一次性明文只在 `onIssue` 的**返回值**里出现一次，视图随后只把它渲染进展示对话框；
 * - 吊销二次确认的 `scope` 固定 `platform`，`allowed` 由容器读登录态注入（包内不持鉴权状态），
 *   门禁语义与原页一致，未放宽；
 * - 留包内的纯 UI 状态：签发对话框开合、表单草稿（含实例/节点勾选与手输）、一次性明文持有、
 *   吊销确认目标与在途标记。其中「对话框打开」是节点候选查询的取数时机，故额外经
 *   `onCreateDialogOpenChange` **单向通知**容器（状态仍归本视图所有，容器只据此收敛 `enabled`）。
 */
export interface AgentTokensPageViewProps {
  /** Token 行（空数组即空态）。 */
  tokens: AgentTokenRow[]
  /** 列表加载态（展示加载文案，表格与空态不渲染）。 */
  isLoading: boolean
  /** 列表取数失败（优先于空态展示）。 */
  isError: boolean
  /** 取数失败的一句话说明（容器取后端 message 或兜底文案后注入）。 */
  errorText?: string
  /** 提示通道：签发表单的本地校验提示经它上报（包内不弹 toast）。 */
  notify: AgentTokensNotice
  /** 能力候选项（容器注入应用侧 `CAPABILITY_OPTIONS`）。 */
  capabilityOptions: readonly AgentTokenOption[]
  /** 签发时默认勾选的能力（容器注入应用侧 `DEFAULT_CAPABILITIES`）。 */
  defaultCapabilities: readonly string[]
  /** V1 写白名单展示映射（容器注入应用侧 `WRITE_ALLOWLIST_OPTIONS`）。 */
  writeAllowlistOptions: readonly AgentTokenOption[]
  /**
   * 实例候选窗口（容器按服务端搜索注入：默认前 N 条 + 键入下发 `q`，可为空）。
   *
   * 千级实例（大档 1200）不得一次拉全量再在弹窗里铺开；候选窗口与总数由容器向服务端要，
   * 本视图只渲染窗口内的行（见 AgentTokenInstanceCandidateView）。
   */
  instanceCandidates?: AgentTokenInstanceOption[]
  /** 实例候选总数（服务端返回），用于候选区的截断提示；不给则不提示。 */
  instanceCandidateTotal?: number
  /** 实例候选键入上报（容器做 300ms 防抖后下发服务端 `q`）。 */
  onInstanceQueryChange?: (keyword: string) => void
  /** 节点候选（容器注入，可为空；容器只在签发对话框打开时取）。 */
  nodeOptions?: AgentTokenNodeOption[]
  /**
   * 签发对话框开合上报（**单向通知，不接管开合状态**，`open` 始终由本视图持有）。
   *
   * 唯一用途是取数时机：原页的节点候选查询写作 `useNodes({ enabled: open })`，只在对话框打开时才请求；
   * 容器据此把 `enabled` 收敛到同一时机，迁移后不因页面加载而多打一次节点列表请求。
   */
  onCreateDialogOpenChange?: (open: boolean) => void
  /**
   * 签发 Token；失败返回 `null`（失败提示已由容器弹出），视图据此保持对话框打开以便修改后重试。
   * 成功返回一次性明文，视图随即关闭表单对话框并打开明文展示对话框。
   */
  onIssue: (payload: AgentTokenIssuePayload) => Promise<IssuedAgentTokenPlain | null>
  /**
   * 吊销已确认的 Token（二次确认已在本视图内完成）。
   * 返回是否成功：成功才关闭确认框；失败（提示已由容器弹出）保留确认框。
   */
  onRevoke: (token: AgentTokenRow) => Promise<boolean>
  /** MCP 端点基址（同源推导留在容器，包内不读 `window.location`），用于明文对话框里的示例片段。 */
  mcpUrl: string
  /** 复制结果上报：成功/失败提示文案由容器决定。 */
  onCopyResult?: (ok: boolean) => void
  /**
   * 页头右端的路由跳转入口（容器注入 react-router `Link` 包裹的按钮；
   * 包内不依赖 react-router）。可省略——组件博物馆等无路由场景即不渲染跳转。
   */
  headerLinks?: ReactNode
}

/** 复制按钮：写剪贴板（兼容 HTTP 非安全上下文），结果上报给外壳决定提示（FR-189 先例）。 */
function CopyButton({
  text,
  label,
  onResult,
}: {
  text: string
  label: string
  onResult?: (ok: boolean) => void
}) {
  const copy = async () => {
    const ok = await copyToClipboard(text)
    onResult?.(ok)
  }
  return (
    <Button type="button" variant="outline" size="sm" onClick={copy} className="shrink-0">
      <Copy className="size-4" /> {label}
    </Button>
  )
}

/** 写白名单展示文案（V1 兼容）。 */
function formatWriteAllowlist(
  list: string[],
  options: readonly AgentTokenOption[],
  t: (k: string) => string,
): string {
  if (!list.length) return t('agentTokens.write.none')
  return list
    .map((v) => {
      const opt = options.find((o) => o.value === v)
      return opt ? t(opt.labelKey) : v
    })
    .join('、')
}

/** V2 能力展示文案。 */
function formatCapabilities(
  list: string[],
  options: readonly AgentTokenOption[],
  t: (k: string) => string,
): string {
  if (!list.length) return t('agentTokens.capability.none')
  return list
    .map((v) => {
      const opt = options.find((o) => o.value === v)
      return opt ? t(opt.labelKey) : v
    })
    .join('、')
}

/** 权限列：V2 显示能力，V1 显示旧写白名单。 */
function formatPermissions(
  tok: Pick<AgentTokenRow, 'policyVersion' | 'capabilities' | 'writeAllowlist'>,
  capabilityOptions: readonly AgentTokenOption[],
  writeAllowlistOptions: readonly AgentTokenOption[],
  t: (k: string) => string,
): string {
  if (tok.policyVersion === 2) return formatCapabilities(tok.capabilities, capabilityOptions, t)
  return formatWriteAllowlist(tok.writeAllowlist, writeAllowlistOptions, t)
}

/** 状态 → StatusBadge 级别。 */
function statusLevel(status: AgentTokenStatus): 'success' | 'warning' | 'danger' {
  if (status === 'active') return 'success'
  if (status === 'expired') return 'warning'
  return 'danger'
}

/**
 * Agent Token 管理页展示层（FR-387）。
 * 列表 / 新建 / 吊销三段；创建成功一次性展示明文 + 复制 env/命令片段。
 * 入口仅管理员可见（侧栏）+ 本页角色兜底 + 后端 RBAC，三重把关——本视图只呈现通过门禁后的内容，
 * 角色门禁由容器在挂载本视图前完成（非管理员不会渲染到这里），吊销确认的门禁由 `DangerConfirm` 自行判定。
 */
export function AgentTokensPageView({
  tokens,
  isLoading,
  isError,
  errorText,
  notify,
  capabilityOptions,
  defaultCapabilities,
  writeAllowlistOptions,
  instanceCandidates,
  instanceCandidateTotal,
  onInstanceQueryChange,
  nodeOptions,
  onCreateDialogOpenChange,
  onIssue,
  onRevoke,
  mcpUrl,
  onCopyResult,
  headerLinks,
}: AgentTokensPageViewProps) {
  const { t } = useTranslation()
  const [showCreate, setShowCreate] = useState(false)
  // 一次性明文：只在签发成功后写入，关闭即清空；不存在任何重新获取/回显明文的路径。
  const [issuedPlain, setIssuedPlain] = useState<IssuedAgentTokenPlain | null>(null)
  const [revokeTarget, setRevokeTarget] = useState<AgentTokenRow | null>(null)
  const [revoking, setRevoking] = useState(false)

  // 对话框开合归本视图；此处只把「打开」这一取数时机通知容器（节点候选查询的门控）。
  useEffect(() => {
    onCreateDialogOpenChange?.(showCreate)
  }, [showCreate, onCreateDialogOpenChange])

  const handleRevoke = async () => {
    if (!revokeTarget) return
    setRevoking(true)
    const ok = await onRevoke(revokeTarget)
    setRevoking(false)
    // 仅成功时关闭（与原页一致）；失败提示已由容器弹出，确认框保留便于重试。
    if (ok) setRevokeTarget(null)
  }

  return (
    // 全量对齐：外壳与页头沿用布局层原语。data-page 由 PageShell spread 透传，保持原值。
    <PageShell data-page="agent-tokens">
      <PageHeader
        title={t('agentTokens.title')}
        description={t('agentTokens.subtitle')}
        actions={
          <>
            {headerLinks}
            <Button onClick={() => setShowCreate(true)}>
              <Plus className="size-4" /> {t('agentTokens.create')}
            </Button>
          </>
        }
      />

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : isError ? (
        <Panel>
          <p className="py-6 text-center text-sm text-muted-foreground">{errorText}</p>
        </Panel>
      ) : tokens.length === 0 ? (
        <Panel>
          <p className="py-6 text-center text-sm text-muted-foreground">{t('agentTokens.empty')}</p>
        </Panel>
      ) : (
        <Panel bodyClassName="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('agentTokens.col.name')}</TableHead>
                <TableHead>{t('agentTokens.col.prefix')}</TableHead>
                <TableHead>{t('agentTokens.col.policy')}</TableHead>
                <TableHead>{t('agentTokens.col.scope')}</TableHead>
                <TableHead>{t('agentTokens.col.write')}</TableHead>
                <TableHead>{t('agentTokens.col.expires')}</TableHead>
                <TableHead>{t('agentTokens.col.lastUsed')}</TableHead>
                <TableHead>{t('agentTokens.col.callCount24h')}</TableHead>
                <TableHead>{t('agentTokens.col.status')}</TableHead>
                <TableHead className="w-28 text-right">{t('common.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {tokens.map((tok) => {
                const nodeLabel =
                  tok.policyVersion === 2
                    ? t('agentTokens.scope.nodesInherit')
                    : t('agentTokens.scope.nodesNoInherit')
                const scope = formatScopeSummary(tok.scopedInstanceIds, tok.scopedNodeIds, {
                  instances: t('agentTokens.scope.instances'),
                  nodes: nodeLabel,
                  none: t('agentTokens.scope.none'),
                })
                const perms = formatPermissions(tok, capabilityOptions, writeAllowlistOptions, t)
                const policyLabel =
                  tok.policyVersion === 2 ? t('agentTokens.policy.v2') : t('agentTokens.policy.v1')
                return (
                  <TableRow key={tok.id}>
                    <TableCell className="font-medium">{tok.name}</TableCell>
                    <TableCell>
                      <code className="font-mono text-xs">{tok.tokenPrefix}…</code>
                    </TableCell>
                    <TableCell>
                      <Badge variant={tok.policyVersion === 2 ? 'default' : 'secondary'} className="text-[10px]">
                        {policyLabel}
                      </Badge>
                    </TableCell>
                    <TableCell className="max-w-[14rem] truncate text-xs text-muted-foreground" title={scope}>
                      {scope}
                    </TableCell>
                    <TableCell className="max-w-[12rem] truncate text-xs" title={perms}>
                      {perms}
                    </TableCell>
                    <TableCell className="whitespace-nowrap text-xs tabular-nums">
                      {tok.expiresAt ? new Date(tok.expiresAt).toLocaleString() : '—'}
                    </TableCell>
                    <TableCell className="whitespace-nowrap text-xs tabular-nums text-muted-foreground">
                      {tok.lastUsedAt ? new Date(tok.lastUsedAt).toLocaleString() : '—'}
                    </TableCell>
                    <TableCell className="tabular-nums text-xs">{tok.callCount24h ?? 0}</TableCell>
                    <TableCell>
                      <StatusBadge
                        level={statusLevel(tok.status)}
                        label={t(`agentTokens.status.${tok.status}`)}
                      />
                    </TableCell>
                    <TableCell className="text-right">
                      {tok.status !== 'revoked' && (
                        <Button
                          variant="outline"
                          size="sm"
                          className="text-destructive"
                          onClick={() => setRevokeTarget(tok)}
                        >
                          {t('agentTokens.revoke')}
                        </Button>
                      )}
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </Panel>
      )}

      <CreateAgentTokenDialog
        open={showCreate}
        capabilityOptions={capabilityOptions}
        defaultCapabilities={defaultCapabilities}
        instanceCandidates={instanceCandidates}
        instanceCandidateTotal={instanceCandidateTotal}
        onInstanceQueryChange={onInstanceQueryChange}
        nodeOptions={nodeOptions}
        notify={notify}
        onClose={() => setShowCreate(false)}
        onSubmit={async (payload) => {
          const res = await onIssue(payload)
          // 成功才关窗并转交一次性明文；失败（null）保持对话框打开，草稿留给用户修正。
          if (res) {
            setShowCreate(false)
            setIssuedPlain(res)
          }
          return res
        }}
      />

      <PlaintextRevealDialog
        open={issuedPlain != null}
        name={issuedPlain?.name ?? ''}
        plaintext={issuedPlain?.plaintext ?? ''}
        mcpUrl={mcpUrl}
        onCopyResult={onCopyResult}
        // 关闭即丢弃明文（state 置空），本页无任何「再次查看」入口。
        onClose={() => setIssuedPlain(null)}
      />

      {/* 吊销二次确认：scope 固定 platform，角色门禁由 DangerConfirm 自行读登录态判定。 */}
      <DangerConfirm
        open={revokeTarget != null}
        title={t('agentTokens.revokeTitle')}
        description={t('agentTokens.revokeDesc', { name: revokeTarget?.name ?? '' })}
        confirmLabel={t('agentTokens.revoke')}
        confirmText={revokeTarget?.name}
        scope="platform"
        pending={revoking}
        onConfirm={handleRevoke}
        onCancel={() => setRevokeTarget(null)}
      />
    </PageShell>
  )
}

/** 候选行高（px）。虚拟化按它等距定位，故行内只留内边距、不加外边距（同 NetworkInstancePickerView 的取舍）。 */
const INSTANCE_CAND_ROW_HEIGHT = 36

/**
 * 实例候选选择器（多选）：服务端搜索 + 虚拟化勾选列表 + 截断提示 + 已选回显。
 *
 * 【为什么不是单选 `InstancePicker`】本表单要「勾选多个实例 → 与手输 ID 合并后签发」的批量语义，
 * 单选 Combobox 无法表达。两者共享同一取舍：候选一律走服务端搜索——实例数是千级（大档 1200），
 * 原先「容器拉全量 `/instances` → 弹窗里一次性挂 1200 × 4 个元素（label/Checkbox/span/Badge）」既让
 * 页面一挂载就付整份列表的代价，也让弹窗每次渲染都把整份列表求值一遍。
 *
 * 候选窗口与总数由容器按服务端搜索结果注入；键入经 `onQueryChange` 上报（防抖与请求都在容器——
 * 那是「何时发请求」的应用侧策略）。本组件只做三件本地事：维护搜索框草稿、按窗口虚拟化渲染、
 * 记下已勾选实例的展示名（候选窗口随键入变化，已选项可能不在窗口内——不记名字就只能回显裸 id）。
 */
export interface AgentTokenInstanceCandidateViewProps {
  /** 候选窗口（容器取数注入）；缺省按空候选渲染。 */
  items?: AgentTokenInstanceOption[]
  /** 候选总数（服务端返回），用于截断提示。 */
  total?: number
  /** 已勾选的实例 id（受控：与「已勾选 N 个实例」计数同源，由对话框持有）。 */
  selected: number[]
  /** 勾选变更上报。 */
  onToggle: (id: number, on: boolean) => void
  /** 键入关键字上报（容器做 300ms 防抖后下发服务端 `q`）。 */
  onQueryChange: (keyword: string) => void
}

/** 实例候选选择器：搜索框 + 虚拟化勾选列表 + 截断提示 + 已选回显。 */
export function AgentTokenInstanceCandidateView({
  items,
  total,
  selected,
  onToggle,
  onQueryChange,
}: AgentTokenInstanceCandidateViewProps) {
  const { t } = useTranslation()
  // 搜索框草稿（纯展示态）：关键字原样上报，防抖与请求由容器负责。
  const [keyword, setKeyword] = useState('')
  // 已勾选实例的展示名（勾选当时从候选行记下）。
  const [names, setNames] = useState<Record<number, string>>({})
  const list = items ?? []

  // 已选态改 Set：候选行按 selected.includes(id) 判断时，勾选累积后会随窗口渲染变成热点。
  const selectedSet = useMemo(() => new Set(selected), [selected])
  /** 服务端截断时提示继续输入缩小范围（按服务端窗口判定，与已勾选多少无关）。 */
  const truncated = total !== undefined && total > list.length

  const toggle = (inst: AgentTokenInstanceOption, on: boolean) => {
    if (on) setNames((prev) => (prev[inst.id] === inst.name ? prev : { ...prev, [inst.id]: inst.name }))
    onToggle(inst.id, on)
  }

  const { containerRef, onScroll, range, totalSize } = useVirtualRows({
    total: list.length,
    itemSize: INSTANCE_CAND_ROW_HEIGHT,
    overscan: 8,
  })

  // 过滤收窄后回到顶部：候选骤短时，上一轮的 scrollOffset 会让窗口落到列表之外（空窗一帧）。
  useEffect(() => {
    if (containerRef.current) containerRef.current.scrollTop = 0
  }, [keyword, containerRef])

  return (
    <div className="space-y-1">
      <Input
        value={keyword}
        onChange={(e) => {
          setKeyword(e.target.value)
          onQueryChange(e.target.value)
        }}
        placeholder={t('agentTokens.field.instanceSearch')}
        aria-label={t('agentTokens.field.instanceSearch')}
        className="h-8 text-xs"
      />
      {truncated && (
        <p className="text-[11px] text-muted-foreground">
          {t('common.searchTruncated', { shown: list.length, total: total ?? 0 })}
        </p>
      )}
      {/* 已选回显：候选窗口之外（键入收窄后）的已选项也在这里显示，否则勾了什么就看不见了。 */}
      {selected.length > 0 && (
        <div className="flex flex-wrap items-center gap-1">
          <span className="text-[11px] text-muted-foreground">
            {t('agentTokens.field.instancesSelected', { count: selected.length })}
          </span>
          {selected.map((id) => (
            <Badge key={id} variant="secondary" className="text-[10px]">
              {names[id] ? `#${id} ${names[id]}` : `#${id}`}
            </Badge>
          ))}
        </div>
      )}
      <div ref={containerRef} onScroll={onScroll} className="max-h-36 overflow-y-auto rounded-md border p-1">
        {list.length === 0 ? (
          <p className="px-2 py-3 text-center text-xs text-muted-foreground">{t('agentTokens.field.noInstances')}</p>
        ) : (
          // 虚拟化：外层撑起总高，内层按 range.before 平移，只渲染窗口内的行。
          <div className="relative" style={{ height: totalSize }}>
            <ul style={{ transform: `translateY(${range.before}px)` }}>
              {list.slice(range.start, range.end).map((inst) => (
                <li key={inst.id} className="h-9">
                  <label className="flex h-full cursor-pointer items-center gap-2 rounded px-1 text-sm hover:bg-muted/60">
                    {/* aria-label 落在 Checkbox（可聚焦元素）上，读屏取到的名字不受行内其它文本影响。 */}
                    <Checkbox
                      checked={selectedSet.has(inst.id)}
                      onCheckedChange={(v) => toggle(inst, v === true)}
                      aria-label={inst.name}
                    />
                    <span className="truncate">
                      #{inst.id} {inst.name}
                    </span>
                    <Badge variant="secondary" className="ml-auto text-[10px]">
                      {inst.status}
                    </Badge>
                  </label>
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>
    </div>
  )
}

/**
 * 新建 Token 对话框（视图内部件）：表单草稿与开合都由宿主视图持有。
 *
 * 校验失败经 `notify` 上报（提示文案与渠道与原页一致），提交经 `onSubmit` 上报——
 * 本身不发请求；成功由宿主关窗并转交一次性明文。
 */
function CreateAgentTokenDialog({
  open,
  capabilityOptions,
  defaultCapabilities,
  instanceCandidates,
  instanceCandidateTotal,
  onInstanceQueryChange,
  nodeOptions,
  notify,
  onClose,
  onSubmit,
}: {
  open: boolean
  capabilityOptions: readonly AgentTokenOption[]
  defaultCapabilities: readonly string[]
  instanceCandidates?: AgentTokenInstanceOption[]
  instanceCandidateTotal?: number
  onInstanceQueryChange?: (keyword: string) => void
  nodeOptions?: AgentTokenNodeOption[]
  notify: AgentTokensNotice
  onClose: () => void
  /** 提交签发；返回 `null` 表示失败（提示已由容器弹出），对话框保持打开。 */
  onSubmit: (payload: AgentTokenIssuePayload) => Promise<IssuedAgentTokenPlain | null>
}) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  const [selectedInst, setSelectedInst] = useState<number[]>([])
  const [selectedNode, setSelectedNode] = useState<number[]>([])
  const [instIdsText, setInstIdsText] = useState('')
  const [nodeIdsText, setNodeIdsText] = useState('')
  const [capabilities, setCapabilities] = useState<string[]>([...defaultCapabilities])
  const [ttlDays, setTtlDays] = useState('90')
  // 提交在途：原先取自 mutation 的 isPending，现由本视图按 await 期间自持（禁用同一批按钮）。
  const [pending, setPending] = useState(false)

  // 每次打开重置表单（父组件改 open 时 Radix 不一定触发 onOpenChange）。
  useEffect(() => {
    if (!open) return
    /* eslint-disable react-hooks/set-state-in-effect -- 弹窗打开瞬间一次性清空，属合法同步 */
    setName('')
    setSelectedInst([])
    setSelectedNode([])
    setInstIdsText('')
    setNodeIdsText('')
    setCapabilities([...defaultCapabilities])
    setTtlDays('90')
    /* eslint-enable react-hooks/set-state-in-effect */
    // eslint-disable-next-line react-hooks/exhaustive-deps -- 仅在弹窗打开瞬间初始化一次；默认能力值变化不应重置用户草稿
  }, [open])

  const handleOpenChange = (next: boolean) => {
    if (!next) onClose()
  }

  /** 勾选变更：按 `on` 精确增删（两个候选区都把它当受控组件用，勾选状态由本对话框持有）。 */
  const toggleId = (list: number[], id: number, on: boolean, set: (v: number[]) => void) => {
    set(on ? (list.includes(id) ? list : [...list, id]) : list.filter((x) => x !== id))
  }

  const toggleCapability = (value: string) => {
    setCapabilities((prev) => (prev.includes(value) ? prev.filter((v) => v !== value) : [...prev, value]))
  }

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    const trimmed = name.trim()
    if (!trimmed) {
      notify('error', t('agentTokens.validation.nameRequired'))
      return
    }
    const ttl = Number(ttlDays)
    if (!Number.isFinite(ttl) || ttl <= 0 || !Number.isInteger(ttl)) {
      notify('error', t('agentTokens.validation.ttlInvalid'))
      return
    }
    if (ttl > 365) {
      notify('error', t('agentTokens.validation.ttlMax'))
      return
    }
    setPending(true)
    try {
      await onSubmit({
        name: trimmed,
        scopedInstanceIds: mergeIds(selectedInst, instIdsText),
        scopedNodeIds: mergeIds(selectedNode, nodeIdsText),
        policyVersion: 2,
        capabilities,
        ttlDays: ttl,
      })
    } finally {
      setPending(false)
    }
  }

  /**
   * 实例候选已改为服务端搜索（候选窗口 ≤ CANDIDATE_LIMIT 条），故原先「弹窗关闭时把千级列表置空」
   * 的 memo 门控不再必要：`.map()` 现在发生在子组件 AgentTokenInstanceCandidateView 内部，
   * 而 Radix 的 DialogContent 关闭时连 children 都不渲染，元素创建成本随之消失。
   * 节点候选由容器按「对话框已打开」门控取数，未取到时为空数组（门控语义见 AgentTokensPageViewProps）。
   */
  const nodeList = nodeOptions ?? []

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className={scrollableDialogContentClass}>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <KeyRound className="size-4" />
            {t('agentTokens.createTitle')}
          </DialogTitle>
          <DialogDescription>{t('agentTokens.createDesc')}</DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit}>
          <ScrollableDialogBody className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="agent-token-name">
                {t('agentTokens.field.name')} <span className="text-destructive">*</span>
              </Label>
              <Input
                id="agent-token-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t('agentTokens.field.namePlaceholder')}
                autoFocus
                maxLength={128}
              />
            </div>

            <div className="space-y-1.5">
              <Label>{t('agentTokens.field.instances')}</Label>
              <p className="text-xs text-muted-foreground">{t('agentTokens.field.instancesHint')}</p>
              {/* 千级实例：候选走服务端搜索（默认前 N 条 + 键入 300ms 防抖下发 q）+ 虚拟化渲染。 */}
              <AgentTokenInstanceCandidateView
                items={instanceCandidates}
                total={instanceCandidateTotal}
                selected={selectedInst}
                onToggle={(id, on) => toggleId(selectedInst, id, on, setSelectedInst)}
                onQueryChange={(kw) => onInstanceQueryChange?.(kw)}
              />
              <Input
                value={instIdsText}
                onChange={(e) => setInstIdsText(e.target.value)}
                placeholder={t('agentTokens.field.idsPlaceholder')}
                aria-label={t('agentTokens.field.instanceIds')}
              />
            </div>

            <div className="space-y-1.5">
              <Label>{t('agentTokens.field.nodes')}</Label>
              <p className="text-xs text-muted-foreground">{t('agentTokens.field.nodesHint')}</p>
              <div className="max-h-36 space-y-1 overflow-y-auto rounded-md border p-2">
                {nodeList.length === 0 ? (
                  <p className="text-xs text-muted-foreground">{t('agentTokens.field.noNodes')}</p>
                ) : (
                  nodeList.map((node) => (
                    <label key={node.id} className="flex cursor-pointer items-center gap-2 text-sm">
                      <Checkbox
                        checked={selectedNode.includes(node.id)}
                        onCheckedChange={(v) => toggleId(selectedNode, node.id, v === true, setSelectedNode)}
                      />
                      <span className="truncate">
                        #{node.id} {node.name}
                      </span>
                    </label>
                  ))
                )}
              </div>
              <Input
                value={nodeIdsText}
                onChange={(e) => setNodeIdsText(e.target.value)}
                placeholder={t('agentTokens.field.idsPlaceholder')}
                aria-label={t('agentTokens.field.nodeIds')}
              />
            </div>

            <div className="space-y-1.5">
              <Label>{t('agentTokens.field.capabilities')}</Label>
              <p className="text-xs text-muted-foreground">{t('agentTokens.field.capabilitiesHint')}</p>
              <div className="max-h-48 space-y-1 overflow-y-auto rounded-md border p-2">
                {capabilityOptions.map((opt) => (
                  <label key={opt.value} className="flex cursor-pointer items-center gap-2 text-sm">
                    <Checkbox
                      checked={capabilities.includes(opt.value)}
                      onCheckedChange={() => toggleCapability(opt.value)}
                    />
                    <span>{t(opt.labelKey)}</span>
                    <code className="ml-auto font-mono text-[10px] text-muted-foreground">{opt.value}</code>
                  </label>
                ))}
              </div>
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="agent-token-ttl">{t('agentTokens.field.ttlDays')}</Label>
              <Input
                id="agent-token-ttl"
                type="number"
                min={1}
                max={365}
                value={ttlDays}
                onChange={(e) => setTtlDays(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">{t('agentTokens.field.ttlHint')}</p>
            </div>
          </ScrollableDialogBody>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => handleOpenChange(false)} disabled={pending}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={pending}>
              {pending ? t('common.saving') : t('agentTokens.createSubmit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * 创建成功后一次性展示明文 + 复制 env / jm-agent 示例。
 *
 * 明文只来自 `plaintext` 这一个入参（签发响应），关闭即由宿主清空 state——
 * 本对话框不缓存、不落库、不提供任何重新打开取回明文的入口。
 */
function PlaintextRevealDialog({
  open,
  name,
  plaintext,
  mcpUrl,
  onCopyResult,
  onClose,
}: {
  open: boolean
  name: string
  plaintext: string
  /** MCP 端点基址（容器注入，包内不读 window.location）。 */
  mcpUrl: string
  onCopyResult?: (ok: boolean) => void
  onClose: () => void
}) {
  const { t } = useTranslation()
  const envLine = useMemo(() => `JM_AGENT_TOKEN=${plaintext}`, [plaintext])
  const whoamiCmd = useMemo(
    () => `jm-agent --token ${plaintext} whoami`,
    [plaintext],
  )

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className={scrollableDialogContentClass}>
        <DialogHeader>
          <DialogTitle>{t('agentTokens.revealTitle')}</DialogTitle>
          <DialogDescription>{t('agentTokens.revealDesc', { name })}</DialogDescription>
        </DialogHeader>
        <ScrollableDialogBody className="space-y-3">
          <div className="rounded-md border border-status-warning/40 bg-status-warning/10 p-2 text-xs text-muted-foreground">
            {t('agentTokens.revealWarning')}
          </div>
          <div className="space-y-1">
            <div className="text-xs font-medium text-muted-foreground">{t('agentTokens.reveal.plaintext')}</div>
            <div className="flex items-start gap-2 rounded-md border bg-muted/50 p-2">
              <code className="flex-1 break-all font-mono text-xs leading-relaxed">{plaintext}</code>
              <CopyButton text={plaintext} label={t('agentTokens.copy')} onResult={onCopyResult} />
            </div>
          </div>
          <div className="space-y-1">
            <div className="text-xs font-medium text-muted-foreground">{t('agentTokens.mcpUrl')}</div>
            <div className="flex items-start gap-2 rounded-md border bg-muted/50 p-2">
              <code className="flex-1 break-all font-mono text-xs leading-relaxed">{mcpUrl}</code>
              <CopyButton text={mcpUrl} label={t('agentTokens.copy')} onResult={onCopyResult} />
            </div>
          </div>
          <div className="space-y-1">
            <div className="text-xs font-medium text-muted-foreground">{t('agentTokens.reveal.env')}</div>
            <div className="flex items-start gap-2 rounded-md border bg-muted/50 p-2">
              <code className="flex-1 break-all font-mono text-xs leading-relaxed">{envLine}</code>
              <CopyButton text={envLine} label={t('agentTokens.copy')} onResult={onCopyResult} />
            </div>
          </div>
          <div className="space-y-1">
            <div className="text-xs font-medium text-muted-foreground">{t('agentTokens.reveal.cli')}</div>
            <div className="flex items-start gap-2 rounded-md border bg-muted/50 p-2">
              <code className="flex-1 break-all font-mono text-xs leading-relaxed">{whoamiCmd}</code>
              <CopyButton text={whoamiCmd} label={t('agentTokens.copy')} onResult={onCopyResult} />
            </div>
          </div>
        </ScrollableDialogBody>
        <DialogFooter>
          <Button onClick={onClose}>{t('agentTokens.revealDone')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export default AgentTokensPageView
