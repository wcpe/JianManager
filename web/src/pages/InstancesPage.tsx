import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useInstances, useStartInstance, useStopInstance, useRestartInstance, useDeleteInstance, useKillInstance } from '@/api/instances'
import { useConsoleStore } from '@/stores/console'
import ConfirmDialog from '@/components/ConfirmDialog'
import CreateInstanceDialog from '@/components/CreateInstanceDialog'
import ProvisionServerDialog from '@/components/ProvisionServerDialog'
import ProvisionProxyDialog from '@/components/ProvisionProxyDialog'
import ProxyRegistrationsDialog from '@/components/ProxyRegistrationsDialog'
import CloneInstanceDialog from '@/components/CloneInstanceDialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

export default function InstancesPage() {
  const { t } = useTranslation()
  // 点实例名进统一的「运维控制台」（终端/文件/配置/Bot），不再跳老的实例详情页。
  const openInstance = useConsoleStore((s) => s.openInstance)
  const [showCreate, setShowCreate] = useState(false)
  const [showProvision, setShowProvision] = useState(false)
  const [showProvisionProxy, setShowProvisionProxy] = useState(false)
  const [manageProxy, setManageProxy] = useState<{ id: number; name: string } | null>(null)
  const [cloneTarget, setCloneTarget] = useState<{ id: number; name: string } | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<number | null>(null)
  const { data: instances, isLoading } = useInstances()
  const start = useStartInstance()
  const stop = useStopInstance()
  const restart = useRestartInstance()
  const kill = useKillInstance()
  const del = useDeleteInstance()

  const statusConfig: Record<string, { text: string; variant: 'default' | 'secondary' | 'destructive' | 'outline' }> = {
    STOPPED: { text: t('instances.stopped'), variant: 'secondary' },
    STARTING: { text: t('instances.starting'), variant: 'outline' },
    RUNNING: { text: t('instances.running'), variant: 'default' },
    STOPPING: { text: t('instances.stopping'), variant: 'outline' },
    CRASHED: { text: t('instances.crashed'), variant: 'destructive' },
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <h1 className="text-2xl font-bold">{t('instances.title')}</h1>
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => setShowProvision(true)}>⚡ {t('provision.entry')}</Button>
          <Button variant="outline" onClick={() => setShowProvisionProxy(true)}>🌐 {t('proxy.entry')}</Button>
          <Button onClick={() => setShowCreate(true)}>+ {t('instances.createInstance')}</Button>
        </div>
      </div>

      <ProvisionServerDialog open={showProvision} onClose={() => setShowProvision(false)} />
      <ProvisionProxyDialog open={showProvisionProxy} onClose={() => setShowProvisionProxy(false)} />
      <CreateInstanceDialog open={showCreate} onClose={() => setShowCreate(false)} />
      {manageProxy && (
        <ProxyRegistrationsDialog proxyId={manageProxy.id} proxyName={manageProxy.name} onClose={() => setManageProxy(null)} />
      )}
      {cloneTarget && (
        <CloneInstanceDialog sourceId={cloneTarget.id} sourceName={cloneTarget.name} onClose={() => setCloneTarget(null)} />
      )}

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : (
        <div className="border rounded-lg">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead>{t('instances.name')}</TableHead>
                <TableHead>{t('instances.type')}</TableHead>
                <TableHead>{t('instances.role')}</TableHead>
                <TableHead>{t('instances.processType')}</TableHead>
                <TableHead>{t('instances.status')}</TableHead>
                <TableHead>{t('instances.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {instances?.map((inst) => {
                const st = statusConfig[inst.status] || statusConfig.STOPPED
                return (
                  <TableRow key={inst.id}>
                    <TableCell className="font-medium">
                      <button
                        type="button"
                        className="text-left text-primary hover:underline"
                        onClick={() => openInstance(inst.id)}
                      >
                        {inst.name}
                      </button>
                    </TableCell>
                    <TableCell className="text-muted-foreground">{inst.type}</TableCell>
                    <TableCell>
                      {inst.role === 'proxy' ? (
                        <Badge variant="default">{t('networks.role_proxy')}</Badge>
                      ) : inst.role === 'backend' ? (
                        <Badge variant="secondary">{t('networks.role_backend')}</Badge>
                      ) : (
                        <span className="text-muted-foreground text-xs">{t('networks.role_universal')}</span>
                      )}
                    </TableCell>
                    <TableCell className="text-muted-foreground">{inst.processType}</TableCell>
                    <TableCell>
                      <Badge variant={st.variant}>{st.text}</Badge>
                    </TableCell>
                    <TableCell>
                      <div className="flex gap-1">
                        {inst.role === 'proxy' && (
                          <Button
                            variant="ghost"
                            size="xs"
                            onClick={() => setManageProxy({ id: inst.id, name: inst.name })}
                            className="text-indigo-600 hover:text-indigo-700"
                          >
                            {t('proxy.manageBackends')}
                          </Button>
                        )}
                        {inst.role === 'backend' && (inst.status === 'STOPPED' || inst.status === 'CRASHED') && (
                          <Button
                            variant="ghost"
                            size="xs"
                            onClick={() => setCloneTarget({ id: inst.id, name: inst.name })}
                            className="text-indigo-600 hover:text-indigo-700"
                          >
                            {t('clone.action')}
                          </Button>
                        )}
                        {(inst.status === 'STOPPED' || inst.status === 'CRASHED') && (
                          <Button
                            variant="ghost"
                            size="xs"
                            onClick={() => start.mutate(inst.id)}
                            className="text-green-600 hover:text-green-700"
                          >
                            {t('instances.start')}
                          </Button>
                        )}
                        {inst.status === 'RUNNING' && (
                          <>
                            <Button
                              variant="ghost"
                              size="xs"
                              onClick={() => stop.mutate(inst.id)}
                              className="text-yellow-600 hover:text-yellow-700"
                            >
                              {t('instances.stop')}
                            </Button>
                            <Button
                              variant="ghost"
                              size="xs"
                              onClick={() => restart.mutate(inst.id)}
                              className="text-blue-600 hover:text-blue-700"
                            >
                              {t('instances.restart')}
                            </Button>
                          </>
                        )}
                        {(inst.status === 'STARTING' || inst.status === 'STOPPING') && (
                          <Button
                            variant="ghost"
                            size="xs"
                            onClick={() => kill.mutate(inst.id)}
                            className="text-yellow-600 hover:text-yellow-700"
                          >
                            {t('instances.kill')}
                          </Button>
                        )}
                        {(inst.status === 'STOPPED' || inst.status === 'CRASHED') && (
                          <Button
                            variant="ghost"
                            size="xs"
                            onClick={() => setDeleteTarget(inst.id)}
                            className="text-red-600 hover:text-red-700"
                          >
                            {t('common.delete')}
                          </Button>
                        )}
                      </div>
                    </TableCell>
                  </TableRow>
                )
              })}
              {(!instances || instances.length === 0) && (
                <TableRow>
                  <TableCell colSpan={6} className="text-center text-muted-foreground">
                    {t('instances.empty')}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      )}

      <ConfirmDialog
        open={deleteTarget !== null}
        title={t('instances.deleteConfirm')}
        description="此操作不可撤销，实例的所有数据将被删除。"
        confirmLabel={t('common.delete')}
        variant="destructive"
        onConfirm={() => { if (deleteTarget) del.mutate(deleteTarget); setDeleteTarget(null) }}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}
