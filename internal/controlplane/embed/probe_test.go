package embed

import "testing"

func TestServerProbeLibrariesZip_InfoConsistent(t *testing.T) {
	libs := ServerProbeLibrariesZip()
	info := ServerProbeJarInfo()

	if len(libs) == 0 {
		if info.LibrariesAvailable || info.LibrariesBytes != 0 || info.LibrariesShortSha != "" {
			t.Fatalf("缓存包缺失时元信息不应标记可用: %+v", info)
		}
		return
	}

	if !info.LibrariesAvailable {
		t.Fatalf("缓存包存在时元信息应标记可用")
	}
	if info.LibrariesBytes != len(libs) {
		t.Fatalf("缓存包大小不一致: got=%d want=%d", info.LibrariesBytes, len(libs))
	}
	if info.LibrariesShortSha != shortSHA256(libs) {
		t.Fatalf("缓存包短指纹不一致: got=%s want=%s", info.LibrariesShortSha, shortSHA256(libs))
	}
}
