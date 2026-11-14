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
├── package.json, bun.lock, bunfig.toml     Bun workspace for the web app
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
    └── web/          TypeScript, React and Vite, built with Bun. Map, charts, the job runner
```

## Running it

You need Docker. Then:

```
make up
```

The first run copies `.env.example` to `.env`. Put your Earthdata token in it as `EARTHDATA_TOKEN`. You get one at [urs.earthdata.nasa.gov](https://urs.earthdata.nasa.gov) under "Generate Token". The same account also has to approve the [NASA GESDISC DATA ARCHIVE](https://urs.earthdata.nasa.gov/approve_app?client_id=e2WVk8Pw6weeLUKZYOxvTQ) app once, or GPM and MERRA-2 requests fail with 403.

So far `make up` starts NATS, Redis and SeaweedFS, runs bootstrap to create the JetStream streams and the S3 buckets, and starts ingest, the gateway and the web app. Open [http://localhost:3000](http://localhost:3000), pick a variable, draw a rectangle and press Fetch the data. The other services join compose as they get built. To try the API with curl, see [services/gateway/README.md](services/gateway/README.md). Other targets:

| Target | What it does |
|---|---|
| `make down` | Stops everything. Data stays in the Docker volumes |
| `make logs` | Follows the logs |
| `make ps` | Lists the containers |
| `make clean` | Stops everything and deletes all data |
| `make test` | Runs the Go unit tests |
| `make test-preprocess`, `make test-analysis` | Runs a Python service's lockfile check, lint and unit tests. Needs uv |
| `make smoke` | With the stack up, runs the Delhi bundle through each stage and checks the results |
| `make smoke-harmony` | Runs a real GPM job through NASA Harmony. Needs `EARTHDATA_TOKEN` in `.env` |
| `make smoke-power` | Runs a real job through NASA POWER. Needs no token |
| `make smoke-giovanni` | Runs a real point job through NASA Giovanni. Needs `EARTHDATA_TOKEN` in `.env` |
| `make web-dev` | Runs the web app with hot reload on http://localhost:5173, against the running stack. Needs [Bun](https://bun.sh) |
| `make web-check` | The web app's type check, unit tests and production build |

The ports listen on 127.0.0.1 only:
- NATS: 4222, and 8222 for monitoring.
- Redis: 6379.
- SeaweedFS: 8333 for S3, 9333 for the master UI, and 8888 for the filer UI.
- Gateway: 8080, the Connect API.
- Narrator: 8081, the Connect stream the gateway proxies.
- Web app: 3000, and 5173 for `make web-dev`.

## Working here

- Each service has its own folder and Dockerfile. Its owner decides what goes inside.
- A contract change goes in `proto/`. Run `make gen` and commit the generated code in the same PR.
- Never commit `.env`.
