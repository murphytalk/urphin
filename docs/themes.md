# Themes

Urphin uses Lip Gloss styles and leaves the terminal background in control. The
default theme is the embedded Catppuccin Mocha palette. Select another theme with
`app.theme` or `-theme`; use `-list-themes` to see bundled and local names.

## Native YAML format

Save a file as `<name>.yaml` in the `themes/` directory beside the config file.
It contains a name and one color for every required role:

```yaml
name: my-theme
fg: '#cdd6f4'
muted: '#bac2de'
faint: '#a6adc8'
accent: '#89b4fa'
gain: '#a6e3a1'
loss: '#f38ba8'
warning: '#f9e2af'
error: '#eba0ac'
border: '#45475a'
selection_bg: '#313244'
popup_bg: '#181825'
series_1: '#fab387'
series_2: '#f9e2af'
series_3: '#74c7ec'
series_4: '#cba6f7'
series_5: '#89b4fa'
series_6: '#b4befe'
gain_dim: '#91c99a' # optional
loss_dim: '#d89aaf' # optional
```

Hex colors accept `#rgb` and `#rrggbb`; quote them as strings. ANSI themes may use
integer indices from 0 through 15 instead. The `ansi` theme uses the terminal's
own palette. Indexed gain/loss roles do not use an RGB gradient because Urphin
cannot know how a terminal owner customized those palette entries.

Roles:

| Roles | Use |
|---|---|
| `fg`, `muted`, `faint` | Main text, secondary text, and dim labels |
| `accent` | Heading, prompt and cursor |
| `gain`, `loss` | Up/down values and price flashes |
| `gain_dim`, `loss_dim` | Optional gradient endpoints for larger changes |
| `warning`, `error` | Warnings and invalid input; independent of price direction |
| `border` | Separators and borders |
| `selection_bg`, `popup_bg` | Backgrounds reserved for selected content and popups |
| `series_1` … `series_6` | Chart series |

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
`catppuccin-mocha`.

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

The Base16 mapping is `base05→fg`, `base04→muted`, `base03→faint,border`,
`base0D→accent,series_1`, `base0B→gain`, `base08→loss,error`, `base0A→warning,series_5`,
`base02→selection_bg`, `base01→popup_bg`, then `base0E`, `base0C`, `base09`,
`base0A`, `base0F` map to `series_2` through `series_6`. `base00` is ignored to
preserve terminal transparency. This mapping deliberately gives Base16 loss and
error the same color; bundled Mocha keeps them distinct.

Catppuccin values come from the official palette. Urphin uses gh-dash text and
status roles and bottom's series palette as references, adapted to keep error
distinct from loss and chart series away from red and green hues.
