//go:build !race

package vlsup

// raceEnabled 在普通构建下为 false（口径见 race_on_test.go）。
const raceEnabled = false
