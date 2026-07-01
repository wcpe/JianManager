import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/** 签名密钥来源（FR-248，见 ADR-052）：env 注入 / 生产自动生成 / dev 回退。 */
export type ClientSignKeySource = 'env' | 'generated' | 'dev'

/** OTA manifest 签名公钥信息（FR-248）。私钥绝不出服务端，仅暴露公钥供配到客户端 updater-core。 */
export interface ClientSignKey {
  /** 公钥 X.509 SubjectPublicKeyInfo DER 的 base64（与客户端内置公钥对照/配置用）。 */
  publicKey: string
  /** 公钥版本标识（默认 k1，须与客户端内置公钥 keyId 一致）。 */
  keyId: string
  /** 密钥来源，供面板展示徽章。 */
  source: ClientSignKeySource
}

/**
 * 查询 OTA 签名公钥（FR-248，见 ADR-052）。仅平台管理员；后端 signer 未配置 → 503。
 * 供「签名公钥」卡片展示公钥 + keyId + 来源徽章，运营者据此配到客户端 updater-core。
 */
export function useClientSignKey() {
  return useQuery({
    queryKey: ['client-dist-sign-key'],
    queryFn: async (): Promise<ClientSignKey> => {
      const { data } = await api.get<ClientSignKey>('/client-dist/sign-key')
      return data
    },
  })
}
