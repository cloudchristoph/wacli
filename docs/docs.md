# docs

Read when: opening the hosted documentation site from the CLI.

`wacli docs` prints the canonical hosted documentation URL: <https://wacli.sh>.
Use it from scripts or terminal sessions when you need a stable pointer to the
GitHub Pages documentation.

## Command

```bash
wacli docs
```

## JSON

```bash
wacli --json docs
```

## Examples

```bash
wacli docs
open "$(wacli docs)"
```
