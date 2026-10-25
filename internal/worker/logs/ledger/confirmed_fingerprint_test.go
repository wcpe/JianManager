package ledger

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func fingerprintFixture(t *testing.T) (*Ledger, SourceKey, Segment, ConfirmedFingerprint) {
	t.Helper()
	l := New()
	key := SourceKey{LogSourceID: "fixture-source", SourceGeneration: "fixture-generation"}
	l.Ensure(key, logtypes.SourceIdentity{})
	segment := Segment{Path: "fixture/live.log", Kind: SegmentLive, StartPos: 100, EndPos: 120, PhysicalStart: 25, PhysicalMappingKnown: true, IdentityBytes: 8, IdentitySHA256: "fixture-identity", ParserVersion: "fixture-parser"}
	if err := l.RegisterSegment(key, segment); err != nil {
		t.Fatal(err)
	}
	fingerprint := ConfirmedFingerprint{SegmentPath: segment.Path, SegmentStartPos: 100, SegmentIdentityBytes: 8, SegmentIdentitySHA256: segment.IdentitySHA256, LogicalPos: 120, PhysicalOffset: 45, WindowBytes: 8, SHA256: "fixture-digest"}
	return l, key, segment, fingerprint
}

func TestConfirmedFingerprintOnlyChangeIsBoundAndIdempotent(t *testing.T) {
	l, key, _, fingerprint := fingerprintFixture(t)
	before := l.Revision(key)
	if !l.SetConfirmedFingerprint(key, fingerprint) || l.Revision(key) <= before {
		t.Fatal("合法绑定没有独立推进修订")
	}
	after := l.Revision(key)
	if !l.SetConfirmedFingerprint(key, fingerprint) || l.Revision(key) != after {
		t.Fatal("相同绑定不幂等")
	}
	if got, ok := l.ConfirmedFingerprint(key); !ok || got != fingerprint {
		t.Fatalf("合法绑定不可读取：got=%+v ok=%v", got, ok)
	}
	copy := l.Get(key)
	copy.ConfirmedFingerprint.SHA256 = "fixture-mutated-copy"
	if got, ok := l.ConfirmedFingerprint(key); !ok || got != fingerprint {
		t.Fatal("Get指纹指针不是深拷贝")
	}
	fingerprint.SHA256 = "fixture-new-digest"
	if !l.SetConfirmedFingerprint(key, fingerprint) || l.Revision(key) <= after {
		t.Fatal("仅摘要变化未推进修订")
	}
	if !l.SetPositionsFingerprint(key, 119, "fixture-legacy-digest") {
		t.Fatal("旧API兼容写失败")
	}
	if _, ok := l.ConfirmedFingerprint(key); ok {
		t.Fatal("旧平面写将新采点贴到了旧分段绑定")
	}
}

func TestConfirmedFingerprintRejectsUnprovenOrWrongPhysicalBinding(t *testing.T) {
	cases := map[string]func(*Segment, *ConfirmedFingerprint){
		"未知物理映射": func(segment *Segment, _ *ConfirmedFingerprint) { segment.PhysicalMappingKnown = false },
		"错误物理采点": func(_ *Segment, fingerprint *ConfirmedFingerprint) { fingerprint.PhysicalOffset = 120 },
		"错误逻辑采点": func(_ *Segment, fingerprint *ConfirmedFingerprint) { fingerprint.LogicalPos = 99 },
		"越过分段末端": func(_ *Segment, fingerprint *ConfirmedFingerprint) { fingerprint.LogicalPos = 121 },
		"其它分段路径": func(_ *Segment, fingerprint *ConfirmedFingerprint) { fingerprint.SegmentPath = "fixture/rotated.log" },
		"其它分段基址": func(_ *Segment, fingerprint *ConfirmedFingerprint) { fingerprint.SegmentStartPos = 0 },
		"错误前缀身份": func(_ *Segment, fingerprint *ConfirmedFingerprint) {
			fingerprint.SegmentIdentitySHA256 = "fixture-other-identity"
		},
		"未知前缀身份": func(segment *Segment, fingerprint *ConfirmedFingerprint) {
			segment.IdentityBytes = 0
			fingerprint.SegmentIdentityBytes = 0
		},
		"窗口超出文件": func(_ *Segment, fingerprint *ConfirmedFingerprint) { fingerprint.WindowBytes = 46 },
		"空采样窗口":  func(_ *Segment, fingerprint *ConfirmedFingerprint) { fingerprint.WindowBytes = 0 },
		"物理映射溢出": func(segment *Segment, _ *ConfirmedFingerprint) { segment.PhysicalStart = math.MaxUint64 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			l, key, segment, fingerprint := fingerprintFixture(t)
			mutate(&segment, &fingerprint)
			if err := l.RegisterSegment(key, segment); err != nil {
				t.Fatal(err)
			}
			before := l.Revision(key)
			if l.SetConfirmedFingerprint(key, fingerprint) || l.Revision(key) != before {
				t.Fatal("未获证指纹被接纳或产生无效修订")
			}
		})
	}
}

func TestConfirmedFingerprintOldFlatStateDoesNotInventBinding(t *testing.T) {
	key := SourceKey{LogSourceID: "fixture-source", SourceGeneration: "fixture-generation"}
	l := New()
	if err := l.Restore([]Entry{{Key: key, PositionsFingerprint: "fixture-legacy-digest", PositionsFingerprintAt: 120, Segments: []Segment{{Path: "fixture/live.log", StartPos: 100, EndPos: 120}}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.ConfirmedFingerprint(key); ok || l.Get(key).Segments[0].PhysicalMappingKnown {
		t.Fatal("旧零值被补造成物理映射或分段指纹")
	}
	if at, digest, ok := l.PositionsFingerprint(key); !ok || at != 120 || digest != "fixture-legacy-digest" {
		t.Fatal("兼容读取丢失旧原始证据")
	}
}

func TestConfirmedFingerprintSamePathRotationInvalidatesOldSegmentBinding(t *testing.T) {
	l, key, segment, fingerprint := fingerprintFixture(t)
	if !l.SetConfirmedFingerprint(key, fingerprint) {
		t.Fatal("夹具绑定失败")
	}
	segment.StartPos, segment.EndPos = 120, 140
	segment.PhysicalStart = 0
	segment.IdentitySHA256 = "fixture-new-identity"
	if err := l.RegisterSegment(key, segment); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.ConfirmedFingerprint(key); ok {
		t.Fatal("旧段采点被当作当前同路径新文件指纹")
	}
}

func TestPhysicalZeroMappingKnownSurvivesThreeJSONRestarts(t *testing.T) {
	l, key, segment, _ := fingerprintFixture(t)
	segment.PhysicalStart = 0
	if err := l.RegisterSegment(key, segment); err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 3; round++ {
		data, err := json.Marshal(l.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		var entries []Entry
		if err := json.Unmarshal(data, &entries); err != nil {
			t.Fatal(err)
		}
		l = New()
		if err := l.Restore(entries); err != nil {
			t.Fatal(err)
		}
		got := l.Get(key).Segments[0]
		if got.PhysicalStart != 0 || !got.PhysicalMappingKnown {
			t.Fatalf("第%d轮无法区分真实物理零起点与未知旧状态", round)
		}
	}
}
