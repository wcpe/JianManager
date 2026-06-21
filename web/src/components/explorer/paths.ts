/**
 * 资源管理器路径工具（FR-070）。
 * 工作目录相对路径统一用正斜杠、无前导斜杠（与后端 validatePath 约定一致）。
 * 纯函数，便于 vitest（node 环境）单测。
 */

/** 拼接目录与名字为相对路径（dir 为空表示根目录）。 */
export function joinPath(dir: string, name: string): string {
  if (!dir) return name
  return `${dir}/${name}`
}

/** 取相对路径的父目录（根下的项返回空串）。 */
export function parentDir(path: string): string {
  const idx = path.lastIndexOf('/')
  return idx === -1 ? '' : path.slice(0, idx)
}

/** 取相对路径的最后一段（文件名/目录名）。 */
export function baseName(path: string): string {
  const idx = path.lastIndexOf('/')
  return idx === -1 ? path : path.slice(idx + 1)
}

/** 取文件扩展名（小写，不含点）；无扩展名返回空串。 */
export function extName(name: string): string {
  const base = baseName(name)
  const dot = base.lastIndexOf('.')
  if (dot <= 0) return '' // 无点或以点开头（隐藏文件无扩展名）
  return base.slice(dot + 1).toLowerCase()
}

/**
 * 把路径切成面包屑段：每段含累积路径与显示名。
 * 例：'plugins/Essentials' → [{name:'plugins',path:'plugins'},{name:'Essentials',path:'plugins/Essentials'}]
 * 空路径返回空数组（根）。
 */
export function breadcrumbs(path: string): { name: string; path: string }[] {
  if (!path) return []
  const parts = path.split('/').filter(Boolean)
  const out: { name: string; path: string }[] = []
  let acc = ''
  for (const part of parts) {
    acc = acc ? `${acc}/${part}` : part
    out.push({ name: part, path: acc })
  }
  return out
}

/**
 * 判断 child 是否在 dir 之内（或等于 dir）。用于防止把目录移动/粘贴到自身或其子目录。
 * dir 为空（根）时视为包含一切。
 */
export function isWithin(dir: string, child: string): boolean {
  if (dir === '') return true
  return child === dir || child.startsWith(`${dir}/`)
}

/** 校验文件/目录名是否合法（非空、无路径分隔符、非 . / ..）。 */
export function isValidName(name: string): boolean {
  if (!name || name === '.' || name === '..') return false
  if (name.includes('/') || name.includes('\\')) return false
  return true
}
