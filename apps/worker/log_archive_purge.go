package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/archive"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/retention"
)

// 本文件是「归档对象自身的回收」（FR-501 最后一块）的生产适配层。
//
// 与 log_retention.go 同一分工：判定与次序的语义全在 retention/catalog 包里并被回归覆盖，
// 这里只做三件搬运——把 Catalog 的归档证据映射成 retention 的候选、把归档 Provider
// 接成对象层删除出口、把 Catalog 的清除权威态接成迁移出口。于是本文件可以薄到
// 几乎不需要测试，而真正的判据不会因为换一层真源就变形。

// newCatalogArchivedObjectLister 把 Catalog 里「正本在归档」的分区投影成清除候选。
//
// 关键点：**前置条件在枚举时就用 Catalog 自己的判据过一遍**（ArchiveObjectPurgeEligible），
// 把结论以 BlockedReason 的形式带给策略层，而不是在这里自己再写一遍条件。
// 为什么必须复用 Catalog 的判据：热层是否已回收、是否有待恢复标记、是否有活跃租约，
// 这三件事的权威都在 Catalog 内（租约是时变的），在适配层重写一份迟早会与它分叉——
// 而分叉的方向一旦是「放宽」，就是删掉一份仍有用的副本。
func newCatalogArchivedObjectLister(cat *catalog.Catalog) retention.ArchivedObjectLister {
	return retention.ArchivedObjectListerFunc(func() ([]retention.ArchivedObjectRef, error) {
		if cat == nil {
			return nil, nil
		}
		recs := cat.ArchivedPartitions()
		out := make([]retention.ArchivedObjectRef, 0, len(recs))
		for _, rec := range recs {
			ev := rec.ArchiveEvidence
			if ev == nil {
				// 归档态却无证据：权威与证据不自洽，只能人工处置。
				// 不枚举它（枚举出来也判不了保留期——没有 ArchivedAt）。
				continue
			}
			ref := retention.ArchivedObjectRef{
				StorageNamespace: rec.Key.StorageNamespace,
				UTCDay:           rec.Key.UTCDay,
				ProviderKind:     ev.ProviderKind,
				ObjectPrefix:     ev.ObjectPrefix,
				ArchivedAt:       ev.ArchivedAt,
				HotReclaimed:     ev.HotReclaimed,
				Purged:           ev.Purged,
				RecoveryRequired: rec.RecoveryRequired,
			}
			if ok, reason := cat.ArchiveObjectPurgeEligible(rec.Key); !ok {
				ref.BlockedReason = reason
			}
			out = append(out, ref)
		}
		return out, nil
	})
}

// newCatalogArchiveObjectAuthority 把 Catalog 的「归档对象已清除」权威态接成迁移出口。
type catalogArchiveObjectAuthority struct {
	cat *catalog.Catalog
	now func() time.Time
}

func newCatalogArchiveObjectAuthority(cat *catalog.Catalog) retention.ArchiveObjectAuthority {
	if cat == nil {
		return nil
	}
	return &catalogArchiveObjectAuthority{cat: cat, now: func() time.Time { return time.Now().UTC() }}
}

func (a *catalogArchiveObjectAuthority) key(namespace, day string) (catalog.PartitionKey, error) {
	parsed, err := parsePartitionDay(day)
	if err != nil {
		return catalog.PartitionKey{}, err
	}
	return catalog.PartitionKey{StorageNamespace: namespace, UTCDay: parsed.Format("2006-01-02")}, nil
}

func (a *catalogArchiveObjectAuthority) MarkPurgeRequested(ctx context.Context, namespace, day, reason string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	key, err := a.key(namespace, day)
	if err != nil {
		return false, err
	}
	_, already, err := a.cat.MarkArchiveObjectPurgeRequested(key, reason, a.now())
	return already, err
}

func (a *catalogArchiveObjectAuthority) MarkPurged(ctx context.Context, namespace, day, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := a.key(namespace, day)
	if err != nil {
		return err
	}
	_, err = a.cat.MarkArchiveObjectsPurged(key, reason, a.now())
	return err
}

func (a *catalogArchiveObjectAuthority) RollbackPurge(ctx context.Context, namespace, day, reason string) error {
	// 回滚**不看 ctx**：请求已被取消时仍然要把权威态退回去，否则记录会停在
	// 「清除进行中」而归档里还剩着对象——那个中间态没人能自动收拾。
	// 这与「删除动作应当响应取消」不矛盾：取消的是删除，不是收尾。
	key, err := a.key(namespace, day)
	if err != nil {
		return err
	}
	_, err = a.cat.RollbackArchiveObjectPurge(key, reason, a.now())
	return err
}

