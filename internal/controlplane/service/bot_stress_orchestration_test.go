package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validStressOrchestrationYAML = `
loop: true
staggerMs: 500
phases:
  - durationSec: 60
    behavior: idle
  - durationSec: 120
    behavior: patrol
    target: "0,64,0;8,64,8"
  - durationSec: 60
    behavior: guard
  - durationSec: 90
    behavior: custom
    steps:
      - type: chat
        message: hello
      - type: wait
        durationMs: 3000
      - type: move
        pos:
          x: 0
          y: 64
          z: 0
`

func TestParseStressOrchestrationYAMLValid(t *testing.T) {
	orc, summary, err := ParseStressOrchestrationYAML(validStressOrchestrationYAML)

	require.NoError(t, err)
	require.NotNil(t, orc)
	assert.True(t, summary.Enabled)
	assert.True(t, summary.Loop)
	assert.Equal(t, 500, summary.StaggerMS)
	assert.Equal(t, 4, summary.PhaseCount)
	assert.Equal(t, 330, summary.DurationSec)
	assert.Equal(t, []string{"idle", "patrol", "guard", "custom"}, summary.Behaviors)
}

func TestParseStressOrchestrationYAMLRejectsInvalidYAML(t *testing.T) {
	_, _, err := ParseStressOrchestrationYAML("phases:\n  - behavior: [")

	require.ErrorIs(t, err, ErrBotStressSessionInvalid)
}

func TestParseStressOrchestrationYAMLRejectsUnknownBehavior(t *testing.T) {
	_, _, err := ParseStressOrchestrationYAML(`
phases:
  - durationSec: 1
    behavior: dance
`)

	require.ErrorIs(t, err, ErrBotStressSessionInvalid)
}

func TestParseStressOrchestrationYAMLRejectsZeroDuration(t *testing.T) {
	_, _, err := ParseStressOrchestrationYAML(`
phases:
  - durationSec: 0
    behavior: idle
`)

	require.ErrorIs(t, err, ErrBotStressSessionInvalid)
}

func TestStressOrchestrationBehaviorConfigForBotAddsStagger(t *testing.T) {
	orc, _, err := ParseStressOrchestrationYAML(validStressOrchestrationYAML)
	require.NoError(t, err)

	raw, err := orc.BehaviorConfigForBot(3)
	require.NoError(t, err)

	var cfg struct {
		Loop         bool `json:"loop"`
		StartDelayMS int  `json:"startDelayMs"`
		Phases       []struct {
			DurationMS int    `json:"durationMs"`
			Behavior   string `json:"behavior"`
			Config     struct {
				Steps []struct {
					Type     string `json:"type"`
					Duration int    `json:"duration"`
				} `json:"steps"`
			} `json:"config"`
		} `json:"phases"`
	}
	require.NoError(t, json.Unmarshal(raw, &cfg))
	assert.True(t, cfg.Loop)
	assert.Equal(t, 1000, cfg.StartDelayMS)
	require.Len(t, cfg.Phases, 4)
	assert.Equal(t, 60000, cfg.Phases[0].DurationMS)
	assert.Equal(t, "custom", cfg.Phases[3].Behavior)
	require.Len(t, cfg.Phases[3].Config.Steps, 3)
	assert.Equal(t, 3000, cfg.Phases[3].Config.Steps[1].Duration)
}
