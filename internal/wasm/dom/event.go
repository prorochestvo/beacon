//go:build js && wasm

package dom

import (
	"sync"
	"syscall/js"
)

// On binds handler to the named DOM event on target and returns a release
// closure. Callers MUST invoke the returned closure when the bound element is
// destroyed; failing to do so leaks an entry in the runtime's function table
// for the lifetime of the WASM module.
//
// The release closure is idempotent: calling it more than once is safe and
// has no additional effect after the first call.
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
