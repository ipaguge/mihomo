package nat

import (
	"net"
	"sync"
)

type Table struct {
	mapping sync.Map
}

func (t *Table) Set(key string, pc net.PacketConn) {
	t.mapping.Store(key, pc)
}

func (t *Table) Get(key string) net.PacketConn {
	item, exist := t.mapping.Load(key)
	if !exist {
		return nil
	}
	return item.(net.PacketConn)
}

func (t *Table) GetOrCreateLock(key string) (*sync.WaitGroup, bool) {
	item, loaded := t.mapping.LoadOrStore(key, &sync.WaitGroup{})
	return item.(*sync.WaitGroup), loaded
}

func (t *Table) Delete(key string) {
	t.mapping.Delete(key)
}

// New return *Cache
func New() *Table {
	return &Table{}
}
