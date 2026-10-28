import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, RefreshCw, Trash2 } from 'lucide-react'
import { Alert, AlertDescription, Badge, Button, Panel, Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui'
import { NativeSelect } from '@jianmanager/ui/components/native-select'
import { Textarea } from '@jianmanager/ui/components/textarea'
import { toast } from 'sonner'
import {
  useCreateMCPConnection,
  useDeleteMCPConnection,
  useDiscoverMCPTools,
  useMCPConnections,
  useMCPTools,
  useProbeMCPConnection,
  useUpdateMCPConnection,
  useUpdateMCPTool,
  type MCPAllowedOrigin,
  type MCPConnection,
  type MCPConnectionPayload,
} from '@/api/assistant'

/**
 * 外部 MCP 连接配置（FR-517 §6）。
 *
 * 界面必须把三件事说清楚，否则管理员会误以为配好就能用：
 *  1. **V1 里外部工具仅平台管理员可用**（无法证明上游按调用者区分身份时，
 *     任何共享连接都会把上游权限借给普通用户）；
 *  2. **连接建立后所有工具默认禁执行**，要逐条启用并设定风险分类；
 *  3. **风险分类不由上游 annotations 推导**——上游可以随便写 readOnlyHint，
 *     所以 `unknown` 一律按「需要确认」处理。
 *
 * 凭据只在上行时传明文（`authSecret`），下行永远拿不到值，只有 `hasCredential`。
 */

/** 表单草稿。 */
interface Draft {
  slug: string
  name: string
  endpoint: string
  authStyle: string
  authHeaderName: string
  authSource: string
  authEnv: string
  authSecret: string
  clearCredential: boolean
  enabled: boolean
  // origin 用单行文本编辑（`scheme://host:port cidr1,cidr2` 一行一条），
  // 避免为一个低频配置做整套嵌套表单。
  originsText: string
}

function emptyDraft(): Draft {
  return {
    slug: '',
    name: '',
    endpoint: '',
    authStyle: 'none',
    authHeaderName: '',
    authSource: 'none',
    authEnv: '',
    authSecret: '',
    clearCredential: false,
    enabled: false,
    originsText: '',
  }
}

function draftOf(c: MCPConnection): Draft {
  return {
    slug: c.slug,
    name: c.name,
    endpoint: c.endpoint,
    authStyle: c.authStyle,
    authHeaderName: c.authHeaderName,
    authSource: c.authSource,
    authEnv: '',
    authSecret: '',
    clearCredential: false,
    enabled: c.enabled,
    originsText: originsToText(c.allowedOrigins ?? []),
  }
}

function originsToText(origins: MCPAllowedOrigin[]): string {
  return origins
    .map((o) => {
      const scheme = o.scheme || 'http'
      const port = o.port ? `:${o.port}` : ''
      return `${scheme}://${o.host}${port} ${(o.cidrs ?? []).join(',')}`
    })
    .join('\n')
}

/**
 * 解析 origin 文本。
 *
 * 返回 `null` 表示有无法解析的行——调用方应阻止保存并把具体行回显给用户，
 * 而不是「跳过坏行」静默保存一个残缺配置。
 */
function parseOriginsText(text: string): { origins: MCPAllowedOrigin[]; badLines: string[] } {
  const origins: MCPAllowedOrigin[] = []
  const badLines: string[] = []
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (line === '') continue
    const parts = line.split(/\s+/)
    if (parts.length < 2) {
      badLines.push(line)
      continue
    }
    const urlPart = parts[0]!
    const cidrs = parts.slice(1).join(',').split(',').map((s) => s.trim()).filter(Boolean)
    let scheme = 'http'
    let rest = urlPart
    const schemeIdx = urlPart.indexOf('://')
    if (schemeIdx > 0) {
      scheme = urlPart.slice(0, schemeIdx).toLowerCase()
      rest = urlPart.slice(schemeIdx + 3)
    }
    const portIdx = rest.lastIndexOf(':')
    let host = rest
    let port = 0
    if (portIdx > 0) {
      const p = Number(rest.slice(portIdx + 1))
      if (Number.isFinite(p)) {
        port = p
        host = rest.slice(0, portIdx)
      }
    }
    if (host === '' || cidrs.length === 0) {
      badLines.push(line)
      continue
    }
    origins.push({ scheme, host, port, cidrs })
  }
  return { origins, badLines }
}

