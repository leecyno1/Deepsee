package http

import (
	"fmt"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestBoundedCacheUsesLRUEviction(t *testing.T) {
	var cache boundedCache[string, int]
	cache.Set("a", 1, 2)
	cache.Set("b", 2, 2)
	if _, ok := cache.Get("a"); !ok {
		t.Fatal("expected cache hit for a")
	}
	cache.Set("c", 3, 2)

	if _, ok := cache.Get("b"); ok {
		t.Fatal("least recently used entry was retained")
	}
	if cache.Len() != 2 {
		t.Fatalf("cache size = %d, want 2", cache.Len())
	}
}

func TestServiceCachesAreBoundedAndClearedOnStop(t *testing.T) {
	s := &Service{}
	for i := 0; i < md5PathCacheLimit+100; i++ {
		value := fmt.Sprintf("%d", i)
		s.rememberMD5Path(value, "/tmp/"+value)
	}
	for i := 0; i < snsMediaKeyCacheLimit+100; i++ {
		value := fmt.Sprintf("%d", i)
		s.rememberSNSMediaKey("https://example.com/"+value, value)
	}
	for i := 0; i < statsCacheLimit+100; i++ {
		s.setStatsCache(fmt.Sprintf("%d", i), gin.H{"value": i}, time.Minute)
	}

	if got := s.md5PathCache.Len(); got != md5PathCacheLimit {
		t.Fatalf("md5 cache size = %d, want %d", got, md5PathCacheLimit)
	}
	if got := s.snsMediaKeyCache.Len(); got != snsMediaKeyCacheLimit {
		t.Fatalf("sns cache size = %d, want %d", got, snsMediaKeyCacheLimit)
	}
	if got := s.statsCache.Len(); got != statsCacheLimit {
		t.Fatalf("stats cache size = %d, want %d", got, statsCacheLimit)
	}

	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if s.md5PathCache.Len() != 0 || s.snsMediaKeyCache.Len() != 0 || s.statsCache.Len() != 0 {
		t.Fatal("runtime caches were retained after stop")
	}
}

func TestExpiredStatsCacheEntryIsDeletedOnRead(t *testing.T) {
	s := &Service{}
	s.statsCache.Set("expired", statsCacheEntry{
		Payload:   gin.H{"value": 1},
		ExpiresAt: time.Now().Add(-time.Second),
	}, statsCacheLimit)

	if _, ok := s.getStatsCache("expired"); ok {
		t.Fatal("expired entry returned as a cache hit")
	}
	if s.statsCache.Len() != 0 {
		t.Fatal("expired entry was retained")
	}
}

func TestSNSHTTPClientIsReused(t *testing.T) {
	s := &Service{}
	first := s.publicHTTPClient()
	second := s.publicHTTPClient()
	if first != second {
		t.Fatal("SNS HTTP client was recreated")
	}
	s.closeIdleSNSConnections()
}
