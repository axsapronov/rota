package safeworker

import (
	"fmt"
	"runtime/debug"

	"github.com/alpkeskin/rota/core/pkg/logger"
)

// Call runs fn and logs a panic instead of crashing the hosting goroutine.
func Call(log *logger.Logger, worker string, fn func()) {
	if fn == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			if log != nil {
				log.Error("background worker panicked",
					"worker", worker,
					"error", r,
					"stack", fmt.Sprintf("%s", debug.Stack()),
				)
			}
		}
	}()
	fn()
}
