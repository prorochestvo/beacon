//go:build js && wasm

package dom

import (
	"sync"
	"syscall/js"
)

// On binds handler to the named DOM event on target and returns a release
// closure. Callers MUST invoke it when the bound element is destroyed; not
// doing so leaks a function-table entry for the WASM module's lifetime.
//
// The release closure is idempotent: calls after the first are no-ops.
func On(target js.Value, event string, handler func(js.Value)) (release func()) {
	fn := js.FuncOf(func(_ js.Value, args []js.Value) any {
		var ev js.Value
		if len(args) > 0 {
			ev = args[0]
		}
		handler(ev)
		return nil
	})
	target.Call("addEventListener", event, fn)

	var once sync.Once
	return func() {
		once.Do(func() {
			target.Call("removeEventListener", event, fn)
			fn.Release()
		})
	}
}
