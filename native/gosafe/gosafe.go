// Package gosafe starts the goroutines of code-forge commands (B509).
//
// A panic in a goroutine is not caught by the recover on the main goroutine
// (B508): Go would exit 2, the usage-error code, with a raw stack trace. Go
// recovers the panic inside the goroutine and gives its site to the handler
// the command installed, which then follows the main-goroutine rules. With
// no handler (package tests, tools) the panic is raised again unchanged.
package gosafe

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
)

// Handler receives a goroutine panic: the goroutine's name and the panic
// site (function, file base name, line). It never sees the panic value, so
// it cannot print it. A handler is expected to end the process.
type Handler func(name, site string)

var handler atomic.Pointer[Handler]

// Seam is nil in the product. Tests set it to make the goroutine with a
// given name panic before its work starts.
var Seam func(name string)

// SetHandler installs h for every later goroutine panic; nil removes it.
func SetHandler(h Handler) {
	if h == nil {
		handler.Store(nil)
		return
	}
	handler.Store(&h)
}

// Go runs fn in a new goroutine named name. A panic in fn goes to the
// installed handler; if the handler returns, the goroutine stops there.
func Go(name string, fn func()) {
	seam := Seam
	go func() {
		defer func() {
			value := recover()
			if value == nil {
				return
			}
			h := handler.Load()
			if h == nil {
				panic(value)
			}
			(*h)(name, Site())
		}()
		if seam != nil {
			seam(name)
		}
		fn()
	}()
}

// Site names the first frame outside the Go runtime below the panic being
// recovered: function, file base name and line. Call it from the deferred
// function that recovers.
func Site() string {
	pcs := make([]uintptr, 64)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(1, pcs)])
	panicking := false
	for {
		frame, more := frames.Next()
		if frame.Function == "runtime.gopanic" {
			panicking = true
		} else if panicking && !strings.HasPrefix(frame.Function, "runtime.") {
			name := frame.Function[strings.LastIndex(frame.Function, "/")+1:]
			name = strings.TrimPrefix(strings.TrimPrefix(name, "main."), "code-forge-native.")
			return fmt.Sprintf("%s %s:%d", name, filepath.Base(frame.File), frame.Line)
		}
		if !more {
			return "unknown site"
		}
	}
}
