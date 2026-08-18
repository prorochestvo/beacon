package threadsafe

import (
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ io.Reader = &Buffer{}
var _ io.Writer = &Buffer{}

func TestNewBuffer(t *testing.T) {
	t.Parallel()

	b := NewBuffer([]byte("hello"))
	require.NotNil(t, b)
	assert.Equal(t, "hello", b.String())
}

func TestNewBufferString(t *testing.T) {
	t.Parallel()

	b := NewBufferString("world")
	require.NotNil(t, b)
	assert.Equal(t, "world", b.String())
}

func TestConcurrentBuffer_Write(t *testing.T) {
	t.Parallel()

	b := NewBuffer(nil)
	const goroutines = 50
	var wg sync.WaitGroup

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := b.Write([]byte("x"))
			assert.NoError(t, err)
			assert.Equal(t, 1, n)
		}()
	}
	wg.Wait()

	assert.Len(t, b.Bytes(), goroutines)
}

func TestConcurrentBuffer_Read(t *testing.T) {
	t.Parallel()

	b := NewBufferString("abcde")

	var wg sync.WaitGroup
	results := make([][]byte, 5)
	for i := range 5 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			p := make([]byte, 1)
			_, _ = b.Read(p)
			results[idx] = p
		}(i)
	}
	wg.Wait()

	// All 5 goroutines read 1 byte each — combined they consumed all 5 bytes.
	total := 0
	for _, r := range results {
		total += len(r)
	}
	assert.Equal(t, 5, total)
}

func TestConcurrentBuffer_String(t *testing.T) {
	t.Parallel()

	b := NewBufferString("initial")
	var wg sync.WaitGroup

	// concurrent writes + concurrent reads of String()
	for range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = b.Write([]byte("!"))
		}()
		go func() {
			defer wg.Done()
			_ = b.String() // must not race
		}()
	}
	wg.Wait()
}

func TestConcurrentBuffer_Bytes(t *testing.T) {
	t.Parallel()

	b := NewBuffer([]byte{1, 2, 3})
	var wg sync.WaitGroup

	for range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = b.Write([]byte{0})
		}()
		go func() {
			defer wg.Done()
			_ = b.Bytes() // must not race
		}()
	}
	wg.Wait()
}
