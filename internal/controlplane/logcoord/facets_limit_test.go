package logcoord

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestMergeFacets_ClampsDimensionLimitToHardCap 守护 MergeFacets 自身的上限兜底。
//
// 为什么不能只靠 CP router 的钳制：MergeFacets 是导出函数，未来新增的管理面、
// 测试代码或第三方调用都可能绕开 router 直接传入未钳制的值。
// 而本函数内的本地裁剪是 field_values 响应的**唯一**体积闸
// （该端点不向 VL 传 limit），限一旦被绕过就是「一次拉全量」。
func TestMergeFacets_ClampsDimensionLimitToHardCap(t *testing.T) {
	// 构造超过硬上限的值集，才能区分「钳了」与「没钳」。
	const total = int(MaxFacetDimensionLimit) + 500
	values := make([]FacetValue, 0, total)
	for i := 0; i < total; i++ {
		values = append(values, FacetValue{Value: fmt.Sprintf("v%04d", i), Count: uint64(total - i)})
	}
	perWorker := []WorkerFacetsResponse{{
		Dimensions: []WorkerFacetDimension{{
			Dimension: "level",
			Values:    values,
		}},
	}}

	out := MergeFacets(perWorker, []string{"level"}, 999999)
	if len(out) != 1 {
		t.Fatalf("expect 1 dimension, got %d", len(out))
	}
	fd := out[0]
	assert.Equal(t, MaxFacetDimensionLimit, fd.Limit,
		"超出硬上限的 limit 必须被钳制：DimensionLimit 是 uint32，"+
			"调用方把用户输入直接转换时负值会溢出成 4294967295，使裁剪与 Truncated 一并失效")
	assert.Equal(t, int(MaxFacetDimensionLimit), len(fd.Values),
		"必须按硬上限裁剪，而不是把全量值集返回")
	assert.True(t, fd.Truncated, "裁剪必须置 Truncated，不得把截断结果标成完整")
	assert.Equal(t, uint64(int(total)-int(MaxFacetDimensionLimit)), fd.TruncatedCount,
		"被省略的数量必须如实上报")
}

// TestMergeFacets_ZeroLimitMeansNoLocalTrim 确认未设上限时不被误钳。
//
// 0 是合法取值（“不本地裁剪”），下游会另行回填默认，
// 不能因为加了硬上限就把 0 也当成超限处理。
func TestMergeFacets_ZeroLimitMeansNoLocalTrim(t *testing.T) {
	perWorker := []WorkerFacetsResponse{{
		Dimensions: []WorkerFacetDimension{{
			Dimension: "level",
			Values: []FacetValue{
				{Value: "INFO", Count: 10},
				{Value: "WARN", Count: 5},
			},
		}},
	}}
	out := MergeFacets(perWorker, []string{"level"}, 0)
	if len(out) != 1 {
		t.Fatalf("expect 1 dimension, got %d", len(out))
	}
	assert.Equal(t, uint32(0), out[0].Limit)
	assert.Len(t, out[0].Values, 2, "limit=0 不得本地裁剪")
}
