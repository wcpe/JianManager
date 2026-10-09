/**
 * 共享文件浏览器的数据契约（FR-213）。
 *
 * 这些类型**与具体后端无关**：实例工作目录（`@/api/files`）、客户端分发 manifest（FR-214）
 * 各自提供一个 {@link FileBrowserSource} 适配器即可复用同一浏览/预览/下载 UI。
 * 组件主体只依赖本文件的类型，不 import 任何后端 api。
 */
import type { ReactNode } from 'react'

/** 文件浏览器内一个节点（目录或文件）。 */
export interface FileEntry {
  /** 相对根、以 "/" 分隔的路径（唯一键）。 */
  path: string
  /** 展示名（通常为 path 末段）。 */
  name: string
  /** 是否目录。 */
  isDir: boolean
  /** 文件字节大小（目录可省略/为 0）。 */
  size?: number
  /** 修改时间（unix 秒，可选，仅展示）。 */
  modTime?: number
}

/**
 * 预览内容结果（由 {@link FileBrowserSource.readContent} 解析后返回）。
 * 降级由本类型**显式表达**——组件不再自行猜测「是不是二进制 / 是不是太大」，
 * 而由各数据源适配器按自身后端口径判定并返回对应 kind。
 */
export type PreviewContent =
  /** 可高亮文本（含配置/json）。truncated=后端因过大截断了内容。 */
  | { kind: 'text'; content: string; truncated?: boolean }
  /** 二进制：不可文本预览，仅可下载。 */
  | { kind: 'binary' }
  /** 超大文件：不读全量，仅可下载。size 为字节数（展示用）。 */
  | { kind: 'too-large'; size: number }
  /** 读取失败。 */
  | { kind: 'error'; message: string }

/**
 * 文件浏览器数据源（注入）。
 * 实例与客户端分发各实现一份；组件据此拉目录、读预览、触发下载。
 */
export interface FileBrowserSource {
  /**
   * 列目录。两种形态由 {@link flat} 区分：
   * - 懒加载分层（flat 省略/false）：传入目录 path，返回该层直接子项。
   * - 扁平全量（flat=true）：忽略 path，一次性返回全部条目，组件内部 {@link buildTree} 建树。
   */
  list: (dirPath: string) => Promise<FileEntry[]>
  /** 是否为「扁平全量」数据源。 */
  flat?: boolean
  /**
   * 读取文件内容用于预览。
   * 适配器负责二进制/超大判定，返回对应 {@link PreviewContent}。
   */
  readContent: (entry: FileEntry) => Promise<PreviewContent>
  /** 下载单文件（适配器触发浏览器下载）。省略则不显示下载入口。 */
  download?: (entry: FileEntry) => void | Promise<void>
}

/**
 * 文件浏览器的额外行操作（可操作态）。
 * 组件**不内置任何写端点**——重命名/删除/编辑等全部经此注入，由调用方实现。
 */
export interface FileBrowserAction {
  /** 稳定键。 */
  key: string
  /** 菜单/按钮文案。 */
  label: string
  /** 图标（可选）。 */
  icon?: ReactNode
  /** 仅对满足条件的条目显示（如仅文件 `(e) => !e.isDir`）。省略=全部可见。 */
  visible?: (entry: FileEntry) => boolean
  /** 触发回调。 */
  onAction: (entry: FileEntry) => void
}
