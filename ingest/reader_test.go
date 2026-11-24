package ingest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// collector gathers the lines sent on a channel.
type collector struct {
	mu    sync.Mutex
	lines []Line
	done  chan struct{}
}

func collect(out <-chan Line) *collector {
	c := &collector{done: make(chan struct{})}

	go func() {
		defer close(c.done)

		for line := range out {
			c.mu.Lock()
			c.lines = append(c.lines, line)
			c.mu.Unlock()
		}
	}()

	return c
}

func (c *collector) texts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	texts := make([]string, len(c.lines))
	for i, l := range c.lines {
		texts[i] = string(l.Text)
	}

	return texts
}

func (c *collector) ends() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	ends := make([]int64, len(c.lines))
	for i, l := range c.lines {
		ends[i] = l.End
	}

	return ends
}

func TestReadAll(t *testing.T) {
	read := func(t *testing.T, input string) *collector {
		t.Helper()

		out := make(chan Line)
		c := collect(out)

		err := ReadAll(t.Context(), strings.NewReader(input), out)
		require.NoError(t, err)
		<-c.done

		return c
	}

	t.Run("splits lines and records end offsets", func(t *testing.T) {
		c := read(t, "one\ntwo\nthree\n")

		require.Equal(t, []string{"one", "two", "three"}, c.texts())
		require.Equal(t, []int64{4, 8, 14}, c.ends())
	})

	t.Run("strips CRLF", func(t *testing.T) {
		c := read(t, "one\r\ntwo\r\n")

		require.Equal(t, []string{"one", "two"}, c.texts())
		require.Equal(t, []int64{5, 10}, c.ends())
	})

	t.Run("keeps empty lines", func(t *testing.T) {
		c := read(t, "one\n\ntwo\n")

		require.Equal(t, []string{"one", "", "two"}, c.texts())
	})

	t.Run("reads a line over 64 KiB", func(t *testing.T) {
		long := strings.Repeat("x", 200<<10)
		c := read(t, "a\n"+long+"\nb\n")

		require.Equal(t, []string{"a", long, "b"}, c.texts())
	})

	t.Run("emits a last line with no newline", func(t *testing.T) {
		c := read(t, "one\ntwo")

		require.Equal(t, []string{"one", "two"}, c.texts())
		require.Equal(t, []int64{4, 7}, c.ends())
	})

	t.Run("closes the channel on empty input", func(t *testing.T) {
		c := read(t, "")

		require.Empty(t, c.texts())
	})

	t.Run("stops when the context ends", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		out := make(chan Line)
		c := collect(out)

		err := ReadAll(ctx, strings.NewReader("one\n"), out)
		require.NoError(t, err)
		<-c.done

		require.Empty(t, c.texts())
	})
}

func TestFollow(t *testing.T) {
	// Real file events can't run in a synctest bubble, so these tests wait
	// for them with require.Eventually.
	const (
		wait = 5 * time.Second
		tick = 10 * time.Millisecond
	)

	start := func(t *testing.T, path string) (*collector, func()) {
		t.Helper()

		f, err := os.Open(path)
		require.NoError(t, err)
		t.Cleanup(func() { _ = f.Close() })

		ctx, cancel := context.WithCancel(t.Context())
		out := make(chan Line)
		c := collect(out)
		errc := make(chan error, 1)

		go func() { errc <- Follow(ctx, f, out) }()

		stop := func() {
			cancel()

			err := <-errc
			require.NoError(t, err)
			<-c.done
		}
		t.Cleanup(cancel)

		return c, stop
	}

	appendTo := func(t *testing.T, path, text string) {
		t.Helper()

		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		require.NoError(t, err)

		_, err = f.WriteString(text)
		require.NoError(t, err)

		err = f.Close()
		require.NoError(t, err)
	}

	t.Run("sends the initial content, then appended lines", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "app.log")

		err := os.WriteFile(path, []byte("one\ntwo\n"), 0o600)
		require.NoError(t, err)

		c, stop := start(t, path)

		require.Eventually(t, func() bool {
			return len(c.texts()) == 2
		}, wait, tick)

		appendTo(t, path, "three\n")
		require.Eventually(t, func() bool {
			return len(c.texts()) == 3
		}, wait, tick)

		appendTo(t, path, "four\nfive\n")
		require.Eventually(t, func() bool {
			return len(c.texts()) == 5
		}, wait, tick)

		stop()

		require.Equal(
			t,
			[]string{"one", "two", "three", "four", "five"},
			c.texts(),
		)
		require.Equal(t, []int64{4, 8, 14, 19, 24}, c.ends())
	})

	t.Run("finishes a partial line on a later write", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "app.log")

		err := os.WriteFile(path, []byte("one\npar"), 0o600)
		require.NoError(t, err)

		c, stop := start(t, path)

		require.Eventually(t, func() bool {
			return len(c.texts()) == 1
		}, wait, tick)

		appendTo(t, path, "tial\n")
		require.Eventually(t, func() bool {
			return len(c.texts()) == 2
		}, wait, tick)

		stop()

		require.Equal(t, []string{"one", "partial"}, c.texts())
	})

	t.Run("waits on an empty file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "app.log")

		err := os.WriteFile(path, nil, 0o600)
		require.NoError(t, err)

		c, stop := start(t, path)

		appendTo(t, path, "first\n")
		require.Eventually(t, func() bool {
			return len(c.texts()) == 1
		}, wait, tick)

		stop()
	})

	t.Run("closes the channel when the context ends", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "app.log")

		err := os.WriteFile(path, []byte("one\n"), 0o600)
		require.NoError(t, err)

		c, stop := start(t, path)
		stop()

		<-c.done
	})
}
