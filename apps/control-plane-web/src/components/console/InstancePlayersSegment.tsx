import { useId, useMemo, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { ListChecks, ShieldBan, Users } from 'lucide-react'

import { Button } from '@jianmanager/ui/components/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { EmptyState } from '@jianmanager/ui/components/empty-state'
import { Input } from '@jianmanager/ui/components/input'
import { Label } from '@jianmanager/ui/components/label'
import { Panel } from '@jianmanager/ui/components/panel'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import DangerConfirm from '@/components/DangerConfirm'
import {
  useBanPlayer,
  useBans,
  useKickPlayer,
  useOnlinePlayers,
  useUnbanPlayer,
  useWhitelist,
  useWhitelistAction,
  type BanRecord,
  type OnlinePlayer,
  type PlayerActionResult,
} from '@/api/players'

type PlayerActionKind = 'kick' | 'ban'

/** 实例玩家分区 props（FR-339）。 */
interface InstancePlayersSegmentProps {
  /** 实例 DB ID：在线列表按其过滤、踢/封 scope 限定、白名单原生按实例。 */
  instanceId: number
}

/**
 * 实例控制台「玩家」分区（FR-339）：本实例作用域的玩家治理。
 * - 在线玩家：`GET /players` 为全后端聚合，前端按 instanceId 过滤（spec §6 拍板，不改后端）；
 * - 踢出/封禁：原因确认弹窗后 mutation 携带 `scope.instanceId` 限定单实例；
 * - 封禁列表：全量展示 + scope 徽章（network/global 封禁同样作用于本实例，不做假实例过滤）；
 * - 白名单：实例原生作用域的查改。
 * 独立页的子服筛选/批量勾选不进本分区（作用域恒为本实例）。
 */
export default function InstancePlayersSegment({ instanceId }: InstancePlayersSegmentProps) {
  return (
    <div className="space-y-3">
      <OnlinePlayersPanel instanceId={instanceId} />
      <BansPanel />
      <WhitelistPanel instanceId={instanceId} />
    </div>
  )
}

/** 表格行加载骨架（共享 Skeleton 原语拼装）。 */
function RowsSkeleton({ rows = 3 }: { rows?: number }) {
  return (
    <div className="space-y-2">
      {Array.from({ length: rows }, (_, i) => (
        <Skeleton key={i} className="h-8 w-full" />
      ))}
    </div>
  )
}

/** 在线玩家：按本实例过滤的列表 + 踢/封（原因确认，scope 限定本实例）。 */
function OnlinePlayersPanel({ instanceId }: { instanceId: number }) {
  const { t } = useTranslation()
  const reasonId = useId()
  const { data, isLoading } = useOnlinePlayers()
  const kick = useKickPlayer()
  const ban = useBanPlayer()
  const [confirm, setConfirm] = useState<{ kind: PlayerActionKind; player: OnlinePlayer } | null>(null)
  const [reason, setReason] = useState('')

  const players = useMemo(
    () => (data?.players ?? []).filter((p) => p.instanceId === instanceId),
    [data?.players, instanceId],
  )
  // 本实例探针不可达 → 降级横幅（复用 players.degraded 文案形态，FR-067）。
  const backend = (data?.backends ?? []).find((b) => b.instanceId === instanceId)
  const degraded = backend !== undefined && !backend.available
  const pending = kick.isPending || ban.isPending

  const closeConfirm = () => {
    setConfirm(null)
    setReason('')
  }

  const runAction = async () => {
    if (!confirm) return
    const mutation = confirm.kind === 'kick' ? kick : ban
    try {
      // scope.instanceId 限定单实例执行（越权由后端 CanAccessInstance 拒）。
      const res: PlayerActionResult = await mutation.mutateAsync({
        name: confirm.player.name,
        scope: { instanceId, reason: reason || undefined },
      })
      const message = t('players.actionResult', { succeeded: res.succeeded, failed: res.failed })
      if (res.failed > 0) toast.error(message)
      else toast.success(message)
    } catch {
      toast.error(t('common.error'))
    }
    closeConfirm()
  }

  return (
    <Panel icon={<Users className="size-3.5" />} title={t('players.tab_online')}>
      {degraded && (
        <div className="mb-3 rounded-md border border-status-warning/40 bg-status-warning/10 px-3 py-2 text-xs text-status-warning">
          {t('players.degraded', { names: backend?.instanceName ?? `#${instanceId}` })}
        </div>
      )}
      {isLoading ? (
        <RowsSkeleton />
      ) : players.length === 0 ? (
        <EmptyState icon={<Users />} title={t('players.noOnline')} />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('players.playerName')}</TableHead>
              <TableHead className="text-right">{t('common.actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {players.map((p) => (
              <TableRow key={p.name}>
                <TableCell className="font-medium">{p.name}</TableCell>
                <TableCell className="text-right">
                  <div className="flex justify-end gap-2">
                    <Button size="xs" variant="destructive" disabled={pending} onClick={() => setConfirm({ kind: 'kick', player: p })}>
                      {t('players.kick')}
                    </Button>
                    <Button size="xs" variant="destructive" disabled={pending} onClick={() => setConfirm({ kind: 'ban', player: p })}>
                      {t('players.ban')}
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      {/* 踢/封确认：需可填原因，复用独立页 Dialog+原因模式（属危险确认例外，非内联表单）。 */}
      <Dialog open={confirm !== null} onOpenChange={(open) => { if (!open) closeConfirm() }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{confirm?.kind === 'ban' ? t('players.banTitle') : t('players.kickTitle')}</DialogTitle>
            <DialogDescription>
              {confirm ? t('players.confirmTarget', { player: confirm.player.name, server: confirm.player.instanceName }) : ''}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <Label htmlFor={reasonId}>{t('players.reason')}</Label>
            <Input
              id={reasonId}
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder={t('players.reasonPlaceholder')}
              autoFocus
            />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={closeConfirm}>
              {t('common.cancel')}
            </Button>
            <Button variant="destructive" onClick={runAction} disabled={pending}>
              {confirm?.kind === 'ban' ? t('players.ban') : t('players.kick')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Panel>
  )
}

/**
 * 封禁列表：全量展示 + scope 徽章 + 解封。
 * 不按实例过滤：network/global 作用域的封禁同样影响本实例，隐藏会误导（spec §6 拍板）。
 */
function BansPanel() {
  const { t } = useTranslation()
  const { data: bans, isLoading } = useBans()
  const unban = useUnbanPlayer()
  const [pendingUnban, setPendingUnban] = useState<string | null>(null)

  const doUnban = () => {
    if (!pendingUnban) return
    unban.mutate(
      { name: pendingUnban },
      {
        onSuccess: () => {
          toast.success(t('players.unbanned', { player: pendingUnban }))
          setPendingUnban(null)
        },
        onError: () => toast.error(t('common.error')),
      },
    )
  }

  const scopeBadge = (b: BanRecord) => (
    <span className="inline-flex w-fit items-center rounded-full bg-muted px-2 py-0.5 text-xs text-muted-foreground">
      {t(`players.scope_${b.scope}`, { defaultValue: b.scope })}
    </span>
  )

  return (
    <Panel icon={<ShieldBan className="size-3.5" />} title={t('players.tab_bans')}>
      {isLoading ? (
        <RowsSkeleton />
      ) : !bans || bans.length === 0 ? (
        <EmptyState icon={<ShieldBan />} title={t('players.noBans')} />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('players.playerName')}</TableHead>
              <TableHead>{t('players.reason')}</TableHead>
              <TableHead>{t('players.scope')}</TableHead>
              <TableHead>{t('players.operator')}</TableHead>
              <TableHead>{t('players.banTime')}</TableHead>
              <TableHead>{t('common.status')}</TableHead>
              <TableHead className="text-right">{t('common.actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {bans.map((b) => (
              <TableRow key={b.id}>
                <TableCell className="font-medium">{b.playerName}</TableCell>
                <TableCell className="text-muted-foreground">{b.reason || '--'}</TableCell>
                <TableCell>{scopeBadge(b)}</TableCell>
                <TableCell className="text-muted-foreground">{b.operator?.username || '--'}</TableCell>
                <TableCell className="text-muted-foreground">{new Date(b.createdAt).toLocaleString()}</TableCell>
                <TableCell>
                  <span className={`inline-flex items-center gap-1.5 text-xs ${b.active ? 'text-status-danger' : 'text-muted-foreground'}`}>
                    <span className={`h-2 w-2 rounded-full ${b.active ? 'bg-status-danger' : 'bg-muted-foreground'}`} />
                    {b.active ? t('players.banActive') : t('players.banLifted')}
                  </span>
                </TableCell>
                <TableCell className="text-right">
                  {b.active && (
                    <Button variant="link" size="xs" className="h-auto p-0" onClick={() => setPendingUnban(b.playerName)}>
                      {t('players.unban')}
                    </Button>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      <DangerConfirm
        open={pendingUnban !== null}
        title={t('players.unbanTitle')}
        description={t('players.unbanConfirm', { player: pendingUnban || '' })}
        confirmLabel={t('players.unban')}
        scope="group"
        pending={unban.isPending}
        onConfirm={doUnban}
        onCancel={() => setPendingUnban(null)}
      />
    </Panel>
  )
}

/** 白名单：实例原生作用域的添加/删除 + 探针不可达与查询失败的降级重试。 */
function WhitelistPanel({ instanceId }: { instanceId: number }) {
  const { t } = useTranslation()
  const { data: wl, isLoading, isError, refetch } = useWhitelist(instanceId)
  const wlAction = useWhitelistAction(instanceId)
  const [name, setName] = useState('')

  const add = (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    wlAction.mutate(
      { action: 'add', player: name.trim() },
      {
        onSuccess: () => {
          toast.success(t('players.whitelistAdded', { player: name.trim() }))
          setName('')
        },
        onError: () => toast.error(t('common.error')),
      },
    )
  }

  const remove = (player: string) => {
    wlAction.mutate(
      { action: 'remove', player },
      {
        onSuccess: () => toast.success(t('players.whitelistRemoved', { player })),
        onError: () => toast.error(t('common.error')),
      },
    )
  }

  return (
    <Panel icon={<ListChecks className="size-3.5" />} title={t('players.tab_whitelist')}>
      {/* 单行输入添加：行内微交互，不属「弹出表单」模态纪律范畴。 */}
      <form onSubmit={add} className="mb-3 flex max-w-md gap-2">
        <Input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={t('players.whitelistAddPlaceholder')}
        />
        <Button type="submit" disabled={wlAction.isPending || !name.trim()}>
          {t('players.whitelistAdd')}
        </Button>
      </form>

      {isLoading ? (
        <RowsSkeleton />
      ) : isError ? (
        /* 查询失败不落空表：显式错误提示 + 重试（复用 WhitelistTab 模式）。 */
        <div className="flex items-center gap-3">
          <p className="text-sm text-destructive">{t('common.error')}</p>
          <Button type="button" variant="outline" size="sm" onClick={() => void refetch()}>
            {t('common.refresh')}
          </Button>
        </div>
      ) : wl && !wl.available ? (
        <p className="text-sm text-status-warning">{t('players.whitelistUnavailable')}</p>
      ) : !wl || wl.players.length === 0 ? (
        <EmptyState icon={<ListChecks />} title={t('players.whitelistEmpty')} />
      ) : (
        <div className="max-w-md rounded-lg border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('players.playerName')}</TableHead>
                <TableHead className="text-right">{t('common.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {wl.players.map((p) => (
                <TableRow key={p}>
                  <TableCell className="font-medium">{p}</TableCell>
                  <TableCell className="text-right">
                    <Button
                      variant="link"
                      size="xs"
                      className="h-auto p-0 text-status-danger hover:text-status-danger"
                      onClick={() => remove(p)}
                    >
                      {t('common.delete')}
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </Panel>
  )
}
