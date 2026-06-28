import { useMemo, useState, type FormEvent } from 'react'
import { useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  ArrowRight,
  Ban,
  Check,
  ChevronLeft,
  Copy,
  DownloadCloud,
  Eye,
  KeyRound,
  Pencil,
  Plus,
} from 'lucide-react'
import {
  useClientChannels,
  useClientChannel,
  useCreateClientChannel,
  useDeleteClientChannel,
  useCreateClientKey,
  useUpdateClientKey,
  useRevokeClientKey,
  useRevealClientKey,
  type ClientChannel,
  type ClientPullKey,
  type ClientKeyWithSecret,
} from '@/api/clientChannels'
import { copyToClipboard } from '@/lib/clipboard'
import {
  deriveReadiness,
  readinessCompletedCount,
  type ReadinessStep,
  type ReadinessStepId,
} from '@/lib/client-readiness'
import { cn } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@/components/ui/scrollable-dialog'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import DangerConfirm from '@/components/DangerConfirm'
import ClientVersionsPanel from '@/components/ClientVersionsPanel'
import ClientStatsPanel from '@/components/ClientStatsPanel'
import ClientIntegrationGuide from '@/components/ClientIntegrationGuide'
import ClientDistFlowGuide from '@/components/ClientDistFlowGuide'

type ErrResp = { response?: { data?: { message?: string } } }
const errMsg = (e: unknown, fallback: string) => (e as ErrResp)?.response?.data?.message || fallback

/** 工作台分段标识，与就绪度步骤 CTA 联动跳转。 */
type WorkbenchTab = 'keys' | 'versions' | 'stats' | 'guide'

/**
 * 客户端分发管理页（FR-086/187，见 ADR-022）。
 * 运营域入口（FR-187 由「系统·平台与维护」迁入，路由 /client-channels 不变）。
 * 频道（每服一个）+ 拉取密钥（落库只存哈希、明文一次性返回）；首次使用以空状态引导卡 +
 * 工作台就绪度步骤器降低门槛。仅平台管理员可用（后端 RBAC 强制）。
 * i18n（FR-016）+ 暗/亮色（FR-026，全程用主题 token）。
 */
export default function ClientChannelsPage() {
  const { t } = useTranslation()
  const { data: channels, isLoading } = useClientChannels()
  const [searchParams, setSearchParams] = useSearchParams()
  // 支持从独立发布页（FR-191）返回时按 `?channel=&tab=` 还原到对应频道工作台版本 tab。
  const [selected, setSelected] = useState<string | null>(() => searchParams.get('channel'))
  const [createOpen, setCreateOpen] = useState(false)

  /** 返回频道列表：清状态与 URL 参数（避免刷新后又自动展开工作台）。 */
  const backToList = () => {
    setSelected(null)
    if (searchParams.has('channel') || searchParams.has('tab')) {
      const next = new URLSearchParams(searchParams)
      next.delete('channel')
      next.delete('tab')
      setSearchParams(next, { replace: true })
    }
  }

  if (selected) {
    return (
      <ChannelWorkbench
        channelId={selected}
        initialTab={searchParams.get('tab') as WorkbenchTab | null}
        onBack={backToList}
      />
    )
  }

  const list = channels ?? []
  const isEmpty = list.length === 0 && !isLoading

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-3 flex-wrap">
        <div>
          <h1 className="text-2xl font-bold flex items-center gap-2">
            <DownloadCloud className="size-6" /> {t('clientChannels.title', '客户端分发')}
          </h1>
          <p className="text-sm text-muted-foreground mt-1 max-w-2xl">
            {t('clientChannels.subtitle', '管理客户端分发频道与拉取密钥。每服一个频道，密钥用于玩家侧更新器拉取。')}
          </p>
        </div>
        {!isEmpty && (
          <Button onClick={() => setCreateOpen(true)} className="shrink-0">
            <Plus className="size-4" /> {t('clientChannels.addChannel', '新增频道')}
          </Button>
        )}
      </div>

      <ClientDistFlowGuide />

      {isEmpty ? (
        <EmptyChannelsGuide onCreate={() => setCreateOpen(true)} />
      ) : (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
          {list.map((ch: ClientChannel) => (
            <ChannelCard key={ch.id} channel={ch} onOpen={() => setSelected(ch.channelId)} />
          ))}
        </div>
      )}

      <CreateChannelDialog open={createOpen} onOpenChange={setCreateOpen} onCreated={(id) => setSelected(id)} />
    </div>
  )
}

