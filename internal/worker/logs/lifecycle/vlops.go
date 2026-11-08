package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// VLPartitionOps 是「VL 物理分区操作」的生产实现。
//
// 2026-10-29：冷热双层已废除，只剩余一个 VL ⇒ **迁移状态机（copy / verify / attach /
// detach / cleanup）连同它的 Cold 客户端一起删除**。不是为少写代码，而是因为它已经变成
// 一条数据毁灭路径：
//
//	它的每一步都只为了「换到另一个物理位置」——
//	drain → snapshot → copy 到 ColdDataRoot → attach 到 Cold 客户端
//	→ detach 热层 → 删热层原物。
//
// 单实例下 Hot ≡ Cold，这条链的净效果是「把日分区抄到自己身上、再把自己删掉」。
// 生产 `log_retention.enabled: true`，留着它就等于留着一颗定时炸弹。
//
// **保留下来的是「到期数据的出口」**（三条，都只用 Hot / HotDataRoot / Catalog）：
//   - 冻结快照（snapshot）：ExportSnapshot 靠它取独立副本；
//   - 归档热层回收（archive_reclaim.go）：RemoveArchivedPartition 靠 Hot + HotDataRoot；
//   - 工作日修复（repairDay）：修已存在的日，不换位置。

type VLPartitionOps struct {
	Hot               *vlsup.Client
	HotDataRoot       string
	Catalog           *catalog.Catalog
	CanonicalDayInput func(*catalog.Record) ([]logtypes.Event, error)
	ResourceBudget    *vlsup.BudgetController

	reservations map[string]func()
	mu           sync.Mutex
	snapshots    map[catalog.PartitionKey]string
}

// NewVLPartitionOps 构造物理适配器。只需要一个 VL 客户端。
func NewVLPartitionOps(hot *vlsup.Client, hotRoot string, cat *catalog.Catalog) (*VLPartitionOps, error) {
	if hot == nil || hotRoot == "" || cat == nil {
		return nil, fmt.Errorf("lifecycle: VL client, data root and Catalog are required")
	}
	return &VLPartitionOps{
		Hot: hot, HotDataRoot: hotRoot, Catalog: cat,
		snapshots:    make(map[catalog.PartitionKey]string),
		reservations: make(map[string]func()),
	}, nil
}

// Ops 返回可注入的物理操作面。
//
// 2026-10-29：迁移各步（Drain / Snapshot / Copy / Verify / Attach / Detach / Cleanup）
// 连同 DayManager、RepairDay 一起删了 —— 它们全部是「把日搬到另一层」的组件。
// 现在 Ops 是空结构，保留类型是因为 manager.StartMigration/ResumeMigration 的状态查询
// 仍需要它（那些是只读状态机，不产生物理动作）。
func (v *VLPartitionOps) Ops() Ops { return Ops{} }

// releaseReservations 释放某一天登记的全部磁盘预留（按 key 后缀匹配）。
func (v *VLPartitionOps) releaseReservations(day string) {
	v.mu.Lock()
	var releases []func()
	for key, release := range v.reservations {
		if strings.HasSuffix(key, "/"+day) {
			releases = append(releases, release)
			delete(v.reservations, key)
		}
	}
	v.mu.Unlock()
	for _, release := range releases {
		release()
	}
}

// prepareHotSource 确保目标物理日可被快照：
//   - 该日必须是已冻结的受控物理日（未冻结就取快照 = 取到还在写的数据）；
//   - 该日分区必须已挂在 VL 上，否则 /internal/partition/snapshot/create 找不到它。
//
// 2026-10-29：原实现还带一个 `RecordDayReattached` 登记（迁移状态机的一部分）。
// 迁移链删掉后不再需要它：导出归档的快照是**只读**动作，不改变归属。
func (v *VLPartitionOps) prepareHotSource(key catalog.PartitionKey) error {
	day, ok := v.Catalog.PhysicalDay(key.UTCDay)
	if !ok || !day.Frozen || len(day.Batches) == 0 {
		return fmt.Errorf("lifecycle: no controlled physical-day source batch")
	}
	name, err := dayName(key)
	if err != nil {
		return err
	}
	return v.withTimeout(func(ctx context.Context) error {
		active, err := partitionActive(ctx, v.Hot, name)
		if err != nil || active {
			return err
		}
		root := filepath.Join(v.HotDataRoot, "partitions", name)
		if err := requirePathUnder(filepath.Join(v.HotDataRoot, "partitions"), root); err != nil {
			return err
		}
		_, err = v.Hot.Post(ctx, "/internal/partition/attach", url.Values{"name": {name}})
		return err
	})
}