function draftToPayload(d: Draft): { payload: MCPConnectionPayload | null; badLines: string[] } {
  const { origins, badLines } = parseOriginsText(d.originsText)
  if (badLines.length > 0) {
    return { payload: null, badLines }
  }
  return {
    payload: {
      slug: d.slug.trim(),
      name: d.name.trim(),
      endpoint: d.endpoint.trim(),
      authStyle: d.authStyle,
      authHeaderName: d.authHeaderName.trim(),
      authSource: d.authSource,
      authEnv: d.authEnv.trim(),
      authSecret: d.authSecret,
      clearCredential: d.clearCredential,
      allowedOrigins: origins,
      enabled: d.enabled,
    },
    badLines: [],
  }
}

/** 把后端错误码翻成人话。 */
function mcpErrorText(t: (k: string, fb: string) => string, code: string | undefined, fallback: string): string {
  switch (code) {
    case 'MCP_SELF_LOOP':
      return t('assistant.mcpErrSelfLoop', '端点指向本平台自身，已拒绝（否则模型可借外部工具回打自身 API）。')
    case 'MCP_TOOLS_LIMIT':
      return t('assistant.mcpErrToolsLimit', '上游工具数超过 200，发现已整次失败（未落库）。请先在上游收窄可见工具范围。')
    case 'MCP_TOO_LARGE':
      return t('assistant.mcpErrTooLarge', '上游响应超过 1 MiB，已拒绝（未当成功处理）。')
    case 'MCP_UNREACHABLE':
      return t('assistant.mcpErrUnreachable', '上游不可达。请检查端点与网络，必要时在出站准入里放行该地址段。')
    case 'MCP_PROTOCOL_ERROR':
      return t('assistant.mcpErrProtocol', '上游响应不符合 JSON-RPC 协议。')
    case 'EGRESS_DENIED':
      return t('assistant.hintEgressDenied', '被出站准入拒绝：目标地址属于内网或被禁止的范围；如需放行，请到「出站准入」区块添加网段。')
    default:
      return fallback
  }
}

