package imgdownload

import "testing"

func TestCDNFieldsPreferAndUsable(t *testing.T) {
	empty := CDNFields{}
	if empty.Usable() {
		t.Fatal("empty should not be usable")
	}
	mid := CDNFields{AesKey: "aa", CdnMidImgURL: "mid-id"}
	if !mid.Usable() || mid.PreferFileID() != "mid-id" {
		t.Fatalf("mid: usable=%v id=%q", mid.Usable(), mid.PreferFileID())
	}
	big := CDNFields{AesKey: "aa", CdnMidImgURL: "mid-id", CdnBigImgURL: "big-id"}
	if big.PreferFileID() != "big-id" {
		t.Fatalf("prefer big, got %q", big.PreferFileID())
	}
}
