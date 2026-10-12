//go:build !race

package ingest

// raceEnabled 在普通构建下为 false（口径与豁免依据见 race_on_test.go）。
const raceEnabled = false
