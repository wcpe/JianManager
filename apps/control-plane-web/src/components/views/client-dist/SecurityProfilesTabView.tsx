/**
 * @file SecurityProfilesTabView：安全侧「客户端画像」Tab（列表 + 详情弹窗）的受控视图。
 *       列表与详情取数、路由查询串读写、脱敏策略由应用容器负责。
 * @input lib/client-dist-security-contracts（ClientDistSecurityProfile/ClientDistSecurityProfileDetail）、
 *        lib/client-dist-query（ClientDistQuery/ClientDistQueryKey）、
 *        views/client-dist/security-format（EmptyState/fmtTime/levelVariant/SECURITY_EMPTY）、
 *        views/UntrustedFieldBadge、Badge/Button/Dialog/Input/Panel/Table 原语、翻译上下文
 * @output SecurityProfilesTabView、SecurityProfilesTabViewProps、ProfileMaskers
 * @sync apps/control-plane-web/src/components/client-dist/SecurityProfilesTab.tsx
 * @since FR-502（组件受控化迁包；原 FR-430 / ADR-088 客户端画像 Tab，含 FR-360 面板脱敏）
 */
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import UntrustedFieldBadge from '@jianmanager/ui/components/views/UntrustedFieldBadge'
import type {
  ClientDistSecurityProfile,
  ClientDistSecurityProfileDetail,
} from '@jianmanager/ui/lib/client-dist-security-contracts'
import type { ClientDistQuery, ClientDistQueryKey } from '@/lib/client-dist-query'
import { EmptyState, SECURITY_EMPTY as EMPTY, fmtTime, levelVariant } from './security-format'

/**
 * 脱敏函数注入（FR-360）：隐私策略归应用侧，包内不持有明文展示策略。
 * 视图只负责把容器给的函数作用到展示字段上，不自行决定打码规则。
 */
export interface ProfileMaskers {
  /** playerName → 展示文本（应用侧 `maskPlayerName`）。 */
  playerName: (value: string | null | undefined) => string
  /** machineId → 展示文本（应用侧 `maskMachineId`）。 */
  machineId: (value: string | null | undefined) => string
  /** installId → 展示文本（应用侧 `maskInstallId`）。 */
  installId: (value: string | null | undefined) => string
}

/**
 * 客户端画像 Tab 的注入契约（受控视图，ADR-097 a 范式）。
 *
 * 受控边界：列表与详情数据、加载/错误态经 props 注入（应用容器调
 * `useClientDistSecurityProfiles` / `useClientDistSecurityProfile`）；会触发重新取数的
 * 查询条件（`channelId`/`machineId` 来自路由查询串，`playerName` 为筛选）经 props 受控，
 * 变更经 `onQueryChange` / `onPlayerNameChange` 上报。
 * 详情弹窗开合与「当前查看的画像 id」属本地 UI 状态，仅在变化时经 `onDetailChange` 上报，
 * 容器只镜像该 id 用于取详情（开合真源仍在视图）。
 */
export interface SecurityProfilesTabViewProps {
  /** 画像列表（空数组即空态）。 */
  profiles: ClientDistSecurityProfile[]
  /** 取数中；仅在无行时展示「加载中」文案。 */
  isLoading: boolean
  /** 取数失败；判定优先于空态。 */
  isError: boolean
  /** 当前查询条件（channelId/machineId 等冻结键，来自路由查询串）。 */
  query: ClientDistQuery
  /** 查询条件写入（容器接 router 查询串）。 */
  onQueryChange: (patch: Partial<Record<ClientDistQueryKey, string | null>>) => void
  /** 玩家名筛选（触发重新取数，故归容器）。 */
  playerName: string
  /** 玩家名筛选变更上报。 */
  onPlayerNameChange: (value: string) => void
  /** 脱敏函数（应用侧隐私策略注入）。 */
  maskers: ProfileMaskers
  /** 详情数据（容器按上报的 id 取数）；未选或未就绪时缺省。 */
  detail?: ClientDistSecurityProfileDetail
  /** 详情取数中。 */
  detailLoading: boolean
  /** 详情取数失败。 */
  detailError: boolean
  /** 详情弹窗目标变更上报（`null` = 已关闭）；容器据此决定是否取详情。 */
  onDetailChange: (id: number | null) => void
}

