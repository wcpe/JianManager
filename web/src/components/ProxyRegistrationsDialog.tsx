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
  useRegistrations,
  useCreateRegistration,
  useDeleteRegistration,
} from '@/api/registrations'
import { useResyncProxy } from '@/api/proxy'
import { MODAL_OVERLAY, MODAL_PANEL } from '@/components/ui/scrollable-dialog'
import { Checkbox } from '@/components/ui/checkbox'
import { Combobox, type ComboboxOption } from '@/components/ui/combobox'
import { FieldLabel, FieldError } from '@/components/ui/field-label'
import { validateHost } from '@/lib/form-validation'

interface ProxyRegistrationsDialogProps {
  proxyId: number
  proxyName: string
  onClose: () => void
}

/** 代理后端注册管理（FR-035）：把已有 backend 注册进代理，编辑 alias/priority/forced-host，并可 resync。 */
export default function ProxyRegistrationsDialog({ proxyId, proxyName, onClose }: ProxyRegistrationsDialogProps) {
  const { t } = useTranslation()
  const { data: regs } = useRegistrations(proxyId)
  const { data: backends } = useInstances({ role: 'backend' })
  const create = useCreateRegistration(proxyId)
  const del = useDeleteRegistration(proxyId)
  const resync = useResyncProxy()

  const [backendId, setBackendId] = useState('')
  const [alias, setAlias] = useState('')
  const [forcedHost, setForcedHost] = useState('')
  const [restricted, setRestricted] = useState(false)

  const registeredBackendIds = new Set(regs?.map((r) => r.backendId))
  const candidates = (backends || []).filter((b) => !registeredBackendIds.has(b.id))
  const backendOptions: ComboboxOption[] = candidates.map((b) => ({
    value: String(b.id),
    label: `${b.name} (:${b.serverPort})`,
  }))
  const forcedHostError = validateHost(forcedHost)

  const add = (e: FormEvent) => {
    e.preventDefault()
    if (!backendId || forcedHostError) return
    create.mutate(
      {
        backendId: Number(backendId),
        alias: alias.trim() || undefined,
        forcedHost: forcedHost.trim() || undefined,
        restricted,
      },
      {
        onSuccess: (resp: { data?: { warning?: string } }) => {
          toast.success(t('proxy.registered'))
          if (resp?.data?.warning) toast.warning(resp.data.warning)
          setBackendId(''); setAlias(''); setForcedHost(''); setRestricted(false)
        },
        onError: (err: Error & { response?: { data?: { error?: string; message?: string } } }) => {
          const code = err.response?.data?.error
          const msg = code === 'ALIAS_CONFLICT' ? t('proxy.aliasConflict')
            : code === 'ALREADY_REGISTERED' ? t('proxy.alreadyRegistered')
            : err.response?.data?.message || t('common.error')
          toast.error(msg)
        },
      },
    )
  }

  const doResync = () => {
    resync.mutate(proxyId, {
      onSuccess: (data) => {
        if (data.secretConsistent === false) toast.warning(t('proxy.secretInconsistent'))
        else toast.success(t('proxy.resynced'))
        ;(data.warnings || []).forEach((w) => toast.warning(w))
      },
      onError: () => toast.error(t('proxy.resyncFailed')),
    })
  }

  return (
    <div className={MODAL_OVERLAY}>
      <div className={`${MODAL_PANEL} max-w-2xl`}>
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-lg font-bold">{t('proxy.manageTitle', { name: proxyName })}</h2>
          <div className="flex items-center gap-3">
            <button onClick={doResync} disabled={resync.isPending}
              className="text-xs px-2 py-1 border rounded-md hover:bg-accent disabled:opacity-50">
              {t('proxy.resync')}
            </button>
            <button onClick={onClose} className="text-sm text-muted-foreground hover:text-foreground">{t('common.close')}</button>
          </div>
        </div>

        <div className="border rounded-md mb-4">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead>{t('proxy.alias')}</TableHead>
                <TableHead>{t('proxy.backend')}</TableHead>
                <TableHead>{t('proxy.priority')}</TableHead>
                <TableHead>{t('proxy.forcedHost')}</TableHead>
                <TableHead className="text-right">{t('common.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {regs?.map((r) => (
                <TableRow key={r.id}>
                  <TableCell className="font-medium">{r.alias}</TableCell>
                  <TableCell>{r.backend?.name || `#${r.backendId}`}</TableCell>
                  <TableCell>{r.priority}</TableCell>
                  <TableCell className="text-muted-foreground">{r.forcedHost || '--'}</TableCell>
                  <TableCell className="text-right">
                    <button className="text-xs text-red-600 hover:underline"
                      onClick={() => del.mutate(r.id, { onSuccess: () => toast.success(t('proxy.unregistered')) })}>
                      {t('proxy.unregister')}
                    </button>
                  </TableCell>
                </TableRow>
              ))}
              {regs && regs.length === 0 && (
                <TableRow>
                  <TableCell colSpan={5} className="text-center text-muted-foreground">{t('proxy.noBackends')}</TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>

        <h3 className="text-sm font-medium mb-2">{t('proxy.registerBackend')}</h3>
        <form onSubmit={add} className="grid grid-cols-2 gap-3 items-start">
          <div>
            <FieldLabel required className="text-xs text-muted-foreground font-normal">{t('proxy.backend')}</FieldLabel>
            <div className="mt-1">
              <Combobox
                options={backendOptions}
                value={backendId}
                onChange={setBackendId}
                allowCustom={false}
                placeholder={t('proxy.selectBackend')}
              />
            </div>
          </div>
          <div>
            <FieldLabel className="text-xs text-muted-foreground font-normal">{t('proxy.alias')} ({t('proxy.aliasOptional')})</FieldLabel>
            <input value={alias} onChange={(e) => setAlias(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm" placeholder="lobby" />
          </div>
          <div>
            <FieldLabel className="text-xs text-muted-foreground font-normal">{t('proxy.forcedHost')}</FieldLabel>
            <input value={forcedHost} onChange={(e) => setForcedHost(e.target.value)}
              aria-invalid={!!forcedHostError}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive" placeholder="play.example.com" />
            <FieldError error={forcedHostError} />
          </div>
          <div className="flex items-center justify-between pt-6">
            <label className="flex items-center gap-2 text-sm">
              <Checkbox checked={restricted} onCheckedChange={(v) => setRestricted(v === true)} aria-label={t('proxy.restricted')} />
              {t('proxy.restricted')}
            </label>
            <button type="submit" disabled={create.isPending || !backendId || !!forcedHostError}
              className="px-3 py-2 text-sm bg-primary text-primary-foreground rounded-md disabled:opacity-50">
              {t('proxy.register')}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
