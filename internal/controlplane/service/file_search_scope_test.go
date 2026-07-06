package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeSearchScope(t *testing.T) {
	scope := normalizeSearchScope(SearchScope{
		RootPath:   `/plugins\Essentials/`,
		Extensions: []string{"yml", ".YML", " json ", ""},
	})

	require.Equal(t, "plugins/Essentials", scope.RootPath)
	require.Equal(t, []string{".yml", ".json"}, scope.Extensions)
}

func TestFilterSearchHitsByRootAndExtensions(t *testing.T) {
	hits := []SearchHit{
		{Path: "plugins/Essentials/config.yml", Line: 1},
		{Path: "plugins/Essentials/readme.txt", Line: 2},
		{Path: "plugins/Other/config.yml", Line: 3},
		{Path: "server.properties", Line: 4},
	}

	filtered, truncated := filterSearchHits(hits, 10, normalizeSearchScope(SearchScope{
		RootPath:   "plugins/Essentials",
		Extensions: []string{".yml"},
	}), false)

	require.False(t, truncated)
	require.Equal(t, []SearchHit{{Path: "plugins/Essentials/config.yml", Line: 1}}, filtered)
}

func TestFilterSearchHitsTruncatesAfterScope(t *testing.T) {
	hits := []SearchHit{
		{Path: "plugins/a.yml", Line: 1},
		{Path: "plugins/b.yml", Line: 2},
		{Path: "plugins/c.yml", Line: 3},
	}

	filtered, truncated := filterSearchHits(hits, 2, normalizeSearchScope(SearchScope{
		RootPath: "plugins",
	}), false)

	require.True(t, truncated)
	require.Equal(t, []SearchHit{
		{Path: "plugins/a.yml", Line: 1},
		{Path: "plugins/b.yml", Line: 2},
	}, filtered)
}
