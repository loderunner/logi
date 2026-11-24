// Package ingest reads log lines from a file or a stream and appends them to
// the store.
package ingest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/fsnotify/fsnotify"
)

// Line is one line of input, without its line terminator.
type Line struct {
	Text []byte
	// End is the byte offset in the input just after the line, for showing
	// how much of the input has been read.
	End int64
}

// lineReader reads lines of any length. It keeps a trailing partial line
// until its newline arrives, so it can resume after io.EOF on a growing file.
type lineReader struct {
	br      *bufio.Reader
	partial []byte
	offset  int64
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{br: bufio.NewReader(r)}
}

// next returns the next complete line. At the end of the input it returns
// io.EOF and keeps any partial line for the next call. It returns the
// context's error if ctx is done.
func (lr *lineReader) next(ctx context.Context) (Line, error) {
	err := ctx.Err()
	if err != nil {
		return Line{}, err
	}

	chunk, err := lr.br.ReadBytes('\n')
	lr.partial = append(lr.partial, chunk...)
	if err != nil {
		return Line{}, err
	}

	text := lr.partial
	lr.partial = nil
	lr.offset += int64(len(text))

	return Line{Text: trimEOL(text), End: lr.offset}, nil
}

// flushPartial returns the partial line left at the end of the input, if any.
func (lr *lineReader) flushPartial() (Line, bool) {
	if len(lr.partial) == 0 {
		return Line{}, false
	}

	text := lr.partial
	lr.partial = nil
	lr.offset += int64(len(text))

	return Line{Text: trimEOL(text), End: lr.offset}, true
}

func trimEOL(b []byte) []byte {
	b = bytes.TrimSuffix(b, []byte("\n"))
	return bytes.TrimSuffix(b, []byte("\r"))
}

// send delivers line on out, or returns the context's error.
func send(ctx context.Context, out chan<- Line, line Line) error {
	select {
	case out <- line:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ReadAll sends the lines of r on out until EOF, including a last line with
// no newline, then closes out. It returns nil if ctx ends first. A blocked
// read on r can't be interrupted, so ReadAll returns only when the read does.
// It serves stdin, and files that aren't followed.
func ReadAll(ctx context.Context, r io.Reader, out chan<- Line) error {
	defer close(out)

	lr := newLineReader(r)

	for {
		line, err := lr.next(ctx)
		if errors.Is(err, io.EOF) {
			last, ok := lr.flushPartial()
			if ok {
				return ignoreCanceled(ctx, send(ctx, out, last))
			}

			return nil
		}
		if err != nil {
			return ignoreCanceled(ctx, err)
		}

		err = send(ctx, out, line)
		if err != nil {
			return ignoreCanceled(ctx, err)
		}
	}
}

// ReadFollow sends the lines of f on out, from its current position, then keeps
// sending lines as they are appended until ctx ends. It then closes out and
// returns nil. Rotation and truncation aren't handled.
//
// ReadFollow waits for writes with fsnotify. It watches f before the first read,
// so a write between the last read and the wait isn't missed.
func ReadFollow(ctx context.Context, f *os.File, out chan<- Line) error {
	defer close(out)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create watcher: %w", err)
	}
	defer func() { _ = watcher.Close() }()

	err = watcher.Add(f.Name())
	if err != nil {
		return fmt.Errorf("watch %s: %w", f.Name(), err)
	}

	lr := newLineReader(f)

	for {
		// Read to EOF.
		for {
			line, err := lr.next(ctx)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return ignoreCanceled(ctx, err)
			}

			err = send(ctx, out, line)
			if err != nil {
				return ignoreCanceled(ctx, err)
			}
		}

		err = waitForWrite(ctx, watcher)
		if err != nil {
			return ignoreCanceled(ctx, err)
		}
	}
}

// waitForWrite blocks until the watched file is written to, then drains the
// events queued behind it.
func waitForWrite(ctx context.Context, watcher *fsnotify.Watcher) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err, ok := <-watcher.Errors:
			if !ok {
				return errors.New("watcher closed")
			}

			return fmt.Errorf("watch: %w", err)
		case ev, ok := <-watcher.Events:
			if !ok {
				return errors.New("watcher closed")
			}
			if ev.Has(fsnotify.Write) {
				drain(watcher)
				return nil
			}
		}
	}
}

func drain(watcher *fsnotify.Watcher) {
	for {
		select {
		case <-watcher.Events:
		default:
			return
		}
	}
}

// ignoreCanceled turns the cancellation of ctx into a normal end, and wraps
// any other error.
func ignoreCanceled(ctx context.Context, err error) error {
	if err == nil || (ctx.Err() != nil && errors.Is(err, ctx.Err())) {
		return nil
	}

	return fmt.Errorf("read input: %w", err)
}
