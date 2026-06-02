package services

import "testing"

func TestGeoAddressQueue_Release(t *testing.T) {
	q := newGeoAddressQueue()
	_ = q.Enqueue([]string{"a:1", "b:2"})
	q.Release([]string{"a:1"})
	if _, ok := q.pending["a:1"]; ok {
		t.Fatal("a:1 should be released from pending")
	}
	if _, ok := q.pending["b:2"]; !ok {
		t.Fatal("b:2 should still be pending")
	}
}
