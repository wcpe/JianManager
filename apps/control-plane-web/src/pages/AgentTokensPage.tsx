/* eslint-disable react-refresh/only-export-components -- 纯函数 parseIdInput/mergeIds/formatScopeSummary 经本文件再导出，保持 AgentTokensPage.dom.test.tsx 既有导入路径可用 */
// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只做平台管理员门禁、列表/候选取数、签发与吊销 mutation、
// 危险操作门禁与 toast 文案接线。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { toast } from 'sonner'
import { useAuthStore } from '@/stores/auth'
import { useAgentTokens, useIssueAgentToken, useRevokeAgentToken, agentTokenStatus, WRITE_ALLOWLIST_OPTIONS, CAPABILITY_OPTIONS, DEFAULT_CAPABILITIES } from '@/api/agentTokens'
import { mcpBaseUrl } from '@/api/agentObservability'
import { useInstanceSearch } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { useDebounced } from '@/lib/hooks/use-debounced'
import { CANDIDATE_LIMIT } from '@/components/views/instances/InstancePicker'
import { Button } from '@jianmanager/ui/components/button'
import { AgentTokensPageView } from '@/components/views/agent/AgentTokensPageView'
import type { AgentTokenRow } from '@/components/views/agent/AgentTokensPageView'

/** 平台管理员角色值（与后端 model.RolePlatformAdmin 对齐）。 */
const ROLE_PLATFORM_ADMIN = 10

type ErrResp = { response?: { data?: { message?: string }; status?: number } }

/** 从 mutation 错误里取后端消息，缺省回落到兜底文案。 */
const errMsg = (e: unknown, fallback: string) => (e as ErrResp)?.response?.data?.message || fallback

/**
 * 视图内的纯函数（`parseIdInput` / `mergeIds` / `formatScopeSummary`）随视图迁入包内，此处原样再导出：
 * `AgentTokensPage.dom.test.tsx` 直接从本模块按名导入并逐项校验，经本文件再导出后，该守卫继续
 * 盯住视图真正使用的那份实现，而不是应用侧的一份副本（同 `McpActivityPage` 的 `WINDOW_PRESETS` 先例）。
 */
export { parseIdInput, mergeIds, formatScopeSummary } from '@/components/views/agent/AgentTokensPageView'

/**
 * Agent Token 管理页容器（FR-387，消费 FR-384 API，ADR-097 b 范式）：平台管理员门禁、列表取数、
 * 签发/吊销两个写动作、候选数据与映射表注入、页头跳转入口都在这里决定，
 * 表格、签发对话框与一次性明文展示交共享视图。
 *
 * 平台管理员三重把关（侧栏入口 + 本页兜底 + 后端 RBAC）中的本页兜底留在本层：非管理员不渲染视图，
 * 且三个查询的 `enabled` 全部收敛到 `isPlatformAdmin`（不因渲染本页而发出任何请求）。
 * 保留原路径与原默认导出，路由表（route-chunks 的 `/agent-tokens`）无需改动。
 */
