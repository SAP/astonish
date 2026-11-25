package sandbox

import (
	"sync"
	"testing"
)

func TestChildAliasLeasePreservesBorrowedClients(t *testing.T) {
	for _, kind := range []string{"backend", "node-adapter"} {
		t.Run(kind, func(t *testing.T) {
			var pool ToolNodePool
			if kind == "backend" {
				pool = &backendPool{clients: make(map[string]*backendNodeClient)}
			} else {
				pool = AsNodePool(NewNodeClientPool(nil, nil, "", nil))
			}
			parent := pool.GetOrCreate("parent")
			pool.Alias("borrowed", "parent")
			leaser := pool.(ChildAliasLeaser)
			if release := leaser.LeaseChildAlias("borrowed", "parent"); release != nil {
				t.Fatal("adopted existing alias")
			}
			release := leaser.LeaseChildAlias("child", "parent")
			if release == nil || pool.GetOrCreate("child") != parent {
				t.Fatal("fresh child not aliased")
			}
			var wg sync.WaitGroup
			for range 16 {
				wg.Add(1)
				go func() { defer wg.Done(); release() }()
			}
			wg.Wait()
			if pool.GetOrCreate("parent") != parent || pool.GetOrCreate("borrowed") != parent {
				t.Fatal("alias release evicted borrowed owner or sibling")
			}
			if pool.GetOrCreate("child") == parent {
				t.Fatal("owned alias remained")
			}
			if release := leaser.LeaseChildAlias("parent", "parent"); release != nil {
				t.Fatal("leased owner")
			}
		})
	}
}

func TestChildAliasLeaseLeavesReplacementUntouched(t *testing.T) {
	p := &backendPool{clients: make(map[string]*backendNodeClient)}
	p.GetOrCreate("parent")
	other := p.GetOrCreate("other")
	release := p.LeaseChildAlias("child", "parent")
	p.Alias("child", "other")
	release()
	if p.GetOrCreate("child") != other {
		t.Fatal("released a replaced alias")
	}
}
