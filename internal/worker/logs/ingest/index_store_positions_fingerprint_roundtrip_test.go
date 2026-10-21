package ingest

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 回归（w18 漂移防护的持久化断层）：`PositionsFingerprint` / `PositionsFingerprintAt`
// 必须随**生产持久化链路**往返，而不只是在 `ledger.Snapshot()`/`Restore()` 里往返。
//
// 原缺陷：生产持久化走 ingest 索引（写侧 `ledgerCoreOf`、读侧 `indexStateToState`），
// 而 `ledger.Snapshot()`/`Restore()` 只有测试在用。两处映射都没搬这两个字段 ⇒ 进程重启
// 或代次补账（runtime.go 经 `saved.Ledger` → `ledger.Restore` 还原）后指纹恒为零值；
// 而段 3（acquire 续读前校验）按设计把「缺指纹」判为不一致 ⇒ 每次重启都走对账补齐、
// 禁止按旧偏移续读——正是该指纹要消除的「漂移重复摄取」。指纹丢了，防护等于没装。
//
// 转红方式（实测）：删掉 `ledgerCoreOf` 中任一指纹字段赋值（写侧丢），或删掉
// `indexStateToState` 中任一指纹字段赋值（读侧丢）⇒ 本条断言立即红。
func TestIndexStorePositionsFingerprintSurvivesIngestRoundTrip(t *testing.T) {
	const (
		keyWithPrint = "inst:w18fp/file"
		keyNoPrint   = "inst:w18fp-control/file"
	)
	m := &Manager{}
	// 两个源对照：带指纹的必须原样往返；从未采样过的源必须仍是零值——否则断言可能被
	// 「恒写某个默认值」满足，测不出真正的搬运。
	st := &persistedState{Sources: map[string]persistedSource{
		keyWithPrint: {Ledger: []ledger.Entry{{
			Key:      ledger.SourceKey{LogSourceID: "inst:w18fp/file", SourceGeneration: "g1"},
			Identity: logtypes.SourceIdentity{LogSourceID: "inst:w18fp/file", SourceGeneration: "g1"},
			Positions: logtypes.Positions{
				Read: 4096, Durable: 4096, Delivery: 4096, Reclaim: 4096,
			},
			PositionsFingerprint:   "sha256:deadbeef",
			PositionsFingerprintAt: 4096,
		}}},
		keyNoPrint: {Ledger: []ledger.Entry{{
			Key:      ledger.SourceKey{LogSourceID: "inst:w18fp-control/file", SourceGeneration: "g1"},
			Identity: logtypes.SourceIdentity{LogSourceID: "inst:w18fp-control/file", SourceGeneration: "g1"},
			Positions: logtypes.Positions{
				Read: 8192, Durable: 8192, Delivery: 8192, Reclaim: 8192,
			},
		}}},
	}}

	// —— 写侧：stateToIndexState 必须把指纹写进 aux payload ——
	rows, err := m.stateToIndexState(st)
	require.NoError(t, err)
	require.Len(t, rows.Aux, 2)

	payloadOf := func(target string) []byte {
		for _, row := range rows.Aux {
			if row.Key == target {
				return row.Payload
			}
		}
		t.Fatalf("aux 缺少源 %s", target)
		return nil
	}
	withPrint := payloadOf(keyWithPrint)
	require.Contains(t, string(withPrint), `"positions_fingerprint":"sha256:deadbeef"`,
		"写侧：指纹必须落进 aux payload（漏搬即重启后归零，段 3 判不一致并禁止按旧偏移续读）")
	require.Contains(t, string(withPrint), `"positions_fingerprint_at":4096`,
		"写侧：指纹必须与其对应位置成对落库（水位变了旧指纹无意义）")
	require.NotContains(t, string(payloadOf(keyNoPrint)), "positions_fingerprint",
		"对照：从未采样过指纹的源不得凭空多出该字段（零值 + omitempty 保持 payload 字节不变）")

	// —— 读侧：indexStateToState 必须把指纹还原到内存条目 ——
	loaded, err := indexStateToState(rows)
	require.NoError(t, err)

	got := loaded.Sources[keyWithPrint]
	require.Len(t, got.Ledger, 1)
	require.Equal(t, "sha256:deadbeef", got.Ledger[0].PositionsFingerprint,
		"读侧：指纹必须读回（否则重启后零值 ⇒ 每次重启都全量对账补齐）")
	require.Equal(t, uint64(4096), got.Ledger[0].PositionsFingerprintAt,
		"读侧：指纹位置必须读回（段 3 要拿它与已确认水位比对，缺了同样判不一致）")
	// 位置本身也要一起回来：指纹的语义是「该位置处的内容」，两者错配即无意义。
	require.Equal(t, uint64(4096), got.Ledger[0].Positions.Read)

	control := loaded.Sources[keyNoPrint]
	require.Len(t, control.Ledger, 1)
	require.Empty(t, control.Ledger[0].PositionsFingerprint, "对照：未采样指纹的源往返后仍为空")
	require.Zero(t, control.Ledger[0].PositionsFingerprintAt, "对照：未采样指纹的源往返后位置仍为零")
}

