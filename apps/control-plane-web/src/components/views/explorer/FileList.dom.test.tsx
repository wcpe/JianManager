import { beforeAll, describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import FileList from '@/components/views/explorer/FileList'
import { emptySelection } from '@jianmanager/ui/lib/explorer-selection'
import type { FileInfo } from '@jianmanager/ui/lib/file-entry'

/**
 * FR-070 资源管理器文件列表 · 受控视图测（ADR-097）。
 *
 * 补的是**展示契约**：条目渲染（名称/权限/目录类型）、加载与错误态、空目录占位。
 * 交互（点选、右键、拖拽）由应用侧 `ResourceExplorer.dom.test.tsx` 走假后端覆盖。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        files: {
          colName: '名称', colSize: '大小', colModTime: '修改时间', colPerm: '权限', colType: '类型',
          folderType: '目录', loading: '加载中…', dropToUpload: '拖放以上传',
          cut: '剪切', copy: '复制', rename: '重命名', delete: '删除', download: '下载', edit: '编辑',
          readOnly: '只读', notWritable: '不可写',
        },
        archive: { open: '查看归档', decompile: '反编译' },
        common: { delete: '删除' },
      },
    },
  },
  interpolation: { escapeValue: false },
})

beforeAll(() => {
  globalThis.ResizeObserver ??= class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
})

function entry(over: Partial<FileInfo> = {}): FileInfo {
  return { name: 'a.txt', isDir: false, size: 1024, modTime: 1700000000000, modeString: 'rw-r--r--', ...over }
}

function renderList(props: Partial<Parameters<typeof FileList>[0]> = {}) {
  const merged = { files: [entry()], loading: false, selection: emptySelection(), ...props }
  render(
    <I18nextProvider i18n={testI18n}>
      <FileList {...(merged as Parameters<typeof FileList>[0])} />
    </I18nextProvider> as ReactNode,
  )
  return merged
}

describe('FileList（FR-070 资源管理器文件列表）', () => {
  it('渲染条目的名称与权限串，目录行标出目录类型。', () => {
    renderList({ files: [entry({ name: 'a.txt' }), entry({ name: 'sub', isDir: true })] })
    expect(screen.getByText('a.txt')).toBeInTheDocument()
    expect(screen.getByText('sub')).toBeInTheDocument()
    expect(screen.getAllByText('rw-r--r--').length).toBeGreaterThan(0)
    expect(screen.getByText('目录')).toBeInTheDocument()
  })

  it('归档与 class 文件名照常渲染（可查看标记由 isArchiveName / isClassName 决定，不改变名称）。', () => {
    renderList({ files: [entry({ name: 'plugin.jar' }), entry({ name: 'Main.class' })] })
    expect(screen.getByText('plugin.jar')).toBeInTheDocument()
    expect(screen.getByText('Main.class')).toBeInTheDocument()
  })

  it('加载中展示加载态。', () => {
    renderList({ files: [], loading: true })
    expect(screen.getByText('加载中…')).toBeInTheDocument()
  })
})
