import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { toast } from 'sonner'
import { Copy, KeyRound, Plus } from 'lucide-react'
import { useAuthStore } from '@/stores/auth'
import {
  useAgentTokens,
  useIssueAgentToken,
  useRevokeAgentToken,
  agentTokenStatus,
  DEFAULT_WRITE_ALLOWLIST,
  WRITE_ALLOWLIST_OPTIONS,
  type AgentTokenInfo,
} from '@/api/agentTokens'
import { mcpBaseUrl } from '@/api/agentObservability'
import { useInstances } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { copyToClipboard } from '@/lib/clipboard'
import DangerConfirm from '@/components/DangerConfirm'
import { Panel } from '@jianmanager/ui/components/panel'
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

/** 平台管理员角色值（与后端 model.RolePlatformAdmin 对齐）。 */
const ROLE_PLATFORM_ADMIN = 10

type ErrResp = { response?: { data?: { message?: string }; status?: number } }
const errMsg = (e: unknown, fallback: string) => (e as ErrResp)?.response?.data?.message || fallback

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

/** 写白名单展示文案。 */
function formatWriteAllowlist(list: string[], t: (k: string) => string): string {
  if (!list.length) return t('agentTokens.write.none')
  return list
    .map((v) => {
      const opt = WRITE_ALLOWLIST_OPTIONS.find((o) => o.value === v)
      return opt ? t(opt.labelKey) : v
    })
    .join('、')
}

function statusLevel(status: ReturnType<typeof agentTokenStatus>): 'success' | 'warning' | 'danger' {
  if (status === 'active') return 'success'
  if (status === 'expired') return 'warning'
  return 'danger'
}

/** 复制按钮：写剪贴板 + toast。 */
function CopyButton({ text, label }: { text: string; label: string }) {
  const { t } = useTranslation()
  const copy = async () => {
    const ok = await copyToClipboard(text)
    if (ok) toast.success(t('agentTokens.copied'))
    else toast.error(t('agentTokens.copyFailed'))
  }
  return (
    <Button type="button" variant="outline" size="sm" onClick={copy} className="shrink-0">
      <Copy className="size-4" /> {label}
    </Button>
  )
}

/**
 * Agent Token 管理页（FR-387，消费 FR-384 API）。
 * 平台管理员可列表 / 新建 / 吊销；创建成功一次性展示明文 + 复制 env/命令片段。
 * 入口仅管理员可见（侧栏）+ 本页角色兜底 + 后端 RBAC，三重把关。
 */
