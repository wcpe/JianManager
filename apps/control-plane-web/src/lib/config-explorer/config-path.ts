/*
 * 配置文件路径判定（前端侧）。
 *
 * 后端 `ValidateConfigInstancePath`（internal/controlplane/service/file.go）只允许
 * `.properties / .yml / .yaml / .toml / .json / .conf` 六种扩展名走配置面，其余一律
 * 「仅允许操作配置文件」422。前端必须在**发请求之前**用同一口径判定，否则配置页
 * （左侧目录树呈现工作目录全部文件）双击 `logs/latest.log` 这类普通文件就会打到
 * `GET /configs/read` 拿到 422。
 *
 * 与后端的对应关系（两边须保持一致，不要单侧放宽）：
 * - `filepath.Ext` ≡ `extOf()`：只看**最后一段**的**最后一个点**之后的串；
 * - `strings.ToLower` ≡ 统一小写后再比；
 * - 后端允许清单不含 `.txt`（注意 `isConfigFile` 是发现用的更宽集合，不是这条边界）。
 */

/** 受支持的配置文件扩展名（含前导点，小写）。与后端 `isConfigPath` 完全一致。 */
export const CONFIG_FILE_EXTENSIONS = ['.properties', '.yml', '.yaml', '.toml', '.json', '.conf'] as const

const CONFIG_FILE_EXTENSION_SET: ReadonlySet<string> = new Set(CONFIG_FILE_EXTENSIONS)

/**
 * 取路径的扩展名（小写，含前导点）；无扩展名返回空串。
 *
 * 与 `filepath.Ext` 对齐：只看最后一段，且 `.bashrc` 这类以点开头的名字的
 * `Ext` 是整段（Go 的 `Ext` 取最后一个点起的部分，`".bashrc"` → `".bashrc"`），
 * 这里同样返回 `'.bashrc'`——它不在允许清单内，结果仍为 false。
 */
export function extOf(path: string): string {
  const name = lastSegment(path)
  const dot = name.lastIndexOf('.')
  if (dot < 0) return ''
  return name.slice(dot).toLowerCase()
}

/** 取路径最后一段（兼容 `\\` 分隔符；尾部斜杠先剥掉）。 */
function lastSegment(path: string): string {
  const normalized = path.replace(/\\/g, '/')
  const trimmed = normalized.replace(/\/+$/, '')
  const idx = trimmed.lastIndexOf('/')
  return idx < 0 ? trimmed : trimmed.slice(idx + 1)
}

/**
 * 路径是否可走配置端点（配置编辑器 / 配置版本）。
 *
 * 非配置文件（日志、jar、dlc、无扩展名文件等）返回 false——调用方据此退回普通
 * 文件能力（`files/read` + CodeEditor / `files/versions` + 文件版本抽屉）。
 */
export function isConfigPath(path: string): boolean {
  if (!path) return false
  const name = lastSegment(path)
  if (!name || name === '.' || name === '..') return false
  return CONFIG_FILE_EXTENSION_SET.has(extOf(name))
}
