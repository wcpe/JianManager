package embed

import _ "embed"

// Worker 一键安装脚本（FR-080，见 ADR-020 §2「也可由 CP 静态托管」）。
//
// 一键命令拼 `curl <cp>/install-worker.sh | sh` / `iwr <cp>/install-worker.ps1 | iex`，
// 故 CP 必须自托管这两个脚本（否则 curl/iwr 404、安装失败——BUG-B 根因）。
// 脚本是仓库内文本源（非构建产物），直接 go:embed 进 CP 二进制，单二进制部署即开箱可拉。
//
// 单一真源：canonical 副本在仓库根 `scripts/install-worker.{sh,ps1}`（随发布分发 / 手动拷贝用），
// 本目录副本由 `make embed-install-scripts` 从其同步；`install_scripts_test.go` 守护两者字节一致防漂移。

//go:embed install-scripts/install-worker.sh
var installWorkerSh []byte

//go:embed install-scripts/install-worker.ps1
var installWorkerPs1 []byte

// InstallWorkerScriptSh 返回 Linux/macOS 一键安装脚本（POSIX sh）字节。
func InstallWorkerScriptSh() []byte { return installWorkerSh }

// InstallWorkerScriptPs1 返回 Windows PowerShell 一键安装脚本字节。
func InstallWorkerScriptPs1() []byte { return installWorkerPs1 }
