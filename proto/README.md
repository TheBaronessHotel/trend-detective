# proto

All contracts, one package per owning service:

- `trenddetective/gateway/v1`: TaskMeta, StageDone, TaskError and the public Connect API
- `trenddetective/ingest/v1`: IngestTask, RawDataset
- `trenddetective/preprocess/v1`: PreprocessTask, filter ops, Series, Grid, the slider-cube header
- `trenddetective/analysis/v1`: AnalysisTask, test specs, results
- `trenddetective/narrator/v1`: Narrate

After editing, run `make gen` and commit the generated code in `gen/` with your change. CI runs `buf lint` and `buf breaking` against `main`.
