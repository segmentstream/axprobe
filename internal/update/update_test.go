package update

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		left, right string
		want        int
	}{
		{"0.1.0", "0.1.1", -1},
		{"0.2.0", "0.1.9", 1},
		{"1.0.0", "1.0.0", 0},
		{"v1.2.3", "1.2.3", 0},
		{"1.0.0-rc1", "1.0.0", -1},
		{"1.0.0", "1.0.0-rc1", 1},
	}
	for _, c := range cases {
		got, err := compareVersions(c.left, c.right)
		if err != nil {
			t.Fatalf("compareVersions(%q,%q) error: %v", c.left, c.right, err)
		}
		if got != c.want {
			t.Fatalf("compareVersions(%q,%q) = %d, want %d", c.left, c.right, got, c.want)
		}
	}
}

func TestCompareVersionsRejectsDev(t *testing.T) {
	if _, err := compareVersions("dev", "0.1.0"); err == nil {
		t.Fatal("expected a dev build to be unparseable (barred from self-update)")
	}
}

func TestChecksumForAsset(t *testing.T) {
	checksums := []byte("abc123  axprobe_darwin_arm64.tar.gz\ndef456  axprobe_linux_amd64.tar.gz\n")
	got, err := checksumForAsset(checksums, "axprobe_linux_amd64.tar.gz")
	if err != nil {
		t.Fatalf("checksumForAsset error: %v", err)
	}
	if got != "def456" {
		t.Fatalf("checksumForAsset = %q, want def456", got)
	}
	if _, err := checksumForAsset(checksums, "missing.tar.gz"); err == nil {
		t.Fatal("expected error for a missing asset")
	}
}

func TestAssetName(t *testing.T) {
	if got := assetName("darwin", "arm64"); got != "axprobe_darwin_arm64.tar.gz" {
		t.Fatalf("assetName = %q", got)
	}
}
