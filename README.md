# urphin

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

`extension/` is a git submodule for commands beyond the public set — it extends the same
interfaces instead of modifying them. The extension shipped with this project lives in a
separate private repository, so a public checkout has an empty `extension/` directory and
builds the public binary only.

Read these instead of a spec here, since the code is the truth:

| File | What it defines |
| --- | --- |
| `tui/commands/registry/registry.go` | the `Command` interface and the registry commands are looked up in |
| `tui/commands/reg.go` | where public commands get registered |
| `tui/commands/reg_extension.go`, `tui/commands/reg_no_extension.go` | the `extension` build tag switch: what an extension must provide, and the no-op used without it |
| `tui/commands/antlr/*.g4` | the command grammar the parser is generated from |

To add your own commands, put a package at `extension/tui/commands` exposing the same
entry point, add its grammar under `extension/tui/commands/antlr/`, and build with the
`extension` tag (`make extension`). Without that tag the public tree compiles as if
`extension/` did not exist.
