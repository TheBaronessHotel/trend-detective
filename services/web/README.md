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
- A job streams its progress through `WatchJob`. A running stage shows its percent and what it's doing when the worker reports them (ingest does, for Harmony), and how long it has run. The gateway only knows a task is running once its worker says so, so any task handed out counts as running. Errors show the gateway's or the task's own message, such as `Row 3: "abc" isn't a number.` for a bad CSV.
- Jobs stop after preprocess for now (`STOP_AFTER` in `App.tsx`), because no analysis service runs in the stack yet, and a job waiting for one would never finish.
- When a job finishes, the app asks `GetResultURL` for the series, downloads preprocess's `series.json` and reads it with the generated schema. `SeriesChart.tsx` draws it as SVG with `d3-scale` and `d3-shape`, in the page's ink so it follows the theme: the values as a line, short gaps filled in dashed (only for drawing), missing steps ticked along the bottom, and, when the series has them, the normal range as a band and anomalies as bars underneath. A crosshair reads any step by pointer or arrow keys, the table under it lists every value, and "Save as CSV" downloads preprocess's CSV. `src/lib/series.ts` holds the logic and is unit-tested. The trend line and its confidence band come with the analysis service.
- Uploads and downloads go straight to SeaweedFS through signed URLs. SeaweedFS answers CORS for any origin, and the gateway allows the origins in `CORS_ORIGINS`.