export function MCPConnectionsPanel() {
  const { t } = useTranslation()
  const { data: connections = [], isLoading } = useMCPConnections()
  const create = useCreateMCPConnection()
  const update = useUpdateMCPConnection()
  const remove = useDeleteMCPConnection()
  const probe = useProbeMCPConnection()
  const discover = useDiscoverMCPTools()

  const [draft, setDraft] = useState<Draft | null>(null)
  const [editing, setEditing] = useState<MCPConnection | null>(null)
  const [expanded, setExpanded] = useState<number | null>(null)

  const openCreate = () => {
    setEditing(null)
    setDraft(emptyDraft())
  }
  const openEdit = (c: MCPConnection) => {
    setEditing(c)
    setDraft(draftOf(c))
  }

  const submit = async () => {
    if (!draft) return
    const { payload, badLines } = draftToPayload(draft)
    if (!payload) {
      toast.error(
        t('assistant.mcpBadOrigins', '这些放行行无法解析（需要形如 http://host:port 10.0.0.5/32）：{{lines}}', {
          lines: badLines.join(' / '),
        }),
      )
      return
    }
    try {
      if (editing) {
        await update.mutateAsync({ id: editing.id, payload: { ...payload, version: editing.version } })
      } else {
        await create.mutateAsync(payload)
      }
      setDraft(null)
      setEditing(null)
    } catch (err) {
      const body = (err as { response?: { data?: { error?: string; message?: string } } })?.response?.data
      toast.error(mcpErrorText(t, body?.error, body?.message ?? String(err)))
    }
  }

  return (
    <Panel
      title={t('assistant.mcpTitle', '外部 MCP 连接')}
      actions={
        <Button variant="outline" size="sm" onClick={openCreate}>
          <Plus className="mr-1 size-3.5" />
          {t('assistant.mcpAdd', '新建连接')}
        </Button>
      }
    >
      <div className="space-y-3">
        <p className="text-xs text-muted-foreground">
          {t(
            'assistant.mcpSubtitle',
            '接入外部 MCP 服务，把第三方工具纳入同一套工具目录与确认机制。仅支持 Streamable HTTP（POST + JSON 或请求范围 SSE）。',
          )}
        </p>
        {/* 这条提示是 spec §9.1 的冻结项，不能被当成「暂时限制」淡化掉。 */}
        <Alert variant="warning">
          <AlertDescription className="text-xs">
            {t(
              'assistant.mcpAdminOnly',
              'V1 里外部连接的工具**仅平台管理员可用**。判据不是「有没有凭据」——无凭据但可访问的开放连接，任何能到达它的请求都获得其全部权限，比带凭据的更危险。',
            )}
          </AlertDescription>
        </Alert>

        {isLoading ? (
          <p className="text-xs text-muted-foreground">{t('common.loading')}</p>
        ) : connections.length === 0 ? (
          <p className="text-xs text-muted-foreground">
            {t('assistant.mcpEmpty', '还没有外部连接。')}
          </p>
        ) : (
          <div className="space-y-2">
            {connections.map((c) => (
              <div key={c.id} className="rounded border border-border/60">
                <div className="flex flex-wrap items-center gap-2 px-2 py-1.5 text-xs">
                  <code className="rounded bg-muted px-1">{c.slug}</code>
                  <span className="font-medium">{c.name}</span>
                  <Badge variant={c.enabled ? 'default' : 'outline'}>
                    {c.enabled ? t('assistant.mcpEnabled', '已启用') : t('assistant.mcpDisabled', '已停用')}
                  </Badge>
                  <span className="text-muted-foreground">
                    {t('assistant.mcpToolCount', '{{n}} 个工具', { n: c.toolCount })}
                  </span>
                  {/* 发现失败时必须标注「目录可能陈旧」，否则管理员以为已同步。 */}
                  {c.discoveryStale ? (
                    <Badge variant="destructive">
                      {t('assistant.mcpStale', '上次探测失败，目录可能陈旧')}
                    </Badge>
                  ) : null}
                  <span className="min-w-0 flex-1 truncate text-muted-foreground" title={c.endpoint}>
                    {c.endpoint}
                  </span>
                  <Button variant="ghost" size="sm" className="h-6 text-[11px]" onClick={() => openEdit(c)}>
                    {t('common.edit', '编辑')}
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-6 text-[11px]"
                    disabled={probe.isPending}
                    onClick={async () => {
                      try {
                        const r = await probe.mutateAsync(c.id)
                        toast.success(t('assistant.mcpProbeOk', '连通正常（{{ms}} ms）', { ms: r.latencyMs }))
                      } catch (err) {
                        const body = (err as { response?: { data?: { error?: string; message?: string } } })?.response?.data
                        toast.error(mcpErrorText(t, body?.error, body?.message ?? String(err)))
                      }
                    }}
                  >
                    {t('assistant.mcpProbe', '探测')}
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-6 text-[11px]"
                    disabled={discover.isPending}
                    onClick={async () => {
                      try {
                        const items = await discover.mutateAsync(c.id)
                        toast.success(t('assistant.mcpDiscoverOk', '发现 {{n}} 个工具', { n: items.length }))
                        setExpanded(c.id)
                      } catch (err) {
                        const body = (err as { response?: { data?: { error?: string; message?: string } } })?.response?.data
                        toast.error(mcpErrorText(t, body?.error, body?.message ?? String(err)))
                      }
                    }}
                  >
                    <RefreshCw className="mr-1 size-3" />
                    {t('assistant.mcpDiscover', '发现工具')}
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-6 text-[11px]"
                    onClick={() => setExpanded(expanded === c.id ? null : c.id)}
                  >
                    {expanded === c.id ? t('assistant.mcpHideTools', '收起工具') : t('assistant.mcpShowTools', '查看工具')}
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-6 text-[11px] text-destructive"
                    onClick={async () => {
                      try {
                        await remove.mutateAsync(c.id)
                      } catch (err) {
                        toast.error(String(err))
                      }
                    }}
                  >
                    <Trash2 className="size-3" />
                  </Button>
                </div>
                {expanded === c.id ? <ToolTable connectionId={c.id} /> : null}
              </div>
            ))}
          </div>
        )}

        {draft ? (
          <div className="space-y-2 rounded border border-border/60 p-2">
            <div className="grid grid-cols-2 gap-2 text-xs">
              <label className="space-y-1">
                <span className="text-muted-foreground">{t('assistant.mcpSlug', 'slug（工具命名空间）')}</span>
                <input
                  className="w-full rounded border border-border bg-background px-2 py-1"
                  value={draft.slug}
                  placeholder="my_mcp"
                  onChange={(e) => setDraft({ ...draft, slug: e.target.value })}
                />
              </label>
              <label className="space-y-1">
                <span className="text-muted-foreground">{t('assistant.mcpName', '显示名')}</span>
                <input
                  className="w-full rounded border border-border bg-background px-2 py-1"
                  value={draft.name}
                  onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                />
              </label>
              <label className="col-span-2 space-y-1">
                <span className="text-muted-foreground">{t('assistant.mcpEndpoint', '端点（origin + path）')}</span>
                <input
                  className="w-full rounded border border-border bg-background px-2 py-1"
                  value={draft.endpoint}
                  placeholder="https://mcp.example.com/mcp"
                  onChange={(e) => setDraft({ ...draft, endpoint: e.target.value })}
                />
              </label>
              <label className="space-y-1">
                <span className="text-muted-foreground">{t('assistant.mcpAuthStyle', '凭据注入方式')}</span>
                <NativeSelect
                  className="w-full"
                  value={draft.authStyle}
                  onChange={(e) => setDraft({ ...draft, authStyle: e.target.value })}
                >
                  <option value="none">none</option>
                  <option value="bearer">bearer</option>
                  <option value="header">header</option>
                </NativeSelect>
              </label>
              <label className="space-y-1">
                <span className="text-muted-foreground">{t('assistant.mcpAuthSource', '凭据来源')}</span>
                <NativeSelect
                  className="w-full"
                  value={draft.authSource}
                  onChange={(e) => setDraft({ ...draft, authSource: e.target.value })}
                >
                  <option value="none">none</option>
                  <option value="env">env</option>
                  <option value="stored">stored</option>
                </NativeSelect>
              </label>
              {draft.authStyle === 'header' ? (
                <label className="space-y-1">
                  <span className="text-muted-foreground">{t('assistant.mcpAuthHeaderName', '头名')}</span>
                  <input
                    className="w-full rounded border border-border bg-background px-2 py-1"
                    value={draft.authHeaderName}
                    placeholder="Authorization"
                    onChange={(e) => setDraft({ ...draft, authHeaderName: e.target.value })}
                  />
                </label>
              ) : null}
              {draft.authSource === 'env' ? (
                <label className="space-y-1">
                  <span className="text-muted-foreground">{t('assistant.mcpAuthEnv', '环境变量名')}</span>
                  <input
                    className="w-full rounded border border-border bg-background px-2 py-1"
                    value={draft.authEnv}
                    onChange={(e) => setDraft({ ...draft, authEnv: e.target.value })}
                  />
                </label>
              ) : null}
              {draft.authSource === 'stored' ? (
                <label className="col-span-2 space-y-1">
                  <span className="text-muted-foreground">
                    {t('assistant.mcpAuthSecret', '凭据（留空表示保留原值）')}
                  </span>
                  <input
                    type="password"
                    className="w-full rounded border border-border bg-background px-2 py-1"
                    value={draft.authSecret}
                    onChange={(e) => setDraft({ ...draft, authSecret: e.target.value })}
                  />
                </label>
              ) : null}
              <label className="col-span-2 space-y-1">
                <span className="text-muted-foreground">
                  {t('assistant.mcpOrigins', '放行规则（每行：http://host:port CIDR，可多条 CIDR 逗号分隔）')}
                </span>
                <Textarea
                  className="h-16 font-mono text-[11px]"
                  value={draft.originsText}
                  placeholder={'http://mcp.internal:8080 10.0.0.5/32'}
                  onChange={(e) => setDraft({ ...draft, originsText: e.target.value })}
                />
                <span className="text-[11px] text-muted-foreground">
                  {t(
                    'assistant.mcpOriginsHint',
                    '留空 = 只允许公网目标。自托管服务需在这里精确放行：只放这一个 origin，且只放它所在的网段。',
                  )}
                </span>
              </label>
              <label className="col-span-2 flex items-center gap-2">
                <input
                  type="checkbox"
                  checked={draft.enabled}
                  onChange={(e) => setDraft({ ...draft, enabled: e.target.checked })}
                />
                <span>{t('assistant.mcpEnableConnection', '启用该连接')}</span>
              </label>
            </div>
            <div className="flex items-center gap-2">
              <Button size="sm" disabled={create.isPending || update.isPending} onClick={() => void submit()}>
                {t('common.save', '保存')}
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => {
                  setDraft(null)
                  setEditing(null)
                }}
              >
                {t('common.cancel', '取消')}
              </Button>
            </div>
          </div>
        ) : null}
      </div>
    </Panel>
  )
}

