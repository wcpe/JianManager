import { useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { useInstance, useStartInstance, useStopInstance, useRestartInstance, useKillInstance } from '@/api/instances'
import { useInstanceMetrics } from '@/api/metrics'
import { useTerminalToken } from '@/api/terminal'
import { useBots } from '@/api/bots'
import ConfigEditor from '@/components/ConfigEditor'
import ResourceExplorer from '@/components/explorer/ResourceExplorer'
import PluginManager from '@/components/plugins/PluginManager'
import TerminalComponent from '@/components/Terminal'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

function formatUptime(seconds: number): string {
  if (!seconds || seconds < 0) return '--'
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

export default function InstanceDetailPage() {
  const { id } = useParams<{ id: string }>()
  const instanceId = Number(id)
  const { data: instance, isLoading } = useInstance(instanceId)
  const { t } = useTranslation()

  const startMut = useStartInstance()
  const stopMut = useStopInstance()
  const restartMut = useRestartInstance()
  const killMut = useKillInstance()

  if (isLoading) {
    return <p className="text-muted-foreground">{t('common.loading')}</p>
  }

  if (!instance) {
    return <p className="text-muted-foreground">{t('instanceDetail.notFound')}</p>
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <div>
          <h1 className="text-2xl font-bold">{instance.name}</h1>
          <p className="text-sm text-muted-foreground">
            {t('instanceDetail.status')}:{' '}
            <span className={
              instance.status === 'RUNNING' ? 'text-green-500' :
              instance.status === 'CRASHED' ? 'text-red-500' :
              instance.status === 'STARTING' || instance.status === 'STOPPING' ? 'text-yellow-500' :
              'text-gray-500'
            }>
              {instance.status}
            </span>
            {' | '}{t('instanceDetail.type')}: {instance.type}
            {' | '}{t('instanceDetail.processType')}: {instance.processType}
          </p>
        </div>
        <div className="flex gap-2">
          {(instance.status === 'STOPPED' || instance.status === 'CRASHED') && (
            <Button
              variant="outline"
              onClick={() => startMut.mutate(instanceId)}
              disabled={startMut.isPending}
              className="text-green-600 border-green-200 hover:bg-green-50 hover:text-green-700"
            >
              {startMut.isPending ? t('instanceDetail.starting') : t('instances.start')}
            </Button>
          )}
          {instance.status === 'RUNNING' && (
            <>
              <Button
                variant="outline"
                onClick={() => stopMut.mutate(instanceId)}
                disabled={stopMut.isPending}
                className="text-yellow-600 border-yellow-200 hover:bg-yellow-50 hover:text-yellow-700"
              >
                {stopMut.isPending ? t('instanceDetail.stopping') : t('instances.stop')}
              </Button>
              <Button
                variant="outline"
                onClick={() => restartMut.mutate(instanceId)}
                disabled={restartMut.isPending}
                className="text-blue-600 border-blue-200 hover:bg-blue-50 hover:text-blue-700"
              >
                {restartMut.isPending ? t('instanceDetail.starting') : t('instances.restart')}
              </Button>
            </>
          )}
          {(instance.status === 'STARTING' || instance.status === 'STOPPING') && (
            <Button
              variant="outline"
              onClick={() => killMut.mutate(instanceId)}
              disabled={killMut.isPending}
              className="text-yellow-600 border-yellow-200 hover:bg-yellow-50 hover:text-yellow-700"
            >
              {killMut.isPending ? t('instanceDetail.terminating') : t('instanceDetail.forceStop')}
            </Button>
          )}
        </div>
      </div>

      <Tabs defaultValue="terminal">
        <TabsList variant="line">
          <TabsTrigger value="terminal">{t('instanceDetail.terminal')}</TabsTrigger>
          <TabsTrigger value="files">{t('instanceDetail.files')}</TabsTrigger>
          <TabsTrigger value="config">{t('instanceDetail.config')}</TabsTrigger>
          <TabsTrigger value="plugins">{t('plugins.tab')}</TabsTrigger>
          <TabsTrigger value="backups">{t('instanceDetail.backups')}</TabsTrigger>
          <TabsTrigger value="bot">{t('instanceDetail.bot')}</TabsTrigger>
        </TabsList>

        <TabsContent value="terminal">
          <TerminalTab instanceId={instanceId} status={instance.status} />
        </TabsContent>
        <TabsContent value="files">
          <ResourceExplorer instanceId={instanceId} />
        </TabsContent>
        <TabsContent value="config">
          <ConfigTab instanceId={instanceId} />
        </TabsContent>
        <TabsContent value="plugins">
          <PluginManager instanceId={instanceId} />
        </TabsContent>
        <TabsContent value="backups">
          <BackupsTab />
        </TabsContent>
        <TabsContent value="bot">
          <BotsTab instanceId={instanceId} />
        </TabsContent>
      </Tabs>
    </div>
  )
}

function TerminalTab({ instanceId, status }: { instanceId: number; status: string }) {
  const { t } = useTranslation()
  const { data: metrics } = useInstanceMetrics(instanceId, status === 'RUNNING')
  const { data: tokenData, isLoading, error } = useTerminalToken(instanceId, status === 'RUNNING' ? 'write' : 'read')

  return (
    <div className="space-y-4">
      {/* 实例指标（基础三项） */}
      <div className="grid grid-cols-3 gap-4">
        <div className="border rounded-lg p-3">
          <p className="text-xs text-muted-foreground">{t('instanceDetail.tps')}</p>
          <p className="text-xl font-bold mt-1">
            {status === 'RUNNING' && metrics
              ? (metrics.tps >= 0 ? metrics.tps.toFixed(2) : t('common.na'))
              : '--'}
          </p>
        </div>
        <div className="border rounded-lg p-3">
          <p className="text-xs text-muted-foreground">{t('instanceDetail.onlinePlayers')}</p>
          <p className="text-xl font-bold mt-1">
            {status === 'RUNNING' && metrics
              ? (metrics.onlinePlayers >= 0 ? metrics.onlinePlayers : t('common.na'))
              : '--'}
          </p>
        </div>
        <div className="border rounded-lg p-3">
          <p className="text-xs text-muted-foreground">{t('instanceDetail.memory')}</p>
          <p className="text-xl font-bold mt-1">
            {status === 'RUNNING' && metrics
              ? (metrics.memoryMb > 0
                  ? (metrics.heapMaxMb > 0
                      ? `${metrics.memoryMb} / ${metrics.heapMaxMb} MB`
                      : `${metrics.memoryMb} MB`)
                  : t('common.na'))
              : '--'}
          </p>
        </div>
      </div>

      {/* 探针富指标（FR-010：MSPT/线程/CPU/运行时长） */}
      {status === 'RUNNING' && metrics?.probeAvailable && (
        <div className="grid grid-cols-4 gap-4">
          <div className="border rounded-lg p-3">
            <p className="text-xs text-muted-foreground">{t('instanceDetail.mspt')}</p>
            <p className="text-lg font-semibold mt-1">{metrics.msptMillis.toFixed(2)} ms</p>
          </div>
          <div className="border rounded-lg p-3">
            <p className="text-xs text-muted-foreground">{t('instanceDetail.threads')}</p>
            <p className="text-lg font-semibold mt-1">{metrics.threads}</p>
          </div>
          <div className="border rounded-lg p-3">
            <p className="text-xs text-muted-foreground">{t('instanceDetail.cpu')}</p>
            <p className="text-lg font-semibold mt-1">{metrics.cpuPercent.toFixed(1)}%</p>
          </div>
          <div className="border rounded-lg p-3">
            <p className="text-xs text-muted-foreground">{t('instanceDetail.uptime')}</p>
            <p className="text-lg font-semibold mt-1">{formatUptime(metrics.uptimeSeconds)}</p>
          </div>
        </div>
      )}

      {/* 世界负载（按世界名展示） */}
      {status === 'RUNNING' && metrics?.probeAvailable && metrics.worlds && metrics.worlds.length > 0 && (
        <div className="border rounded-lg p-3">
          <p className="text-sm font-semibold mb-2">{t('instanceDetail.worldLoad')}</p>
          <table className="w-full text-xs">
            <thead className="text-muted-foreground">
              <tr>
                <th className="text-left font-normal pb-1">{t('common.name')}</th>
                <th className="text-right font-normal pb-1">{t('instanceDetail.worldChunks')}</th>
                <th className="text-right font-normal pb-1">{t('instanceDetail.worldEntities')}</th>
                <th className="text-right font-normal pb-1">{t('instanceDetail.worldTileEntities')}</th>
              </tr>
            </thead>
            <tbody>
              {metrics.worlds.map((w) => (
                <tr key={w.name} className="border-t">
                  <td className="py-1 font-mono">{w.name}</td>
                  <td className="py-1 text-right">{w.loadedChunks}</td>
                  <td className="py-1 text-right">{w.entities}</td>
                  <td className="py-1 text-right">{w.tileEntities}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* 探针未安装提示（基础三项仍可用） */}
      {status === 'RUNNING' && metrics && !metrics.probeAvailable && (
        <div className="border border-blue-200 bg-blue-50 dark:bg-blue-950 dark:border-blue-800 rounded-lg p-2 text-xs text-blue-700 dark:text-blue-300">
          ℹ {t('instanceDetail.probeUnavailable')}
        </div>
      )}

      {/* 状态提示 */}
      {status === 'CRASHED' && (
        <div className="border border-red-300 bg-red-50 dark:bg-red-950 dark:border-red-800 rounded-lg p-3 text-sm text-red-700 dark:text-red-300">
          ⚠ {t('instanceDetail.crashWarning')}
        </div>
      )}
      {status === 'STARTING' && (
        <div className="border border-yellow-300 bg-yellow-50 dark:bg-yellow-950 dark:border-yellow-800 rounded-lg p-3 text-sm text-yellow-700 dark:text-yellow-300">
          ⏳ {t('instanceDetail.startingWarning')}
        </div>
      )}
      {status !== 'RUNNING' && status !== 'CRASHED' && status !== 'STARTING' && (
        <div className="border border-yellow-300 bg-yellow-50 dark:bg-yellow-950 dark:border-yellow-800 rounded-lg p-2 text-xs text-yellow-700 dark:text-yellow-300">
          {t('instanceDetail.terminalReadOnly', { status })}
        </div>
      )}

      {/* 终端 */}
      {error ? (
        <div className="border rounded-lg p-4 bg-[#1a1b26] min-h-[400px] flex items-center justify-center">
          <p className="text-muted-foreground text-sm">{t('instanceDetail.terminalConnectFailed')}: {(error as Error).message || t('common.error')}</p>
        </div>
      ) : (
        <TerminalComponent
          instanceId={String(instanceId)}
          wsUrl={tokenData?.wsUrl}
          token={tokenData?.token}
          readOnly={status !== 'RUNNING'}
          isLoading={isLoading}
        />
      )}
    </div>
  )
}

function ConfigTab({
  instanceId,
}: {
  instanceId: number
}) {
  return <ConfigEditor instanceId={instanceId} />
}

function BackupsTab() {
  const { t } = useTranslation()

  return (
    <div>
      <p className="text-sm text-muted-foreground mb-2">{t('instanceDetail.backups')}</p>
      <div className="border rounded-lg p-4 min-h-[200px]">
        <p className="text-muted-foreground">{t('common.loading')}</p>
      </div>
    </div>
  )
}

function BotsTab({ instanceId }: { instanceId: number }) {
  const { t } = useTranslation()
  const { data: botList, isLoading } = useBots({ instanceId })
  const bots = botList?.items

  return (
    <div>
      <p className="text-sm text-muted-foreground mb-2">{t('instanceDetail.instanceBots')}</p>
      {isLoading ? (
        <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
      ) : bots && bots.length > 0 ? (
        <div className="border rounded-lg">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead>{t('common.name')}</TableHead>
                <TableHead>{t('common.status')}</TableHead>
                <TableHead>{t('bots.behavior')}</TableHead>
                <TableHead>{t('instanceDetail.server')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {bots.map((bot) => {
                let server = ''
                let port: number | string = ''
                try {
                  const cfg = JSON.parse(bot.config) as { server?: string; port?: number }
                  server = cfg.server ?? ''
                  port = cfg.port ?? ''
                } catch {
                  // config 非法 JSON 时留空
                }
                return (
                <TableRow key={bot.id}>
                  <TableCell className="font-medium">{bot.name}</TableCell>
                  <TableCell>
                    <span className={bot.status === 'connected' ? 'text-green-500' : 'text-gray-500'}>
                      {bot.status === 'connected' ? `● ${t('instanceDetail.connected')}` : `○ ${t('instanceDetail.disconnected')}`}
                    </span>
                  </TableCell>
                  <TableCell className="text-muted-foreground">{bot.behavior}</TableCell>
                  <TableCell className="text-muted-foreground">
                    {server}:{port}
                  </TableCell>
                </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </div>
      ) : (
        <div className="border rounded-lg p-4 min-h-[200px]">
          <p className="text-muted-foreground">{t('instanceDetail.noBots')}</p>
        </div>
      )}
    </div>
  )
}
