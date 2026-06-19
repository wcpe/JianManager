import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { useInstances } from '@/api/instances'
import {
  useNetworks,
  useNetwork,
  useCreateNetwork,
  useDeleteNetwork,
  useAddNetworkMembers,
  useRemoveNetworkMember,
  useNetworkAction,
  type NetworkSummary,
} from '@/api/networks'

/** 群组（Network 软标签）管理页（FR-032 / ADR-007）：非独占分组，用于筛选与批量运维。 */
export default function NetworksPage() {
  const { t } = useTranslation()
  const { data: networks, isLoading } = useNetworks()
  const [createOpen, setCreateOpen] = useState(false)
  const [detailId, setDetailId] = useState<number | null>(null)
  const del = useDeleteNetwork()

  const handleDelete = (n: NetworkSummary) => {
    if (!window.confirm(t('networks.deleteConfirm', { name: n.name }))) return
    del.mutate(n.id, {
      onSuccess: () => toast.success(t('networks.deleted')),
      onError: () => toast.error(t('common.error')),
    })
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-1">
        <h1 className="text-2xl font-bold">{t('networks.title')}</h1>
        <button
          onClick={() => setCreateOpen(true)}
          className="px-3 py-2 text-sm bg-primary text-primary-foreground rounded-md"
        >
          {t('networks.create')}
        </button>
      </div>
      <p className="text-xs text-muted-foreground mb-4">{t('networks.subtitle')}</p>

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : (
        <div className="border rounded-lg">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead>{t('networks.name')}</TableHead>
                <TableHead>{t('networks.description')}</TableHead>
                <TableHead>{t('networks.members')}</TableHead>
                <TableHead className="text-right">{t('common.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {networks?.map((n) => (
                <TableRow key={n.id}>
                  <TableCell className="font-medium">{n.name}</TableCell>
                  <TableCell className="text-muted-foreground">{n.description || '--'}</TableCell>
                  <TableCell>{t('networks.memberCount', { count: n.memberCount })}</TableCell>
                  <TableCell className="text-right space-x-3">
                    <button className="text-xs text-blue-600 hover:underline" onClick={() => setDetailId(n.id)}>
                      {t('networks.view')}
                    </button>
                    <button className="text-xs text-red-600 hover:underline" onClick={() => handleDelete(n)}>
                      {t('common.delete')}
                    </button>
                  </TableCell>
                </TableRow>
              ))}
              {(!networks || networks.length === 0) && (
                <TableRow>
                  <TableCell colSpan={4} className="text-center text-muted-foreground">
                    {t('networks.empty')}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      )}

      {createOpen && <CreateNetworkModal onClose={() => setCreateOpen(false)} />}
      {detailId !== null && <NetworkDetailModal networkId={detailId} onClose={() => setDetailId(null)} />}
    </div>
  )
}

