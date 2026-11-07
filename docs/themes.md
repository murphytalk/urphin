# Themes

Urphin uses Lip Gloss styles and leaves the terminal background in control. The
default theme is the embedded Catppuccin Mocha palette. Select another theme with
`app.theme` or `-theme`; use `-list-themes` to see bundled and local names.

Themes from [tinted-theming](https://github.com/tinted-theming/schemes) can be imported.

## Native YAML format

Each theme is a root key in YAML. Put one theme in `<name>.yaml` under the `themes/` directory beside the config file. The embedded `default.yaml` contains all bundled themes:

```yaml
my-theme:
  fg: '#cdd6f4' # Main text
  muted: '#bac2de' # Secondary text
  faint: '#a6adc8' # Dim labels
  accent: '#89b4fa' # Heading, prompt, and cursor
  gain: '#23f311' # Up values and price flashes
  loss: '#ec260c' # Down values and price flashes
  warning: '#bd558e' # Warnings
  error: '#eb0808' # Errors and invalid input
  border: '#45475a' # Separators and borders
  selection_bg: '#313244' # Selected content background
  popup_bg: '#181825' # Popup background
  series_1: '#fab387' # Chart series 1
  series_2: '#f9e2af' # Chart series 2
  series_3: '#74c7ec' # Chart series 3
  series_4: '#cba6f7' # Chart series 4
  series_5: '#89b4fa' # Chart series 5
  series_6: '#b4befe' # Chart series 6
  gain_dim: '#91c99a' # Optional gain gradient endpoint
  loss_dim: '#d89aaf' # Optional loss gradient endpoint
```

Hex colors accept `#rgb` and `#rrggbb`; quote them as strings. ANSI themes may use
integer indices from 0 through 15 instead. The `ansi` theme uses the terminal's
own palette. Indexed gain/loss roles do not use an RGB gradient because Urphin
cannot know how a terminal owner customized those palette entries.

Roles and Base16 mapping:

| Role | Use | Base16 source |
|---|---|---|
| `fg` | Main text | `base05` |
| `muted` | Secondary text | `base04` |
| `faint` | Dim labels | `base03` |
| `accent` | Heading, prompt and cursor | `base0D` |
| `gain` | Up values and price flashes | `base0B` |
| `loss` | Down values and price flashes | `base08` |
| `warning` | Warnings | `base0A` |
| `error` | Invalid input | `base08` |
| `border` | Separators and borders | `base03` |
| `selection_bg` | Selected content background | `base02` |
| `popup_bg` | Popup background | `base01` |
| `gain_dim` | Optional gain gradient endpoint | Derived from `gain` blended toward `muted` |
| `loss_dim` | Optional loss gradient endpoint | Derived from `loss` blended toward `muted` |
| `series_1` | Chart series 1 | `base0D` |
| `series_2` | Chart series 2 | `base0E` |
| `series_3` | Chart series 3 | `base0C` |
| `series_4` | Chart series 4 | `base09` |
| `series_5` | Chart series 5 | `base0A` |
| `series_6` | Chart series 6 | `base0F` |

Missing required roles use the embedded Mocha value and are logged. Invalid colors
name the source and role; a requested theme that cannot load falls back to Mocha,
so the TUI still starts. The loader rejects unknown keys to catch misspellings.

Gain/loss colors follow the amount direction. `app.price-colors: red-up` swaps the
gain and loss roles, including dim endpoints and flashes; the default is
`green-up`. Small nonzero percentage changes start at the role color and the
existing HSV gradient reaches the dim endpoint at 10%. Zero uses muted and has no
arrow. If dim roles are omitted, Urphin blends the role halfway toward `muted` in
HSV. ANSI colors do not interpolate.

## Lookup order

`-theme` overrides `app.theme`. A name resolves to
`<directory containing -config>/themes/<name>.yaml` first, then an embedded theme.
A local file may shadow a bundled name. A value containing a path separator, or
ending in `.yaml`/`.yml`, loads as a file path directly. Relative explicit paths
are relative to the current working directory. With no selector, Urphin uses
`catppuccin-mocha` from embedded `default.yaml`.

All role colors come from the selected theme. `app.price-colors` only controls gain/loss direction.

`NO_COLOR` disables color whenever it is nonempty. Direction arrows, bold and
underlining remain. The root view, heading, footer, watchlist, summary and viewport
have no background. Backgrounds are used by `Selected` and `Popup`; reverse video
on the single virtual cursor cell is retained.

## Flags

```sh
urphin -theme ansi
urphin -theme ./my-theme.yaml
urphin -list-themes
urphin -config ~/.urphin/urphin.yaml -import-theme ./catppuccin-mocha.yaml
```

`-list-themes` prints sorted embedded and local names without starting feeds or a
backend. `-import-theme` converts Base16 YAML into Urphin's native YAML and writes
it to the config's `themes/` directory. It refuses to overwrite an existing file;
the source filename (without extension) becomes the selectable name. It does not
change the config or automatically select the imported theme.

The importer writes the selected theme as a single root entry. It requires six-digit Base16 values for every Base16 source key listed in the
table. `base00`, `base06`, and `base07` are ignored; in particular, ignoring
`base00` preserves terminal transparency. Base16 maps `base08` to both `loss` and
`error`, so imported themes may show those roles in the same color. Bundled Mocha
keeps them distinct.

Catppuccin values come from the official palette. Urphin uses gh-dash text and
status roles and bottom's series palette as references, adapted to keep error
distinct from loss and chart series away from red and green hues.
