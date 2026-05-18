// Package threadsafe provides mutex-protected wrappers around standard types.
// Every exported type in this package is safe for concurrent use by multiple goroutines.
package threadsafe

import (
	"bytes"
	"sync"
)

// NewBuffer creates a thread-safe Buffer initialised with the given byte slice.
func NewBuffer(b []byte) *Buffer {
	return &Buffer{
		buf: bytes.NewBuffer(b),
	}
}

// NewBufferString creates a thread-safe Buffer initialised with the given string.
func NewBufferString(s string) *Buffer {
	return &Buffer{
		buf: bytes.NewBufferString(s),
	}
}

// Buffer is a goroutine-safe wrapper around bytes.Buffer.
// All operations are serialised with a mutex.
type Buffer struct {
	buf *bytes.Buffer
	m   sync.Mutex
}

// Read reads from the underlying buffer, acquiring the mutex for the duration.
func (b *Buffer) Read(p []byte) (n int, err error) {
	b.m.Lock()
	defer b.m.Unlock()
	return b.buf.Read(p)
}

// Write appends p to the underlying buffer, acquiring the mutex for the duration.
func (b *Buffer) Write(p []byte) (n int, err error) {
	b.m.Lock()
	defer b.m.Unlock()
	return b.buf.Write(p)
}

// String returns the accumulated bytes as a string, acquiring the mutex for the duration.
func (b *Buffer) String() string {
	b.m.Lock()
	defer b.m.Unlock()
	return b.buf.String()
}

// Bytes returns a slice of the accumulated bytes, acquiring the mutex for the duration.
func (b *Buffer) Bytes() []byte {
	b.m.Lock()
	defer b.m.Unlock()
	return b.buf.Bytes()
}