function CreateNetworkModal({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const create = useCreateNetwork()

  const submit = (e: FormEvent) => {
    e.preventDefault()
    create.mutate(
      { name, description: description || undefined },
      {
        onSuccess: () => {
          toast.success(t('networks.created'))
          onClose()
        },
        onError: (err: Error & { response?: { data?: { error?: string; message?: string } } }) => {
          if (err.response?.data?.error === 'NETWORK_NAME_CONFLICT') {
            toast.error(t('networks.nameConflict'))
            return
          }
          toast.error(err.response?.data?.message || t('networks.createFailed'))
        },
      },
    )
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
      <div className="bg-background border rounded-lg p-6 w-full max-w-md shadow-lg">
        <h2 className="text-lg font-bold mb-4">{t('networks.create')}</h2>
        <form onSubmit={submit} className="space-y-3">
          <div>
            <label className="text-sm font-medium">{t('networks.name')}</label>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
              placeholder="survival"
              required
            />
          </div>
          <div>
            <label className="text-sm font-medium">{t('networks.description')}</label>
            <input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
            />
          </div>
          <div className="flex justify-end gap-2 pt-2">
            <button type="button" onClick={onClose} className="px-4 py-2 text-sm border rounded-md hover:bg-accent">
              {t('common.cancel')}
            </button>
            <button type="submit" disabled={create.isPending || !name} className="px-4 py-2 text-sm bg-primary text-primary-foreground rounded-md disabled:opacity-50">
              {create.isPending ? t('common.creating') : t('common.create')}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

const roleColor: Record<string, string> = {
  RUNNING: 'bg-green-500',
  STARTING: 'bg-amber-500',
  STOPPING: 'bg-amber-500',
  CRASHED: 'bg-red-500',
}

function NetworkDetailModal({ networkId, onClose }: { networkId: number; onClose: () => void }) {
  const { t } = useTranslation()
  const { data: detail } = useNetwork(networkId)
  const { data: instances } = useInstances()
  const addMembers = useAddNetworkMembers(networkId)
  const removeMember = useRemoveNetworkMember(networkId)
  const action = useNetworkAction(networkId)
  const [selected, setSelected] = useState<number[]>([])

  const memberIds = new Set(detail?.members.map((m) => m.instanceId))
  const candidates = (instances || []).filter((i) => !memberIds.has(i.id))

  const roleLabel = (role: string) => t(`networks.role_${role}`, { defaultValue: role })

  const runBatch = (act: 'start' | 'stop') => {
    action.mutate(act, {
      onSuccess: (res) => toast.success(t('networks.batchResult', { succeeded: res.succeeded, failed: res.failed })),
      onError: () => toast.error(t('common.error')),
    })
  }

  const addSelected = () => {
    if (selected.length === 0) return
    addMembers.mutate(selected, {
      onSuccess: () => {
        toast.success(t('networks.added', { count: selected.length }))
        setSelected([])
      },
      onError: () => toast.error(t('common.error')),
    })
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
      <div className="bg-background border rounded-lg p-6 w-full max-w-2xl shadow-lg max-h-[85vh] overflow-y-auto">
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-lg font-bold">{detail?.name}</h2>
          <button onClick={onClose} className="text-sm text-muted-foreground hover:text-foreground">
            {t('common.close')}
          </button>
        </div>

        <div className="flex gap-2 mb-3">
          <button onClick={() => runBatch('start')} disabled={action.isPending} className="px-3 py-1.5 text-xs border rounded-md hover:bg-accent disabled:opacity-50">
            {t('networks.batchStart')}
          </button>
          <button onClick={() => runBatch('stop')} disabled={action.isPending} className="px-3 py-1.5 text-xs border rounded-md hover:bg-accent disabled:opacity-50">
            {t('networks.batchStop')}
          </button>
        </div>

        <h3 className="text-sm font-medium mb-2">{t('networks.members')}</h3>
        <div className="border rounded-md mb-4">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead>{t('common.name')}</TableHead>
                <TableHead>{t('users.role')}</TableHead>
                <TableHead>{t('common.status')}</TableHead>
                <TableHead className="text-right">{t('common.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {detail?.members.map((m) => (
                <TableRow key={m.instanceId}>
                  <TableCell className="font-medium">{m.name}</TableCell>
                  <TableCell>{roleLabel(m.role)}</TableCell>
                  <TableCell>
                    <span className="inline-flex items-center gap-1.5">
                      <span className={`h-2 w-2 rounded-full ${roleColor[m.status] || 'border border-muted-foreground'}`} />
                      {m.status}
                    </span>
                  </TableCell>
                  <TableCell className="text-right">
                    <button
                      className="text-xs text-red-600 hover:underline"
                      onClick={() => removeMember.mutate(m.instanceId)}
                    >
                      {t('networks.removeMember')}
                    </button>
                  </TableCell>
                </TableRow>
              ))}
              {detail && detail.members.length === 0 && (
                <TableRow>
                  <TableCell colSpan={4} className="text-center text-muted-foreground">
                    {t('networks.noMembers')}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>

        <h3 className="text-sm font-medium mb-2">{t('networks.addMembers')}</h3>
        {candidates.length === 0 ? (
          <p className="text-xs text-muted-foreground">{t('networks.noCandidates')}</p>
        ) : (
          <>
            <div className="max-h-40 overflow-y-auto border rounded-md p-2 space-y-1">
              {candidates.map((i) => (
                <label key={i.id} className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={selected.includes(i.id)}
                    onChange={(e) =>
                      setSelected((prev) => (e.target.checked ? [...prev, i.id] : prev.filter((x) => x !== i.id)))
                    }
                  />
                  <span>{i.name}</span>
                  <span className="text-xs text-muted-foreground">({roleLabel((i as { role?: string }).role || 'universal')})</span>
                </label>
              ))}
            </div>
            <div className="flex justify-end mt-2">
              <button
                onClick={addSelected}
                disabled={selected.length === 0 || addMembers.isPending}
                className="px-3 py-1.5 text-xs bg-primary text-primary-foreground rounded-md disabled:opacity-50"
              >
                {t('networks.addSelected', { count: selected.length })}
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  )
}
