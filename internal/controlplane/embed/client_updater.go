package embed

import "embed"

// clientUpdaterFS 内嵌客户端 OTA 更新器两件套 jar（FR-107 运营方接入引导）。
// jar 为 client-updater Gradle 构建产物，经 `make embed-client-updater` 注入到 embed/client-updater/；
// 目录内的 .gitignore 保证未注入 jar 时该目录依然存在、go:embed 不致编译失败。
//
//go:embed all:client-updater
var clientUpdaterFS embed.FS

// ClientUpdaterEmbeddedVersion 是内嵌客户端更新器 jar 的版本号（构建期常量，与 client-updater
// Gradle `version`（0.1.0-SNAPSHOT）同步）。jar 的 MANIFEST 不稳定携带版本，故以此常量作展示用。
// 升级客户端更新器时同步本常量。
const ClientUpdaterEmbeddedVersion = "0.1.0"

// ClientUpdaterEmbeddedCoreVersion 是内嵌 updater-core 的**整数版本号**，作 manifest `agent.core.version`
// 的来源（FR-193，见 ADR-045 改写）。客户端 FR-091 的自更新只把**更高** `agent.core.version` 暂存为
// pending（int 比较），故此值须为单调整数、随 CP/更新器版本演进只升不降。`ClientUpdaterEmbeddedVersion`
// 是给运营看的展示串（语义版本），本常量是给客户端用的单调整数轴——升级内嵌 updater-core 时一并 +1。
// 默认 1；可经构建期 `-ldflags "-X ...embed.ClientUpdaterEmbeddedCoreVersion=N"` 覆盖（须为整数字面量）。
var ClientUpdaterEmbeddedCoreVersion = "1"

// WedgeJar 返回内嵌的楔子 jar 字节；未经 `make embed-client-updater` 注入时返回 nil。
func WedgeJar() []byte {
	b, err := clientUpdaterFS.ReadFile("client-updater/wedge.jar")
	if err != nil {
		return nil
	}
	return b
}

// UpdaterCoreJar 返回内嵌的 updater-core jar 字节；未注入时返回 nil。
func UpdaterCoreJar() []byte {
	b, err := clientUpdaterFS.ReadFile("client-updater/updater-core.jar")
	if err != nil {
		return nil
	}
	return b
}
