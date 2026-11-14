# Trend Detective

Our entry for NASA Space Apps 2026, [Be An Earth System Trend Detective!](https://www.spaceappschallenge.org/2026/challenges/earth-system-trend-detective/).

Pick a rectangle on the map and an Earth science variable, such as rainfall, groundwater or NO2. The app plots the region's monthly series with the seasonal cycle removed. It reports how fast the variable is changing and whether that trend is statistically significant. A heatmap shows where inside the rectangle the change is happening.

The design is in [ARCHITECTURE.md](ARCHITECTURE.md). Tasks and decisions live in the team's Notion.

## Layout

```
.
├── ARCHITECTURE.md
├── compose.yaml, .env.example               the local stack and its settings
├── Makefile                                up, down, logs, gen, lint and more
├── buf.yaml, buf.gen.yaml                  buf module and codegen settings
├── go.mod                                  one Go module for gen/go, gateway and ingest
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

You need Docker. Then:

```
make up
```

The first run copies `.env.example` to `.env`. Put your Earthdata token in it as `EARTHDATA_TOKEN`. You get one at [urs.earthdata.nasa.gov](https://urs.earthdata.nasa.gov) under "Generate Token".

So far `make up` starts the infrastructure: NATS, Redis and SeaweedFS. Bootstrap then creates the JetStream streams and the S3 buckets. The services join compose as they get built. Other targets:

| Target | What it does |
|---|---|
| `make down` | Stops everything. Data stays in the Docker volumes |
| `make logs` | Follows the logs |
| `make ps` | Lists the containers |
| `make clean` | Stops everything and deletes all data |

The ports listen on 127.0.0.1 only:
- NATS: 4222, and 8222 for monitoring.
- Redis: 6379.
- SeaweedFS: 8333 for S3, 9333 for the master UI, and 8888 for the filer UI.

## Working here

- Each service has its own folder and Dockerfile. Its owner decides what goes inside.
- A contract change goes in `proto/`. Run `make gen` and commit the generated code in the same PR.
- Never commit `.env`.
