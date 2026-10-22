import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useAuthStore } from '@/stores/auth'
import { isPlatformAdmin } from '@/lib/shared/roles'
import {
  useAssistantAdminModels,
  useAssistantModels,
  useAssistantProviders,
  useCreateModel,
  useCreateProvider,
  useDeleteModel,
  useDeleteProvider,
  useSetDefaultModel,
  useUpdateModel,
  useUpdateProvider,
} from '@/api/assistant'
import type { ModelPayload, ProviderPayload } from '@/api/assistant'
import { AssistantSettingsView } from '@/components/views/settings/AssistantSettingsView'

/** 从 mutation 错误里取后端消息，缺省回落到兜底文案。 */
function errMessage(err: unknown, fallback: string): string {
  const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
  return msg || fallback
}

/**
 * AI 助手配置页容器（FR-514，见 ADR-105）。
 *
 * 取数、mutation 与 toast 都在这里；视图不 import 任何 API 模块。
 * 管理面的真实门禁在服务端（IsPlatformAdmin），此处的角色判定只用来决定
 * 是否发起请求与展示提示——非管理员不该看到一堆 403。
 */
export default function AssistantSettingsPage() {
  const { t } = useTranslation()
  const role = useAuthStore((s) => s.role)
  const admin = isPlatformAdmin(role)

  const providersQuery = useAssistantProviders(admin)
  const modelsQuery = useAssistantAdminModels(admin)
  const availableQuery = useAssistantModels()

  const createProvider = useCreateProvider()
  const updateProvider = useUpdateProvider()
  const deleteProvider = useDeleteProvider()
  const createModel = useCreateModel()
  const updateModel = useUpdateModel()
  const deleteModel = useDeleteModel()
  const setDefaultModel = useSetDefaultModel()

  const saving =
    createProvider.isPending ||
    updateProvider.isPending ||
    deleteProvider.isPending ||
    createModel.isPending ||
    updateModel.isPending ||
    deleteModel.isPending ||
    setDefaultModel.isPending

  /** 统一的「执行 → 提示 → 返回是否成功」包装。 */
  const run = async (fn: () => Promise<unknown>, successKey: string): Promise<boolean> => {
    try {
      await fn()
      toast.success(t(successKey, '已保存'))
      return true
    } catch (err) {
      toast.error(errMessage(err, t('assistant.saveFailed', '保存失败')))
      return false
    }
  }

  return (
    <AssistantSettingsView
      isPlatformAdmin={admin}
      providers={providersQuery.data}
      models={modelsQuery.data}
      availableModels={availableQuery.data?.items}
      defaultModelId={availableQuery.data?.defaultModelId ?? null}
      isLoading={admin && (providersQuery.isLoading || modelsQuery.isLoading)}
      isError={admin && (providersQuery.isError || modelsQuery.isError)}
      saving={saving}
      onCreateProvider={(payload: ProviderPayload) =>
        run(() => createProvider.mutateAsync(payload), 'assistant.providerCreated')
      }
      onUpdateProvider={(id: number, payload: ProviderPayload) =>
        run(() => updateProvider.mutateAsync({ id, payload }), 'assistant.providerUpdated')
      }
      onDeleteProvider={(id: number) =>
        run(() => deleteProvider.mutateAsync(id), 'assistant.providerDeleted')
      }
      onCreateModel={(payload: ModelPayload) =>
        run(() => createModel.mutateAsync(payload), 'assistant.modelCreated')
      }
      onUpdateModel={(id: number, payload: ModelPayload) =>
        run(() => updateModel.mutateAsync({ id, payload }), 'assistant.modelUpdated')
      }
      onDeleteModel={(id: number) => run(() => deleteModel.mutateAsync(id), 'assistant.modelDeleted')}
      onSetDefaultModel={(modelId: number | null) =>
        run(() => setDefaultModel.mutateAsync(modelId), 'assistant.defaultSet')
      }
    />
  )
}
