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
  useTestModel,
  useTestProvider,
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
 * 分类码 → 处置建议。
 *
 * 为什么在前端做这层映射：分类码本身是给人看的缩写（upstream_auth），
 * 而管理员需要的是**下一步动作**。密钥错要「换密钥」，限流要「稍后再试」，
 * 被准入挡住要「放行目标地址」——三者完全不同的修法，
 * 只显示分类码等于让管理员自己去查文档。
 */
function probeHint(code: string | undefined, t: (key: string, fallback: string) => string): string {
  switch (code) {
    case 'upstream_auth':
      return t('assistant.hintAuth', '上游拒绝认证：请检查密钥是否正确、是否过期，以及该密钥是否被允许使用此模型。')
    case 'upstream_rate_limited':
      return t('assistant.hintRateLimited', '上游限流：请稍后再试，或检查该密钥的配额。')
    case 'upstream_rejected':
      return t('assistant.hintRejected', '上游拒绝了请求：请确认模型标识与所选协议是否匹配。')
    case 'upstream_error':
      return t('assistant.hintUpstreamError', '上游服务端错误：问题在供应商一侧，请稍后重试。')
    case 'upstream_timeout':
      return t('assistant.hintTimeout', '请求超时：请检查目标地址是否可达，以及网络出口是否正常。')
    case 'upstream_unreachable':
      return t('assistant.hintUnreachable', '无法连接到上游：请检查域名解析与网络连通性。')
    case 'upstream_stream_truncated':
      return t('assistant.hintTruncated', '上游响应意外中断：可能是链路不稳定或上游提前关闭了连接。')
    case 'egress_denied':
      return t('assistant.hintEgressDenied', '被出站准入拒绝：目标地址属于内网或被禁止的范围。')
    case 'egress_proxy_unsupported':
      return t('assistant.hintProxyUnsupported', '该目标会经代理出站，无法固定目标 IP，因此被拒绝。请为该域名配置 NO_PROXY，或改用直连。')
    case 'tool_call_invalid':
      return t('assistant.hintToolCallInvalid', '工具调用参数不完整：该模型可能不擅长严格按 schema 输出工具参数。')
    default:
      return ''
  }
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
  const testProvider = useTestProvider()
  const testModel = useTestModel()

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

  /**
   * 探测类操作的提示（供应商测试 / 模型测试共用）。
   *
   * 失败不是「保存失败」那种错误提示：`ok:false` 是**结论**，要带上分类码对应的
   * 处置建议（密钥错了就提示换密钥，限流就提示稍后再试），而不是只甩一句英文。
   */
  const reportProbe = (
    result: { ok: boolean; latencyMs: number; classifiedError?: string; message?: string },
    label: string,
  ) => {
    if (result.ok) {
      toast.success(t('assistant.testOk', '{{label}}连通正常（{{ms}} ms）', { label, ms: result.latencyMs }))
      return
    }
    const hint = probeHint(result.classifiedError, t)
    toast.error(hint || result.message || t('assistant.testFailed', '{{label}}测试失败', { label }))
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
      onTestProvider={async (id: number) => {
        try {
          reportProbe(await testProvider.mutateAsync(id), t('assistant.providerLabel', '供应商'))
        } catch (err) {
          toast.error(errMessage(err, t('assistant.testRequestFailed', '测试请求失败')))
        }
      }}
      onTestModel={async (id: number, withTools: boolean) => {
        try {
          reportProbe(await testModel.mutateAsync({ id, withTools }), t('assistant.modelLabel', '模型'))
        } catch (err) {
          toast.error(errMessage(err, t('assistant.testRequestFailed', '测试请求失败')))
        }
      }}
    />
  )
}
