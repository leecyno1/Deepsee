package model

import "testing"

func TestContactV4WrapKeepsAvatarURLs(t *testing.T) {
	contact := (&ContactV4{
		UserName:     "wxid_avatar",
		NickName:     "头像用户",
		BigHeadURL:   "https://example.test/avatar/0",
		SmallHeadURL: "https://example.test/avatar/132",
	}).Wrap()
	if contact.BigHeadURL != "https://example.test/avatar/0" {
		t.Fatalf("big avatar URL = %q", contact.BigHeadURL)
	}
	if contact.SmallHeadURL != "https://example.test/avatar/132" {
		t.Fatalf("small avatar URL = %q", contact.SmallHeadURL)
	}
}
