// 实现已回迁应用侧（原 ADR-097 迁包已撤销），此处保留 re-export 维持既有导入路径。
// 本地草稿源零网络，可直接复用；二进制 / 超大判定与实例源同口径（PREVIEW_MAX_BYTES / looksBinary）。
export {
  localDraftSource,
  looksBinary,
  PREVIEW_MAX_BYTES,
  type LocalDraftFile,
} from '@/lib/file-sources'
