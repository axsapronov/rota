package safeworker

import (
	"testing"
)

func TestCall_recoversPanic(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		Call(nil, "test", func() {
			panic("boom")
		})
		Call(nil, "test", func() {})
	}()
	<-done
}
