package version

// Version 是当前发布版本号，由发版流程维护。
// 构建时可通过 -ldflags 覆盖：go build -ldflags "-X github.com/wxys233/JianManager/internal/version.Version=vX.Y.Z"
var Version = "0.3.0"