/** 空状态大引导卡：说明用途 + 主 CTA「创建第一个分发频道」。 */
function EmptyChannelsGuide({ onCreate }: { onCreate: () => void }) {
  const { t } = useTranslation()
  return (
    <div className="rounded-xl border border-dashed bg-card/40 p-10 text-center flex flex-col items-center gap-4">
      <span className="grid size-14 place-items-center rounded-full bg-primary/10 text-primary">
        <DownloadCloud className="size-7" />
      </span>
      <div className="space-y-1 max-w-md">
        <h2 className="text-lg font-semibold">{t('clientChannels.emptyTitle', '创建第一个分发频道')}</h2>
        <p className="text-sm text-muted-foreground">
          {t(
            'clientChannels.emptyDesc',
            '分发频道是玩家客户端 OTA 更新的入口：建频道 → 拉取密钥 → 发布版本 → 接入启动器，四步即可让玩家自动收到更新。',
          )}
        </p>
      </div>
      <Button onClick={onCreate} size="lg">
        <Plus className="size-4" /> {t('clientChannels.createFirst', '创建分发频道')}
      </Button>
    </div>
  )
}

/** 频道卡片：当前版本 / 密钥数 + 就绪度小标，点击进入工作台。 */
function ChannelCard({ channel, onOpen }: { channel: ClientChannel; onOpen: () => void }) {
  const { t } = useTranslation()
  const steps = useMemo(
    () => deriveReadiness({ keyCount: channel.keyCount ?? 0, currentVersion: channel.currentVersion }),
    [channel.keyCount, channel.currentVersion],
  )
  const completed = readinessCompletedCount(steps)
  const ready = completed === steps.length

  return (
    <button
      type="button"
      onClick={onOpen}
      className="group text-left rounded-xl border bg-card/40 p-4 transition-colors hover:border-primary/40 hover:bg-accent/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="font-semibold truncate">{channel.name}</div>
          <div className="font-mono text-xs text-muted-foreground truncate">{channel.channelId}</div>
        </div>
        <ArrowRight className="size-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5" />
      </div>

      {channel.description && (
        <p className="mt-2 text-xs text-muted-foreground line-clamp-2">{channel.description}</p>
      )}

      <div className="mt-3 flex items-center gap-2 flex-wrap text-xs">
        <Badge variant={channel.currentVersion > 0 ? 'default' : 'outline'}>
          {channel.currentVersion > 0
            ? `v${channel.currentVersion}`
            : t('clientChannels.unpublished', '未发布')}
        </Badge>
        <Badge variant="outline">
          {t('clientChannels.keyCountBadge', '{{n}} 个密钥', { n: channel.keyCount ?? 0 })}
        </Badge>
        {ready ? (
          <span className="inline-flex items-center gap-1 text-emerald-600 dark:text-emerald-500">
            <Check className="size-3.5" /> {t('clientChannels.ready', '已就绪')}
          </span>
        ) : (
          <span className="text-muted-foreground">
            {t('clientChannels.readinessShort', '就绪度 {{c}}/{{n}}', { c: completed, n: steps.length })}
          </span>
        )}
      </div>
    </button>
  )
}

