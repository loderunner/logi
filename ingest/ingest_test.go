package ingest

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	ingest_mock "github.com/loderunner/logi/ingest/mock_"
)

func TestRun(t *testing.T) {
	// start runs Run in the bubble and returns what it reports.
	type harness struct {
		lines    chan Line
		cancel   context.CancelFunc
		done     chan error
		notified *[]Ingested
	}

	start := func(t *testing.T, w Appender) harness {
		t.Helper()

		ctx, cancel := context.WithCancel(t.Context())
		lines := make(chan Line)
		done := make(chan error, 1)

		var notified []Ingested

		go func() {
			done <- Run(ctx, lines, w, func(i Ingested) {
				notified = append(notified, i)
			})
		}()

		synctest.Wait()

		return harness{
			lines:    lines,
			cancel:   cancel,
			done:     done,
			notified: &notified,
		}
	}

	t.Run("flushes at 10,000 rows", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockAppender := ingest_mock.NewMockAppender(ctrl)
			mockAppender.EXPECT().
				Append(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				Return(nil).
				Times(10_000)
			mockAppender.EXPECT().Flush().Return(nil, nil).Times(1)

			h := start(t, mockAppender)

			for i := range 10_000 {
				h.lines <- Line{Text: []byte("x"), End: int64(i + 1)}
			}

			synctest.Wait()
			require.Equal(
				t,
				[]Ingested{{Rows: 10_000, Bytes: 10_000}},
				*h.notified,
			)

			h.cancel()

			err := <-h.done
			require.NoError(t, err)
		})
	})

	t.Run("flushes after 100ms with fewer rows", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockAppender := ingest_mock.NewMockAppender(ctrl)
			mockAppender.EXPECT().
				Append(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				Return(nil).
				Times(3)
			mockAppender.EXPECT().Flush().Return(nil, nil).Times(1)

			h := start(t, mockAppender)

			for i := range 3 {
				h.lines <- Line{Text: []byte("x"), End: int64(i + 1)}
			}

			synctest.Wait()
			require.Empty(t, *h.notified)

			synctest.Sleep(100 * time.Millisecond)
			require.Equal(t, []Ingested{{Rows: 3, Bytes: 3}}, *h.notified)

			h.cancel()

			err := <-h.done
			require.NoError(t, err)
		})
	})

	t.Run("does not flush when idle", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			// No expectations: any call fails the test.
			mockAppender := ingest_mock.NewMockAppender(ctrl)

			h := start(t, mockAppender)

			synctest.Sleep(time.Second)
			require.Empty(t, *h.notified)

			h.cancel()

			err := <-h.done
			require.NoError(t, err)
		})
	})

	t.Run("flushes pending rows when the context ends", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockAppender := ingest_mock.NewMockAppender(ctrl)
			mockAppender.EXPECT().
				Append(gomock.Any(), gomock.Any(), "x", "x", map[string]string{"_raw": "x"}).
				Return(nil)
			mockAppender.EXPECT().Flush().Return(nil, nil)

			h := start(t, mockAppender)
			h.lines <- Line{Text: []byte("x"), End: 2}
			synctest.Wait()

			h.cancel()

			err := <-h.done
			require.NoError(t, err)
		})
	})

	t.Run("reports EOF when the channel closes", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockAppender := ingest_mock.NewMockAppender(ctrl)
			mockAppender.EXPECT().
				Append(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				Return(nil).
				Times(2)
			mockAppender.EXPECT().Flush().Return(nil, nil).Times(1)

			h := start(t, mockAppender)
			h.lines <- Line{Text: []byte("a"), End: 2}
			h.lines <- Line{Text: []byte("b"), End: 4}
			close(h.lines)

			err := <-h.done
			require.NoError(t, err)
			require.Equal(t, []Ingested{
				{Rows: 2, Bytes: 4},
				{Rows: 2, Bytes: 4, EOF: true},
			}, *h.notified)
		})
	})

	t.Run("reports EOF without a flush on empty input", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockAppender := ingest_mock.NewMockAppender(ctrl)

			h := start(t, mockAppender)
			close(h.lines)

			err := <-h.done
			require.NoError(t, err)
			require.Equal(t, []Ingested{{EOF: true}}, *h.notified)
		})
	})

	t.Run("assigns increasing ids and arrival times", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			type row struct {
				id, ts int64
				raw    string
			}

			var rows []row

			ctrl := gomock.NewController(t)
			mockAppender := ingest_mock.NewMockAppender(ctrl)
			mockAppender.EXPECT().
				Append(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				DoAndReturn(func(id, ts int64, raw, _ string, _ map[string]string) error {
					rows = append(rows, row{id, ts, raw})
					return nil
				}).
				Times(3)
			mockAppender.EXPECT().Flush().Return(nil, nil).AnyTimes()

			h := start(t, mockAppender)

			h.lines <- Line{Text: []byte("a"), End: 2}
			synctest.Sleep(time.Millisecond)
			h.lines <- Line{Text: []byte("b"), End: 4}
			h.lines <- Line{Text: []byte("c"), End: 6}
			close(h.lines)

			err := <-h.done
			require.NoError(t, err)

			require.Len(t, rows, 3)

			for i, r := range rows {
				require.Equal(t, int64(i+1), r.id)
			}

			require.Equal(t, "a", rows[0].raw)
			require.Equal(t, "c", rows[2].raw)
			require.Equal(t, int64(time.Millisecond), rows[1].ts-rows[0].ts)
			require.GreaterOrEqual(t, rows[2].ts, rows[1].ts)
		})
	})

	t.Run("stores a parsed line with its fields", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockAppender := ingest_mock.NewMockAppender(ctrl)
			mockAppender.EXPECT().
				Append(
					int64(1),
					gomock.Any(),
					`{"level":"info","http":{"status":200}}`,
					"info 200",
					map[string]string{"level": "info", "http.status": "200"},
				).
				Return(nil)
			mockAppender.EXPECT().Flush().Return(nil, nil)

			h := start(t, mockAppender)
			h.lines <- Line{
				Text: []byte(`{"level":"info","http":{"status":200}}`),
				End:  40,
			}
			close(h.lines)

			err := <-h.done
			require.NoError(t, err)
		})
	})

	t.Run("stores a line that isn't JSON under _raw", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockAppender := ingest_mock.NewMockAppender(ctrl)
			mockAppender.EXPECT().
				Append(
					int64(1),
					gomock.Any(),
					"not json",
					"not json",
					map[string]string{"_raw": "not json"},
				).
				Return(nil)
			mockAppender.EXPECT().Flush().Return(nil, nil)

			h := start(t, mockAppender)
			h.lines <- Line{Text: []byte("not json"), End: 9}
			close(h.lines)

			err := <-h.done
			require.NoError(t, err)
		})
	})

	t.Run("reports the new keys of each flush", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockAppender := ingest_mock.NewMockAppender(ctrl)
			mockAppender.EXPECT().
				Append(
					gomock.Any(),
					gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
				).
				Return(nil).
				Times(2)
			gomock.InOrder(
				mockAppender.EXPECT().Flush().Return([]string{"a", "b"}, nil),
				mockAppender.EXPECT().Flush().Return(nil, nil),
			)

			h := start(t, mockAppender)
			h.lines <- Line{Text: []byte(`{"a":"1","b":"2"}`), End: 18}

			synctest.Sleep(flushInterval)

			h.lines <- Line{Text: []byte(`{"a":"3"}`), End: 28}
			close(h.lines)

			err := <-h.done
			require.NoError(t, err)
			require.Equal(t, []Ingested{
				{Rows: 1, Bytes: 18, NewKeys: []string{"a", "b"}},
				{Rows: 2, Bytes: 28},
				{Rows: 2, Bytes: 28, EOF: true},
			}, *h.notified)
		})
	})

	t.Run("returns a flush error", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			boom := errors.New("boom")
			mockAppender := ingest_mock.NewMockAppender(ctrl)
			mockAppender.EXPECT().
				Append(
					gomock.Any(),
					gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
				).
				Return(nil)
			mockAppender.EXPECT().Flush().Return(nil, boom)

			h := start(t, mockAppender)
			h.lines <- Line{Text: []byte("x"), End: 2}
			close(h.lines)

			err := <-h.done
			require.ErrorIs(t, err, boom)
		})
	})
}
