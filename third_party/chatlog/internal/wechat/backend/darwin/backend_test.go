package darwin

import "testing"

func TestBackendExposesVersion4Capabilities(t *testing.T) {
	platform := New()
	if platform.ID() != "darwin" {
		t.Fatalf("backend ID = %q", platform.ID())
	}
	if platform.Detector() == nil {
		t.Fatal("detector capability is missing")
	}
	if extractor, err := platform.NewExtractor(Version4); err != nil || extractor == nil {
		t.Fatalf("extractor capability = %T, %v", extractor, err)
	}
	if decryptor, err := platform.NewDecryptor(Version4); err != nil || decryptor == nil {
		t.Fatalf("decryptor capability = %T, %v", decryptor, err)
	}
}

func TestBackendRejectsUnknownVersionAtCapabilityBoundary(t *testing.T) {
	platform := New()
	if _, err := platform.NewExtractor(3); err == nil {
		t.Fatal("unknown extractor version was accepted")
	}
	if _, err := platform.NewDecryptor(3); err == nil {
		t.Fatal("unknown decryptor version was accepted")
	}
}