/** 创建频道模态（FR-187，取代原内联展开表单；内容自适应壳）。 */
function CreateChannelDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onCreated: (channelId: string) => void
}) {
  const { t } = useTranslation()
  const create = useCreateClientChannel()
  const [channelId, setChannelId] = useState('')
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')

  const slugOk = /^[a-z0-9][a-z0-9-]{1,63}$/.test(channelId)
  const canSubmit = slugOk && name.trim() !== '' && !create.isPending

  const reset = () => {
    setChannelId('')
    setName('')
    setDescription('')
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!canSubmit) return
    try {
      await create.mutateAsync({ channelId, name, description })
      toast.success(t('clientChannels.created', '频道已创建'))
      const created = channelId
      reset()
      onOpenChange(false)
      onCreated(created)
    } catch (e) {
      toast.error(errMsg(e, t('clientChannels.createFailed', '创建频道失败')))
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(v: boolean) => {
        if (!v) reset()
        onOpenChange(v)
      }}
    >
      <DialogContent className={cn(scrollableDialogContentClass, 'sm:max-w-lg')}>
        <DialogHeader>
          <DialogTitle>{t('clientChannels.addChannel', '新增频道')}</DialogTitle>
          <DialogDescription>
            {t('clientChannels.createDialogDesc', '为一个服务器创建分发频道；创建后进入工作台继续配置密钥与发布版本。')}
          </DialogDescription>
        </DialogHeader>
        <form id="create-channel-form" onSubmit={submit}>
          <ScrollableDialogBody className="space-y-3">
            <label className="flex flex-col gap-1 text-sm">
              {t('clientChannels.channelId', '频道标识')}
              <input
                className="p-2 border rounded bg-background font-mono aria-invalid:border-destructive"
                placeholder="skyblock-s1"
                aria-invalid={channelId !== '' && !slugOk}
                value={channelId}
                onChange={(e) => setChannelId(e.target.value)}
                autoFocus
              />
              <span className="text-xs text-muted-foreground">
                {t('clientChannels.channelIdHint', '小写字母/数字/连字符，2-64 位，创建后不可改')}
              </span>
            </label>
            <label className="flex flex-col gap-1 text-sm">
              {t('common.name', '名称')}
              <input
                className="p-2 border rounded bg-background"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </label>
            <label className="flex flex-col gap-1 text-sm">
              {t('clientChannels.description', '描述')}
              <input
                className="p-2 border rounded bg-background"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
              />
            </label>
          </ScrollableDialogBody>
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel', '取消')}
          </Button>
          <Button type="submit" form="create-channel-form" disabled={!canSubmit}>
            {t('common.create', '创建')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/**
 * 频道工作台：顶部就绪度步骤器（状态由 keyCount/currentVersion 推导）+
 * 密钥 / 版本 / 统计 / 接入指引 分段。取代原 ChannelDetail，全程模态化。
 */
function ChannelWorkbench({
  channelId,
  initialTab,
  onBack,
}: {
  channelId: string
  /** 还原入口（FR-191 从发布页返回时为 'versions'）；非法/缺省回落 'keys'。 */
  initialTab?: WorkbenchTab | null
  onBack: () => void
}) {
  const { t } = useTranslation()
  const { data: detail, isLoading } = useClientChannel(channelId)
  const del = useDeleteClientChannel()

  const validInitialTab: WorkbenchTab = (['keys', 'versions', 'core', 'stats', 'guide'] as const).includes(
    initialTab as WorkbenchTab,
  )
    ? (initialTab as WorkbenchTab)
    : 'keys'
  const [tab, setTab] = useState<WorkbenchTab>(validInitialTab)
  const [deleteChannel, setDeleteChannel] = useState(false)
  // 就绪度步骤器「创建密钥」CTA 直接开建密钥模态（BUG-E）：开关上提到工作台、随 tab 自动归零。
  const [keyCreateOpen, setKeyCreateOpen] = useState(false)

  const keyCount = detail?.keys?.length ?? 0
  const steps = useMemo(
    () => deriveReadiness({ keyCount, currentVersion: detail?.currentVersion ?? 0 }),
    [keyCount, detail?.currentVersion],
  )

  const doDeleteChannel = () => {
    setDeleteChannel(false)
    del.mutate(channelId, {
      onSuccess: () => {
        toast.success(t('clientChannels.channelDeleted', '频道已删除'))
        onBack()
      },
      onError: (e) => toast.error(errMsg(e, t('clientChannels.deleteFailed', '删除频道失败'))),
    })
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between flex-wrap gap-2">
        <div className="flex items-center gap-2">
          <button
            className="text-muted-foreground hover:text-foreground"
            onClick={onBack}
            aria-label={t('common.back', '返回')}
          >
            <ChevronLeft className="size-5" />
          </button>
          <div>
            <h1 className="text-2xl font-bold flex items-center gap-2">
              <KeyRound className="size-5" /> {detail?.name ?? channelId}
            </h1>
            <p className="text-xs text-muted-foreground font-mono mt-1">{channelId}</p>
          </div>
        </div>
        <button
          className="text-destructive hover:underline text-sm"
          onClick={() => setDeleteChannel(true)}
        >
          {t('clientChannels.deleteChannel', '删除频道')}
        </button>
      </div>

      <ReadinessStepper
        steps={steps}
        onCta={(id) => {
          setTab(STEP_META[id].goto)
          if (id === 'keys') setKeyCreateOpen(true)
        }}
      />

      <Tabs value={tab} onValueChange={(v) => setTab(v as WorkbenchTab)}>
        <TabsList variant="line">
          <TabsTrigger value="keys">{t('clientChannels.manageKeys', '拉取密钥')}</TabsTrigger>
          <TabsTrigger value="versions">{t('clientVersions.tab', '版本管理')}</TabsTrigger>
          <TabsTrigger value="stats">{t('clientStats.tab', '统计')}</TabsTrigger>
          <TabsTrigger value="guide">{t('clientGuide.tab', '接入指引')}</TabsTrigger>
        </TabsList>
        <TabsContent value="keys">
          <KeysSegment
            channelId={channelId}
            keys={detail?.keys ?? []}
            loading={isLoading}
            createOpen={keyCreateOpen && tab === 'keys'}
            onCreateOpenChange={setKeyCreateOpen}
          />
        </TabsContent>
        <TabsContent value="versions">
          <ClientVersionsPanel channelId={channelId} />
        </TabsContent>
        <TabsContent value="stats">
          <ClientStatsPanel channelId={channelId} />
        </TabsContent>
        <TabsContent value="guide">
          <ClientIntegrationGuide channelId={channelId} />
        </TabsContent>
      </Tabs>

      <DangerConfirm
        open={deleteChannel}
        title={t('clientChannels.deleteChannelConfirm', '确定删除此频道？')}
        description={t('clientChannels.deleteChannelDesc', '将连同其全部拉取密钥一并删除，不可恢复。')}
        scope="platform"
        confirmText={channelId}
        confirmLabel={t('common.delete', '删除')}
        onConfirm={doDeleteChannel}
        onCancel={() => setDeleteChannel(false)}
      />
    </div>
  )
}

/** 步骤器文案配置（标题 + 引导说明 + CTA 跳转 tab）。 */
const STEP_META: Record<
  ReadinessStepId,
  { titleKey: string; titleFallback: string; hintKey: string; hintFallback: string; ctaKey: string; ctaFallback: string; goto: WorkbenchTab }
> = {
  channel: {
    titleKey: 'clientChannels.stepChannel',
    titleFallback: '创建频道',
    hintKey: 'clientChannels.stepChannelHint',
    hintFallback: '频道已创建。',
    ctaKey: '',
    ctaFallback: '',
    goto: 'keys',
  },
  keys: {
    titleKey: 'clientChannels.stepKeys',
    titleFallback: '拉取密钥',
    hintKey: 'clientChannels.stepKeysHint',
    hintFallback: '创建一个拉取密钥，供玩家侧更新器鉴权拉取。',
    ctaKey: 'clientChannels.createKey',
    ctaFallback: '创建密钥',
    goto: 'keys',
  },
  version: {
    titleKey: 'clientChannels.stepVersion',
    titleFallback: '发布版本',
    hintKey: 'clientChannels.stepVersionHint',
    hintFallback: '上传客户端文件并发布第一个版本，玩家即可拉取 latest。',
    ctaKey: 'clientVersions.publish',
    ctaFallback: '发布新版本',
    goto: 'versions',
  },
  integrate: {
    titleKey: 'clientChannels.stepIntegrate',
    titleFallback: '接入启动器',
    hintKey: 'clientChannels.stepIntegrateHint',
    hintFallback: '照接入指引把更新器接入整合包并下发玩家。',
    ctaKey: 'clientChannels.viewGuide',
    ctaFallback: '查看接入指引',
    goto: 'guide',
  },
}

/** 顶部常驻就绪度步骤器（纯展示，状态由 detail 推导）。 */
function ReadinessStepper({ steps, onCta }: { steps: ReadinessStep[]; onCta: (stepId: ReadinessStepId) => void }) {
  const { t } = useTranslation()
  const current = steps.find((s) => s.current)
  const completed = readinessCompletedCount(steps)
  const allReady = completed === steps.length

  return (
    <div className="rounded-xl border bg-card/40 p-4 space-y-3">
      <div className="flex items-center justify-between gap-2 flex-wrap">
        <h2 className="text-sm font-semibold">{t('clientChannels.readinessTitle', '接入就绪度')}</h2>
        <span className="text-xs text-muted-foreground">
          {allReady
            ? t('clientChannels.readyAll', '全部就绪，玩家可正常更新')
            : t('clientChannels.readinessShort', '就绪度 {{c}}/{{n}}', { c: completed, n: steps.length })}
        </span>
      </div>

      <ol className="flex items-stretch gap-2 flex-wrap">
        {steps.map((s, i) => {
          const meta = STEP_META[s.id]
          return (
            <li key={s.id} className="flex items-center gap-2">
              <div
                className={cn(
                  'flex items-center gap-2 rounded-lg border px-3 py-2 text-sm',
                  s.current && 'border-primary/50 bg-primary/5',
                  s.done && !s.current && 'border-emerald-500/30 bg-emerald-500/5',
                )}
              >
                <span
                  className={cn(
                    'grid size-6 shrink-0 place-items-center rounded-full text-xs font-medium',
                    s.done
                      ? 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-500'
                      : s.current
                        ? 'bg-primary text-primary-foreground'
                        : 'bg-muted text-muted-foreground',
                  )}
                >
                  {s.done ? <Check className="size-3.5" /> : i + 1}
                </span>
                <span className={cn('whitespace-nowrap', s.done && !s.current && 'text-muted-foreground')}>
                  {t(meta.titleKey, meta.titleFallback)}
                </span>
              </div>
              {i < steps.length - 1 && <ArrowRight className="size-3.5 shrink-0 text-muted-foreground/50" />}
            </li>
          )
        })}
      </ol>

      {current && (
        <div className="flex items-center justify-between gap-3 flex-wrap rounded-lg bg-muted/40 px-3 py-2">
          <p className="text-xs text-muted-foreground">{t(STEP_META[current.id].hintKey, STEP_META[current.id].hintFallback)}</p>
          {STEP_META[current.id].ctaKey && (
            <Button size="sm" variant="outline" className="shrink-0" onClick={() => onCta(current.id)}>
              {t(STEP_META[current.id].ctaKey, STEP_META[current.id].ctaFallback)}
              <ArrowRight className="size-3.5" />
            </Button>
          )}
        </div>
      )}
    </div>
  )
}

/** 密钥分段：列表 + 「创建密钥」模态 + 查看/编辑/吊销（DangerConfirm）+ 一次性明文弹窗。 */
function KeysSegment({
  channelId,
  keys,
  loading,
  createOpen,
  onCreateOpenChange,
}: {
  channelId: string
  keys: ClientPullKey[]
  loading: boolean
  createOpen: boolean
  onCreateOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation()
  const revokeKey = useRevokeClientKey()
  const revealKey = useRevealClientKey()

  const [secret, setSecret] = useState<ClientKeyWithSecret | null>(null)
  const [revokeTarget, setRevokeTarget] = useState<ClientPullKey | null>(null)
  const [editTarget, setEditTarget] = useState<ClientPullKey | null>(null)
  // 查看明文弹窗（FR-192）：保存当前查看到的密钥名 + 明文。
  const [revealed, setRevealed] = useState<{ name: string; key: string } | null>(null)

  const doReveal = async (key: ClientPullKey) => {
    if (!key.revealable) {
      toast.error(t('clientChannels.notRevealable'))
      return
    }
    try {
      const res = await revealKey.mutateAsync({ channelId, keyId: key.id })
      setRevealed({ name: key.name, key: res.key })
    } catch (e) {
      // 兜底：后端返 KEY_NOT_REVEALABLE（如并发改值/降级）也走不可找回提示。
      const code = (e as { response?: { data?: { error?: string } } })?.response?.data?.error
      if (code === 'KEY_NOT_REVEALABLE') toast.error(t('clientChannels.notRevealable'))
      else toast.error(errMsg(e, t('clientChannels.revealFailed', '查看密钥失败')))
    }
  }

  const doRevoke = (key: ClientPullKey) => {
    setRevokeTarget(null)
    revokeKey.mutate(
      { channelId, keyId: key.id },
      {
        onSuccess: () => toast.success(t('clientChannels.revoked', '密钥已吊销')),
        onError: (e) => toast.error(errMsg(e, t('clientChannels.revokeFailed', '吊销密钥失败'))),
      },
    )
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between flex-wrap gap-2">
        <p className="text-sm text-muted-foreground max-w-2xl">
          {t(
            'clientChannels.keysSubtitleViewable',
            '拉取密钥以可逆加密存储，发出后永久使用、可随时查看明文并复制到 jm-updater.json。可编辑密钥值（改值会使持旧值的已分发客户端失效）。',
          )}
        </p>
        <Button onClick={() => onCreateOpenChange(true)} className="shrink-0">
          <Plus className="size-4" /> {t('clientChannels.createKey', '创建密钥')}
        </Button>
      </div>

      <div className="overflow-hidden rounded-lg border">
        <Table>
          <TableHeader className="bg-muted/50">
            <TableRow>
              <TableHead>{t('common.name', '名称')}</TableHead>
              <TableHead>{t('clientChannels.keyPrefix', '前缀')}</TableHead>
              <TableHead>{t('common.status', '状态')}</TableHead>
              <TableHead>{t('clientChannels.expiresAt', '过期时间')}</TableHead>
              <TableHead>{t('clientChannels.lastUsed', '最近使用')}</TableHead>
              <TableHead className="text-right">{t('common.actions', '操作')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {keys.map((k) => (
              <TableRow key={k.id}>
                <TableCell className="font-medium">{k.name}</TableCell>
                <TableCell className="font-mono text-xs">{k.keyPrefix}…</TableCell>
                <TableCell>
                  {k.revoked ? (
                    <Badge variant="outline" className="border-destructive/40 text-destructive">
                      {t('clientChannels.statusRevoked', '已吊销')}
                    </Badge>
                  ) : (
                    <Badge variant="outline">{t('clientChannels.statusActive', '有效')}</Badge>
                  )}
                </TableCell>
                <TableCell className="text-xs">{k.expiresAt ? new Date(k.expiresAt).toLocaleString() : '-'}</TableCell>
                <TableCell className="text-xs">{k.lastUsedAt ? new Date(k.lastUsedAt).toLocaleString() : '-'}</TableCell>
                <TableCell>
                  <div className="flex justify-end gap-1">
                    <Button
                      variant="ghost"
                      size="xs"
                      onClick={() => doReveal(k)}
                      disabled={!k.revealable || revealKey.isPending}
                      title={k.revealable ? undefined : t('clientChannels.notRevealable')}
                    >
                      <Eye className="size-3.5" /> {t('clientChannels.reveal', '查看')}
                    </Button>
                    <Button variant="ghost" size="xs" onClick={() => setEditTarget(k)} disabled={k.revoked}>
                      <Pencil className="size-3.5" /> {t('common.edit', '编辑')}
                    </Button>
                    <Button
                      variant="ghost"
                      size="xs"
                      className="text-status-danger hover:text-status-danger"
                      onClick={() => setRevokeTarget(k)}
                      disabled={k.revoked}
                    >
                      <Ban className="size-3.5" /> {t('clientChannels.revoke', '吊销')}
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
            {keys.length === 0 && !loading && (
              <TableRow>
                <TableCell colSpan={6} className="h-16 text-center text-muted-foreground">
                  {t('clientChannels.noKeys', '暂无密钥')}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>

      <CreateKeyDialog
        channelId={channelId}
        open={createOpen}
        onOpenChange={onCreateOpenChange}
        onCreated={(s) => setSecret(s)}
      />

      <EditKeyDialog
        channelId={channelId}
        target={editTarget}
        onOpenChange={(v) => !v && setEditTarget(null)}
        onUpdated={(s) => {
          // 改了值才回显新明文弹窗（后端 key 非空表示改了值）；仅改名不弹。
          if (s.key) setSecret(s)
        }}
      />

      <SecretDialog secret={secret} onClose={() => setSecret(null)} />

      <RevealDialog revealed={revealed} onClose={() => setRevealed(null)} />

      <DangerConfirm
        open={revokeTarget !== null}
        title={t('clientChannels.revokeConfirm', '确定吊销此密钥？')}
        description={t(
          'clientChannels.revokeConfirmDesc',
          '吊销不可恢复：使用此密钥的已分发客户端将无法再更新（拉取 manifest/制品一律被拒）。仅在确认该密钥不再服务于任何已发出的整合包时吊销。',
        )}
        scope="platform"
        confirmLabel={t('clientChannels.revoke', '吊销')}
        onConfirm={() => revokeTarget && doRevoke(revokeTarget)}
        onCancel={() => setRevokeTarget(null)}
      />
    </div>
  )
}

/** 创建拉取密钥模态（FR-187，取代原内联常驻表单；内容自适应壳）。 */
function CreateKeyDialog({
  channelId,
  open,
  onOpenChange,
  onCreated,
}: {
  channelId: string
  open: boolean
  onOpenChange: (v: boolean) => void
  onCreated: (secret: ClientKeyWithSecret) => void
}) {
  const { t } = useTranslation()
  const createKey = useCreateClientKey()
  const [keyName, setKeyName] = useState('')
  const [expiresAt, setExpiresAt] = useState('')
  // 自定义密钥值（可空=自动生成）。FR-192：管理员可自控这把永久 key。
  const [customValue, setCustomValue] = useState('')

  const canSubmit = keyName.trim() !== '' && !createKey.isPending

  const reset = () => {
    setKeyName('')
    setExpiresAt('')
    setCustomValue('')
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!canSubmit) return
    try {
      const res = await createKey.mutateAsync({
        channelId,
        name: keyName,
        expiresAt: expiresAt ? new Date(expiresAt).toISOString() : undefined,
        value: customValue.trim() || undefined,
      })
      reset()
      onOpenChange(false)
      onCreated(res)
    } catch (e) {
      toast.error(errMsg(e, t('clientChannels.keyCreateFailed', '创建密钥失败')))
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(v: boolean) => {
        if (!v) reset()
        onOpenChange(v)
      }}
    >
      <DialogContent className={cn(scrollableDialogContentClass, 'sm:max-w-md')}>
        <DialogHeader>
          <DialogTitle>{t('clientChannels.createKey', '创建密钥')}</DialogTitle>
          <DialogDescription>
            {t('clientChannels.createKeyDescViewable', '密钥发出后永久使用；创建后可随时查看明文。留空密钥值则自动生成。')}
          </DialogDescription>
        </DialogHeader>
        <form id="create-key-form" onSubmit={submit}>
          <ScrollableDialogBody className="space-y-3">
            <label className="flex flex-col gap-1 text-sm">
              {t('clientChannels.keyName', '密钥名称')}
              <input
                className="p-2 border rounded bg-background"
                placeholder={t('clientChannels.keyNamePlaceholder', '如：正式包 / 灰度')}
                value={keyName}
                onChange={(e) => setKeyName(e.target.value)}
                autoFocus
              />
            </label>
            <label className="flex flex-col gap-1 text-sm">
              {t('clientChannels.keyValue', '密钥值（可选）')}
              <input
                className="p-2 border rounded bg-background font-mono"
                placeholder={t('clientChannels.keyValuePlaceholder', '留空自动生成；可填自定义值')}
                value={customValue}
                onChange={(e) => setCustomValue(e.target.value)}
              />
              <span className="text-xs text-muted-foreground">
                {t('clientChannels.keyValueHint', '自定义则用作明文；可随时查看/编辑。')}
              </span>
            </label>
            <label className="flex flex-col gap-1 text-sm">
              {t('clientChannels.expiresAt', '过期时间（可选）')}
              <input
                type="datetime-local"
                className="p-2 border rounded bg-background"
                value={expiresAt}
                onChange={(e) => setExpiresAt(e.target.value)}
              />
            </label>
          </ScrollableDialogBody>
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel', '取消')}
          </Button>
          <Button type="submit" form="create-key-form" disabled={!canSubmit}>
            {t('clientChannels.createKey', '创建密钥')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/**
 * 编辑拉取密钥模态（FR-192）：改名 + 可选改值。
 * 改值会重算鉴权哈希、使持旧值的已分发客户端失效——表单内强警告。改值后回显新明文供复制。
 * 外层壳：target 存在时以 key=target.id 挂载内部表单，使其状态随目标重新初始化（避免 effect 改状态）。
 */
function EditKeyDialog({
  channelId,
  target,
  onOpenChange,
  onUpdated,
}: {
  channelId: string
  target: ClientPullKey | null
  onOpenChange: (v: boolean) => void
  onUpdated: (secret: ClientKeyWithSecret) => void
}) {
  if (!target) return null
  return (
    <EditKeyForm
      key={target.id}
      channelId={channelId}
      target={target}
      onOpenChange={onOpenChange}
      onUpdated={onUpdated}
    />
  )
}

/** 编辑密钥表单（内部组件，target 非空；状态由 props 初始化，挂载即新表单）。 */
function EditKeyForm({
  channelId,
  target,
  onOpenChange,
  onUpdated,
}: {
  channelId: string
  target: ClientPullKey
  onOpenChange: (v: boolean) => void
  onUpdated: (secret: ClientKeyWithSecret) => void
}) {
  const { t } = useTranslation()
  const updateKey = useUpdateClientKey()
  const [name, setName] = useState(target.name)
  // 值不回显既有明文，留空=不改值。
  const [value, setValue] = useState('')

  const canSubmit = name.trim() !== '' && !updateKey.isPending

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!canSubmit) return
    try {
      const res = await updateKey.mutateAsync({
        channelId,
        keyId: target.id,
        name: name.trim(),
        value: value.trim() || undefined,
      })
      toast.success(t('clientChannels.keyUpdated', '密钥已更新'))
      onOpenChange(false)
      onUpdated(res)
    } catch (e) {
      toast.error(errMsg(e, t('clientChannels.keyUpdateFailed', '更新密钥失败')))
    }
  }

  return (
    <Dialog open onOpenChange={(v: boolean) => onOpenChange(v)}>
      <DialogContent className={cn(scrollableDialogContentClass, 'sm:max-w-md')}>
        <DialogHeader>
          <DialogTitle>{t('clientChannels.editKey', '编辑密钥')}</DialogTitle>
          <DialogDescription>
            {t('clientChannels.editKeyDesc', '修改名称或密钥值。留空密钥值仅改名；填入则改值。')}
          </DialogDescription>
        </DialogHeader>
        <form id="edit-key-form" onSubmit={submit}>
          <ScrollableDialogBody className="space-y-3">
            <label className="flex flex-col gap-1 text-sm">
              {t('clientChannels.keyName', '密钥名称')}
              <input
                className="p-2 border rounded bg-background"
                value={name}
                onChange={(e) => setName(e.target.value)}
                autoFocus
              />
            </label>
            <label className="flex flex-col gap-1 text-sm">
              {t('clientChannels.keyNewValue', '新密钥值（可选）')}
              <input
                className="p-2 border rounded bg-background font-mono"
                placeholder={t('clientChannels.keyNewValuePlaceholder', '留空则不改值，仅改名')}
                value={value}
                onChange={(e) => setValue(e.target.value)}
              />
            </label>
            {value.trim() !== '' && (
              <p className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-xs text-destructive">
                {t(
                  'clientChannels.editKeyValueWarn',
                  '改值后旧值立即失效：持旧值的已分发客户端将无法再更新，需把新值下发给玩家。请确认确需更换。',
                )}
              </p>
            )}
          </ScrollableDialogBody>
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel', '取消')}
          </Button>
          <Button type="submit" form="edit-key-form" disabled={!canSubmit}>
            {t('common.save', '保存')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** 一次性明文展示弹窗：仅创建/轮换后可见，提示复制保存（复制走 copyToClipboard 兜底 HTTP 非安全上下文）。 */
function SecretDialog({ secret, onClose }: { secret: ClientKeyWithSecret | null; onClose: () => void }) {
  const { t } = useTranslation()

  const copy = async () => {
    if (!secret) return
    const ok = await copyToClipboard(secret.key)
    if (ok) toast.success(t('clientChannels.copied', '已复制到剪贴板'))
    else toast.error(t('clientChannels.copyFailed', '复制失败，请手动选择复制'))
  }

  return (
    <Dialog open={secret !== null} onOpenChange={(v: boolean) => { if (!v) onClose() }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('clientChannels.secretTitle', '拉取密钥（仅此一次）')}</DialogTitle>
          <DialogDescription>
            {t('clientChannels.secretDesc', '请立即复制保存。关闭后将无法再次查看完整密钥。')}
          </DialogDescription>
        </DialogHeader>
        <div className="flex items-center gap-2 rounded-md border bg-muted/50 p-3">
          <code className="flex-1 break-all font-mono text-sm">{secret?.key}</code>
          <Button variant="outline" size="sm" onClick={copy} className="shrink-0">
            <Copy className="size-4" /> {t('clientChannels.copy', '复制')}
          </Button>
        </div>
        <DialogFooter>
          <Button onClick={onClose}>{t('common.close', '关闭')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** 查看已存密钥明文弹窗（FR-192，可逆加密存储 → 可随时查看）：展示明文 + 复制（走 copyToClipboard）。 */
function RevealDialog({
  revealed,
  onClose,
}: {
  revealed: { name: string; key: string } | null
  onClose: () => void
}) {
  const { t } = useTranslation()

  const copy = async () => {
    if (!revealed) return
    const ok = await copyToClipboard(revealed.key)
    if (ok) toast.success(t('clientChannels.copied', '已复制到剪贴板'))
    else toast.error(t('clientChannels.copyFailed', '复制失败，请手动选择复制'))
  }

  return (
    <Dialog open={revealed !== null} onOpenChange={(v: boolean) => { if (!v) onClose() }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t('clientChannels.revealTitle', '拉取密钥明文')}
            {revealed ? `（${revealed.name}）` : ''}
          </DialogTitle>
          <DialogDescription>
            {t('clientChannels.revealDesc', '用于玩家侧更新器鉴权拉取，请复制到 jm-updater.json 妥善保存。')}
          </DialogDescription>
        </DialogHeader>
        <div className="flex items-center gap-2 rounded-md border bg-muted/50 p-3">
          <code className="flex-1 break-all font-mono text-sm">{revealed?.key}</code>
          <Button variant="outline" size="sm" onClick={copy} className="shrink-0">
            <Copy className="size-4" /> {t('clientChannels.copy', '复制')}
          </Button>
        </div>
        <DialogFooter>
          <Button onClick={onClose}>{t('common.close', '关闭')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