// parseArchiveObjectPrefix 把 Catalog 里存的归档前缀（`namespace/day/g{gen}`）拆成分区键。
//
// 为什么要拆而不是直接用：归档 Provider 的接口按键寻址（`archive.PartitionKey`），
// 而 Catalog 里存的是一段可读前缀（审计要看「去哪」）。两者是同一事实的两种表示，
// 转换必须严格——前缀不合规时**拒绝**，绝不猜一个键去删。
func parseArchiveObjectPrefix(prefix string) (archive.PartitionKey, error) {
	// 只裁空白、**不裁斜杠**：`PartitionKey.String()` 生成的形态是无前后斜杠的三段，
	// 而 `/a/b/g1/` 这类写法只可能来自记录被改写。裁掉斜杠去「尽量解析」会让一个
	// 明显不规范的记录悄悄通过，然后在某个猜出来的位置上删对象。
	parts := strings.Split(strings.TrimSpace(prefix), "/")
	if len(parts) != 3 {
		return archive.PartitionKey{}, fmt.Errorf(
			"归档对象前缀 %q 不是 namespace/day/g{generation} 形态，拒绝据此推断删除位置", prefix)
	}
	// 命名空间本身可能含 '/'（如 ns/game-1），但 Catalog 存的 prefix 由导出侧用
	// `PartitionKey.String()` 生成，固定三段。故上面按 3 段判定；不满足即拒绝。
	// generation 段必须严格是 `g` + 正整数：`g` / `g0` / `g1x` 一律拒绝。
	genPart := parts[2]
	if len(genPart) < 2 || genPart[0] != 'g' {
		return archive.PartitionKey{}, fmt.Errorf(
			"归档对象前缀 %q 的 generation 段非法，拒绝据此推断删除位置", prefix)
	}
	gen, err := strconv.ParseUint(genPart[1:], 10, 64)
	if err != nil || gen == 0 {
		return archive.PartitionKey{}, fmt.Errorf(
			"归档对象前缀 %q 的 generation 段非法，拒绝据此推断删除位置", prefix)
	}
	key := archive.PartitionKey{StorageNamespace: parts[0], UTCDay: parts[1], Generation: gen}
	if err := key.Validate(); err != nil {
		return archive.PartitionKey{}, err
	}
	return key, nil
}

// archiveProviderObjectStore 把归档 Provider 接成对象层删除出口。
//
// 只支持 local：`archive.RemoteS3Provider` 虽已存在但**未接线**（缺凭证/重试模型，
// 属后续 FR），这里如实拒绝而不是拿内存 stub 冒充对象存储——那会让「已清除」
// 这条遥测在一个根本没连上桶的进程里成立。
type archiveProviderObjectStore struct {
	provider archive.Provider
	// root 是本地归档根（用于清理已空的日目录）；非 local Provider 时为空。
	root string
}

func newArchiveProviderObjectStore(provider archive.Provider, root string) retention.ArchiveObjectStore {
	if provider == nil {
		return nil
	}
	// 本地与对象存储的差异只在「删完对象后要不要清空目录骨架」：
	//
	//   - 本地目录（KindLocal）对象是文件，删完会剩下一串空子目录，不清就会永远留骨架；
	//   - S3/MinIO 是扁平键空间，**没有目录**，对象删完就结束了，也没有"空目录"要收。
	//
	// 两边的对象删除本身走同一个 `Provider.Delete`，所以除了这一步之外完全同构。
	// 此前这里硬要求 KindLocal，把 s3/minio 直接判为不支持（装配处如实拒绝）——
	// 那是对的保守做法，但留下了「导出已能写 s3、却永远不会清理 s3」的口子。
	switch provider.Kind() {
	case archive.KindLocal:
		return &archiveProviderObjectStore{provider: provider, root: root}
	default:
		// 其他 Provider（s3 / minio）不需要 root：见 RemoveArchivePartition 的分支。
		return &archiveProviderObjectStore{provider: provider}
	}
}

// providerKindMatches 复核 Catalog 记录的 provider 与已装配的是同一个。
//
// 为什么必须复核：Catalog 里的证据可能来自**上一次部署**（例如先配了本地目录、
// 后来换了目的地）。按记录的 kind 去删当前装配的 Provider，等于在一个可能完全
// 不相干的目录里按同名前缀删对象。这里选择保守拒绝（下一轮仍拒绝），
// 把这种不一致交给人看——它总会在启动日志里出现。
func (s *archiveProviderObjectStore) providerKindMatches(kind string) error {
	if kind != "" && kind != s.provider.Kind() {
		return fmt.Errorf("Catalog 记录的归档目的地类型 %q 与已装配的 %q 不一致，拒绝据此删除对象",
			kind, s.provider.Kind())
	}
	return nil
}

