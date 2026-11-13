import { describe, expect, it } from 'vitest'
import { CONFIG_FILE_EXTENSIONS, extOf, isConfigPath } from './config-path'

/**
 * 配置文件路径判定（前端侧，与后端边界同口径）。
 *
 * 这些用例直接钉住「哪些路径可以走配置端点」：`logs/latest.log` 必须为 false，
 * 否则配置页打开它就会打到 `GET /configs/read` 拿 422「仅允许操作配置文件」。
 */
describe('isConfigPath（与后端 ValidateConfigInstancePath 同口径）', () => {
  it('六种受支持扩展名', () => {
    for (const ext of CONFIG_FILE_EXTENSIONS) expect(isConfigPath(`server${ext}`)).toBe(true)
    expect([...CONFIG_FILE_EXTENSIONS]).toHaveLength(6)
  })

  it('大小写不敏感', () => {
    expect(isConfigPath('server.PROPERTIES')).toBe(true)
    expect(isConfigPath('Bukkit.Yml')).toBe(true)
    expect(isConfigPath('plugins/AllinGame/CONFIG.JSON')).toBe(true)
    expect(isConfigPath('velocity.TOML')).toBe(true)
  })

  it('嵌套路径只看最后一段', () => {
    expect(isConfigPath('plugins/Foo/config.yml')).toBe(true)
    expect(isConfigPath('config/paper-global.yaml')).toBe(true)
    expect(isConfigPath('logs/server.properties')).toBe(true)
  })

  it('非配置文件为 false（本次缺陷：logs/latest.log）', () => {
    expect(isConfigPath('logs/latest.log')).toBe(false)
    expect(isConfigPath('logs/2026-01-01-1.log.gz')).toBe(false)
    expect(isConfigPath('plugins/Essentials.jar')).toBe(false)
    expect(isConfigPath('world/level.dat')).toBe(false)
    expect(isConfigPath('LICENSE')).toBe(false)
    // 配置文件加后缀备份不再是配置文件（与后端 filepath.Ext 一致）。
    expect(isConfigPath('server.properties.bak')).toBe(false)
  })

  it('反斜杠分隔也按最后一段判定', () => {
    expect(isConfigPath('plugins\\Foo\\config.yml')).toBe(true)
    expect(isConfigPath('logs\\latest.log')).toBe(false)
  })

  it('extOf 与 filepath.Ext 同口径', () => {
    expect(extOf('server.properties')).toBe('.properties')
    expect(extOf('plugins/Foo/CONFIG.Yml')).toBe('.yml')
    expect(extOf('LICENSE')).toBe('')
    expect(extOf('.bashrc')).toBe('.bashrc')
    expect(extOf('a/b/')).toBe('')
  })

  it('空路径 / 目录路径为 false', () => {
    expect(isConfigPath('')).toBe(false)
    expect(isConfigPath('plugins/')).toBe(false)
  })
})
