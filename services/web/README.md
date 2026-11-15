# web

The browser app: pick a variable, draw a rectangle, pick dates and a source, and run a job. It's TypeScript with React and Vite, built and tested with Bun. It follows the look of [lein3000.dev](https://lein3000.dev): monochrome, monospace, thin lines, pill buttons, a dot-grid ground, and a theme button for system, light and dark. It talks to the gateway through the Connect client generated into `gen/ts`.

## Running it

`make up` builds it and serves it with nginx on [http://localhost:3000](http://localhost:3000). For hot reload while working on it, keep the stack up and run:

```
make web-dev
```

That serves it on [http://localhost:5173](http://localhost:5173). Both talk to the gateway on `http://localhost:8080`. Set `VITE_GATEWAY_URL` to point somewhere else, or `WEB_GATEWAY_URL` for the compose build.

`make web-check` runs the type check, the unit tests and the production build, the same as CI.

## Layout

| Path | What it is |
|---|---|
| `src/api.ts` | The gateway client, error messages for people, and the CSV upload through a signed URL |
| `src/App.tsx` | The page: the four steps, and running a job through `CreateJob` and `WatchJob` |
| `src/components/` | The variable picker, the map, the rectangle fields, the dates, the source picker and the job panel |
| `src/lib/` | Logic with no React in it, unit-tested: dates, rectangles, enum labels, and reading a job's progress |
| `src/styles.css` | The lein3000.dev look: monochrome tokens for light and dark, type, lines, pills |
| `test/` | `bun test` unit tests |

## How it fits together

- The repo root's `package.json` makes this app a Bun workspace, and `bunfig.toml` hoists packages into the root `node_modules`. That's what lets the generated files in `gen/ts` find `@bufbuild/protobuf`: they look upward from `gen/`, not inside this folder.
- `@gen/...` imports point at `gen/ts/trenddetective/...`, set in both `tsconfig.json` and `vite.config.ts`. After `make gen`, the app picks up the new code with no copy step.
- The map is an ordinary OpenStreetMap map on a globe: MapLibre GL in its globe projection, with [OpenFreeMap](https://openfreemap.org)'s gray "positron" style in the light theme and its "dark" style in the dark one, swapped when the theme changes. Nothing needs a key. `MapView.tsx` adjusts the styles a little: labels show one name, in English where OpenStreetMap has it, rather than two scripts stacked; and the dark style gets readable sea names, ocean names, and region names only from zoom 5, like the light one. A CSS drop shadow on the map's canvas, which is see-through around the sphere, draws a thin ring and a soft shadow so the globe stands off the dot grid. If the style can't load, the globe is a plain sphere. Drag to spin it, scroll to zoom, or press "Draw a rectangle" and drag to draw one. The fields beside it show the rectangle and accept exact edges.
- MapLibre names its worker file at run time, which Vite can't see, so `MapView.tsx` imports the worker with `?worker&url` and passes it to `setWorkerUrl`. The map is loaded lazily, because MapLibre is most of the bundle.
- Colour only appears where it means something: green and red for a job's state, always beside a glyph.
- A running job has a "Stop" button, which calls `CancelJob`. The source step shows the Giovanni quota and the busy Harmony slots from `GetQuota`, for sources that use them, and refreshes them every 30 seconds.
- A job streams its progress through `WatchJob`. If the stream drops before the job finishes, the app reads the job and watches again, waiting longer each time, and gives up only after 8 failures in a row. A running stage shows its percent and what it's doing when the worker reports them (ingest does, for Harmony), and how long it has run. The gateway only knows a task is running once its worker says so, so any task handed out counts as running. Errors show the gateway's or the task's own message, such as `Row 3: "abc" isn't a number.` for a bad CSV.
- Jobs run every stage (`STOP_AFTER` in `App.tsx`). The chart shows as soon as preprocess's series exists, even if the trend tests then fail.
- The trend tests come from `GetResultURL(ARTIFACT_SERIES_ANALYSIS)`. `VerdictPanel.tsx` words them for people: rising or falling, the rate and its likely range, how often chance would give a trend this strong, whether Mann-Kendall agrees, the shuffle test, how many years it would take to tell, and the biggest shift. `src/lib/verdict.ts` holds the wording and is unit-tested. The chart draws the fitted line and its band on the series analysis tested, anomalies by default. The band comes from the fit's standard errors around the mean time (`src/lib/trend.ts`).
- When a job finishes, the app asks `GetResultURL` for the series, downloads preprocess's `series.json` and reads it with the generated schema. `SeriesChart.tsx` draws it as SVG with `d3-scale` and `d3-shape`, in the page's ink so it follows the theme: the values as a line, short gaps filled in dashed (only for drawing), missing steps ticked along the bottom, and, when the series has them, the normal range as a band and anomalies as bars underneath. A crosshair reads any step by pointer or arrow keys, the table under it lists every value, and "Save as CSV" downloads preprocess's CSV. `src/lib/series.ts` holds the logic and is unit-tested.
- For gridded jobs, the app also loads preprocess's `Grid` (`ARTIFACT_GRID`) and analysis's `GridAnalysis`. `lib/grid.ts` turns them into one polygon per cell, from the Grid's exact edges, cut to the job's rectangle: pixels centred on the rectangle's edges reach half a pixel past it, so edge cells are drawn cut while their numbers still come from the whole pixels, and the key says so. `MapView.tsx` draws them on the globe in a blue–grey–red diverging scale, symmetric around zero out to the 95th percentile of the slopes, with cells that pass the FDR-corrected test outlined in ink. Hovering a cell gives its numbers, and `HeatmapLegend.tsx` explains the colours.
- "Month by month" over the globe (`MonthsBar.tsx`) is the time slider. It downloads the grid's values and anomalies from the `data_url` and `anomalies_url` that `GetResultURL(ARTIFACT_GRID)` returns, once, and draws any month locally: `lib/months.ts` reads the float32 blobs and builds one polygon per cell, and `MapView.tsx` colours them through MapLibre feature state, so a new month only changes numbers, not shapes. "Difference from normal" uses the heatmap's diverging scale, symmetric out to the 98th percentile of the anomalies' sizes; "Measured" uses a one-hue green scale (light to dark, the other way round on the dark map) from the 2nd to the 98th percentile. The scales stay fixed across months, so months compare. Play steps a month at a time and stops at the end. While it's open, the heatmap is hidden.
- With results, the job panel shows the verdict and the heatmap's key side by side, then the chart, with how the job ran folded away under them. "Bigger map" folds the panel to one line, so the globe gets the room.
- "Why we think so" under the verdict draws two charts (`EvidenceCharts.tsx`, logic in `lib/evidence.ts`): the shuffle test as a histogram of the shuffled records' trends with this record's trend as a line, and the trend triangle, the trend over every stretch of years at least `min_years` long, with dots where a stretch passes on its own. Pointing at a bar or a square reads it out under the chart.
- "Try an example" (`ExamplesBar.tsx`, cases in `lib/examples.ts`) fills in the form and runs a curated job. A unit test checks each case against the catalog's dates.
- "Map cell size" in the form sets `aggregation.block_pixels` for map sources. It previews the grid from the catalog's `pixel_lat_deg`, `pixel_lon_deg` and `default_block_pixels`, with the same rule preprocess uses (`blockSize`, tested to match).
- "In plain words" (`NarratePanel.tsx`) streams `Narrate` through the gateway. "Explain the results" works without a key: the narrator writes the overview from the results with a template, and the line under it says so. Questions need the user's own AI key (Anthropic, or anything that speaks OpenAI's Chat Completions, with an optional base URL). The key stays in the page's memory and goes to the gateway in `X-Llm-Api-Key` with each call. Numbers the narrator flags as not in the results are listed as a warning under the answer. Answers render as Markdown, since models write it (`Markdown.tsx`: `react-markdown` with `remark-gfm` for tables, loaded on its own the first time an answer shows). It builds React elements, so HTML in an answer shows as text and unsafe links are dropped; headings sit under the panel's title, and wide tables scroll. The user's own questions stay plain text.
- Uploads and downloads go straight to SeaweedFS through signed URLs. SeaweedFS answers CORS for any origin, and the gateway allows the origins in `CORS_ORIGINS`.
