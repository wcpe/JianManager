package stateindex

import (
	"context"
	"database/sql"
	"fmt"
)

// rowReader 是只读查询面（*sql.DB 与 *sql.Tx 都满足），用于需要在一致快照里读的场合。
type rowReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// planState 把索引的期望状态拆成各表的行计划，并按「归属键」分组。
//
// 归属键 = 表主键的第一列（缺口/WAL/批次/游标等都以日志源为归属，实例绑定表没有归属，用 ""）。
// 分组是「只写变更行」从 O(总量) 降到 O(变更源) 的前提：持久化只对本批次变更的源做行规划，
// 未变更源的行根本不会被构造、不会被指纹、不会与镜像比对。
//
// 只有「内容列」参与指纹：updated_at 之类的写入时刻列不参与，否则每次持久化都会让所有行
// 「看起来变了」，把增量写入退化成整本重写。
func planState(st State, nowUnixMilli int64) ([tableCount]map[string][]rowData, error) {
	var plans [tableCount]map[string][]rowData
	appendRow := func(table int, owner string, row rowData) {
		if plans[table] == nil {
			plans[table] = make(map[string][]rowData)
		}
		row.owner = owner
		plans[table][owner] = append(plans[table][owner], row)
	}
	for _, row := range st.Sources {
		row := row
		appendRow(tblSource, row.Key, rowData{
			key: mirrorKey(row.Key),
			fp:  fingerprint(row.LogSourceID, row.SourceGeneration, row.StorageNamespace),
			values: func() ([]any, error) {
				updated := row.UpdatedAtUnixMilli
				if updated == 0 {
					updated = nowUnixMilli
				}
				return []any{row.Key, row.LogSourceID, row.SourceGeneration, row.StorageNamespace, updated}, nil
			},
		})
	}
	for _, row := range st.Positions {
		row := row
		appendRow(tblPosition, row.Key, rowData{
			key: mirrorKey(row.Key),
			fp: fingerprint(row.ReadPos, row.DurablePos, row.ReclaimPos, row.AcquirePaused,
				nullableText(row.PauseReason)),
			values: func() ([]any, error) {
				return []any{row.Key, toInt64(row.ReadPos), toInt64(row.DurablePos), toInt64(row.ReclaimPos),
					boolToInt(row.AcquirePaused), nullableText(row.PauseReason)}, nil
			},
		})
	}
	for _, row := range st.Gaps {
		row := row
		appendRow(tblGap, row.Key, rowData{
			key: mirrorKey(row.Key, row.ID),
			fp: fingerprint(row.StartPos, row.EndPos, row.Reason, nullableText(row.Detail), row.Resolved,
				nullableText(row.Resolution), nullableBytes(row.FoldSpans), row.FoldSpanMerges, row.ReasonCode, row.ResolvedAtUTC, row.ResolvedBy, row.FoldSpansUnknown, row.FirstSeenUnix),
			values: func() ([]any, error) {
				return []any{row.Key, row.ID, toInt64(row.StartPos), toInt64(row.EndPos), row.Reason,
					nullableText(row.Detail), boolToInt(row.Resolved), nullableText(row.Resolution), nullableBytes(row.FoldSpans),
					int64(row.FoldSpanMerges), row.ReasonCode, row.ResolvedAtUTC, row.ResolvedBy, boolToInt(row.FoldSpansUnknown), row.FirstSeenUnix}, nil
			},
		})
	}
	for _, row := range st.Projections {
		row := row
		appendRow(tblProjection, row.Key, rowData{
			key: mirrorKey(row.Key),
			fp:  fingerprint(row.Generation, row.EventsStoredThrough, row.Pending),
			values: func() ([]any, error) {
				updated := row.UpdatedAtUnixMilli
				if updated == 0 {
					updated = nowUnixMilli
				}
				return []any{row.Key, row.Generation, toInt64(row.EventsStoredThrough), boolToInt(row.Pending), updated}, nil
			},
		})
	}
	for _, row := range st.Instances {
		row := row
		// 归属取主键首列（uuid），与镜像载入侧的 mirrorOwner 同口径——两侧不一致会把同一行
		// 归到不同组，重启后该表出现「旧的被删、新的被写」（实测 instance_binding 一写一删）。
		// 该表不参与按归属增量（不在 scopedTables），归属值本身不承载语义，只要求两侧一致。
		appendRow(tblInstanceBinding, row.UUID, rowData{
			key: mirrorKey(row.UUID),
			// fp 必须覆盖全部可变列（含 FR-502 的 rule_key/relative_path），
			// 否则规则变更不会被判为「本行已变更」而漏写。
			fp: fingerprint(row.Namespace, row.Generation, row.Mode, row.WorkDir, row.RuleKey, row.RelativePath),
			values: func() ([]any, error) {
				return []any{row.UUID, row.Namespace, row.Generation, row.Mode, row.WorkDir, row.RuleKey, row.RelativePath}, nil
			},
		})
	}
	for _, row := range st.Aux {
		row := row
		fp := fingerprint(row.Config, row.Payload)
		appendRow(tblSourceAux, row.Key, rowData{
			key: mirrorKey(row.Key),
			fp:  fp,
			values: func() ([]any, error) {
				return []any{row.Key, nullableBytes(row.Config), nullableBytes(row.Payload), int64(fp)}, nil
			},
		})
	}
	for _, row := range st.Batches {
		row := row
		appendRow(tblDeliveryBatch, row.Key, rowData{
			key: mirrorKey(row.Key, row.Ordinal),
			fp:  fingerprint(row.StartPos, row.EndPos, row.State),
			values: func() ([]any, error) {
				return []any{row.Key, row.Ordinal, toInt64(row.StartPos), toInt64(row.EndPos), row.State}, nil
			},
		})
	}
	return plans, nil
}

func boolToInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

// nullableText 把空字符串转换为 NULL：让「未设置」与「空串」在库中形态一致，也使指纹对二者
// 给出同一结果（避免读回 NULL、写入 "" 造成每轮都判定为变更）。
func nullableText(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func nullableBytes(v []byte) any {
	if len(v) == 0 {
		return nil
	}
	return string(v)
}

// valuesSize 估算一行写入的字节量（文本/二进制按实际长度，其余标量按 8 字节），
// 供 Stats.BytesWritten 观测「写入成本是否随总量线性增长」。
func valuesSize(values []any) int64 {
	var total int64
	for _, value := range values {
		switch v := value.(type) {
		case nil:
		case string:
			total += int64(len(v))
		case []byte:
			total += int64(len(v))
		default:
			total += 8
		}
	}
	return total
}

// Validate 检查一份期望状态自身是否自洽（父行存在、主键不重复）。
//
// 这不是储层必须做的事，而是给上层的便宜自检：把「行键重复」「子行缺父行」这类编码错误在
// 写库之前暴露成明确错误，而不是让它变成外键报错或静默覆盖。
func Validate(st State) error {
	keys := make(map[string]struct{}, len(st.Sources))
	for _, row := range st.Sources {
		keys[row.Key] = struct{}{}
	}
	requireParent := func(table, key string) error {
		if _, ok := keys[key]; !ok {
			return fmt.Errorf("stateindex: %s 行引用了不存在的 source(%q)", table, key)
		}
		return nil
	}
	for _, row := range st.Positions {
		if err := requireParent("position", row.Key); err != nil {
			return err
		}
	}
	for _, row := range st.Gaps {
		if err := requireParent("gap", row.Key); err != nil {
			return err
		}
	}
	for _, row := range st.Projections {
		if err := requireParent("projection", row.Key); err != nil {
			return err
		}
	}
	for _, row := range st.Aux {
		if err := requireParent("source_aux", row.Key); err != nil {
			return err
		}
	}
	for _, row := range st.Batches {
		if err := requireParent("delivery_batch", row.Key); err != nil {
			return err
		}
	}
	seenGap := make(map[string]struct{}, len(st.Gaps))
	for _, row := range st.Gaps {
		key := mirrorKey(row.Key, row.ID)
		if _, ok := seenGap[key]; ok {
			return fmt.Errorf("stateindex: gap 主键重复（%s 的 %d 号缺口被登记两次）", row.Key, row.ID)
		}
		seenGap[key] = struct{}{}
	}
	return nil
}
