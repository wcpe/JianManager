import { toast } from 'sonner'
import { useInstances } from '@/api/instances'
import { useRegistrations, useCreateRegistration, useDeleteRegistration } from '@/api/registrations'
import { useResyncProxy } from '@/api/proxy'
import ProxyRegistrationsDialogView from '@/components/views/instances/ProxyRegistrationsDialog'

/**
 * 代理后端注册管理的应用接线层（ADR-097 b 范式）。
 *
 * 对话框本体是受控视图（见 components/views）；本层取注册列表与候选后端、接三个动作、把提示交给 toast。
 * 候选后端按「已注册的排除掉」在壳侧算好（视图不该为了过滤再去理解注册集合）。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function ProxyRegistrationsDialog({
  proxyId,
  proxyName,
  onClose,
}: {
  proxyId: number
  proxyName: string
  onClose: () => void
}) {
  const { data: regs } = useRegistrations(proxyId)
  const { data: backends } = useInstances({ role: 'backend' })
  const create = useCreateRegistration(proxyId)
  const del = useDeleteRegistration(proxyId)
  const resync = useResyncProxy()

  const registeredBackendIds = new Set(regs?.map((r) => r.backendId))
  const candidates = (backends || []).filter((b) => !registeredBackendIds.has(b.id))

  return (
    <ProxyRegistrationsDialogView
      proxyName={proxyName}
      onClose={onClose}
      registrations={regs ?? []}
      candidates={candidates.map((b) => ({ id: b.id, name: b.name, serverPort: b.serverPort }))}
      onRegister={async (payload) => {
        const resp = await create.mutateAsync(payload)
        return { warning: resp?.data?.warning }
      }}
      onUnregister={async (registrationId) => {
        await del.mutateAsync(registrationId)
      }}
      onResync={async () => {
        const data = await resync.mutateAsync(proxyId)
        return { secretConsistent: data.secretConsistent, warnings: data.warnings }
      }}
      notify={(kind, message) => {
        if (kind === 'success') toast.success(message)
        else if (kind === 'warning') toast.warning(message)
        else toast.error(message)
      }}
    />
  )
}
