// 视图已迁至 @jianmanager/ui（ADR-097）；本层只做内嵌更新器取数、密钥揭示与 wedge 下载接线。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { ClientIntegrationGuideView } from '@jianmanager/ui/components/views/client-dist/ClientIntegrationGuideView'
import {
  useUpdaterJarsInfo,
  downloadUpdaterJar,
  useRevealClientKey,
  type ClientPullKey,
} from '@/api/clientChannels'

/**
 * 客户端更新器接入指引（FR-107 / FR-259）的接线层（ADR-097）。面向运营方：在频道详情一页拿齐——下载楔子、
 * 该频道专属 jm-updater.json、启动器 JVM 参数、行为说明，照做即可接入并下发玩家。
 *
 * FR-259 起 core 不再随整合包附带：整合包只带 wedge.jar（~30KB），首次启动楔子自动经
 * API 根 endpoint 拼接 updater-core 端点并拉取（gradle-wrapper 模式，见 FR-258）。
 *
 * 展示层已归包（`ClientIntegrationGuideView`），此处只保留应用侧职责：
 * - 取数：内嵌更新器 jar 信息（`useUpdaterJarsInfo`）注入视图；
 * - **密钥揭示（敏感边界）**：选中密钥与已揭示明文由本层持有，明文只在「用户主动选择 + 后端 reveal 成功」
 *   时落定、改选或失败一律清空（与迁包前逐字一致），只经 props 交给视图用于拼 jm-updater.json；
 * - wedge.jar 下载（`downloadUpdaterJar`）与全部 toast（复制结果 / 下载前置校验 / 下载失败 / 揭示结果）。
 * 保留原路径与原默认导出、原 props 签名，调用点（频道工作台「接入指引」Tab）零改动。
 */
export default function ClientIntegrationGuide({ channelId, keys }: { channelId: string; keys: ClientPullKey[] }) {
  const { t } = useTranslation()
  const { data: jars } = useUpdaterJarsInfo()
  const revealKey = useRevealClientKey()
  // 选中密钥 id（'' = 不自动填入，保留占位）与已揭示明文：上提本层（选谁触发揭示请求），改选/失败清空明文。
  const [selectedKeyId, setSelectedKeyId] = useState('')
  const [revealedKeyPlaintext, setRevealedKeyPlaintext] = useState('')

  /**
   * 选择密钥并揭示明文：先落选中值与清空上一次明文（下拉立即响应），再请后端 reveal。
   * 不可查看明文的密钥不发请求、直接提示；失败只提示不改选中值，明文保持为空（视图侧因此仍禁用配置下载）。
   */
  const revealSelectedKey = async (keyId: string) => {
    setSelectedKeyId(keyId)
    setRevealedKeyPlaintext('')
    const key = keys.find((item) => String(item.id) === keyId)
    if (!key) return
    if (!key.revealable) {
      toast.error(t('clientGuide.keyNotRevealable', '此密钥不可查看明文，请在「拉取密钥」Tab 编辑为已知值后再选择。'))
      return
    }
    try {
      const res = await revealKey.mutateAsync({ channelId, keyId: key.id })
      setRevealedKeyPlaintext(res.key)
      toast.success(t('clientGuide.keyFilled', '已填入所选密钥'))
    } catch {
      toast.error(t('clientGuide.keyRevealFailed', '读取密钥明文失败'))
    }
  }

  return (
    <ClientIntegrationGuideView
      channelId={channelId}
      keys={keys}
      jars={jars}
      selectedKeyId={selectedKeyId}
      revealedKeyPlaintext={revealedKeyPlaintext}
      revealing={revealKey.isPending}
      onSelectKey={(keyId) => void revealSelectedKey(keyId)}
      onDownloadWedge={() => downloadUpdaterJar('wedge')}
      onNotify={(level, message) => (level === 'success' ? toast.success(message) : toast.error(message))}
    />
  )
}
