# Trend Detective

Our entry for NASA Space Apps 2026, [Be An Earth System Trend Detective!](https://www.spaceappschallenge.org/2026/challenges/earth-system-trend-detective/).

Pick a rectangle on the map and an Earth science variable, such as rainfall, groundwater or NO2. The app plots the region's monthly series with the seasonal cycle removed. It reports how fast the variable is changing and whether that trend is statistically significant. A heatmap shows where inside the rectangle the change is happening.

The design is in [ARCHITECTURE.md](ARCHITECTURE.md). Tasks and decisions live in the team's Notion.

## Layout

```
.
├── ARCHITECTURE.md
├── compose.yaml, .env.example, Makefile    (coming in PLAT-2 to PLAT-4)
├── buf.yaml, buf.gen.yaml                  (coming in CON-1)
├── proto/trenddetective/<service>/v1/      contracts, one package per owning service
├── gen/                                    generated Go, Python and TS code, committed
├── deploy/                                 NATS, Redis and SeaweedFS configs, bootstrap container
└── services/
    ├── gateway/      Go. Orchestrates jobs, owns job state and the browser API
    ├── ingest/       Go. Fetches raw files from NASA sources
    ├── preprocess/   Decodes, cleans and aggregates data into series and grids
    ├── analysis/     Trend tests on series and grid cells
    ├── narrator/     Optional LLM explanations, bring your own key
    └── web/          Map, time slider, charts, heatmap
```

## Running it

Nothing runs yet. Once compose lands, it will be:

```
cp .env.example .env    # then set EARTHDATA_TOKEN
make up
```

You need Docker. Get an Earthdata token at [urs.earthdata.nasa.gov](https://urs.earthdata.nasa.gov) under "Generate Token".

## Working here

- Each service has its own folder and Dockerfile. Its owner decides what goes inside.
- A contract change goes in `proto/`. Run `make gen` and commit the generated code in the same PR.
- Never commit `.env`.
