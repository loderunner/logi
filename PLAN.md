# logi plan

logi is a terminal UI for JSONL logs: Kibana as `htop`. It reads a file or
stdin, keeps reading as input grows, and shows three panes:

1. A bar chart of log volume over time.
2. A filter expression.
3. A scrollable table of log lines.

Main use cases:

- `kubectl logs -f <pod> | logi`
- `logi app.log` on a log file that hasn't rotated yet (usually under
  100 MB)

Supported, but not optimized: files up to 4 GB.

## Decisions

### Stack

- Go 1.27. Use the standard library's `encoding/json/v2`,
  `encoding/json/jsontext`, `uuid` and `testing/synctest`.
- `charm.land/bubbletea/v2`, `charm.land/bubbles/v2`,
  `charm.land/lipgloss/v2`.
  - The table is our own, not `bubbles/table`. That one holds every row in a
    slice, stops its cursor silently at the first and last rows, always draws
    a header, and doesn't sanitize. We query only the rows on screen.
    bubbles is for the filter's text input.
- `github.com/duckdb/duckdb-go/v2`, which needs cgo. Building needs a C
  toolchain.
- `github.com/spf13/viper` and `github.com/spf13/pflag` for configuration.
- golangci-lint v2, with the config copied from hodor-shield. The default
  `forbidigo` rules ban `fmt.Print*`, which would corrupt the TUI.
- Conventions: the `loderunner:go-conventions` skill. Packages are organized
  vertically by feature.

### Storage: DuckDB in memory

A benchmark on a 1 GiB, 3M-line file compared the two candidates, using
the same ingest path for both:

| | SQLite (`modernc`, expression indexes, FTS5) | DuckDB (in memory, `VARCHAR` columns) |
|---|---|---|
| Catch-up ingest | 20s + 2m10s index build | 51s, no indexes |
| Filter (count, page, histogram) | 0.1ms – 6.4s | 2 – 11ms, ~80ms for free text |
| Live insert after catch-up | 5k rows/s | 31–50k rows/s |
| Peak RSS | 1.6 GiB | 1.2 GiB |

- The DuckDB database is in memory. `memory_limit` comes from
  `--memory-limit` (default `1GB`). Data over the limit spills to a private
  temp directory, which logi removes on exit. On startup, logi removes temp
  directories left behind by crashed runs. There is no database file.
- Schema: `logs(id BIGINT, ts BIGINT, raw VARCHAR, vals VARCHAR, <one
  VARCHAR column per key>)`.
  - `ts` is Unix nanoseconds.
  - `raw` is the original line.
  - `vals` is all values joined with spaces, for free-text search. Keys are
    never searched.
  - When a key first appears, logi adds its column with `ALTER TABLE`.
  - Key columns have generated names (`k1`, `k2`, …), and `logstore` keeps
    the key-to-column map. DuckDB identifiers are case-insensitive, and `id`,
    `ts`, `raw` and `vals` are taken, so keys such as `ID`, `raw`, or `Level`
    next to `level` would collide. Callers pass keys. Only `logstore` sees
    column names. A key becomes visible to queries after its `ALTER TABLE`
    commits.
  - Value text: a string is its decoded text, and a number or boolean is its
    literal text. A `null` and an empty object have no value, so they have no
    column. An array is compact JSON text. With duplicate keys, the last one
    wins.
  - A VARCHAR that isn't valid UTF-8 would silently become `NULL`, so
    `logstore` replaces invalid bytes with U+FFFD.
- Every key column is `VARCHAR`, so a key can't change type between lines.
  Numeric comparisons compile to `TRY_CAST(col AS DOUBLE)`, which the
  benchmark measured as fast as typed columns.
