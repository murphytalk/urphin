# urphin

[![Go build](https://github.com/murphytalk/urphin/actions/workflows/go.yml/badge.svg)](https://github.com/murphytalk/urphin/actions/workflows/go.yml)

A handcrafted personal finance dashboard with a terminal UI.

## Build

### Prerequisites

| Tool | Version | Why |
| --- | --- | --- |
| Go | see `go.mod` | builds the binary |
| ANTLR | 4.13.x | generates the command parser |
| JDK | 11+ | runs ANTLR |

Generated parser code is **not** committed (`**/generated/` is gitignored), so a fresh
checkout cannot compile it until ANTLR has run. Every `make` build target regenerates
first; a bare `go build` fails on the missing `tui/commands/antlr/generated` package.
Run `make antlr-public` once, or just use the `make` targets.

The Go side needs `github.com/antlr4-go/antlr/v4`, which `go mod` fetches on its own.
Keep the tool and the runtime on the same ANTLR minor version (tool 4.13.x, runtime
v4.13.1 in `go.mod`).

### Installing ANTLR

Install it per the official guide: <https://github.com/antlr/antlr4/blob/master/doc/getting-started.md>
You need an `antlr4` command (4.13.x) on `PATH` and a JDK.

To use a different launcher, set `ANTLR` to a single executable — the script quotes it,
so wrap a multi-word command in a script:

```sh
ANTLR=/opt/antlr/bin/antlr4 make public
```

### Targets

```sh
make              # check + build (default)
make public       # regenerate the parser, build build/urphin
make check        # gofmt check, go test, go vet
make antlr-public # regenerate the parser only
```

`go vet` and `gofmt` skip `*/generated/*`: ANTLR output trips `unreachable code`
warnings and is not meant to be edited or reformatted.

## Extension

See [theme configuration and import](./docs/themes.md) for color roles, theme
selection, ANSI support and Base16 importing.

The optional `extension/` submodule adds commands and data backends. It lives in a
separate private repository, so public checkouts build the public version. With access to
the submodule, build the extension version using `make extension`.

Commands implement the shared `Command` interface and are added through `Register`; the
extension build calls this from its build-tagged registration hook. Extended data
backends use a similar build-tagged hook to delegate connections to the extension package.
See [the command registry](./tui/commands/registry/registry.go),
[command registration](./tui/commands/reg_extension.go), and
[data backend hook](./data/data_extension.go).

## Credits

- The Yahoo Finance and CoinBase market data implementation under `internal/` are basically copied from [ticker](https://github.com/achannarasappa/ticker) - as well as the live market data display.
