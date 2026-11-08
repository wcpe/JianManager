package query

import "github.com/wcpe/JianManager/internal/worker/logs/catalog"

// queryableRecord 为旧规划夹具补上明确已发布前缀，不让无投影记录伪装可查询。
func queryableRecord(key catalog.PartitionKey, owner catalog.Owner, generation uint64, dir string) *catalog.Record {
	record := catalog.NewStableRecord(key, owner, generation, dir)
	record.PublishedProjection = &catalog.PublishedProjection{ManifestVersion: "fixture-v1",
		CoverageComplete: true, QueryLocationDirID: dir, QueryGeneration: generation,
		ClosedVisibleSeq: map[string]uint64{key.String(): 100}}
	return record
}