- No indexes. Columnar scans are fast enough at this scale.
- Nothing persists between runs except preferences (see
  [Configuration](#configuration)).

### Input

- A file argument is read from the start, then followed for appended lines.
  logi ignores rotation and truncation.
- Stdin is read with plain blocking reads until EOF. The status bar then
  shows `EOF`.
- Following a file waits for writes with fsnotify, watching that one file.
  The reader adds the watch before its first read, so a write between the
  last read and the wait isn't missed. At EOF it blocks until a `Write`
  event arrives, then reads again. Other events are ignored: rotation and
  truncation are out of scope. A read on a regular file returns EOF at once
  instead of blocking until more data arrives, so the reader can't just
  call `Read` in a loop.
- Each line is parsed with `jsontext.Decoder`, with `AllowInvalidUTF8(true)`
  and `AllowDuplicateNames(true)`, because real logs break both rules.
- Nested objects flatten to dotted keys (`http.status`). Arrays are kept as
  JSON text.
- A line that isn't JSON becomes a row with a single `_raw` key. So does a
  JSON value that isn't an object, and an object followed by anything but
  whitespace. Such a line never has partial fields, and its `vals` is the
  line.
- Timestamps:
  - The time field is detected among `time`, `ts`, `timestamp` and
    `@timestamp`, or set with `--time-field`.
  - Formats are RFC3339 with any fractional precision, and numeric epochs in
    s, ms or ns (guessed from the magnitude).
  - A line with no timestamp gets its arrival time.
- The catch-up speed (~60k rows/s) is accepted. Rows appear within ~70ms.
  For file input, the status bar shows the percentage loaded so far.

### Concurrency

- **Reader goroutine.** Reads lines from the file or stdin, blocking on
  stdin and waiting for file writes when following a file. Sends lines on a
  channel.
- **Ingest goroutine.** The only writer. It parses lines, adds columns for
  new keys, and appends rows with the DuckDB Appender. It flushes every 10k
  rows or 100ms, whichever comes first. After each flush it sends
  `Ingested{rows, newKeys}` to the bubbletea program.
  - The Appender's column set is fixed, so on new keys it flushes, closes
    the Appender, runs `ALTER TABLE`, and reopens it.
- **bubbletea loop.** Never blocks on the database. Each query (table
  window, histogram, counts) runs as a `tea.Cmd` on its own connection from
  the pool. Starting a new query cancels the previous one of the same kind
  through its context, and late results are dropped by generation number.
  The histogram is refreshed at most twice per second while ingest runs.
- The table only queries the rows on screen, with keyset pagination on
  `(ts, id)`.

### Filter language (KQL subset)

| Syntax | Meaning |
|---|---|
| `field:value` | Exact match, case-sensitive. |
| `field:~value` | Exact match, case-insensitive. |
| `field:val*`, `field:*val`, `field:*val*` | Prefix, suffix or substring match. Wildcards only at the front and/or back. |
| `field>N`, `field>=N`, `field<N`, `field<=N` | Numeric comparison. |
| `word` | Free-text search: case-insensitive substring match over all values. |
| `AND`, `OR`, `NOT`, `( … )` | Boolean logic. Adjacent terms are combined with `AND`. |

- The filter applies when you press Enter. Parse errors appear inline under
  the filter.
- The time range isn't part of KQL. It comes from the histogram window and
  is combined with the filter (AND).
- The filter is compiled to a SQL `WHERE` fragment with bound parameters.
  Column names are quoted identifiers.

### UI

- **Table**
  - Sorted by `(ts, id)`, newest at the top by default. The timestamp
    header shows `▼` or `▲`.
  - The header row is a focus stop. Enter on it flips newest at the top or
    the bottom. Timestamp is the only sort key, so the header has no column
    cursor; the stop is skipped when the timestamp column isn't shown.
  - While you're at the newest row, the table follows new rows. Scrolling
    away pauses following, and the view stays anchored on the top row's
    `(ts, id)`, so late lines don't shift what you're reading.
- **Default columns.** Timestamp, a level-like key and a message-like key
  when detected; otherwise timestamp and `_raw`. Keys match without regard to
  case, in priority order. Level: `level`, `lvl`, `severity`, `loglevel`,
  `log_level`, `levelname`. Message: `msg`, `message`, `text`, `log`, `event`.
  When every shown key is empty for a row, the row shows its raw line across
  those columns. That covers non-JSON lines and JSON lines without a level
  or message.
- **Column widths.** The timestamp is fixed. Another column except the last
  is as wide as the widest value it has shown, capped at a quarter of the
  pane, so streaming `info` and `error` rows don't make the message column
  jump. The last column takes the rest.
- **Detail pane.** Enter on a row opens its pretty-printed JSON. Esc closes
  it.
- **Column picker**
  - `c` opens it, with type-to-narrow search over all known keys. Space
    toggles a column.
  - Columns appear in the order added, with no reordering.
  - Ctrl+X resets to the default columns.
  - A saved column whose key hasn't appeared yet stays hidden until it does.
- **Histogram**
  - Counts the rows matching the filter. Bucket size comes from the visible
    span and the terminal width.
  - `←`/`→` move the cursor, and the window pans at the edges. `+`/`-` zoom
    in and out around the cursor.
  - The visible window also filters the table.
  - `=` pins the window to the live edge. While pinned, follow is on and the
    window slides with new data. Panning or zooming away from the live edge
    unpins it.
- **Focus**
  - Tab cycles focus histogram → filter → table.
  - `↑`/`↓` also walk that chain: down from the histogram goes to the filter,
    down from the filter goes to the table header, down from the header goes
    to row 0, and down from row 0 goes to row 1. Hidden panes are skipped.
  - Tab into the table lands on the selected row, not the header.
  - `/` focuses the filter, and unhides it if it was hidden.
  - The table has focus at startup.
- **Pane toggles.** Ctrl+G (graph), Ctrl+F (filter), Ctrl+T (table). Ctrl+F is
  removed from the filter text input's key map; `→` still moves its
  cursor.
- **Quit.** `q` (outside the filter) or Ctrl+C.
- **Keys outside the filter.** `+`, `-`, `=`, `c` and `q` act only when
  the filter doesn't have focus. While it does, they're typed as text.

### Configuration

- Precedence, highest first:
  1. Flags
  2. Environment variables (`LOGI_` + the flag name in upper snake case,
     e.g. `LOGI_HIDE_HISTOGRAM`)
  3. Explicit config file (`--config` or `LOGI_CONFIG`)
  4. Implicit config file (`$XDG_CONFIG_HOME/logi/config.json`, else
     `~/.config/logi/config.json`, including on macOS)
  5. Defaults
- The config file is JSON.
- These preferences changed in the TUI are written to the config file in
  use: pane visibility, column layout, and row order. logi writes only the
  changed key. It never calls viper's `WriteConfig`, which would also dump
  flag and environment overrides into the file.
- Flags: `--config`, `--time-field`, `--memory-limit`, `--hide-histogram`,
  `--hide-filter`, `--hide-log`.

### Out of scope

- Rotation and truncation handling.
- A persistent database.
- Saved queries.
- Mouse support.
- Multiple input files.
- Regex.
- Wildcards in the middle of field values.
- `kubectl` `--prefix` and `--timestamps` line prefixes.
- Column reordering.
- A stacked or per-level histogram.
- Aggregations other than count.
- Export.
- Themes.
- Ingest speed-ups, unless a real input needs them.

## Package layout

| Package | Owns |
|---|---|
| `main` (module root) | Wiring: config, input, store, program. |
| `config/` | viper and pflag loading, precedence, writing changed keys. |
| `ingest/` | Reading and following input, parsing and flattening lines, timestamp detection, batching. |
| `logstore/` | The DuckDB lifecycle (memory limit, temp directory), schema, the Appender, and the window, count and histogram queries. The only package that writes SQL. |
| `kql/` | Filter parser, AST, and compiling to a `logstore` filter. |
| `tui/` | Root model: layout, focus, key map, status bar. |
| `tui/table/`, `tui/histogram/`, `tui/filterbar/`, `tui/picker/`, `tui/detail/` | One package per pane. |
| `cmd/genlogs/` | Synthetic log generator, for tests and manual runs. |

## Deliverables

Each deliverable ends with you running logi and seeing something new. Make
test input with:

```bash
go run ./cmd/genlogs -size-mb 50 > /tmp/app.log
```

The engine deliverables (E1–E4) are larger. After them, each deliverable is
one feature.

### Engine

| # | You run | You see | Built |
|---|---|---|---|
| E1 | `logi /tmp/app.log`, `kubectl logs -f … \| logi`, and `go run ./cmd/genlogs >> /tmp/app.log` in another shell | Raw lines in a one-column table, newest at the top, filling while loading. Appended lines arrive. Status bar: rows, rows/s, % loaded or `EOF`. `q` quits. | Module setup, reader and ingest goroutines, in-memory DuckDB with a fixed memory limit and private temp directory (stale ones cleaned up), Appender batching, `Ingested` messages, bubbletea v2 table querying only the rows on screen, arrival-order `ts`. |
| E2 | `logi /tmp/app.log` | Real columns: timestamp, level and message. Lines that aren't JSON show as `_raw`. | `jsontext` flattening, a `VARCHAR` column added per new key, `vals` column, detection of level and message keys. |
| E3 | `logi /tmp/app.log` | Rows sorted by their own timestamps, newest on top. Late lines don't move a scrolled view. | Timestamp detection and parsing, `ts` from the line with arrival time as fallback, keyset scrolling on `(ts, id)`, follow while at the top. |
| E4 | `LOGI_TIME_FIELD=time logi --memory-limit 512MB /tmp/app.log` and `logi --help` | Flags, environment and config files all take effect, in precedence order. | `config/` with viper and pflag, `--config`, `--time-field`, `--memory-limit`, writing a single key back to the selected file. |

### Features

| # | You see | Built |
|---|---|---|
| F1 | Enter opens a row's pretty-printed JSON, Esc closes it. | `tui/detail`. |
| F2 | `/` focuses a filter bar. `level:error` plus Enter filters the table. Typos show an inline error. | `tui/filterbar`, `kql` with a single `field:value` term, compiling to `WHERE`, cancelling superseded queries. |
| F3 | `service:billing AND (level:warn OR level:error)`, `NOT level:debug`. | Boolean operators, parentheses, implicit AND. |
| F4 | `http.status>=500`. | Numeric comparisons with `TRY_CAST`. |
| F5 | `msg:cache*`, `msg:*timeout`, `msg:*reset*`. | Wildcards at the front and back. |
| F6 | `level:~ERROR`. | Case-insensitive terms. |
| F7 | A bare `timeout` matches anywhere in the values. | Free-text terms over `vals`. |
| F8 | Tab and `↑`/`↓` move between filter and table: filter, header, row 0, row 1. | Focus chain in the root model, header as a stop. |
| F9 | A histogram above the filter, filling in live as rows load. | `tui/histogram`, bucket query, bucket size from the terminal width, throttled refresh. Joins the focus chain. |
| F10 | The histogram reshapes when the filter changes. | Filter shared between the histogram and table queries. |
| F11 | In the histogram, `←`/`→` move a cursor, and `+`/`-` zoom around it. | Window state: span and cursor. |
| F12 | Zooming or panning narrows the table to the visible window. | Window combined with the filter (AND) in table queries. |
| F13 | `=` jumps back to the live edge and follows. Panning away stops following. | Pinned and unpinned states, linked to table follow. |
| F14 | `c` opens a column picker. Toggling adds or removes columns. | `tui/picker`, list of known keys with type-to-narrow search. |
| F15 | Columns are remembered on the next run, including with another file. Keys missing from that file stay hidden until they appear. | Column layout written to and read from the config file. |
| F16 | Ctrl+X in the picker restores the default columns. | Reset, plus writing the reset layout back. |
| F17 | Enter on the table header flips newest at the top or the bottom, with `▼`/`▲` on the timestamp header, and it's remembered. | Order toggle, follow at either end, persisted. |
| F18 | Ctrl+G, Ctrl+F and Ctrl+T hide and show the panes, and it's remembered. `/` brings the filter back. | Pane visibility in the config file, layout reflow, Ctrl+F removed from the text input's key map. |
| F19 | `logi --hide-histogram /tmp/app.log` and `LOGI_HIDE_LOG=1 logi …`. | `--hide-*` flags and environment variables, through config precedence. |
