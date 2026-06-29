package version

// Version 是当前发布版本号，由发版流程维护，对齐最新已发版本（消除与 CHANGELOG 漂移）。
// 真值由发布管线（FR-173）在 go build 时经 -ldflags 注入，覆盖此默认值：
// go build -ldflags "-X github.com/wcpe/JianManager/internal/version.Version=vX.Y.Z"
var Version = "0.12.0"
