// Package embed 把一键安装脚本嵌进 CP 二进制（脚本也可由 CP 静态托管）。
//
// # 为什么嵌包和脚本放在同一个目录
//
// `go:embed` **不允许 `..` 路径**（编译器直接拒绝），所以声明 embed 的 .go 文件必须与
// 被嵌文件同目录（或其子目录）。
//
// 这样做的收益是**脚本只有一份**：真源就在这儿，嵌入也读这儿。
// 不再需要 `make embed-install-scripts` 从 scripts/ 复制到 internal/controlplane/embed/
// 再嵌入——那套复制法的代价是「改了真源忘同步」会让内嵌的悄悄变成旧版。
//
// # 用途
//
// 一键命令拼 `curl <cp>/install-worker.sh | sh` /
// `iex (iwr <cp>/install-worker.ps1 -UseBasicParsing).Content`，
// CP 必须自托管这两个脚本，否则 curl/iwr 404、安装失败（BUG-B 根因）。
package embed

import _ "embed"

//go:embed install-worker.sh
var installWorkerSh []byte

//go:embed install-worker.ps1
var installWorkerPs1 []byte

// InstallWorkerScriptSh 返回 Linux/macOS 一键安装脚本（POSIX sh）字节。
func InstallWorkerScriptSh() []byte { return installWorkerSh }

// InstallWorkerScriptPs1 返回 Windows PowerShell 一键安装脚本字节。
func InstallWorkerScriptPs1() []byte { return installWorkerPs1 }
