# Assets

## `logo.svg` / `logo.png`

The OpenCode mark used as the plugin icon in the CLIProxyAPI plugin store.

Geometry and colors follow the official OpenCode favicon
(`packages/ui/src/assets/favicon/favicon.svg` in
[sst/opencode](https://github.com/sst/opencode), as published on
<https://opencode.ai/brand>): a 512×512 canvas, a 256×320 plate at `(128, 96)`
with a 64-unit frame, and a 128×128 block inside the plate at `(192, 160)` whose
upper third keeps the background color.

| Element | Color |
| --- | --- |
| Canvas | `#131010` |
| Plate and frame | `#FFFFFF` |
| Inner block, lower two thirds | `#5A5858` |

`logo.png` is a 512×512 raster of `logo.svg`, and is what the plugin store
registry references:

```text
https://raw.githubusercontent.com/dillonzq/cpa-opencode-go/main/assets/logo.png
```

Regenerate the PNG after editing the SVG, for example:

```bash
rsvg-convert -w 512 -h 512 assets/logo.svg -o assets/logo.png
```

## Trademark

The OpenCode name and mark identify OpenCode (Anomaly) and are used here only to
refer to the upstream service this plugin connects to. They are not covered by
this repository's MIT license, and this project is not affiliated with or
endorsed by Anomaly. Replace `assets/logo.svg` and `assets/logo.png` if that
usage is not acceptable for a redistribution.