export default function AgentTokensPage() {
  const { t } = useTranslation()
  const role = useAuthStore((s) => s.role)
  const isPlatformAdmin = role === ROLE_PLATFORM_ADMIN

  // 吊销是平台级破坏操作：角色门禁在应用侧判定后注入（包内不持鉴权状态），scope 固定 platform 不降级。

  const listQ = useAgentTokens({ enabled: isPlatformAdmin })
  const issue = useIssueAgentToken()
  const revoke = useRevokeAgentToken()

  // 签发对话框的开合状态归视图持有；这里只接收「已打开」的单向通知，用于把节点与实例候选查询
  // 都收敛到同一取数时机（节点沿用原页 `useNodes({ enabled: open })`；实例候选见下）。
  const [createDialogOpen, setCreateDialogOpen] = useState(false)
  /**
   * 实例候选：改为服务端搜索（默认前 CANDIDATE_LIMIT 条 + 键入 300ms 防抖下发 `q`）。
   *
   * 原页是 `useInstances()` 无门控地随页面取数、并在弹窗里把整份列表（千级：大档 1200）铺成勾选项，
   * 等于页面一挂载就付约 1MB 的整份列表，且每次渲染都要把 1200 × 4 个元素求值一遍；现按同一时机
   * 收敛到「弹窗已打开」，候选窗口与总数由服务端给，视图只渲染窗口内的行。
   */
  const [instKeyword, setInstKeyword] = useState('')
  const instQ = useDebounced(instKeyword, 300).trim()
  const { data: instPage } = useInstanceSearch(
    { ...(instQ ? { q: instQ } : {}), page: 1, pageSize: CANDIDATE_LIMIT, sort: 'name', order: 'asc' },
    isPlatformAdmin && createDialogOpen,
  )
  const { data: nodes } = useNodes({ enabled: isPlatformAdmin && createDialogOpen })

  if (!isPlatformAdmin) {
    return (
      <div className="grid h-full place-items-center text-sm text-muted-foreground" data-page="agent-tokens">
        {t('agentTokens.forbidden')}
      </div>
    )
  }

  // 行 = 规范化后的 Token 元数据 + 展示状态。状态判定沿用 API 层已被 `agentTokens.test.ts` 覆盖的
  // `agentTokenStatus`，包内不再复制一份规则，避免两侧漂移。
  const tokens: AgentTokenRow[] = (listQ.data ?? []).map((tok) => ({
    ...tok,
    status: agentTokenStatus(tok),
  }))

  return (
    <AgentTokensPageView
      tokens={tokens}
      isLoading={listQ.isLoading}
      isError={listQ.isError}
      errorText={errMsg(listQ.error, t('agentTokens.loadFailed'))}
      notify={(kind, message) => {
        if (kind === 'success') toast.success(message)
        else toast.error(message)
      }}
      capabilityOptions={CAPABILITY_OPTIONS}
      defaultCapabilities={DEFAULT_CAPABILITIES}
      writeAllowlistOptions={WRITE_ALLOWLIST_OPTIONS}
      instanceCandidates={instPage?.items}
      instanceCandidateTotal={instPage?.total}
      onInstanceQueryChange={setInstKeyword}
      nodeOptions={nodes}
      onCreateDialogOpenChange={(open) => {
        // 关键字归容器持有、搜索框草稿归视图，而搜索框在弹窗关闭时随视图卸载：
        // 不归零就会出现「框是空的、候选却仍被上次的关键字过滤」。
        // 但本回调在父级每次渲染都会被视图的 effect 重放一次（prop 是新函数引用），
        // 故只在**关闭→打开**这一跳归零，否则会把用户正在输入的关键字反复清掉。
        if (open && !createDialogOpen) setInstKeyword('')
        setCreateDialogOpen(open)
      }}
      mcpUrl={mcpBaseUrl()}
      onCopyResult={(ok) => {
        if (ok) toast.success(t('agentTokens.copied'))
        else toast.error(t('agentTokens.copyFailed'))
      }}
      onIssue={async (payload) => {
        try {
          const res = await issue.mutateAsync(payload)
          toast.success(t('agentTokens.createSuccess'))
          // 明文只在本次响应里出现一次，原样交给视图做一次性展示。
          return { name: res.token.name, plaintext: res.plaintext }
        } catch (e) {
          const status = (e as ErrResp)?.response?.status
          if (status === 403) toast.error(t('agentTokens.forbidden'))
          else toast.error(errMsg(e, t('agentTokens.createFailed')))
          // 失败返回 null：视图保持对话框打开以便修正后重试（与原页 onError 分支一致），提示已在此弹出。
          return null
        }
      }}
      onRevoke={async (tok) => {
        try {
          await revoke.mutateAsync(tok.id)
          toast.success(t('agentTokens.revokeSuccess'))
          return true
        } catch (e) {
          toast.error(errMsg(e, t('agentTokens.revokeFailed')))
          // 失败返回 false：视图保留二次确认框（与原页一致），提示已在此弹出。
          return false
        }
      }}
      headerLinks={
        <>
          <Button variant="outline" size="sm" asChild>
            <Link to="/mcp-activity">{t('agentTokens.openSessions')}</Link>
          </Button>
          <Button variant="outline" size="sm" asChild>
            <Link to="/agent-call-logs">{t('agentTokens.openLogs')}</Link>
          </Button>
        </>
      }
    />
  )
}