func dayName(key catalog.PartitionKey) (string, error) {
	day, err := time.Parse("2006-01-02", key.UTCDay)
	if err != nil {
		return "", err
	}
	return day.Format("20060102"), nil
}

func (v *VLPartitionOps) withTimeout(fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return fn(ctx)
}

// snapshot 取一个冻结快照目录。
//
// 它是「导出独立副本」的唯一取数入口：
//   - `CheckPhysicalDayDrained` 是双闸（无在途写许可 + 无活跃读取），不是可选的前置；
//   - `prepareHotSource` 确保该日的物理目录存在且可读；
//   - VL 必须返回**恰好一个**位于受管 snapshots 根下的稳定目录（多于一个是状态不明，不是“挑一个”）。
func (v *VLPartitionOps) snapshot(key catalog.PartitionKey, _ string) (string, error) {
	if err := v.Catalog.CheckPhysicalDayDrained(key.UTCDay); err != nil {
		return "", err
	}
	if err := v.prepareHotSource(key); err != nil {
		return "", err
	}
	day, err := dayName(key)
	if err != nil {
		return "", err
	}
	var paths []string
	err = v.withTimeout(func(ctx context.Context) error {
		body, err := v.Hot.Post(ctx, "/internal/partition/snapshot/create", url.Values{"partition_prefix": {day}})
		if err != nil {
			return err
		}
		return json.Unmarshal(body, &paths)
	})
	if err != nil {
		return "", err
	}
	if len(paths) != 1 {
		return "", fmt.Errorf("lifecycle: snapshot for %s returned %d paths", day, len(paths))
	}
	if err := requirePathUnder(filepath.Join(v.HotDataRoot, "partitions", day, "snapshots"), paths[0]); err != nil {
		return "", err
	}
	v.mu.Lock()
	v.snapshots[key] = paths[0]
	v.mu.Unlock()
	return paths[0], nil
}

// AllowDetach 是 last-copy 保留门禁。
//
// 2026-10-29：迁移链已删，不再存在「把最后一份拷走再删源」的路径 ⇒ 恒为拒绝。
// 保留这个方法是因为 RetentionGate 接口仍有装配点，而「拒绝」是唯一安全的默认：
// 任何未来调用者拿到 unsupported 都会保留原物。
func (v *VLPartitionOps) AllowDetach(key catalog.PartitionKey, _, _ string) (bool, string) {
	return false, "cold-tier migration has been removed; the only VL instance is the authoritative copy"
}

func partitionActive(ctx context.Context, client *vlsup.Client, day string) (bool, error) {
	body, err := client.Post(ctx, "/internal/partition/list", nil)
	if err != nil {
		return false, err
	}
	var names []string
	if err := json.Unmarshal(body, &names); err != nil {
		return false, err
	}
	for _, name := range names {
		if name == day {
			return true, nil
		}
	}
	return false, nil
}

func requirePathUnder(root, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("lifecycle: path escapes managed root")
	}
	return nil
}

// treeDigest 计算目录树的稳定摘要（路径 + 内容）。
//
// 只被「导出副本校验」与快照完整性使用；符号链一律拒绝（它可能指向快照之外）。
func treeDigest(root string) (string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("lifecycle: symlink is not allowed")
		}
		if !entry.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		_, _ = io.WriteString(h, filepath.ToSlash(rel))
		_, _ = h.Write([]byte{0})
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		_, err = io.Copy(h, f)
		closeErr := f.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
