package agent

import (
	"fmt"
	"testing"
	"time"
)

func webCacheSize(t *testing.T) int {
	t.Helper()
	webCacheMu.Lock()
	defer webCacheMu.Unlock()
	return len(webCache)
}

func webSearchCacheSize(t *testing.T) int {
	t.Helper()
	webSearchCacheMu.Lock()
	defer webSearchCacheMu.Unlock()
	return len(webSearchCache)
}

// Overflowing the fetch cache must evict only the oldest entry: every other
// live entry survives (the old wholesale reset threw them all away).
func TestWebCacheEvictionKeepsFreshEntries(t *testing.T) {
	webCacheReset(t)
	base := time.Now().Add(-time.Minute)
	for i := 0; i < webCacheCap; i++ {
		webCacheStore(fmt.Sprintf("key-%d", i), webCacheEntry{
			at:   base.Add(time.Duration(i) * time.Second), // key-0 is the oldest
			body: []byte("v"),
		})
	}
	webCacheStore("key-new", webCacheEntry{at: time.Now(), body: []byte("v")})

	if _, ok := webCacheLookup("key-0"); ok {
		t.Fatal("the oldest entry must be evicted first")
	}
	if _, ok := webCacheLookup("key-1"); !ok {
		t.Fatal("a fresh entry must survive eviction (no wholesale reset)")
	}
	if _, ok := webCacheLookup("key-new"); !ok {
		t.Fatal("the newly stored entry must be cached")
	}
	if n := webCacheSize(t); n > webCacheCap {
		t.Fatalf("cache over cap: %d entries", n)
	}
}

// Expired entries are evicted before any live one has to go.
func TestWebCacheEvictionPrefersExpired(t *testing.T) {
	webCacheReset(t)
	stale := time.Now().Add(-2 * webCacheTTL)
	for i := 0; i < webCacheCap; i++ {
		webCacheStore(fmt.Sprintf("stale-%d", i), webCacheEntry{at: stale, body: []byte("v")})
	}
	webCacheStore("fresh", webCacheEntry{at: time.Now(), body: []byte("v")})

	if _, ok := webCacheLookup("stale-0"); ok {
		t.Fatal("an expired entry must be evicted")
	}
	if _, ok := webCacheLookup("fresh"); !ok {
		t.Fatal("the new entry must be cached")
	}
	if n := webCacheSize(t); n != 1 {
		t.Fatalf("expected only the fresh entry to remain, got %d", n)
	}
}

// The search cache follows the same eviction policy; exactly one fresh entry
// makes room for the newcomer.
func TestWebSearchCacheEvictionKeepsFreshEntries(t *testing.T) {
	webCacheReset(t)
	for i := 0; i < webSearchCacheCap; i++ {
		webSearchCacheStore(fmt.Sprintf("q-%d", i), fmt.Sprintf("content-%d", i))
	}
	webSearchCacheStore("q-new", "content-new")

	if _, ok := webSearchCacheLookup("q-new"); !ok {
		t.Fatal("the newly stored entry must be cached")
	}
	survivors := 0
	for i := 0; i < webSearchCacheCap; i++ {
		if _, ok := webSearchCacheLookup(fmt.Sprintf("q-%d", i)); ok {
			survivors++
		}
	}
	if survivors != webSearchCacheCap-1 {
		t.Fatalf("expected exactly one entry evicted, %d of %d survived", survivors, webSearchCacheCap-1)
	}
	if n := webSearchCacheSize(t); n > webSearchCacheCap {
		t.Fatalf("search cache over cap: %d entries", n)
	}
}
