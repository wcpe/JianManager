import { describe, it, expect } from 'vitest'
import {
  instanceFilesCapability,
  instanceBrowseCapability,
  storageBrowseCapability,
  clientDistBrowseCapability,
  customExplorerCapability,
  browserPropsFromCapability,
} from './capability'

describe('ExplorerCapability（FR-378）', () => {
  it('instance-files 全开写', () => {
    const c = instanceFilesCapability()
    expect(c.mode).toBe('instance-files')
    expect(c.canWrite && c.canUpload && c.canChmod).toBe(true)
  })

  it('storage-browse 只读无下载', () => {
    const c = storageBrowseCapability()
    expect(c.mode).toBe('browser')
    expect(c.canWrite).toBe(false)
    expect(c.canDownload).toBe(false)
  })

  it('browserPropsFromCapability：无 action 则 readOnly', () => {
    expect(browserPropsFromCapability(storageBrowseCapability())).toEqual({
      readOnly: true,
      actions: [],
    })
  })

  it('browserPropsFromCapability：有下载 action 则非 readOnly', () => {
    const act = {
      key: 'download',
      label: 'dl',
      onAction: () => {},
    }
    const p = browserPropsFromCapability(instanceBrowseCapability(act))
    expect(p.readOnly).toBe(false)
    expect(p.actions).toHaveLength(1)
  })

  it('clientDist / custom 预设', () => {
    expect(clientDistBrowseCapability().id).toBe('client-dist-browse')
    expect(customExplorerCapability('publish-tree').mode).toBe('custom')
  })
})
