# Bundled datasets

Datasets shipped with the app, for offline demos and the demo video. A task picks one with `IngestTask.bundle.name`, which is the folder name.

Each folder holds a `manifest.json` and the files it lists. The manifest is a `trenddetective.ingest.v1.RawDataset` in protojson:
- `files[].blob.key` is a file name relative to the folder. The other `blob` fields are ignored.
- `source` is where the files were originally downloaded from, so the app shows real provenance.
- `format` tells preprocess which decoder to use.

Ingest hashes the manifest and every listed file into the cache key, so editing a bundle invalidates its cached copy. `make test` checks that every bundle here loads.

## power-delhi-monthly

NASA POWER monthly means for one point in Delhi (28.61° N, 77.21° E), January 1981 to December 2024. It has two parameters: `T2M`, the air temperature at 2 m in °C, and `PRECTOTCORR`, the corrected precipitation in mm/day. It was downloaded on 2026-11-14 from:

```
https://power.larc.nasa.gov/api/temporal/monthly/point?parameters=T2M,PRECTOTCORR&community=AG&longitude=77.21&latitude=28.61&start=1981&end=2024&format=JSON
```

POWER adds a 13th "month" to every year, holding that year's mean. For example, `202413` is the mean for 2024. Preprocess has to skip it.
