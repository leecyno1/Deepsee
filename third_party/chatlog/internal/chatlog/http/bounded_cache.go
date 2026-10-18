package http

import (
	"container/list"
	"sync"
)

type boundedCacheEntry[K comparable, V any] struct {
	key   K
	value V
}

// boundedCache is a concurrency-safe LRU cache. Its zero value is ready for
// use; the capacity is supplied on the first Set call.
type boundedCache[K comparable, V any] struct {
	mu         sync.Mutex
	maxEntries int
	items      map[K]*list.Element
	order      *list.List
}

func (c *boundedCache[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var zero V
	if c.items == nil {
		return zero, false
	}
	element, ok := c.items[key]
	if !ok {
		return zero, false
	}
	c.order.MoveToFront(element)
	return element.Value.(boundedCacheEntry[K, V]).value, true
}

func (c *boundedCache[K, V]) Set(key K, value V, maxEntries int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.init(maxEntries)
	if element, ok := c.items[key]; ok {
		element.Value = boundedCacheEntry[K, V]{key: key, value: value}
		c.order.MoveToFront(element)
		return
	}

	element := c.order.PushFront(boundedCacheEntry[K, V]{key: key, value: value})
	c.items[key] = element
	for len(c.items) > c.maxEntries {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		entry := oldest.Value.(boundedCacheEntry[K, V])
		delete(c.items, entry.key)
		c.order.Remove(oldest)
	}
}

func (c *boundedCache[K, V]) Delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if element, ok := c.items[key]; ok {
		delete(c.items, key)
		c.order.Remove(element)
	}
}

func (c *boundedCache[K, V]) DeleteIf(remove func(K, V) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key, element := range c.items {
		entry := element.Value.(boundedCacheEntry[K, V])
		if remove(key, entry.value) {
			delete(c.items, key)
			c.order.Remove(element)
		}
	}
}

func (c *boundedCache[K, V]) Values() []V {
	c.mu.Lock()
	defer c.mu.Unlock()

	values := make([]V, 0, len(c.items))
	if c.order == nil {
		return values
	}
	for element := c.order.Front(); element != nil; element = element.Next() {
		values = append(values, element.Value.(boundedCacheEntry[K, V]).value)
	}
	return values
}

func (c *boundedCache[K, V]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = nil
	c.order = nil
}

func (c *boundedCache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

func (c *boundedCache[K, V]) init(maxEntries int) {
	if maxEntries <= 0 {
		maxEntries = 1
	}
	if c.maxEntries <= 0 {
		c.maxEntries = maxEntries
	}
	if c.items == nil {
		c.items = make(map[K]*list.Element)
	}
	if c.order == nil {
		c.order = list.New()
	}
}
