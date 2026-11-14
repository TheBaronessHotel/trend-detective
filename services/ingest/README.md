# ingest

Go. Fetches raw files from NASA sources into the `raw` bucket, with one adapter per source: `harmony-subset`, `power`, `giovanni-area`, `giovanni-point`, `csv-upload` and `bundle`. It moves bytes only and never parses NetCDF.

See the Data sources section of [ARCHITECTURE.md](../../ARCHITECTURE.md).
