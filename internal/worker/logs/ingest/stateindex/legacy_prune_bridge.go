package stateindex

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
)

// legacyPruneVariant 只重建原始批次可证明的尾窗裁剪形态，水位由来源原始双水位批准。
// 推断只选择保留条数，随后仍须逐列验真所有实际行；绝不把水位当作请求成功或未知证据。
func legacyPruneVariant(ctx context.Context, reader migrationQuery, legacy State, through map[string]uint64, encodedKeeps string, infer bool) (State, string, error) {
	owners := make(map[string][]BatchRow)
	for _, row := range legacy.Batches {
		owners[row.Key] = append(owners[row.Key], row)
	}
	keeps := make(map[string]int)
	if infer {
		for owner, watermark := range through {
			if watermark > 0 && len(owners[owner]) > 0 {
				keeps[owner] = 0
			}
		}
		rows, err := reader.QueryContext(ctx, "SELECT key,end_pos FROM delivery_batch")
		if err != nil {
			return State{}, "", err
		}
		for rows.Next() {
			var owner string
			var end int64
			if err := rows.Scan(&owner, &end); err != nil {
				_ = rows.Close()
				return State{}, "", err
			}
			if watermark := through[owner]; watermark > 0 && end >= 0 && uint64(end) <= watermark {
				keeps[owner]++
			}
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return State{}, "", err
		}
	} else if err := json.Unmarshal([]byte(encodedKeeps), &keeps); err != nil || keeps == nil {
		return State{}, "", errors.New("stateindex: 旧批次裁剪形态凭据损坏")
	}
	variant := legacy
	variant.Batches = nil
	for owner, batches := range owners {
		sort.Slice(batches, func(i, j int) bool { return batches[i].Ordinal < batches[j].Ordinal })
		watermark := through[owner]
		keep, pruned := keeps[owner]
		if !pruned {
			variant.Batches = append(variant.Batches, batches...)
			continue
		}
		if watermark == 0 || keep < 0 {
			return State{}, "", errors.New("stateindex: 旧批次裁剪缺少有效来源水位")
		}
		eligible := 0
		for _, batch := range batches {
			if batch.EndPos <= watermark {
				eligible++
			}
		}
		if keep > eligible {
			return State{}, "", errors.New("stateindex: 旧批次保留尾窗超出来源完整历史")
		}
		tailFrom := len(batches)
		if keep > 0 {
			seen := 0
			for i := len(batches) - 1; i >= 0; i-- {
				if batches[i].EndPos <= watermark {
					seen++
					tailFrom = i
					if seen == keep {
						break
					}
				}
			}
		}
		ordinal := int64(0)
		for i, batch := range batches {
			if i < tailFrom && batch.EndPos <= watermark {
				continue
			}
			batch.Ordinal = ordinal
			ordinal++
			variant.Batches = append(variant.Batches, batch)
		}
	}
	for owner := range keeps {
		if through[owner] == 0 || len(owners[owner]) == 0 {
			return State{}, "", errors.New("stateindex: 旧批次裁剪凭据含未授权归属")
		}
	}
	encoded, err := json.Marshal(keeps)
	return variant, string(encoded), err
}

// prunedLegacyMigration 将已验真的物理批次形态同时绑定到新旧计划，新增凭据仍保留来源原值。
func (s *Store) prunedLegacyMigration(ctx context.Context, reader migrationQuery, sourceSHA string, desired, legacy State, through map[string]uint64, encodedKeeps string, infer bool) (*Migration, *Migration, error) {
	variant, encodedKeeps, err := legacyPruneVariant(ctx, reader, legacy, through, encodedKeeps, infer)
	if err != nil {
		return nil, nil, err
	}
	oldMapping, err := s.buildMigration(sourceSHA, 1, variant)
	if err != nil {
		return nil, nil, err
	}
	desired.Batches = variant.Batches
	migration, err := s.buildMigration(sourceSHA, 2, desired)
	if err != nil {
		return nil, nil, err
	}
	migration.legacyRows = oldMapping.rows
	migration.record.LegacyPruneKeeps = encodedKeeps
	return migration, oldMapping, nil
}
