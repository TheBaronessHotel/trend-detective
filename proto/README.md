# proto

All contracts. Each package belongs to the service that produces its messages.

| Package | Owner | What's in it |
|---|---|---|
| `trenddetective/common/v1` | gateway | `BBox`, `LatLon`, `Date`, `DateRange`, `YearRange`, `TimeStep`, `BlobRef`. It imports nothing |
| `trenddetective/gateway/v1` | gateway | `task.proto` has the `Task` envelope, `TaskMeta`, `StageDone` and `TaskError`. `gateway.proto` has the public Connect API |
| `trenddetective/ingest/v1` | ingest | `IngestTask`, with one settings message per adapter, and `RawDataset` |
| `trenddetective/preprocess/v1` | preprocess | `PreprocessTask`, the filter chain and `PreprocessResult`. `data.proto` has `Series`, `Grid` and `CubeHeader` |
| `trenddetective/analysis/v1` | analysis | `AnalysisTask`, `TestPlan`, the test specs and `AnalysisResult`. `results.proto` has `SeriesAnalysis` and `GridAnalysis` |
| `trenddetective/narrator/v1` | narrator | `NarratorService.Narrate` |

## How a job's messages flow

1. The gateway publishes a `gateway.v1.Task` on `tasks.ingest`, `tasks.preprocess` or `tasks.analysis`. Its `spec` holds the stage's own task message.
2. The stage writes its outputs to SeaweedFS. Then it publishes a `gateway.v1.StageDone` on `events.stage.done`, with its result (`RawDataset`, `PreprocessResult` or `AnalysisResult`) or a `TaskError`.
3. The gateway copies that result into the next stage's task.

Result messages hold only `BlobRef`s. The documents they point to (`Series`, `Grid`, `CubeHeader`, `SeriesAnalysis`, `GridAnalysis`) are protojson files. Grid values and cube data are raw little-endian binary files, and each header explains its layout.

Imports go one way only: common, ingest, preprocess, analysis, narrator, gateway. Each package imports only packages earlier in that list, so the Go packages can't form an import cycle.

Secrets never go in messages, because JetStream stores every message. A user's Earthdata token travels in the `x-earthdata-token` header, and the gateway passes Redis keys to ingest. An LLM key travels in the `x-llm-api-key` header.

## Changing a contract

1. Edit the `.proto` file.
2. Run `make lint`, then `make gen`.
3. Commit the proto and `gen/` together.

CI fails if `gen/` is out of date. It also runs `buf breaking` against `main`, which rejects renaming, renumbering or deleting a field. To retire a field, mark its number and name `reserved`.

## Using the generated code

- **Go.** The repo root is one Go module. Import, for example, `github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1`, which is package `ingestv1`. The Connect code uses `connectrpc.com/connect/v2`.
- **Python.** Put `gen/python` on `PYTHONPATH` and install `protobuf>=7.36.2,<8`. Use `google.protobuf.json_format` to read and write the protojson documents.
- **TypeScript.** Import from `gen/ts` with `@bufbuild/protobuf` 2.17 and connect-es v2.

In protojson, field names come out in lowerCamelCase, as in `variableId`, and parsers accept snake_case too. NaN is written as the string `"NaN"`.

Codegen uses pinned remote plugins from the Buf Schema Registry, so you only need `buf` itself (`brew install bufbuild/buf/buf`).
