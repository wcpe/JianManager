/**
 * 原生 `webkitGetAsEntry()` entry → {@link FileSystemEntryLike} 适配器（FR-250 / BUG-F）。
 *
 * 浏览器拖拽经 `DataTransferItem.webkitGetAsEntry()` 得到的是**回调式**原生 entry：
 * 文件 `entry.file(successCb, errorCb)`、目录 `entry.createReader().readEntries(cb, errCb)`，
 * 且目录 entry **没有** `readEntries` 方法。而 {@link collectEntries} 递归消费的是 Promise 化的
 * `FileSystemEntryLike`（`file(): Promise<File>`、`readEntries(): Promise<...>`）。
 * 直接把原生 entry 喂进去 → 文件 `file()` 无回调返回 undefined、目录无 `readEntries` 被跳过，
 * 拖拽整体失效。本适配器把原生回调式补成 Promise 式，抹平这层差异。
 */
import type { FileSystemEntryLike } from './client-publish-wizard'

/**
 * 浏览器原生 `FileSystemEntry`（回调式）的最小结构（FR-250）。
 * DOM lib 的 FileSystemFileEntry/FileSystemDirectoryEntry 未在所有 tsconfig 下齐备，
 * 且其 `file`/`readEntries` 为回调式难在单测构造，故显式声明所需回调形态。
 */
export interface NativeFileSystemEntry {
  isFile: boolean
  isDirectory: boolean
  fullPath: string
  name: string
  /** 文件 entry：回调式取 File（`file(onSuccess, onError)`）。 */
  file?: (onSuccess: (file: File) => void, onError?: (err: unknown) => void) => void
  /** 目录 entry：取一个分批读子项的 reader。 */
  createReader?: () => NativeDirectoryReader
}

/** 原生目录 reader：`readEntries(cb)` 一次返回一批，读空数组表示读完（FR-250）。 */
export interface NativeDirectoryReader {
  readEntries: (
    onSuccess: (entries: NativeFileSystemEntry[]) => void,
    onError?: (err: unknown) => void,
  ) => void
}

/**
 * 反复调用 `reader.readEntries` 直到返回空数组，聚合目录**全部**直接子项（FR-250）。
 * 原生 `readEntries` 一次只返回一批（Chromium 约 100 项），必须循环读到空，
 * 否则大目录会漏掉第一批之后的项。每批子项递归 {@link adaptEntry} 适配为 Promise 形态。
 *
 * 下一批读取用 `queueMicrotask` 排到下一微任务再发起，而非在成功回调里同步递归：
 * ① 避免大目录深度同步递归爆栈；② 防御同步实现的 reader（若其在回调返回后才更新
 * 内部游标，同步重入会读到旧游标而死循环）——真实浏览器 reader 异步，此举无害且更稳。
 */
function readAllEntries(reader: NativeDirectoryReader): Promise<FileSystemEntryLike[]> {
  return new Promise((resolve, reject) => {
    const acc: FileSystemEntryLike[] = []
    const readBatch = () => {
      reader.readEntries((batch) => {
        if (batch.length === 0) {
          resolve(acc)
          return
        }
        for (const child of batch) acc.push(adaptEntry(child))
        queueMicrotask(readBatch)
      }, reject)
    }
    readBatch()
  })
}

/**
 * 把一个原生回调式 entry 适配为 Promise 化的 {@link FileSystemEntryLike}（FR-250 / BUG-F）。
 * 文件 entry → `file()` 返回 Promise<File>（Promise 化原生 `file(cb, errCb)`）；
 * 目录 entry → `readEntries()` 返回 Promise（内部 {@link readAllEntries} 循环读全 + 递归适配）。
 * 非文件非目录的 entry（罕见）两个方法皆缺省，交由 collectEntries 自然跳过。
 */
export function adaptEntry(native: NativeFileSystemEntry): FileSystemEntryLike {
  return {
    isFile: native.isFile,
    isDirectory: native.isDirectory,
    fullPath: native.fullPath,
    file:
      native.isFile && native.file
        ? () =>
            new Promise<File>((resolve, reject) => {
              native.file!(resolve, reject)
            })
        : undefined,
    readEntries:
      native.isDirectory && native.createReader
        ? () => readAllEntries(native.createReader!())
        : undefined,
  }
}
