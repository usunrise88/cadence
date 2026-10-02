# Captures

`capture.mjs` opens the screen a figure spec in `figures/` describes, on a running Cadence, and writes
`<id>.png` and `<id>.json` (each region's box in CSS pixels of the 1440×900 viewport; the PNG is at 2×).
Method: `docs/tutorial/GUIDELINES.md` §5.3 and §5.5.

The key comes from `.doc-capture-key` at the repository root, mounted into the container; every API request that
is not a GET is refused by the script. `FIGURE_VARS` fills `{{name}}` placeholders with the reference project's ids.

```sh
docker run --rm --network host --user 1000:1000 -e HOME=/tmp \
  -e FIGURE_VARS='{"gateEval":"evl_…"}' \
  -v "$PWD/docs/tutorial/capture":/cap:ro -v "$PWD/.doc-capture-key":/key:ro -v "$PWD/docs/tutorial/screens":/out \
  -w /tmp mcr.microsoft.com/playwright:v1.63.0-noble \
  sh -c 'npm init -y >/dev/null && npm i -s playwright@1.63.0 >/dev/null && cp /cap/capture.mjs . && node capture.mjs /cap/figures/gate-verdict.json /out'
```

Targets are CSS selectors today; the ones without `data-command` / `data-panel` move to `data-tour` attributes as
chapters are written (GUIDELINES §5.2).

`../build/annotated-prototype.html` is the first rendering of an annotated figure (Chapter 9, Figure 9-1),
published for review; the HTML edition's builder will generate this markup from a chapter's `annotated` blocks.
