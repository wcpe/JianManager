//go:build race

package stateindex

// raceEnabled 在 -race 构建下为 true。
//
// 用途：只在不带竞态检测时对**绝对耗时**下断言。竞态检测器给每次内存访问加检查，实测把
// SQLite 写入路径整体放慢 3–5 倍，绝对阈值（如验收线 50 ms）在 -race 下失去意义；而比值断言
// （切分单元 vs 不切分整轮）在两种构建下都成立，故绝对断言按本开关收窄。
const raceEnabled = true
