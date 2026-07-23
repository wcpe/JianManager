import { describe, it, expect } from 'vitest'
import { joinAbsPath, isPermissionErrorMessage } from './import-server-path'

describe('import-server-path（FR-374）', () => {
  it('joinAbsPath Unix', () => {
    expect(joinAbsPath('/home/wxys233', 'server.properties')).toBe('/home/wxys233/server.properties')
    expect(joinAbsPath('/home/wxys233/', 'server.properties')).toBe('/home/wxys233/server.properties')
  })

  it('joinAbsPath Windows', () => {
    expect(joinAbsPath('C:\\srv\\paper', 'server.properties')).toBe('C:\\srv\\paper\\server.properties')
  })

  it('isPermissionErrorMessage', () => {
    expect(isPermissionErrorMessage('permission denied')).toBe(true)
    expect(isPermissionErrorMessage('没有权限读取该目录')).toBe(true)
    expect(isPermissionErrorMessage('路径不存在')).toBe(false)
  })
})
