# Trend Detective

Our entry for NASA Space Apps 2026, [Be An Earth System Trend Detective!](https://www.spaceappschallenge.org/2026/challenges/earth-system-trend-detective/).

Pick one of 34 things to measure, such as air temperature, sea ice, groundwater or NO2, draw a rectangle on the globe and pick the years. Trend Detective fetches the data from NASA, removes the seasonal cycle and tells you, in plain words, whether it's changing, how fast, and how likely that is to be chance. For map variables it also shows where inside the rectangle the change is, and lets you play the months back on the globe.

It's written for high-school students: every number comes with a sentence saying what it means, and the caveats sit next to the result.

## What it does

- **A variable browser.** Like Giovanni's: search, topics (air, air quality, water, land and plants, ocean, snow and ice) and measurements, and a filter for the ones with a map.
- **Choose what to make.** The region's chart always comes; the trend tests, the trend map and the month-by-month map are each optional, so a quick look at the map skips the tests.
- **One answer up top.** "Rising, and unlikely to be chance", "Falling…" or "No clear trend yet", with the rate per decade, the range it likely falls in, and how often chance alone would give a trend this strong.
- **Where it's changing.** For map variables, a heatmap on the globe colours each cell by its own trend. Cells that still pass after correcting for testing hundreds of cells at once are outlined.
- **Month by month.** A time slider plays the grid back, as measured values or as the difference from normal, so you can watch a trend build up.
- **Why we think so.** The shuffle test as a histogram, and a triangle of the trend over every stretch of years, to show whether the answer hangs on the years picked. Plus the caveats the tests raised: short records, a sudden shift, odd months.
- **The chart.** The region's monthly values and anomalies, with the fitted trend and its band, a crosshair, a table view, and a CSV download.
- **Examples.** One click runs a curated case: Europe warming, CO2 rising, cleaner air over China, groundwater falling in northwest India, farmland greening in Punjab, a mixed picture over India, and London rain with no clear trend.
- **In plain words.** An explanation of the results, written from the numbers without any AI model. Questions about them work with your own Anthropic or OpenAI-compatible key, which stays in the page.
- **Your own data.** Upload a CSV of dates and values and run the same tests on it.

## Where the data comes from

All from NASA, fetched live when a job runs and cached afterwards.

| Service | Variables | What we ask it for |
|---|---|---|
| [Harmony](https://harmony.earthdata.nasa.gov) | From MERRA-2, back to 1980: air and sea surface temperature, humidity, water vapour, cloud cover, sunlight, wind, sea-level pressure, rain, snowfall, snow cover, sea ice, ozone, carbon monoxide, and haze, dust, smoke and sulfate particles. From GLDAS: soil moisture at the top and in the root zone, soil temperature, evaporation, runoff, snow water and snow depth. Rain and snow from GPM IMERG | The rectangle cut out of each monthly file, for the maps |
| [Giovanni](https://giovanni.gsfc.nasa.gov) | NO2 (OMI), groundwater (GLDAS), CO2 (OCO-2), ocean chlorophyll (NOBM) | The rectangle's average over time |
| [POWER](https://power.larc.nasa.gov) | Air temperature, rain and snow | A point's monthly series |
| [AppEEARS](https://appeears.earthdatacloud.nasa.gov) | Plant greenness (MODIS NDVI), land surface temperature (MODIS) | The rectangle's statistics per image |

## How it decides

The tests run on anomalies, each month's difference from its own normal, so the seasons don't pass for a trend.

- **Least squares** on time, with the effective sample size correction for autocorrelation (Santer et al. 2000).
- **Mann-Kendall** with the Hamed and Rao correction, and Sen's slope. The verdict says when it and least squares disagree.
- **A block shuffle test**: the record cut into years, shuffled 1,000 times, refitted each time.
- **The Pettitt test** for a sudden shift, residual checks and outlier months, all reported as caveats.
- **The trend triangle**, the trend over every stretch of at least 10 years, and **years to significance** (Weatherhead et al. 1998).
- **On the map**, a test per cell, then the Benjamini-Hochberg false discovery rate across cells (Wilks 2016).

As a check, run on NOAA's Mauna Loa CO2 record, our trend comes within 0.02 ppm a year of the growth NOAA publishes for 2010 to 2019 and for 2000 to 2024. The methods and their sources are in [services/analysis/README.md](services/analysis/README.md), and the whole system in [ARCHITECTURE.md](ARCHITECTURE.md). Tasks and decisions live in the team's Notion.

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

AppEEARS logs in with an Earthdata account rather than the token, so the MODIS variables also need `EARTHDATA_USERNAME` and `EARTHDATA_PASSWORD` in `.env`.

`make up` starts NATS, Redis and SeaweedFS, runs bootstrap to create the JetStream streams and the S3 buckets, and starts ingest, preprocess, analysis, the narrator, the gateway and the web app. Open [http://localhost:3000](http://localhost:3000) and press one of the examples, or pick a variable, draw a rectangle and press Fetch the data. A first run of a long record can take several minutes while NASA prepares the files; the same request again comes from the cache. To try the API with curl, see [services/gateway/README.md](services/gateway/README.md). Other targets:

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
| `make smoke-appeears` | Runs a real NDVI job through NASA AppEEARS. Needs `EARTHDATA_USERNAME` and `EARTHDATA_PASSWORD` in `.env`, and 5 to 15 minutes |
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
