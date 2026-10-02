//go:build race

package ingest

// raceEnabled 在 -race 构建下为 true。
//
// 用途：**只在竞态检测构建下豁免两条登记延迟预算用例**（见 register_lock_contention_test.go 的
// 说明）。竞态检测器给每次内存访问加检查，实测把索引写入路径整体放慢 10–20 倍（同一夹具：
// 非 -race 单次登记最坏 35.6ms、轮时长 1.91s；-race 下单单元被放大到 ~1s 量级）⇒ 以「墙上时钟」
// 为口径的 1s 预算在 -race 下失去判别力 ✗；而**结构性**断言（登记在该轮结束之前返回、只等短临界区）
// 在两种构建下都成立 ✓，故只豁免绝对阈值那一层。
const raceEnabled = true
