# AGENTS.md

## What this is

logi is a terminal UI for JSONL logs: Kibana as `htop`. It reads a file or
stdin, keeps reading as input grows, and shows the lines in a table backed by an
in-memory DuckDB. See [PLAN.md](PLAN.md) for the design and the deliverables
(E1, E2…).

## Layout

```
main.go                 wiring: config, store, reader, ingest, TUI
scripts/setup.sh        sets up a Linux dev machine (the cloud environment runs it)
config/                 flags, LOGI_ variables and the JSON config file
cmd/genlogs/            streams synthetic logs to stdout, or writes a sized batch
logstore/               the only package that writes SQL (DuckDB)
kql/                    parses filter text into a logstore.Filter
ingest/                 reads lines (file, stdin) and appends them to the store
  mock_/                generated mocks for ingest's interfaces
tui/                    the bubbletea root model
  table/                the table pane, and the page queries that fill it
    mock_/              generated mocks for table's interfaces
  filterbar/            the filter bar: one line of text, and its error
  detail/               the detail pane: one row's JSON
  termtext/             makes log text safe to print
```

Each feature package owns its slice. Interfaces are declared in the package that
uses them (`ingest.Appender`, `table.Rows`), and the concrete types in `logstore`
satisfy them. `main.go` connects the two.

## Tasks

Tooling: `brew install go go-task golangci-lint`. Building needs a C toolchain,
because DuckDB uses cgo. On a Linux remote machine with Nix, run
`scripts/setup.sh` instead of brew; it is idempotent. mockgen is a Go tool in
`go.mod`, so there is nothing else to install. Use `task`, not raw `go` or
`golangci-lint`, except for single tests.

- `task generate`: `go generate ./...`, which regenerates the mocks. Run it
  after changing an interface that has a mock, and commit the result.
- `task fmt`: golangci-lint fmt. Run after editing `.go` files.
- `task lint`: golangci-lint run.
- `task test`: `go test -race ./...`.
- `task build`: `logi` and `genlogs` into `bin/`.

Before calling a change done, run `task generate fmt lint test build`.

## Mindset

Work like a Pragmatic Programmer: take responsibility for the outcome, not just
the diff. If something is wrong, fix it or say so, and do not hide behind "it
was already like that". Do not live with broken windows: a bad name, a stale
comment or a failing test left in place invites more decay, so repair small
problems as you pass them. When you do not know something, read the code or run
it instead of guessing, and when a requirement is unclear, ask. Care about the
craft, and keep learning about the codebase as you change it.

Keep the design easy to change. Say each thing once (DRY): a fact, rule or piece
of knowledge should have a single home, such as SQL in `logstore` or an
interface in the package that uses it. Keep components orthogonal, so that a
change in one place does not ripple through unrelated ones. Prefer decisions you
can reverse, because requirements will move. Build the simplest thing that
works, ship a thin end-to-end slice first (a tracer bullet, like E1), then grow
it. Do not build for speculative needs (YAGNI).

Make failures loud and early. Crash early rather than carry on with bad state,
and return errors instead of swallowing them. Use assertions and tests to state
what must be true, and fix the root cause rather than the symptom. Debug by
reproducing the problem and isolating it, not by trying random changes. Know
your tools well enough that the next change is cheap: automate repeated steps,
and let `task` and the linters do the checking so that you can think about the
design.

Communicate plainly. Write code and docs for the next reader, comment on why
rather than what. Write comments that improve the whole file or package, not
ones that describe only your most recent change. No comment on good code is
better than a noisy comment added for every change. Leave the codebase a little
better than you found it, but do not gold-plate: good enough software that ships
and can be improved beats a perfect one that does not.

## Conventions

- Before writing, editing or reviewing any `.go` file, invoke the
  `loderunner:go-conventions` skill and follow it.
- After adding or upgrading a dependency, read its documentation and source at
  the version in `go.mod` before you use it, instead of working from memory:
  APIs change between versions. `go doc <pkg>` and the module cache
  (`go env GOMODCACHE`) hold the exact code you build against.
- Mocks are generated with mockgen into `mock_/` subpackages and committed. The
  package is named `<parent>_mock`.
- `fmt.Print*` is banned (`forbidigo`) because it corrupts the TUI. Write to
  `os.Stderr` with `fmt.Fprint*` when a command needs to print.
- SQL lives only in `logstore`.
- Formatting Markdown and YAML files with Prettier is recommended but not
  mandatory. Markdown should be formatted with `proseWrap: always`.

### bubbletea models

The models form a tree, like iOS view controllers. The root `tui.Model` is the
only `tea.Model`. Each pane is a child model in its own package.

- A child is shaped like a bubbles component: `Update(tea.Msg) (Model, tea.Cmd)`
  returns its concrete type and `View()` returns a string. It has an `Init` when
  it has a command to start. A child can be setters and `View` only. It gets
  `Update` once it can handle a message with its own state alone.
- A source (a store, a query with its generation number and cancel function)
  lives in the deepest model that uses it. When panes share data, their common
  ancestor holds it if it has reason to orchestrate it. Otherwise, when their
  access patterns differ, each pane holds its own.
- The parent owns layout, visibility and focus, and orchestrates in `Update` and
  `View`. It pushes each child's size with `SetSize`. It calls `Focus` and
  `Blur` only on a child that draws differently while focused.
- The parent routes every message explicitly: it switches on the type and calls
  the child that needs it. Keys go to the focused or shown child. A message the
  parent routes is exported.
- A child tells its parent about something the parent owns (closing, a new
  layout, a failure) by returning a `tea.Cmd` that yields an exported message of
  its own package, such as `detail.CloseMsg` or `table.ErrMsg`.
- Models use value receivers and shallow copies by default. Clone at the
  boundaries: when a slice or map leaves `Update` inside a `tea.Cmd` closure,
  and when two owners would otherwise share a mutable collection. Everywhere
  else, mutate your own copy freely.
- Cmds do their work in the returned closure and report errors as messages, as
  in the bubbletea commands tutorial.
