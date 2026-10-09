import { precheckClientFiles, uploadClientFilesBatch } from '@/api/clientVersions'
import type { ClientFileResult } from '@/api/clientVersions'

// 编排逻辑与类型已回迁应用侧（原 ADR-097 迁包已撤销）；此处转出，调用点零改动。
export * from '@/lib/efficient-upload'

import { uploadFilesEfficient as runEfficientUpload } from '@/lib/efficient-upload'
import type { EfficientUploadEntry, EfficientUploadOptions } from '@/lib/efficient-upload'
import { uploadFileChunked } from './chunkedUpload'

/**
 * 批量上传一组文件，返回 key → ClientFileResult 映射（与逐文件 uploadFileChunked 同构结果）。
 * 任一任务失败即 fail-fast 抛错（调用方保草稿可重试）；取消抛 AbortError。
 *
 * 接线层（ADR-097）：编排逻辑在包内，三个取数依赖（预查 / 聚合批 / 分块）在这里注入；
 * 对外签名与迁移前一致，消费方（ClientPublishPage）零改动。
 */
export async function uploadFilesEfficient(
  channelId: string,
  entries: EfficientUploadEntry[],
  opts: EfficientUploadOptions = {},
): Promise<Map<string, ClientFileResult>> {
  return runEfficientUpload(
    {
      precheck: precheckClientFiles,
      uploadBatch: uploadClientFilesBatch,
      uploadChunked: uploadFileChunked,
    },
    channelId,
    entries,
    opts,
  )
}
