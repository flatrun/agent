package runtimes

import "fmt"

func (d *Docker) leaseCache(key string) (string, func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cacheSlots == nil {
		d.cacheSlots = map[string]map[int]bool{}
	}
	slots := d.cacheSlots[key]
	if slots == nil {
		slots = map[int]bool{}
		d.cacheSlots[key] = slots
	}
	slot := 0
	for slots[slot] {
		slot++
	}
	slots[slot] = true
	return fmt.Sprintf("%s-%d", cacheName(d.Scope, key), slot), func() { d.mu.Lock(); defer d.mu.Unlock(); delete(slots, slot) }
}
