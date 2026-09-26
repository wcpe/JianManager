package vlrange

import (
	"context"
	"fmt"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// Tiered chooses exactly the Catalog owner namespace for each range. A missing
// namespace is a visible range failure; it never falls back to HOT data.
type Tiered struct {
	Hot, Cold, Rehydrate *Client
}

func (t *Tiered) forRange(rng query.AuthoritativeRange) (*Client, error) {
	if t == nil {
		return nil, fmt.Errorf("vlrange: tiered client is unavailable")
	}
	var selected *Client
	switch rng.Owner {
	case catalog.OwnerHot:
		selected = t.Hot
	case catalog.OwnerCold:
		selected = t.Cold
	case catalog.OwnerArchive:
		selected = t.Rehydrate
	}
	if selected == nil {
		return nil, fmt.Errorf("vlrange: authoritative namespace %s is not ready", rng.Owner)
	}
	return selected, nil
}

func (t *Tiered) Search(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.RangeResult, error) {
	c, err := t.forRange(rng)
	if err != nil {
		return query.RangeResult{}, err
	}
	return c.Search(ctx, rng, q)
}

func (t *Tiered) Stats(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.StatsResult, error) {
	c, err := t.forRange(rng)
	if err != nil {
		return query.StatsResult{}, err
	}
	return c.Stats(ctx, rng, q)
}

func (t *Tiered) Tail(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery, mode query.TailMode) (query.RangeResult, error) {
	c, err := t.forRange(rng)
	if err != nil {
		return query.RangeResult{}, err
	}
	return c.Tail(ctx, rng, q, mode)
}

func (t *Tiered) Fields(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.FieldsResult, error) {
	c, err := t.forRange(rng)
	if err != nil {
		return query.FieldsResult{}, err
	}
	return c.Fields(ctx, rng, q)
}

func (t *Tiered) Facets(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery, dimensions []string, limit uint32) (query.RangeFacetsResult, error) {
	c, err := t.forRange(rng)
	if err != nil {
		return query.RangeFacetsResult{}, err
	}
	return c.Facets(ctx, rng, q, dimensions, limit)
}

func (t *Tiered) FollowLive(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.RangeResult, error) {
	c, err := t.forRange(rng)
	if err != nil {
		return query.RangeResult{}, err
	}
	return c.FollowLive(ctx, rng, q)
}

var _ query.RangeClient = (*Tiered)(nil)
var _ query.FieldsRangeClient = (*Tiered)(nil)
var _ query.FacetsRangeClient = (*Tiered)(nil)
var _ query.FollowLiveRangeClient = (*Tiered)(nil)
