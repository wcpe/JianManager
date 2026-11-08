package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/archive"
)

// 本文件是 ADR-098 §5「实例本地 gz 长期吸存」在**真实生产数据**上的验收用例。
//
// 它补充的是 log_instance_gz_adopt_test.go 覆盖不到的那一层：那边用 t.TempDir()
// 造三五个假 .gz 验语义，这边用现场 `$JM_FR501_ADOPT_SERVERS` 下 19 个实例、上千个
// 真实 .gz 验**规模与副作用**——尤其是 ADR §5 第 4 条（本地 gz 一律不动）在真文件上
// 是否成立，以及第二轮是否真的全部幂等跳过。
//
// 门控（缺任一即 skip，不影响常规 CI）：
//
//	JM_FR501_ADOPT_SERVERS  实例根目录（如 /srv/game/instances）
//	JM_FR501_ADOPT_ARCHIVE  归档目的地根目录（必须可写；用例只写这个目录）
//
// 为什么必须门控而不是默认跑：它会真的读 1157 个生产文件、真的往归档目录写 6.8MB 副本，
// 属于「现场取证」而非「单元回归」。默认跳过，CI 不受影响。

// fr501AdoptDirs 扫描实例根目录下的 logs/ 子目录，构造 uuid→work_dir 映射。
//
// 与生产 newInstanceGzAdopter 的 instanceDirs 同形态：真实实现从 process.Manager 取
// WorkDir，这里从磁盘布局取——两者产出的都是「uuid → 含 logs/ 的工作目录」。
func fr501AdoptDirs(root string) (map[string]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if st, err := os.Stat(filepath.Join(dir, "logs")); err != nil || !st.IsDir() {
			continue
		}
		// 生产里 key 是实例 UUID；这里用目录名（稳定且可复现），
		// 分区键前缀仍是 "inst:<name>"，与生产同一形态。
		out["disk-"+e.Name()] = dir
	}
	return out, nil
}

// fr501CountLocalGz 统计实例根下所有 logs/*.gz 的数量与总字节，并给出「清单指纹」。
//
// 指纹含 path + mtime + size：只比数量会漏掉「删一个、加一个」这种掉包，
// 而 ADR §5 第 4 条要求的是**这些文件原样还在**。
func fr501CountLocalGz(t *testing.T, root string) (count int, bytes int64, fingerprint string) {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".gz") {
			return nil
		}
		if filepath.Base(filepath.Dir(p)) != "logs" {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		count++
		bytes += info.Size()
		lines = append(lines, p+"|"+info.ModTime().UTC().Format(time.RFC3339Nano)+"|"+fr501Itoa(info.Size()))
		return nil
	})
	require.NoError(t, err)
	sort.Strings(lines)
	return count, bytes, strings.Join(lines, "\n")
}

func fr501Itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf []byte
	for v > 0 {
		buf = append([]byte{byte('0' + v%10)}, buf...)
		v /= 10
	}
	if neg {
		return "-" + string(buf)
	}
	return string(buf)
}