export function SecurityProfilesTabView({
  profiles,
  isLoading,
  isError,
  query,
  onQueryChange,
  playerName,
  onPlayerNameChange,
  maskers,
  detail,
  detailLoading,
  detailError,
  onDetailChange,
}: SecurityProfilesTabViewProps) {
  const { t } = useTranslation()
  const [detailId, setDetailId] = useState<number | null>(null)

  // 开合状态留在视图（纯 UI 状态），同时向容器上报目标 id 以驱动详情取数。
  const changeDetail = (id: number | null) => {
    setDetailId(id)
    onDetailChange(id)
  }

  return (
    <div className="space-y-4">
      <Panel
        title={t('clientDistOps.profiles.title')}
        actions={
          <div className="flex flex-wrap gap-2">
            <Input className="w-40" placeholder={t('clientDistOps.profiles.phPlayer')} value={playerName} onChange={(e) => onPlayerNameChange(e.target.value)} />
            <Input className="w-40" placeholder={t('clientDistOps.profiles.phChannel')} value={query.channelId ?? ''} onChange={(e) => onQueryChange({ channelId: e.target.value || null })} />
            <Input className="w-40" placeholder={t('clientDistOps.profiles.phMachine')} value={query.machineId ?? ''} onChange={(e) => onQueryChange({ machineId: e.target.value || null })} />
          </div>
        }
      >
        {isError ? (
          <EmptyState text={t('clientDistOps.profiles.error')} />
        ) : profiles.length === 0 ? (
          <EmptyState text={isLoading ? t('common.loading') : t('clientDistOps.profiles.empty')} />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('clientDistOps.profiles.colPlayerChannel')}</TableHead>
                <TableHead>{t('clientDistOps.profiles.colDevice')}</TableHead>
                <TableHead>{t('clientDistOps.profiles.colLastIpKey')}</TableHead>
                <TableHead>{t('clientDistOps.profiles.colEnv')}</TableHead>
                <TableHead>{t('clientDistOps.profiles.colVersion')}</TableHead>
                <TableHead>{t('clientDistOps.profiles.colRisk')}</TableHead>
                <TableHead>{t('clientDistOps.profiles.colLastSeen')}</TableHead>
                <TableHead className="text-right">{t('common.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {profiles.map((profile) => (
                <TableRow key={profile.id}>
                  <TableCell>
                    <div className="flex items-center gap-1 font-medium" title={profile.playerName || undefined}>
                      <span>{maskers.playerName(profile.playerName) || EMPTY}</span>
                      {profile.playerName ? <UntrustedFieldBadge /> : null}
                    </div>
                    <div className="text-xs text-muted-foreground">{profile.channelId || EMPTY}</div>
                  </TableCell>
                  <TableCell className="max-w-56">
                    <div className="truncate text-xs font-mono" title={profile.machineId || undefined}>
                      machine: {maskers.machineId(profile.machineId) || EMPTY}
                    </div>
                    <div className="truncate text-xs font-mono text-muted-foreground" title={profile.installId || undefined}>
                      install: {maskers.installId(profile.installId) || EMPTY}
                    </div>
                  </TableCell>
                  <TableCell>
                    <div>{profile.lastIp || EMPTY}</div>
                    <div className="text-xs text-muted-foreground">{profile.keyPrefix || profile.keyId || EMPTY}</div>
                  </TableCell>
                  <TableCell>
                    <div>{profile.os || EMPTY} {profile.arch || ''}</div>
                    <div className="text-xs text-muted-foreground">{profile.javaVendor || EMPTY} {profile.javaVersion || ''}</div>
                  </TableCell>
                  <TableCell>
                    <div>core {profile.coreVersion || EMPTY}</div>
                    <div className="text-xs text-muted-foreground">manifest {profile.manifestVersion || EMPTY}</div>
                  </TableCell>
                  <TableCell>
                    <Badge variant={levelVariant(profile.riskLevel)}>{profile.riskLevel || 'info'} · {profile.riskScore}</Badge>
                  </TableCell>
                  <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{fmtTime(profile.lastSeen)}</TableCell>
                  <TableCell className="text-right">
                    <Button type="button" size="xs" variant="outline" onClick={() => changeDetail(profile.id)}>
                      {t('clientDistOps.profiles.viewDetail')}
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Panel>
      <ProfileDetailDialog
        open={detailId !== null}
        onOpenChange={(open) => !open && changeDetail(null)}
        maskers={maskers}
        detail={detail}
        isLoading={detailLoading}
        isError={detailError}
      />
    </div>
  )
}

/** 画像详情弹窗（展示层；数据与加载/错误态由容器按 `detailId` 注入）。 */
function ProfileDetailDialog({
  open,
  onOpenChange,
  maskers,
  detail,
  isLoading,
  isError,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  maskers: ProfileMaskers
  detail?: ClientDistSecurityProfileDetail
  isLoading: boolean
  isError: boolean
}) {
  const { t } = useTranslation()
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t('clientDistOps.profiles.detailTitle')}</DialogTitle>
          <DialogDescription>{t('clientDistOps.profiles.detailDesc')}</DialogDescription>
        </DialogHeader>
        {isError ? <EmptyState text={t('clientDistOps.profiles.detailError')} /> : null}
        {isLoading ? <EmptyState text={t('common.loading')} /> : null}
        {detail ? (
          <div className="space-y-4 text-sm">
            <div className="grid grid-cols-2 gap-2 rounded-lg border p-3 text-xs">
              <div>
                <div className="text-muted-foreground">{t('clientDistOps.profiles.fPlayer')}</div>
                <div className="flex items-center gap-1 font-medium">
                  <span>{maskers.playerName(detail.playerName) || EMPTY}</span>
                  {detail.playerName ? <UntrustedFieldBadge /> : null}
                </div>
              </div>
              <div>
                <div className="text-muted-foreground">{t('clientDistOps.profiles.fChannel')}</div>
                <div>{detail.channelId || EMPTY}</div>
              </div>
              <div>
                <div className="text-muted-foreground">{t('clientDistOps.profiles.fMachine')}</div>
                <div className="font-mono" title={detail.machineId || undefined}>{maskers.machineId(detail.machineId) || EMPTY}</div>
              </div>
              <div>
                <div className="text-muted-foreground">Install</div>
                <div className="font-mono" title={detail.installId || undefined}>{maskers.installId(detail.installId) || EMPTY}</div>
              </div>
              <div>
                <div className="text-muted-foreground">Java</div>
                <div>
                  <span>{detail.javaVendor || EMPTY}</span>
                  {detail.javaVersion ? <span className="ml-1">{detail.javaVersion}</span> : null}
                </div>
              </div>
              <div>
                <div className="text-muted-foreground">{t('clientDistOps.profiles.fTimezone')}</div>
                <div>
                  <span>{detail.timezone || EMPTY}</span>
                  <span className="mx-1">·</span>
                  <span>{detail.locale || EMPTY}</span>
                </div>
              </div>
              <div>
                <div className="text-muted-foreground">Core / Wedge</div>
                <div>{detail.coreVersion || EMPTY} / {detail.wedgeVersion || EMPTY}</div>
              </div>
              <div>
                <div className="text-muted-foreground">{t('clientDistOps.profiles.fMemoryTier')}</div>
                <div>{detail.memoryTier || EMPTY}</div>
              </div>
            </div>
            <div>
              <div className="mb-2 text-xs font-medium text-muted-foreground">{t('clientDistOps.profiles.timelineTitle')}</div>
              <ul className="space-y-2">
                {(detail.recentEvents ?? []).map((ev) => (
                  <li key={`ev-${ev.id}`} className="rounded border px-3 py-2 text-xs">
                    <div className="flex items-center justify-between gap-2">
                      <span className="font-mono">{ev.ruleCode || EMPTY}</span>
                      <Badge variant={levelVariant(ev.severity)}>{ev.severity}</Badge>
                    </div>
                    <div className="mt-1 text-muted-foreground">{ev.reason || EMPTY} · {fmtTime(ev.createdAt)}</div>
                  </li>
                ))}
                {(detail.protectionActions ?? []).map((act) => (
                  <li key={`act-${act.id}`} className="rounded border px-3 py-2 text-xs">
                    <div className="font-mono">{act.action || EMPTY}</div>
                    <div className="mt-1 text-muted-foreground">{act.reason || EMPTY} · {fmtTime(act.createdAt)}</div>
                  </li>
                ))}
                {(detail.recentEvents ?? []).length === 0 && (detail.protectionActions ?? []).length === 0 ? (
                  <li className="text-xs text-muted-foreground">{t('clientDistOps.profiles.timelineEmpty')}</li>
                ) : null}
              </ul>
            </div>
          </div>
        ) : null}
      </DialogContent>
    </Dialog>
  )
}
