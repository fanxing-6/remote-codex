package controller

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestSerialTakeoverAndStaleRelease(t *testing.T) {
	var resets atomic.Int64
	var closing atomic.Int64
	manager := New(0, func(uint64) error { resets.Add(1); return nil })
	generation, err := manager.Acquire("initial", false, func(code int) {
		if code != 4009 {
			t.Error("close code")
		}
		closing.Add(1)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Acquire("ordinary", false, func(int) {}); err == nil {
		t.Fatal("implicit takeover")
	}
	var group sync.WaitGroup
	for i := 0; i < 40; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := manager.Acquire("replacement", true, func(int) { closing.Add(1) }); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	owner, current := manager.Status()
	if current != generation+40 || owner != "replacement" || closing.Load() != 40 {
		t.Fatal("takeover ordering")
	}
	if err = manager.Release("initial", generation); err != nil {
		t.Fatal(err)
	}
	_, after := manager.Status()
	if after != current {
		t.Fatal("stale close changed generation")
	}
	if err = manager.With("initial", generation, func() error { t.Error("stale callback executed"); return nil }); err == nil {
		t.Fatal("stale write accepted")
	}
	if resets.Load() != 41 {
		t.Fatal("reset count")
	}
}
func TestResumeCannotReplaceAnotherController(t *testing.T) {
	manager := New(41, func(uint64) error { return nil })
	first, err := manager.Acquire("first", false, func(int) {})
	if err != nil {
		t.Fatal(err)
	}
	if first != 42 {
		t.Fatal("initial generation was not preserved")
	}
	resumed, err := manager.Resume("first", "resumed", func(int) {})
	if err != nil || resumed <= first {
		t.Fatal("owned reload failed")
	}
	replacement, err := manager.Acquire("replacement", true, func(int) {})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Resume("resumed", "stale-reload", func(int) {}); err == nil {
		t.Fatal("stale reload took control")
	}
	owner, generation := manager.Status()
	if owner != "replacement" || generation != replacement {
		t.Fatal("stale reload changed generation")
	}
}