// TestE2E_InstanceGzAdoptOnRealProductionData 在真实生产 .gz 上跑两轮：
//
//	第一轮：全部首登记（registered == scanned、failed == 0、localDeleted == 0）
//	第二轮：全部幂等跳过（registered == 0、alreadyRegistered == scanned）
//	两轮之间与之后：本地 .gz 的数量、字节、清单指纹**完全不变**（ADR §5 第 4 条）
//	归档侧：每个文件都能在 archive 里查到（raw/instance-gz/<name> 形态）
func TestE2E_InstanceGzAdoptOnRealProductionData(t *testing.T) {
	serversRoot := os.Getenv("JM_FR501_ADOPT_SERVERS")
	archiveRoot := os.Getenv("JM_FR501_ADOPT_ARCHIVE")
	if serversRoot == "" || archiveRoot == "" {
		t.Skip("未设置 JM_FR501_ADOPT_SERVERS / JM_FR501_ADOPT_ARCHIVE，跳过真实生产数据用例")
	}
	require.NoError(t, os.MkdirAll(archiveRoot, 0o755))

	dirs, err := fr501AdoptDirs(serversRoot)
	require.NoError(t, err)
	require.NotEmpty(t, dirs, "实例根 %s 下没找到任何带 logs/ 的实例目录", serversRoot)
	t.Logf("扫描到 %d 个实例目录（%s）", len(dirs), serversRoot)

	// —— 前置：记录本地 .gz 基线（ADR §5 第 4 条的对照面）——
	beforeCount, beforeBytes, beforeFp := fr501CountLocalGz(t, serversRoot)
	t.Logf("吸存前：本地 .gz 数量=%d 字节=%d", beforeCount, beforeBytes)
	require.Positive(t, beforeCount, "前置条件：真实数据里应有 .gz（否则本用例什么都没验证）")

	provider := archive.NewLocalArchive(archiveRoot)
	registry := archive.NewRegistry(provider)
	adopter := &instanceGzAdopter{
		registry:     registry,
		instanceDirs: func() map[string]string { return dirs },
		now:          func() time.Time { return time.Now().UTC() },
	}

	// —— 第一轮：首登记 ——
	started := time.Now()
	stats1, err := adopter.AdoptOnce(context.Background())
	elapsed1 := time.Since(started)
	t.Logf("第一轮读数：scanned=%d registered=%d alreadyRegistered=%d failed=%d localDeleted=%d elapsed=%v err=%v",
		stats1.Scanned, stats1.Registered, stats1.AlreadyRegistered, stats1.Failed, stats1.LocalDeleted, elapsed1, err)
	require.NoError(t, err, "第一轮不应有错误（单文件失败也会以 firstErr 形式返回）")
	require.Equal(t, beforeCount, stats1.Scanned, "应扫到与基线相同数量的 .gz")
	require.Equal(t, stats1.Scanned, stats1.Registered, "第一轮应全部首登记")
	require.Equal(t, 0, stats1.AlreadyRegistered, "第一轮不该有幂等命中（归档目录是全新的）")
	require.Equal(t, 0, stats1.Failed, "第一轮不该有失败")
	require.Equal(t, 0, stats1.LocalDeleted, "ADR §5 第 4 条：平台不删实例本地 gz")

	// —— 现场证据①：本地 .gz 一个没动 ——
	midCount, midBytes, midFp := fr501CountLocalGz(t, serversRoot)
	require.Equal(t, beforeCount, midCount, "吸存后本地 .gz 数量必须不变")
	require.Equal(t, beforeBytes, midBytes, "吸存后本地 .gz 总字节必须不变")
	require.Equal(t, beforeFp, midFp, "吸存后本地 .gz 清单指纹必须不变（路径/mtime/size 全部一致）")
	t.Logf("第一轮后：本地 .gz 数量=%d 字节=%d（与基线完全一致）", midCount, midBytes)

	// —— 现场证据②：归档里确实出现了 raw/instance-gz/<文件名> 形态的对象 ——
	// 直接数归档根下的文件，而不是只信 Registry 的读数——后者可能自己算错。
	var archivedFiles []string
	require.NoError(t, filepath.WalkDir(archiveRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(archiveRoot, p)
		if relErr == nil {
			archivedFiles = append(archivedFiles, filepath.ToSlash(rel))
		}
		return nil
	}))
	var gzObjects int
	for _, f := range archivedFiles {
		if strings.Contains(f, "/raw/instance-gz/") {
			gzObjects++
		}
	}
	t.Logf("归档根下文件总数=%d，其中 raw/instance-gz/ 形态=%d", len(archivedFiles), gzObjects)
	require.Equal(t, beforeCount, gzObjects,
		"归档里 raw/instance-gz/ 形态的对象数应等于扫到的本地 .gz 数（%d）", beforeCount)
	// 抽样确认路径形态与代码一致：<ns>/<day>/g1/raw/instance-gz/<name>
	var sampleShown int
	for _, f := range archivedFiles {
		if strings.Contains(f, "/raw/instance-gz/") && sampleShown < 3 {
			t.Logf("  归档对象样例：%s", f)
			require.Contains(t, f, "/g1/raw/instance-gz/", "归档对象必须落在 g1/raw/instance-gz/ 下（实际 %s）", f)
			sampleShown++
		}
	}

	// —— 第二轮：全部幂等跳过 ——
	started2 := time.Now()
	stats2, err2 := adopter.AdoptOnce(context.Background())
	elapsed2 := time.Since(started2)
	t.Logf("第二轮读数：scanned=%d registered=%d alreadyRegistered=%d failed=%d localDeleted=%d elapsed=%v err=%v",
		stats2.Scanned, stats2.Registered, stats2.AlreadyRegistered, stats2.Failed, stats2.LocalDeleted, elapsed2, err2)
	require.NoError(t, err2)
	require.Equal(t, stats1.Scanned, stats2.Scanned, "第二轮应扫到同样多的文件")
	require.Equal(t, 0, stats2.Registered, "第二轮不该有新登记（object_id 幂等）")
	require.Equal(t, stats2.Scanned, stats2.AlreadyRegistered, "第二轮应全部命中幂等去重")
	require.Equal(t, 0, stats2.Failed)
	require.Equal(t, 0, stats2.LocalDeleted)

	// —— 第二轮后：本地仍然一个没动 ——
	afterCount, afterBytes, afterFp := fr501CountLocalGz(t, serversRoot)
	require.Equal(t, beforeCount, afterCount)
	require.Equal(t, beforeBytes, afterBytes)
	require.Equal(t, beforeFp, afterFp, "两轮吸存后本地 .gz 清单必须与基线逐字节一致")
	t.Logf("第二轮后：本地 .gz 数量=%d 字节=%d（与基线完全一致）", afterCount, afterBytes)

	// —— 归档侧不因第二轮而增长 ——
	var afterObjects int
	require.NoError(t, filepath.WalkDir(archiveRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.Contains(filepath.ToSlash(p), "/raw/instance-gz/") {
			afterObjects++
		}
		return nil
	}))
	require.Equal(t, gzObjects, afterObjects, "第二轮不得新增任何归档对象")

	t.Logf("结论：%d 个真实 .gz 两轮吸存，本地零改动，归档零重复", beforeCount)
}