export default function AgentTokensPage() {
  const { t } = useTranslation()
  const role = useAuthStore((s) => s.role)
  const isPlatformAdmin = role === ROLE_PLATFORM_ADMIN

  const listQ = useAgentTokens({ enabled: isPlatformAdmin })
  const issue = useIssueAgentToken()
  const revoke = useRevokeAgentToken()

  const [showCreate, setShowCreate] = useState(false)
  const [issuedPlain, setIssuedPlain] = useState<{ name: string; plaintext: string } | null>(null)
  const [revokeTarget, setRevokeTarget] = useState<AgentTokenInfo | null>(null)

  if (!isPlatformAdmin) {
    return (
      <div className="grid h-full place-items-center text-sm text-muted-foreground" data-page="agent-tokens">
        {t('agentTokens.forbidden')}
      </div>
    )
  }

  const tokens = listQ.data ?? []

  const onRevoke = () => {
    if (!revokeTarget) return
    revoke.mutate(revokeTarget.id, {
      onSuccess: () => {
        toast.success(t('agentTokens.revokeSuccess'))
        setRevokeTarget(null)
      },
      onError: (e) => toast.error(errMsg(e, t('agentTokens.revokeFailed'))),
    })
  }

  return (
    <div data-page="agent-tokens" className="jm-page-stack space-y-4">
      <div className="jm-page-header">
        <div>
          <h1 className="jm-page-title">{t('agentTokens.title')}</h1>
          <p className="jm-page-subtitle">{t('agentTokens.subtitle')}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" asChild>
            <Link to="/mcp-sessions">{t('agentTokens.openSessions')}</Link>
          </Button>
          <Button variant="outline" size="sm" asChild>
            <Link to="/agent-call-logs">{t('agentTokens.openLogs')}</Link>
          </Button>
          <Button onClick={() => setShowCreate(true)}>
            <Plus className="size-4" /> {t('agentTokens.create')}
          </Button>
        </div>
      </div>

      {listQ.isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : listQ.isError ? (
        <Panel>
          <p className="py-6 text-center text-sm text-muted-foreground">
            {errMsg(listQ.error, t('agentTokens.loadFailed'))}
          </p>
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
                const status = agentTokenStatus(tok)
                const scope = formatScopeSummary(tok.scopedInstanceIds, tok.scopedNodeIds, {
                  instances: t('agentTokens.scope.instances'),
                  nodes: t('agentTokens.scope.nodes'),
                  none: t('agentTokens.scope.none'),
                })
                return (
                  <TableRow key={tok.id}>
                    <TableCell className="font-medium">{tok.name}</TableCell>
                    <TableCell>
                      <code className="font-mono text-xs">{tok.tokenPrefix}…</code>
                    </TableCell>
                    <TableCell className="max-w-[14rem] truncate text-xs text-muted-foreground" title={scope}>
                      {scope}
                    </TableCell>
                    <TableCell className="max-w-[12rem] truncate text-xs" title={formatWriteAllowlist(tok.writeAllowlist, t)}>
                      {formatWriteAllowlist(tok.writeAllowlist, t)}
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
                        level={statusLevel(status)}
                        label={t(`agentTokens.status.${status}`)}
                      />
                    </TableCell>
                    <TableCell className="text-right">
                      {status !== 'revoked' && (
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
        pending={issue.isPending}
        onClose={() => setShowCreate(false)}
        onSubmit={(body) => {
          issue.mutate(body, {
            onSuccess: (res) => {
              setShowCreate(false)
              setIssuedPlain({ name: res.token.name, plaintext: res.plaintext })
              toast.success(t('agentTokens.createSuccess'))
            },
            onError: (e) => {
              const status = (e as ErrResp)?.response?.status
              if (status === 403) toast.error(t('agentTokens.forbidden'))
              else toast.error(errMsg(e, t('agentTokens.createFailed')))
            },
          })
        }}
      />

      <PlaintextRevealDialog
        open={issuedPlain != null}
        name={issuedPlain?.name ?? ''}
        plaintext={issuedPlain?.plaintext ?? ''}
        onClose={() => setIssuedPlain(null)}
      />

      <DangerConfirm
        open={revokeTarget != null}
        title={t('agentTokens.revokeTitle')}
        description={t('agentTokens.revokeDesc', { name: revokeTarget?.name ?? '' })}
        confirmLabel={t('agentTokens.revoke')}
        confirmText={revokeTarget?.name}
        scope="platform"
        pending={revoke.isPending}
        onConfirm={onRevoke}
        onCancel={() => setRevokeTarget(null)}
      />
    </div>
  )
}

/** 新建 Token 对话框。 */
function CreateAgentTokenDialog({
  open,
  pending,
  onClose,
  onSubmit,
}: {
  open: boolean
  pending: boolean
  onClose: () => void
  onSubmit: (body: {
    name: string
    scopedInstanceIds: number[]
    scopedNodeIds: number[]
    writeAllowlist: string[]
    ttlDays: number
  }) => void
}) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  const [selectedInst, setSelectedInst] = useState<number[]>([])
  const [selectedNode, setSelectedNode] = useState<number[]>([])
  const [instIdsText, setInstIdsText] = useState('')
  const [nodeIdsText, setNodeIdsText] = useState('')
  const [writeAllow, setWriteAllow] = useState<string[]>([...DEFAULT_WRITE_ALLOWLIST])
  const [ttlDays, setTtlDays] = useState('90')

  const { data: instances } = useInstances()
  const { data: nodes } = useNodes({ enabled: open })

  // 每次打开重置表单（父组件改 open 时 Radix 不一定触发 onOpenChange）。
  useEffect(() => {
    if (!open) return
    setName('')
    setSelectedInst([])
    setSelectedNode([])
    setInstIdsText('')
    setNodeIdsText('')
    setWriteAllow([...DEFAULT_WRITE_ALLOWLIST])
    setTtlDays('90')
  }, [open])

  const handleOpenChange = (next: boolean) => {
    if (!next) onClose()
  }

  const toggleId = (list: number[], id: number, set: (v: number[]) => void) => {
    set(list.includes(id) ? list.filter((x) => x !== id) : [...list, id])
  }

  const toggleWrite = (value: string) => {
    setWriteAllow((prev) => (prev.includes(value) ? prev.filter((v) => v !== value) : [...prev, value]))
  }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    const trimmed = name.trim()
    if (!trimmed) {
      toast.error(t('agentTokens.validation.nameRequired'))
      return
    }
    const ttl = Number(ttlDays)
    if (!Number.isFinite(ttl) || ttl <= 0 || !Number.isInteger(ttl)) {
      toast.error(t('agentTokens.validation.ttlInvalid'))
      return
    }
    if (ttl > 365) {
      toast.error(t('agentTokens.validation.ttlMax'))
      return
    }
    onSubmit({
      name: trimmed,
      scopedInstanceIds: mergeIds(selectedInst, instIdsText),
      scopedNodeIds: mergeIds(selectedNode, nodeIdsText),
      writeAllowlist: writeAllow,
      ttlDays: ttl,
    })
  }

  const instList = instances ?? []
  const nodeList = nodes ?? []

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
              <div className="max-h-36 space-y-1 overflow-y-auto rounded-md border p-2">
                {instList.length === 0 ? (
                  <p className="text-xs text-muted-foreground">{t('agentTokens.field.noInstances')}</p>
                ) : (
                  instList.map((inst) => (
                    <label key={inst.id} className="flex cursor-pointer items-center gap-2 text-sm">
                      <Checkbox
                        checked={selectedInst.includes(inst.id)}
                        onCheckedChange={() => toggleId(selectedInst, inst.id, setSelectedInst)}
                      />
                      <span className="truncate">
                        #{inst.id} {inst.name}
                      </span>
                      <Badge variant="secondary" className="ml-auto text-[10px]">
                        {inst.status}
                      </Badge>
                    </label>
                  ))
                )}
              </div>
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
                        onCheckedChange={() => toggleId(selectedNode, node.id, setSelectedNode)}
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
              <Label>{t('agentTokens.field.writeAllowlist')}</Label>
              <p className="text-xs text-muted-foreground">{t('agentTokens.field.writeHint')}</p>
              <div className="space-y-1 rounded-md border p-2">
                {WRITE_ALLOWLIST_OPTIONS.map((opt) => (
                  <label key={opt.value} className="flex cursor-pointer items-center gap-2 text-sm">
                    <Checkbox
                      checked={writeAllow.includes(opt.value)}
                      onCheckedChange={() => toggleWrite(opt.value)}
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

/** 创建成功后一次性展示明文 + 复制 env / jm-agent 示例。 */
function PlaintextRevealDialog({
  open,
  name,
  plaintext,
  onClose,
}: {
  open: boolean
  name: string
  plaintext: string
  onClose: () => void
}) {
  const { t } = useTranslation()
  const envLine = useMemo(() => `JM_AGENT_TOKEN=${plaintext}`, [plaintext])
  const whoamiCmd = useMemo(
    () => `jm-agent --token ${plaintext} whoami`,
    [plaintext],
  )
  const mcpUrl = useMemo(() => mcpBaseUrl(), [])

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
              <CopyButton text={plaintext} label={t('agentTokens.copy')} />
            </div>
          </div>
          <div className="space-y-1">
            <div className="text-xs font-medium text-muted-foreground">{t('agentTokens.mcpUrl')}</div>
            <div className="flex items-start gap-2 rounded-md border bg-muted/50 p-2">
              <code className="flex-1 break-all font-mono text-xs leading-relaxed">{mcpUrl}</code>
              <CopyButton text={mcpUrl} label={t('agentTokens.copy')} />
            </div>
          </div>
          <div className="space-y-1">
            <div className="text-xs font-medium text-muted-foreground">{t('agentTokens.reveal.env')}</div>
            <div className="flex items-start gap-2 rounded-md border bg-muted/50 p-2">
              <code className="flex-1 break-all font-mono text-xs leading-relaxed">{envLine}</code>
              <CopyButton text={envLine} label={t('agentTokens.copy')} />
            </div>
          </div>
          <div className="space-y-1">
            <div className="text-xs font-medium text-muted-foreground">{t('agentTokens.reveal.cli')}</div>
            <div className="flex items-start gap-2 rounded-md border bg-muted/50 p-2">
              <code className="flex-1 break-all font-mono text-xs leading-relaxed">{whoamiCmd}</code>
              <CopyButton text={whoamiCmd} label={t('agentTokens.copy')} />
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
