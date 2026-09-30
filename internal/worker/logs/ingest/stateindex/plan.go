package stateindex

import "fmt"

// planState 把索引的期望状态拆成各表的行计划（行顺序与 specs 一致，父表在前）。
//
// 只有「内容列」参与指纹：updated_at 之类的写入时刻列不参与，否则每次持久化都会让所有行
// 「看起来变了」，把增量写入退化成整本重写。
func planState(st State, nowUnixMilli int64) ([tableCount][]rowData, error) {
	var plans [tableCount][]rowData
	for _, row := range st.Sources {
		row := row
		plans[tblSource] = append(plans[tblSource], rowData{
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
		plans[tblPosition] = append(plans[tblPosition], rowData{
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
		plans[tblGap] = append(plans[tblGap], rowData{
			key: mirrorKey(row.Key, row.ID),
			fp: fingerprint(row.StartPos, row.EndPos, row.Reason, nullableText(row.Detail), row.Resolved,
				nullableText(row.Resolution)),
			values: func() ([]any, error) {
				return []any{row.Key, row.ID, toInt64(row.StartPos), toInt64(row.EndPos), row.Reason,
					nullableText(row.Detail), boolToInt(row.Resolved), nullableText(row.Resolution)}, nil
			},
		})
	}
	for _, row := range st.Projections {
		row := row
		plans[tblProjection] = append(plans[tblProjection], rowData{
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
		plans[tblInstanceBinding] = append(plans[tblInstanceBinding], rowData{
			key: mirrorKey(row.UUID),
			fp:  fingerprint(row.Namespace, row.Generation, row.Mode, row.WorkDir),
			values: func() ([]any, error) {
				return []any{row.UUID, row.Namespace, row.Generation, row.Mode, row.WorkDir}, nil
			},
		})
	}
	for _, row := range st.Aux {
		row := row
		fp := fingerprint(row.Config, row.Payload)
		plans[tblSourceAux] = append(plans[tblSourceAux], rowData{
			key: mirrorKey(row.Key),
			fp:  fp,
			values: func() ([]any, error) {
				return []any{row.Key, nullableBytes(row.Config), nullableBytes(row.Payload), int64(fp)}, nil
			},
		})
	}
	for _, row := range st.WAL {
		row := row
		fp := WALFingerprint(row)
		plans[tblSourceWAL] = append(plans[tblSourceWAL], rowData{
			key: mirrorKey(row.Key, row.Seq, row.EventID, row.RecordStart, row.RecordEnd),
			fp:  fp,
			values: func() ([]any, error) {
				return []any{row.Key, toInt64(row.Seq), row.EventID, toInt64(row.RecordStart),
					toInt64(row.RecordEnd), boolToInt(row.Appended), boolToInt(row.Durable),
					nullableBytes(row.Body), int64(fp)}, nil
			},
		})
	}
	for _, row := range st.Batches {
		row := row
		plans[tblDeliveryBatch] = append(plans[tblDeliveryBatch], rowData{
			key: mirrorKey(row.Key, row.Ordinal),
			fp:  fingerprint(row.StartPos, row.EndPos, row.State),
			values: func() ([]any, error) {
				return []any{row.Key, row.Ordinal, toInt64(row.StartPos), toInt64(row.EndPos), row.State}, nil
			},
		})
	}
	return plans, nil
}

// WALFingerprint 合成 source_wal 行的变更判据。
//
// 覆盖范围 = 该行除正文之外的全部列（序号、事件身份、标志、正文是否内联）+ 调用方给出的
// 正文判据 FP。事件身份（EventID）由 log_source_id / source_generation / parser_version /
// record 范围派生，因此同一身份下正文是否变化，完全由 FP 决定——调用方必须让 FP 覆盖正文的
// 每个字段（ingest 侧的 walBodyFingerprint 覆盖 logtypes.Event 的全部 JSON 字段，并有
// TestWALBodyFingerprintCoversEveryEventField 守着这一点）。
func WALFingerprint(row WALRow) uint64 {
	return fingerprint(row.Seq, row.EventID, row.RecordStart, row.RecordEnd, row.Appended, row.Durable,
		row.Body != nil, row.FP)
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
	for _, row := range st.WAL {
		if err := requireParent("source_wal", row.Key); err != nil {
			return err
		}
	}
	for _, row := range st.Batches {
		if err := requireParent("delivery_batch", row.Key); err != nil {
			return err
		}
	}
	seenWAL := make(map[string]struct{}, len(st.WAL))
	for _, row := range st.WAL {
		key := mirrorKey(row.Key, row.Seq, row.EventID, row.RecordStart, row.RecordEnd)
		if _, ok := seenWAL[key]; ok {
			return fmt.Errorf("stateindex: source_wal 主键重复（同一源的 %d 号条目被登记两次）", row.Seq)
		}
		seenWAL[key] = struct{}{}
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
