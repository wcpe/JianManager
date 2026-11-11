package vlrange

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

func TestStatsNumericSamplesUseNumericDenominator(t *testing.T) {
	pipe, err := statsPipe(query.RangeQuery{MetricField: "duration_ms", TimeBucket: "5m"})
	require.NoError(t, err)
	require.Contains(t, pipe, " | filter duration_ms:range[-inf,inf] | stats")
	require.Less(t, strings.Index(pipe, "filter duration_ms"), strings.Index(pipe, "count()"), "count must measure the same numeric sample set as sum/min/max")
	plain, err := statsPipe(query.RangeQuery{TimeBucket: "5m"})
	require.NoError(t, err)
	require.NotContains(t, plain, "filter", "ordinary volume counts still include every log event")
	_, err = statsPipe(query.RangeQuery{MetricField: "x) | fields *"})
	require.Error(t, err)
}