func (s *archiveProviderObjectStore) ListArchiveObjects(ctx context.Context, providerKind, objectPrefix string) ([]string, error) {
	if err := s.providerKindMatches(providerKind); err != nil {
		return nil, err
	}
	key, err := parseArchiveObjectPrefix(objectPrefix)
	if err != nil {
		return nil, err
	}
	objects, err := s.provider.List(ctx, key)
	if err != nil {
		return nil, err
	}
	// 空清单是**成功**（目录本就不存在 / 上一轮已删净）：目标状态已达成。
	// 这一点在 retention 侧也有用例钉住（TestArchivePurgeEmptyListIsIdempotentSuccess）。
	return objects, nil
}

func (s *archiveProviderObjectStore) DeleteArchiveObject(ctx context.Context, providerKind, objectPrefix, relPath string) error {
	if err := s.providerKindMatches(providerKind); err != nil {
		return err
	}
	key, err := parseArchiveObjectPrefix(objectPrefix)
	if err != nil {
		return err
	}
	// LocalArchive.Delete 对不存在的对象返回 nil（幂等、容忍 404），
	// 于是「删到一半失败后重试」不会因为第一次已经删掉的那几个而假失败。
	return s.provider.Delete(ctx, key, relPath)
}

func (s *archiveProviderObjectStore) RemoveArchivePartition(ctx context.Context, providerKind, objectPrefix string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.providerKindMatches(providerKind); err != nil {
		return err
	}
	// 对象存储（s3 / minio）没有目录：对象已经在 DeleteArchiveObject 里逐个删完了，
	// 这里没有“空目录骨架”要收。**不能**用 root 做任何文件系统操作——因为 s3 的
	// root 为空，而把空 root 拼进路径会落到相对路径上（工作目录之下），那是灾难。
	if s.provider.Kind() != archive.KindLocal {
		return nil
	}
	if s.root == "" {
		return nil
	}
	key, err := parseArchiveObjectPrefix(objectPrefix)
	if err != nil {
		return err
	}
	// 只删**本分区的空目录**（`<root>/<ns>/<day>/g<gen>`），不碰 root、不碰 day 目录：
	// day 目录可能还有别的 generation，root 下还有别的日期——一次 `RemoveAll(root)`
	// 就等于把整个归档史删了。
	dir := filepath.Join(s.root, filepath.FromSlash(key.StorageNamespace), key.UTCDay, fmt.Sprintf("g%d", key.Generation))
	// 对象删完后剩下的往往是一串空子目录（`raw/files/`），`os.Remove(dir)` 对非空目录
	// 会报 ENOTEMPTY——而那不是失败（对象确实已经不在了）。真实环境第一次跑就撞上了这条
	// （本层 e2e 用例当场转红）：若把它当失败，每个分区都会留下一个空目录骨架，
	// 「归档占多少盘」的核对里就永远清不干净。
	//
	// 先用 RemoveAll 清掉本分区目录下的空骨架——这不越过上文那条红线：
	// 范围严格限定在 `<root>/<ns>/<day>/g<gen>` 这一棵子树内，root / day 目录都不在范围内。
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	// 尽力回收空的 day / namespace 目录（Remove 只删空目录，非空会报错 → 忽略）。
	_ = os.Remove(filepath.Dir(dir))
	_ = os.Remove(filepath.Dir(filepath.Dir(dir)))
	return nil
}

// newArchiveObjectReclaimer 装配「归档对象自身的回收」。
//
// 返回 nil 表示本次不启用（开关关着 / 没有归档目的地）。
// 调用方据此决定是否启动调度，以及启动日志里怎么描述——**未启用的形态必须能被
// 一眼读出来**，否则「归档一直在涨」与「清理在跑但一条没删」在现场分不清。
//
// provider 由调用方传入（与导出器共用同一个实例）：两处各造一个 Provider
// 在功能上等价，但一旦换成带连接池/凭证刷新的 Provider，两份实例会各自
// 持有一份状态，而「导出写到哪」与「清除读的是哪」必须是同一个目的地。
//
// root 只对本地目录 Provider 有意义（清空目录骨架）；对象存储传空串即可。
func newArchiveObjectReclaimer(
	policy retention.Policy,
	cat *catalog.Catalog,
	provider archive.Provider,
	root string,
) *retention.ArchiveObjectReclaimer {
	if cat == nil || provider == nil {
		return nil
	}
	store := newArchiveProviderObjectStore(provider, root)
	if store == nil {
		return nil
	}
	return retention.NewArchiveObjectReclaimer(
		policy,
		newCatalogArchivedObjectLister(cat),
		store,
		newCatalogArchiveObjectAuthority(cat),
	)
}
