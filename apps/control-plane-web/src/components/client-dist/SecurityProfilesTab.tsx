import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useClientDistSecurityProfile, useClientDistSecurityProfiles } from '@/api/clientDistSecurity'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import UntrustedFieldBadge from '@/components/UntrustedFieldBadge'
import { maskInstallId, maskMachineId, maskPlayerName } from '@/lib/privacy-mask'
import { EmptyState, SECURITY_EMPTY as EMPTY, fmtTime, levelVariant, useSecurityQuery } from './security-shared'

/**
 * 安全侧「客户端画像」Tab + 详情弹窗（FR-430 / ADR-088）。
 * 由旧 `ProtectionCenterPage.tsx` 内联实现原样迁出，行为不变。
 */

export function ProfilesTab() {
  const { t } = useTranslation()
  const { query, updateQuery } = useSecurityQuery()
  const [playerName, setPlayerName] = useState('')
  const [detailId, setDetailId] = useState<number | null>(null)
  const { data, isError, isLoading } = useClientDistSecurityProfiles({
    playerName: playerName || undefined,
    channelId: query.channelId,
    machineId: query.machineId,
    ip: query.ip,
    limit: 200,
  })
  const profiles = data ?? []
  return (
    <div className="space-y-4">
      <Panel
        title={t('clientDistOps.profiles.title')}
        actions={
          <div className="flex flex-wrap gap-2">
            <Input className="w-40" placeholder={t('clientDistOps.profiles.phPlayer')} value={playerName} onChange={(e) => setPlayerName(e.target.value)} />
            <Input className="w-40" placeholder={t('clientDistOps.profiles.phChannel')} value={query.channelId ?? ''} onChange={(e) => updateQuery({ channelId: e.target.value || null })} />
            <Input className="w-40" placeholder={t('clientDistOps.profiles.phMachine')} value={query.machineId ?? ''} onChange={(e) => updateQuery({ machineId: e.target.value || null })} />
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
                      <span>{maskPlayerName(profile.playerName) || EMPTY}</span>
                      {profile.playerName ? <UntrustedFieldBadge /> : null}
                    </div>
                    <div className="text-xs text-muted-foreground">{profile.channelId || EMPTY}</div>
                  </TableCell>
                  <TableCell className="max-w-56">
                    <div className="truncate text-xs font-mono" title={profile.machineId || undefined}>
                      machine: {maskMachineId(profile.machineId) || EMPTY}
                    </div>
                    <div className="truncate text-xs font-mono text-muted-foreground" title={profile.installId || undefined}>
                      install: {maskInstallId(profile.installId) || EMPTY}
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
                    <Button type="button" size="xs" variant="outline" onClick={() => setDetailId(profile.id)}>
                      {t('clientDistOps.profiles.viewDetail')}
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Panel>
      <ProfileDetailDialog id={detailId} open={detailId !== null} onOpenChange={(open) => !open && setDetailId(null)} />
    </div>
  )
}

function ProfileDetailDialog({ id, open, onOpenChange }: { id: number | null; open: boolean; onOpenChange: (open: boolean) => void }) {
  const { t } = useTranslation()
  const { data, isLoading, isError } = useClientDistSecurityProfile(id)
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t('clientDistOps.profiles.detailTitle')}</DialogTitle>
          <DialogDescription>{t('clientDistOps.profiles.detailDesc')}</DialogDescription>
        </DialogHeader>
        {isError ? <EmptyState text={t('clientDistOps.profiles.detailError')} /> : null}
        {isLoading ? <EmptyState text={t('common.loading')} /> : null}
        {data ? (
          <div className="space-y-4 text-sm">
            <div className="grid grid-cols-2 gap-2 rounded-lg border p-3 text-xs">
              <div>
                <div className="text-muted-foreground">{t('clientDistOps.profiles.fPlayer')}</div>
                <div className="flex items-center gap-1 font-medium">
                  <span>{maskPlayerName(data.playerName) || EMPTY}</span>
                  {data.playerName ? <UntrustedFieldBadge /> : null}
                </div>
              </div>
              <div>
                <div className="text-muted-foreground">{t('clientDistOps.profiles.fChannel')}</div>
                <div>{data.channelId || EMPTY}</div>
              </div>
              <div>
                <div className="text-muted-foreground">{t('clientDistOps.profiles.fMachine')}</div>
                <div className="font-mono" title={data.machineId || undefined}>{maskMachineId(data.machineId) || EMPTY}</div>
              </div>
              <div>
                <div className="text-muted-foreground">Install</div>
                <div className="font-mono" title={data.installId || undefined}>{maskInstallId(data.installId) || EMPTY}</div>
              </div>
              <div>
                <div className="text-muted-foreground">Java</div>
                <div>
                  <span>{data.javaVendor || EMPTY}</span>
                  {data.javaVersion ? <span className="ml-1">{data.javaVersion}</span> : null}
                </div>
              </div>
              <div>
                <div className="text-muted-foreground">{t('clientDistOps.profiles.fTimezone')}</div>
                <div>
                  <span>{data.timezone || EMPTY}</span>
                  <span className="mx-1">·</span>
                  <span>{data.locale || EMPTY}</span>
                </div>
              </div>
              <div>
                <div className="text-muted-foreground">Core / Wedge</div>
                <div>{data.coreVersion || EMPTY} / {data.wedgeVersion || EMPTY}</div>
              </div>
              <div>
                <div className="text-muted-foreground">{t('clientDistOps.profiles.fMemoryTier')}</div>
                <div>{data.memoryTier || EMPTY}</div>
              </div>
            </div>
            <div>
              <div className="mb-2 text-xs font-medium text-muted-foreground">{t('clientDistOps.profiles.timelineTitle')}</div>
              <ul className="space-y-2">
                {(data.recentEvents ?? []).map((ev) => (
                  <li key={`ev-${ev.id}`} className="rounded border px-3 py-2 text-xs">
                    <div className="flex items-center justify-between gap-2">
                      <span className="font-mono">{ev.ruleCode || EMPTY}</span>
                      <Badge variant={levelVariant(ev.severity)}>{ev.severity}</Badge>
                    </div>
                    <div className="mt-1 text-muted-foreground">{ev.reason || EMPTY} · {fmtTime(ev.createdAt)}</div>
                  </li>
                ))}
                {(data.protectionActions ?? []).map((act) => (
                  <li key={`act-${act.id}`} className="rounded border px-3 py-2 text-xs">
                    <div className="font-mono">{act.action || EMPTY}</div>
                    <div className="mt-1 text-muted-foreground">{act.reason || EMPTY} · {fmtTime(act.createdAt)}</div>
                  </li>
                ))}
                {(data.recentEvents ?? []).length === 0 && (data.protectionActions ?? []).length === 0 ? (
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
