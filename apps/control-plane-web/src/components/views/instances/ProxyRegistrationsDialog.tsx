import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import {
  ScrollableDialogBody,
  scrollableDialogContentClass,
} from '@jianmanager/ui/components/scrollable-dialog'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { Button } from '@jianmanager/ui/components/button'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import { validateHost } from '@/lib/form-validation'
import type {
  ProxyRegistration,
  ProxyResyncResult,
  RegisterProxyBackendPayload,
} from '@/lib/proxy-registration'

/** 提示通道：视图算好文案交外壳展示（本包不弹 toast）。 */
export type ProxyNotice = (kind: 'success' | 'warning' | 'error', message: string) => void

/** 可选后端（外壳已排除已注册的）。 */
export interface BackendCandidate {
  id: number
  name: string
  serverPort?: number
}

/**
 * 代理后端注册管理（FR-035）：把已有 backend 注册进代理，编辑 alias/priority/forced-host，并可 resync。
 *
 * 受控视图（ADR-097 b 范式）：不取数、不发请求、不弹 toast —— 注册项与候选后端由外壳注入，
 * 三个动作经回调上报。错误码到文案的映射（ALIAS_CONFLICT / ALREADY_REGISTERED）与
 * 表单校验这些**本组件自己的语义**留在视图内。
 */
export interface ProxyRegistrationsDialogProps {
  proxyName: string
  onClose: () => void
  /** 已注册后端（含后端名回填）。 */
  registrations: ProxyRegistration[]
  /** 可选后端。 */
  candidates: BackendCandidate[]
  /** 注册后端；成功可回 warning，失败抛错（视图按 error 码映射文案）。 */
  onRegister: (payload: RegisterProxyBackendPayload) => Promise<{ warning?: string }>
  /** 取消注册。 */
  onUnregister: (registrationId: number) => Promise<void>
  /** 重新同步。 */
  onResync: () => Promise<ProxyResyncResult>
  /** 提示通道。 */
  notify: ProxyNotice
}

export default function ProxyRegistrationsDialog({
  proxyName,
  onClose,
  registrations,
  candidates,
  onRegister,
  onUnregister,
  onResync,
  notify,
}: ProxyRegistrationsDialogProps) {
  const { t } = useTranslation()

  const [backendId, setBackendId] = useState('')
  const [alias, setAlias] = useState('')
  const [forcedHost, setForcedHost] = useState('')
  const [restricted, setRestricted] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [resyncing, setResyncing] = useState(false)

  const backendOptions: ComboboxOption[] = candidates.map((b) => ({
    value: String(b.id),
    label: `${b.name} (:${b.serverPort})`,
  }))
  const forcedHostError = validateHost(forcedHost)

  const add = async (e: FormEvent) => {
    e.preventDefault()
    if (!backendId || forcedHostError) return
    setSubmitting(true)
    try {
      const resp = await onRegister({
        backendId: Number(backendId),
        alias: alias.trim() || undefined,
        forcedHost: forcedHost.trim() || undefined,
        restricted,
      })
      notify('success', t('proxy.registered'))
      if (resp?.warning) notify('warning', resp.warning)
      setBackendId(''); setAlias(''); setForcedHost(''); setRestricted(false)
    } catch (err) {
      const body = (err as { response?: { data?: { error?: string; message?: string } } })?.response?.data
      const code = body?.error
      notify(
        'error',
        code === 'ALIAS_CONFLICT'
          ? t('proxy.aliasConflict')
          : code === 'ALREADY_REGISTERED'
            ? t('proxy.alreadyRegistered')
            : body?.message || t('common.error'),
      )
    } finally {
      setSubmitting(false)
    }
  }

  const doResync = async () => {
    setResyncing(true)
    try {
      const data = await onResync()
      if (data.secretConsistent === false) notify('warning', t('proxy.secretInconsistent'))
      else notify('success', t('proxy.resynced'))
      ;(data.warnings || []).forEach((w) => notify('warning', w))
    } catch {
      notify('error', t('proxy.resyncFailed'))
    } finally {
      setResyncing(false)
    }
  }

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose() }}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-2xl`} showCloseButton={false}>
        <DialogHeader>
          <div className="flex items-center justify-between gap-3">
            <DialogTitle>{t('proxy.manageTitle', { name: proxyName })}</DialogTitle>
            <div className="flex items-center gap-3">
              <Button type="button" size="xs" variant="outline" onClick={doResync} disabled={resyncing}>
                {t('proxy.resync')}
              </Button>
              <Button type="button" size="xs" variant="ghost" onClick={onClose} className="text-muted-foreground">
                {t('common.close')}
              </Button>
            </div>
          </div>
        </DialogHeader>

        <ScrollableDialogBody className="space-y-4 py-2">
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
                {registrations.map((r) => (
                  <TableRow key={r.id}>
                    <TableCell className="font-medium">{r.alias}</TableCell>
                    <TableCell>{r.backend?.name || `#${r.backendId}`}</TableCell>
                    <TableCell>{r.priority}</TableCell>
                    <TableCell className="text-muted-foreground">{r.forcedHost || '--'}</TableCell>
                    <TableCell className="text-right">
                      <Button type="button" variant="link" size="xs" className="h-auto p-0 text-status-danger hover:text-status-danger"
                        onClick={async () => {
                          await onUnregister(r.id)
                          notify('success', t('proxy.unregistered'))
                        }}>
                        {t('proxy.unregister')}
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
                {registrations.length === 0 && (
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
              <Button type="submit" size="sm" disabled={submitting || !backendId || !!forcedHostError}>
                {t('proxy.register')}
              </Button>
            </div>
          </form>
        </ScrollableDialogBody>
      </DialogContent>
    </Dialog>
  )
}
