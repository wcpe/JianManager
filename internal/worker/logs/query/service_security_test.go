package query

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type fieldProbeClient struct {
	UnimplementedRangeClient
	facetDimensions []string
}

func (*fieldProbeClient) Fields(context.Context, AuthoritativeRange, RangeQuery) (FieldsResult, error) {
	return FieldsResult{Fields: []string{
		"level", "instance_id", "event_id", "canonical_content_hash", "user_secret",
	}}, nil
}

func (f *fieldProbeClient) Facets(_ context.Context, _ AuthoritativeRange, _ RangeQuery, dimensions []string, _ uint32) (RangeFacetsResult, error) {
	f.facetDimensions = append([]string(nil), dimensions...)
	return RangeFacetsResult{Values: []FacetValue{{Dimension: "level", Value: "INFO", Count: 1}}}, nil
}

func TestFieldsAndFacetsRestrictExposedDimensions(t *testing.T) {
	probe := &fieldProbeClient{}
	svc := NewService(NewPlanner(newCatalogWithHot(t), nil), probe, "test")
	req := QueryRequest{AuthorizedTargets: []string{"ns/game-1"}}
	fields := svc.Fields(context.Background(), req)
	require.Nil(t, fields.Err)
	require.Equal(t, []string{"instance_id", "level", "stream"}, fields.Fields)

	facets := svc.Facets(context.Background(), req, []string{"event_id", "level", "user_secret"}, 10)
	require.Nil(t, facets.Err)
	require.Equal(t, []string{"level"}, probe.facetDimensions)
	require.Equal(t, []FacetValue{{Dimension: "level", Value: "INFO", Count: 1}}, facets.Values)

	denied := svc.Facets(context.Background(), req, []string{"event_id", "user_secret"}, 10)
	require.NotNil(t, denied.Err)
	require.Equal(t, ErrCodeUnsupported, denied.Err.Code)
}