// 旧索引兼容：索引里根本没有这两个字段时（升级前的 payload），读回必须是**零值且不报错**。
//
// 为什么必须是零值而不是报错或特殊分支：零值正是段 3 既有的「缺指纹」语义（判不一致 ⇒
// 走对账补齐，宁可重补也不按旧偏移回读错内容）。若这里报错，升级当天所有源都会因读取失败
// 拒绝启动采集；若这里造一个"兼容哨兵"值，又会把缺指纹伪装成一致，反而放开按旧偏移续读 ✗。
// 所以正确做法就是让它自然落到零值，由下一轮采样补齐。
func TestIndexStoreLegacyAuxWithoutPositionsFingerprintLoadsZeroValue(t *testing.T) {
	const key = "inst:legacyfp/file"

	// 两个子用例：① 用当前结构体编码（零值 + omitempty ⇒ 与旧 payload 字节等价）；
	// ② 手写旧格式 JSON 字面量，确保断言不依赖当前结构的字段顺序。
	legacyPayloads := map[string][]byte{
		"encoded-without-fingerprint": func() []byte {
			data, err := json.Marshal(&indexAux{LedgerCore: &indexLedgerCore{
				Key:      ledger.SourceKey{LogSourceID: "inst:legacyfp/file", SourceGeneration: "g1"},
				Identity: logtypes.SourceIdentity{LogSourceID: "inst:legacyfp/file", SourceGeneration: "g1"},
			}})
			require.NoError(t, err)
			return data
		}(),
		"handwritten-legacy-json": []byte(`{"ledger_core":{"key":{"log_source_id":"inst:legacyfp/file",` +
			`"source_generation":"g1"}}}`),
	}

	for name, payload := range legacyPayloads {
		t.Run(name, func(t *testing.T) {
			require.NotContains(t, string(payload), "positions_fingerprint",
				"夹具前提：旧 payload 里不得出现指纹字段")

			state, err := indexStateToState(stateindex.State{
				Sources: []stateindex.SourceRow{{
					Key: key, LogSourceID: key, SourceGeneration: "g1", StorageNamespace: key,
				}},
				// 读侧以「position 行存在」为该源有状态的判据。
				Positions: []stateindex.PositionRow{{Key: key, ReadPos: 1234, DurablePos: 1234, ReclaimPos: 1234}},
				Aux:       []stateindex.AuxRow{{Key: key, Payload: payload}},
			})
			require.NoError(t, err, "旧索引缺指纹字段不得报错（否则升级当天所有源拒绝启动采集）")

			saved := state.Sources[key]
			require.Len(t, saved.Ledger, 1, "旧索引的账本条目仍须正常还原")
			require.Empty(t, saved.Ledger[0].PositionsFingerprint,
				"缺字段 ⇒ 零值：这正是段 3 的「缺指纹」语义（判不一致、走对账补齐），不能被伪造成一致")
			require.Zero(t, saved.Ledger[0].PositionsFingerprintAt)
			// 其余字段不受影响，证明兼容不是靠整条重建换来的。
			require.Equal(t, uint64(1234), saved.Ledger[0].Positions.Read)
		})
	}
}
