package undo

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"
)

func TestRingLatestFor(t *testing.T) {
	ring := NewRing()
	ring.Push(Entry{Context: "c", Kind: "Secret", Namespace: "n", Name: "one", Previous: map[string][]byte{"a": []byte("old-a")}})
	ring.Push(Entry{Context: "c", Kind: "Secret", Namespace: "n", Name: "two", Previous: map[string][]byte{"b": []byte("old-b")}})
	ring.Push(Entry{Context: "c", Kind: "Secret", Namespace: "n", Name: "one", Previous: map[string][]byte{"c": []byte("old-c")}})
	entry, ok := ring.LatestFor("c", "Secret", "n", "one")
	if !ok || string(entry.Previous["c"]) != "old-c" {
		t.Fatalf("LatestFor() = %+v, %t", entry, ok)
	}
	again, ok := ring.LatestFor("c", "Secret", "n", "one")
	if !ok || !reflect.DeepEqual(again, entry) {
		t.Fatal("LatestFor() consumed the entry")
	}
	if _, ok := ring.LatestFor("c", "Secret", "n", "missing"); ok {
		t.Fatal("LatestFor(missing) ok = true")
	}
}

func TestRingCapacity(t *testing.T) {
	ring := NewRing()
	for i := range 25 {
		name := "kept"
		if i < 5 {
			name = "evicted"
		}
		ring.Push(Entry{Context: "c", Kind: "Secret", Namespace: "n", Name: name, Previous: map[string][]byte{"k": []byte(fmt.Sprint(i))}})
	}
	if ring.Len() != Capacity {
		t.Fatalf("Len() = %d, want %d", ring.Len(), Capacity)
	}
	if _, ok := ring.LatestFor("c", "Secret", "n", "evicted"); ok {
		t.Fatal("oldest entries were not evicted")
	}
}

func TestPushClonesMaps(t *testing.T) {
	ring := NewRing()
	previous := map[string][]byte{"k": []byte("old")}
	added := []string{"new"}
	ring.Push(Entry{Context: "c", Kind: "Secret", Namespace: "n", Name: "one", Previous: previous, Added: added})
	previous["k"][0] = 'X'
	previous["other"] = []byte("changed")
	added[0] = "changed"

	entry, ok := ring.LatestFor("c", "Secret", "n", "one")
	if !ok || !bytes.Equal(entry.Previous["k"], []byte("old")) || len(entry.Previous) != 1 || !reflect.DeepEqual(entry.Added, []string{"new"}) {
		t.Fatalf("stored entry = %+v, %t", entry, ok)
	}
	entry.Previous["k"][0] = 'Y'
	entry.Previous["other"] = []byte("changed")
	entry.Added[0] = "changed"
	again, _ := ring.LatestFor("c", "Secret", "n", "one")
	if !bytes.Equal(again.Previous["k"], []byte("old")) || len(again.Previous) != 1 || !reflect.DeepEqual(again.Added, []string{"new"}) {
		t.Fatalf("returned entry was not cloned: %+v", again)
	}
}
