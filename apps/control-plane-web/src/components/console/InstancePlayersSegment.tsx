import { useId, useMemo, useState, type FormEvent, type ReactNode } from 'react'
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
 *
 * 布局为分栏（FR-423）：左 38% 放两块窄内容（在线玩家 + 白名单），右 62% 放宽表（封禁记录）。
 * 分栏动因是量测出来的——三块原先纵向堆叠，宽度各行其是（1700 / 1700 / 460px），
 * 在线列表的名字与操作按钮被顶到 1920 视口的两端、视线要横跨 1650px；同时内容底边止于
 * y=880，底部白白空掉 150px。按「内容天然宽度需求」分栏后，窄内容不再被拉宽，
 * 宽表拿到它需要的横向空间，垂直方向也由封禁记录吃满。
 *
 * 三块卡一律「内容驱动高度 + 栏高封顶」：数据多时撑到上限后卡内滚动，少时贴合内容，
 * 空白一致落在栏位空隙而非卡片内部（spec §3.2）。封禁记录不写死 `flex-1`——
 * 否则 2 条记录也会撑满 800+px，正是本批要消灭的「矮内容被拉平成死区」。
 */
export default function InstancePlayersSegment({ instanceId }: InstancePlayersSegmentProps) {
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2 xl:flex-row">
      {/* 左栏 38%：两卡均内容驱动高度。xl 以下退成整页纵向堆叠，权重只在 xl 生效——
          窄屏若仍按权重分高度，两栏会互相挤压，页面该滚的时候滚不动。 */}
      <div className="flex flex-none flex-col gap-2 xl:min-h-0 xl:flex-1">
        <OnlinePlayersPanel instanceId={instanceId} />
        <WhitelistPanel instanceId={instanceId} />
      </div>

      {/* 右栏 62%：封禁记录是 7 列宽表，横向要空间、纵向要行数。 */}
      <div className="flex flex-none flex-col xl:min-h-0 xl:flex-[1.6]">
        <BansPanel />
      </div>
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

/**
 * 行卡片列表容器（FR-423）。
 *
 * 700px 上限是验收项（「最长行宽 ≤ 700px」）而非审美偏好：左栏按比例分宽，
 * 在 2560 及更宽的屏上 38% 会重新超过 700px，视线距离又会涨回去，故显式兜住。
 */
function PlayerRowList({ label, children }: { label: string; children: ReactNode }) {
  return (
    <ul aria-label={label} className="max-w-[700px] overflow-hidden rounded-lg border">
      {children}
    </ul>
  )
}

/**
 * 玩家行卡片（FR-423）：头像块 + 名字 + 操作按钮收在同一行内。
 *
 * 取代原先的 2 列表格——表格把名字压到最左、操作按钮压到最右，中间的空列越宽视线越长。
 * 行卡片让「谁」和「怎么处理」贴在一处，配合 `PlayerRowList` 的宽度上限收口。
 */
function PlayerRow({ name, actions }: { name: string; actions: ReactNode }) {
  return (
    <li className="flex items-center gap-2.5 border-b px-2.5 py-1.5 last:border-b-0 hover:bg-muted/60">
      {/* 头像统一取主色而不按名字哈希配色：status-* 那套色带语义（红=危险），
          拿来给玩家头像上色会读成「这人有问题」。 */}
      <span
        aria-hidden
        className="grid size-7 shrink-0 place-items-center rounded-md bg-accent text-[11px] font-semibold text-primary"
      >
        {name.slice(0, 1).toUpperCase()}
      </span>
      <span className="min-w-0 flex-1 truncate text-[13px] font-medium">{name}</span>
      <div className="flex shrink-0 items-center gap-1.5">{actions}</div>
    </li>
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
    // flex-none：玩家数少时不被拉高，空白落到栏位空隙（spec §3.2 内容驱动卡）。
    <Panel className="flex-none" icon={<Users className="size-3.5" />} title={t('players.tab_online')}>
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
        <PlayerRowList label={t('players.tab_online')}>
          {players.map((p) => (
            <PlayerRow
              key={p.name}
              name={p.name}
              actions={
                <>
                  <Button size="xs" variant="destructive" disabled={pending} onClick={() => setConfirm({ kind: 'kick', player: p })}>
                    {t('players.kick')}
                  </Button>
                  <Button size="xs" variant="destructive" disabled={pending} onClick={() => setConfirm({ kind: 'ban', player: p })}>
                    {t('players.ban')}
                  </Button>
                </>
              }
            />
          ))}
        </PlayerRowList>
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
 *
 * 列宽显式约束（FR-424）：`table-fixed` 把宽度从「内容说了算」改成「布局说了算」——
 * 一条长封禁原因原先能把整表推宽、把时间/操作列挤出视野。现在只有原因列吃剩余宽度并省略号截断，
 * 全文走 title 悬停；其余六列按内容量给定宽，时间列右对齐等宽字体便于纵向比对。
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
    // 内容驱动 + 栏高封顶（FR-423）：记录多时撑到栏高上限后卡内滚动，
    // 少时贴合内容——若写死 flex-1，2 条封禁也会撑满 800+px，正是本批要消灭的
    // 「矮内容被拉平成死区」。空白落到栏位空隙，与左栏两卡策略一致。
    <Panel
      className="max-h-full min-h-0 flex-none"
      bodyClassName="flex min-h-0 flex-col overflow-hidden p-0"
      icon={<ShieldBan className="size-3.5" />}
      title={t('players.tab_bans')}
    >
      {isLoading ? (
        <div className="p-3">
          <RowsSkeleton />
        </div>
      ) : !bans || bans.length === 0 ? (
        <EmptyState icon={<ShieldBan />} title={t('players.noBans')} />
      ) : (
        <div className="min-h-0 overflow-auto">
          <Table className="table-fixed">
            <TableHeader>
              <TableRow>
                <TableHead className="w-[8.5rem]">{t('players.playerName')}</TableHead>
                {/* 无宽度 = 吃剩余空间：原因是唯一长度不可预期的列，让它当伸缩列。 */}
                <TableHead>{t('players.reason')}</TableHead>
                <TableHead className="w-[5rem]">{t('players.scope')}</TableHead>
                <TableHead className="w-[7rem]">{t('players.operator')}</TableHead>
                <TableHead className="w-[10.5rem] text-right">{t('players.banTime')}</TableHead>
                <TableHead className="w-[5.5rem]">{t('common.status')}</TableHead>
                <TableHead className="w-[4.5rem] text-right">{t('common.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {bans.map((b) => (
                <TableRow key={b.id}>
                  <TableCell className="truncate font-medium">{b.playerName}</TableCell>
                  {/* 截断而非换行：换行会让行高随原因长度跳动，破坏纵向扫读的节奏。 */}
                  <TableCell
                    className="overflow-hidden text-ellipsis whitespace-nowrap text-muted-foreground"
                    title={b.reason || undefined}
                  >
                    {b.reason || '--'}
                  </TableCell>
                  <TableCell>{scopeBadge(b)}</TableCell>
                  <TableCell className="truncate text-muted-foreground">{b.operator?.username || '--'}</TableCell>
                  <TableCell className="text-right font-mono text-xs tabular-nums text-muted-foreground">
                    {new Date(b.createdAt).toLocaleString()}
                  </TableCell>
                  <TableCell>
                    <span className={`inline-flex items-center gap-1.5 text-xs ${b.active ? 'text-status-danger' : 'text-muted-foreground'}`}>
                      <span className={`h-2 w-2 shrink-0 rounded-full ${b.active ? 'bg-status-danger' : 'bg-muted-foreground'}`} />
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
        </div>
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
    // 同为内容驱动卡：白名单通常十几人，撑高只会在卡内留死区。
    <Panel className="flex-none" icon={<ListChecks className="size-3.5" />} title={t('players.tab_whitelist')}>
      {/* 单行输入添加：行内微交互，不属「弹出表单」模态纪律范畴。
          宽度与下方行卡片列表取同一上限——原先表单 max-w-md、列表 max-w-md、
          而在线列表却铺满整宽，同一页三种宽度策略本身就是病症之一。 */}
      <form onSubmit={add} className="mb-3 flex max-w-[700px] gap-2">
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
        <PlayerRowList label={t('players.tab_whitelist')}>
          {wl.players.map((p) => (
            <PlayerRow
              key={p}
              name={p}
              actions={
                <Button
                  variant="link"
                  size="xs"
                  className="h-auto p-0 text-status-danger hover:text-status-danger"
                  onClick={() => remove(p)}
                >
                  {t('common.delete')}
                </Button>
              }
            />
          ))}
        </PlayerRowList>
      )}
    </Panel>
  )
}