/** 工具表：逐条启停 + 风险分类。 */
function ToolTable({ connectionId }: { connectionId: number }) {
  const { t } = useTranslation()
  const { data: tools = [], isLoading } = useMCPTools(connectionId)
  const updateTool = useUpdateMCPTool()

  if (isLoading) {
    return <p className="px-2 py-1 text-xs text-muted-foreground">{t('common.loading')}</p>
  }
  if (tools.length === 0) {
    return (
      <p className="px-2 py-1 text-xs text-muted-foreground">
        {t('assistant.mcpNoTools', '还没有发现工具。点「发现工具」从上游拉取。')}
      </p>
    )
  }

  return (
    <div className="border-t border-border/60 px-2 py-1.5">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('assistant.mcpColTool', '工具')}</TableHead>
            <TableHead>{t('assistant.mcpColRisk', '风险分类')}</TableHead>
            <TableHead>{t('assistant.mcpColEnabled', '可执行')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {tools.map((tool) => (
            <TableRow key={tool.id}>
              <TableCell className="max-w-[18rem]">
                <code className="text-[11px]">{tool.namespacedName}</code>
                {/* 上游描述是不可信数据：原样展示但明确标注来源。 */}
                <p className="truncate text-[11px] text-muted-foreground" title={tool.description}>
                  {tool.description}
                </p>
              </TableCell>
              <TableCell>
                <NativeSelect
                  size="sm"
                  className="w-24 text-[11px]"
                  value={tool.risk}
                  onChange={(e) =>
                    updateTool.mutate({ connectionId, toolId: tool.id, risk: e.target.value })
                  }
                >
                  <option value="read">read</option>
                  <option value="write">write</option>
                  <option value="unknown">unknown</option>
                </NativeSelect>
              </TableCell>
              <TableCell>
                <input
                  type="checkbox"
                  checked={tool.enabled}
                  aria-label={t('assistant.mcpToolEnabled', '工具可执行')}
                  onChange={(e) =>
                    updateTool.mutate({ connectionId, toolId: tool.id, enabled: e.target.checked })
                  }
                />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      <p className="mt-1 text-[11px] text-muted-foreground">
        {t(
          'assistant.mcpRiskHint',
          '风险分类由你确认，不由上游 annotations 推导。只有 read 会自动执行；write 与 unknown 都需要用户逐次确认。',
        )}
      </p>
    </div>
  )
}
