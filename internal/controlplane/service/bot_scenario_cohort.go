package service

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
)

type scenarioOrdinalScore struct {
	ordinal int
	score   [32]byte
}

// AssignScenarioCohorts 按 seed 和稳定 ordinal 生成精确分组结果。
func AssignScenarioCohorts(seed int64, botCount int, cohorts []ScenarioCohort) ([]string, error) {
	if botCount < 1 {
		return nil, fmt.Errorf("Bot 数量必须大于 0")
	}
	quotas, err := scenarioCohortQuotas(botCount, cohorts)
	if err != nil {
		return nil, err
	}
	ordinals := seededScenarioOrdinals(seed, botCount)
	assigned := make([]string, botCount)
	cursor := 0
	for cohortIndex, cohort := range cohorts {
		for range quotas[cohortIndex] {
			assigned[ordinals[cursor].ordinal-1] = cohort.Key
			cursor++
		}
	}
	return assigned, nil
}

func scenarioCohortQuotas(botCount int, cohorts []ScenarioCohort) ([]int, error) {
	if len(cohorts) < 1 || len(cohorts) > 20 {
		return nil, fmt.Errorf("cohort 数量必须在 1..20 之间")
	}
	quotas := make([]int, len(cohorts))
	totalPercent, assigned := 0, 0
	for index, cohort := range cohorts {
		if !scenarioKeyPattern.MatchString(cohort.Key) || cohort.Percent < 1 || cohort.Percent > 100 {
			return nil, fmt.Errorf("cohort %d 配置无效", index)
		}
		totalPercent += cohort.Percent
		quotas[index] = botCount * cohort.Percent / 100
		assigned += quotas[index]
	}
	if totalPercent != 100 {
		return nil, fmt.Errorf("cohort percent 合计必须恰为 100")
	}
	for index := 0; assigned < botCount; index++ {
		quotas[index]++
		assigned++
	}
	return quotas, nil
}

func seededScenarioOrdinals(seed int64, botCount int) []scenarioOrdinalScore {
	ordinals := make([]scenarioOrdinalScore, botCount)
	var payload [16]byte
	binary.BigEndian.PutUint64(payload[:8], uint64(seed))
	for index := range ordinals {
		ordinal := index + 1
		binary.BigEndian.PutUint64(payload[8:], uint64(ordinal))
		ordinals[index] = scenarioOrdinalScore{ordinal: ordinal, score: sha256.Sum256(payload[:])}
	}
	sort.Slice(ordinals, func(left, right int) bool {
		for index := range ordinals[left].score {
			if ordinals[left].score[index] != ordinals[right].score[index] {
				return ordinals[left].score[index] < ordinals[right].score[index]
			}
		}
		return ordinals[left].ordinal < ordinals[right].ordinal
	})
	return ordinals
}

// ScenarioCohortJSONMap 生成下发给单 Bot 的 cohort 子树规范 JSON。
func ScenarioCohortJSONMap(scenario *ScenarioV2) (map[string]string, error) {
	if scenario == nil {
		return nil, nil
	}
	out := make(map[string]string, len(scenario.Cohorts))
	for _, cohort := range scenario.Cohorts {
		envelope := struct {
			Seed     int64          `json:"seed"`
			Scenario ScenarioCohort `json:"scenario"`
		}{Seed: scenario.Seed, Scenario: cohort}
		raw, err := json.Marshal(envelope)
		if err != nil {
			return nil, fmt.Errorf("序列化 cohort %s 失败: %w", cohort.Key, err)
		}
		out[cohort.Key] = string(raw)
	}
	return out, nil
}
