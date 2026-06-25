//go:build js && wasm

package dom

import (
	"context"
	"encoding/json"
	"fmt"
	"syscall/js"
)

// FetchNoContent issues an HTTP request via window.fetch and discards the
// response body. For 204 No Content endpoints (e.g. PATCH
// /api/sources/{name}/active), where JSON-decoding would yield an unmarshal
// error.
//
// Same js.Func lifecycle as FetchJSON: every allocation has a deferred Release.
func FetchNoContent(ctx context.Context, method, url string, body any, headers map[string]string) error {
	opts := map[string]any{"method": method}

	if len(headers) > 0 {
		h := map[string]any{}
		for k, v := range headers {
			h[k] = v
		}
		opts["headers"] = h
	}

	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		opts["body"] = string(buf)
	}

	type result struct {
		err error
	}
	ch := make(chan result, 1)

	var thenFn, fetchErr js.Func

	thenFn = js.FuncOf(func(_ js.Value, args []js.Value) any {
		resp := args[0]
		// window.fetch resolves even for 4xx/5xx; response.ok is false for any
		// status outside 200–299, so treat those as errors.
		if !resp.Get("ok").Bool() {
			ch <- result{err: fmt.Errorf("http %d", resp.Get("status").Int())}
			return nil
		}
		ch <- result{}
		return nil
	})
	fetchErr = js.FuncOf(func(_ js.Value, args []js.Value) any {
		ch <- result{err: fmt.Errorf("fetch: %s", args[0].String())}
		return nil
	})
	defer thenFn.Release()
	defer fetchErr.Release()

	js.Global().Call("fetch", url, js.ValueOf(opts)).Call("then", thenFn, fetchErr)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case r := <-ch:
		return r.err
	}
}

// FetchJSON issues an HTTP request via window.fetch and decodes the response
// body as JSON into T.
//
// FetchJSON MUST be called from a goroutine: the JS event loop is owned by the
// main goroutine, where waiting synchronously deadlocks.
//
// Every js.FuncOf allocation has a deferred Release so the runtime's function
// table does not grow unbounded across calls.
//
// When body is nil the "body" key is omitted (an explicit null on GET can
// misbehave in older browsers); when headers is nil or empty "headers" is
// omitted.
//
// If ctx is cancelled mid-flight, FetchJSON returns ctx.Err() immediately. The
// browser-side promise still runs to completion, but the callbacks are released
// before it settles, so the WASM runtime logs "call to released function" to the
// console for each callback it eventually fires. Cosmetic — no panic, no leak.
func FetchJSON[T any](ctx context.Context, method, url string, body any, headers map[string]string) (T, error) {
	var zero T

	opts := map[string]any{"method": method}

	if len(headers) > 0 {
		h := map[string]any{}
		for k, v := range headers {
			h[k] = v
		}
		opts["headers"] = h
	}

	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return zero, fmt.Errorf("marshal body: %w", err)
		}
		opts["body"] = string(buf)
	}

	type result struct {
		text string
		err  error
	}
	ch := make(chan result, 1)

	var thenFn, textOK, textErr, fetchErr js.Func

	thenFn = js.FuncOf(func(_ js.Value, args []js.Value) any {
		resp := args[0]
		// window.fetch resolves even for 4xx/5xx; response.ok is false for any
		// status outside 200–299, so treat those as errors.
		if !resp.Get("ok").Bool() {
			ch <- result{err: fmt.Errorf("http %d", resp.Get("status").Int())}
			return nil
		}
		resp.Call("text").Call("then", textOK, textErr)
		return nil
	})
	textOK = js.FuncOf(func(_ js.Value, args []js.Value) any {
		ch <- result{text: args[0].String()}
		return nil
	})
	textErr = js.FuncOf(func(_ js.Value, args []js.Value) any {
		ch <- result{err: fmt.Errorf("read body: %s", args[0].String())}
		return nil
	})
	fetchErr = js.FuncOf(func(_ js.Value, args []js.Value) any {
		ch <- result{err: fmt.Errorf("fetch: %s", args[0].String())}
		return nil
	})
	defer thenFn.Release()
	defer textOK.Release()
	defer textErr.Release()
	defer fetchErr.Release()

	js.Global().Call("fetch", url, js.ValueOf(opts)).Call("then", thenFn, fetchErr)

	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return zero, r.err
		}
		var out T
		if err := json.Unmarshal([]byte(r.text), &out); err != nil {
			return zero, fmt.Errorf("unmarshal response: %w", err)
		}
		return out, nil
	}
}
