// 实现已迁至 @jianmanager/ui（ADR-097），此处保留 re-export 维持既有导入路径。
// 本地草稿源零网络，可直接复用；二进制 / 超大判定与实例源同口径（PREVIEW_MAX_BYTES / looksBinary）。
export {
  localDraftSource,
  looksBinary,
  PREVIEW_MAX_BYTES,
  type LocalDraftFile,
} from '@jianmanager/ui/lib/file-sources'
