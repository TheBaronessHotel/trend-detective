from trenddetective.common.v1 import common_pb2 as _common_pb2
from trenddetective.ingest.v1 import ingest_pb2 as _ingest_pb2
from trenddetective.preprocess.v1 import data_pb2 as _data_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class PreprocessTask(_message.Message):
    __slots__ = ("raw", "source_units", "filters", "aggregation")
    RAW_FIELD_NUMBER: _ClassVar[int]
    SOURCE_UNITS_FIELD_NUMBER: _ClassVar[int]
    FILTERS_FIELD_NUMBER: _ClassVar[int]
    AGGREGATION_FIELD_NUMBER: _ClassVar[int]
    raw: _containers.RepeatedCompositeFieldContainer[_ingest_pb2.RawDataset]
    source_units: str
    filters: FilterChain
    aggregation: Aggregation
    def __init__(self, raw: _Optional[_Iterable[_Union[_ingest_pb2.RawDataset, _Mapping]]] = ..., source_units: _Optional[str] = ..., filters: _Optional[_Union[FilterChain, _Mapping]] = ..., aggregation: _Optional[_Union[Aggregation, _Mapping]] = ...) -> None: ...

class FilterChain(_message.Message):
    __slots__ = ("ops",)
    OPS_FIELD_NUMBER: _ClassVar[int]
    ops: _containers.RepeatedCompositeFieldContainer[FilterOp]
    def __init__(self, ops: _Optional[_Iterable[_Union[FilterOp, _Mapping]]] = ...) -> None: ...

class FilterOp(_message.Message):
    __slots__ = ("qc_range", "linear", "monthly_mean", "anomaly", "log10", "coarsen", "gap_fill_display")
    QC_RANGE_FIELD_NUMBER: _ClassVar[int]
    LINEAR_FIELD_NUMBER: _ClassVar[int]
    MONTHLY_MEAN_FIELD_NUMBER: _ClassVar[int]
    ANOMALY_FIELD_NUMBER: _ClassVar[int]
    LOG10_FIELD_NUMBER: _ClassVar[int]
    COARSEN_FIELD_NUMBER: _ClassVar[int]
    GAP_FILL_DISPLAY_FIELD_NUMBER: _ClassVar[int]
    qc_range: QcRange
    linear: Linear
    monthly_mean: MonthlyMean
    anomaly: Anomaly
    log10: Log10
    coarsen: Coarsen
    gap_fill_display: GapFillDisplay
    def __init__(self, qc_range: _Optional[_Union[QcRange, _Mapping]] = ..., linear: _Optional[_Union[Linear, _Mapping]] = ..., monthly_mean: _Optional[_Union[MonthlyMean, _Mapping]] = ..., anomaly: _Optional[_Union[Anomaly, _Mapping]] = ..., log10: _Optional[_Union[Log10, _Mapping]] = ..., coarsen: _Optional[_Union[Coarsen, _Mapping]] = ..., gap_fill_display: _Optional[_Union[GapFillDisplay, _Mapping]] = ...) -> None: ...

class QcRange(_message.Message):
    __slots__ = ("min", "max")
    MIN_FIELD_NUMBER: _ClassVar[int]
    MAX_FIELD_NUMBER: _ClassVar[int]
    min: float
    max: float
    def __init__(self, min: _Optional[float] = ..., max: _Optional[float] = ...) -> None: ...

class Linear(_message.Message):
    __slots__ = ("scale", "offset", "units")
    SCALE_FIELD_NUMBER: _ClassVar[int]
    OFFSET_FIELD_NUMBER: _ClassVar[int]
    UNITS_FIELD_NUMBER: _ClassVar[int]
    scale: float
    offset: float
    units: str
    def __init__(self, scale: _Optional[float] = ..., offset: _Optional[float] = ..., units: _Optional[str] = ...) -> None: ...

class MonthlyMean(_message.Message):
    __slots__ = ("min_valid_days",)
    MIN_VALID_DAYS_FIELD_NUMBER: _ClassVar[int]
    min_valid_days: int
    def __init__(self, min_valid_days: _Optional[int] = ...) -> None: ...

class Anomaly(_message.Message):
    __slots__ = ("kind", "baseline", "min_baseline_coverage", "method", "harmonics")
    KIND_FIELD_NUMBER: _ClassVar[int]
    BASELINE_FIELD_NUMBER: _ClassVar[int]
    MIN_BASELINE_COVERAGE_FIELD_NUMBER: _ClassVar[int]
    METHOD_FIELD_NUMBER: _ClassVar[int]
    HARMONICS_FIELD_NUMBER: _ClassVar[int]
    kind: _data_pb2.AnomalyKind
    baseline: _common_pb2.YearRange
    min_baseline_coverage: float
    method: _data_pb2.ClimatologyMethod
    harmonics: int
    def __init__(self, kind: _Optional[_Union[_data_pb2.AnomalyKind, str]] = ..., baseline: _Optional[_Union[_common_pb2.YearRange, _Mapping]] = ..., min_baseline_coverage: _Optional[float] = ..., method: _Optional[_Union[_data_pb2.ClimatologyMethod, str]] = ..., harmonics: _Optional[int] = ...) -> None: ...

class Log10(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class Coarsen(_message.Message):
    __slots__ = ("factor",)
    FACTOR_FIELD_NUMBER: _ClassVar[int]
    factor: int
    def __init__(self, factor: _Optional[int] = ...) -> None: ...

class GapFillDisplay(_message.Message):
    __slots__ = ("max_gap",)
    MAX_GAP_FIELD_NUMBER: _ClassVar[int]
    max_gap: int
    def __init__(self, max_gap: _Optional[int] = ...) -> None: ...

class Aggregation(_message.Message):
    __slots__ = ("subregion_n", "max_grid", "min_valid_fraction")
    SUBREGION_N_FIELD_NUMBER: _ClassVar[int]
    MAX_GRID_FIELD_NUMBER: _ClassVar[int]
    MIN_VALID_FRACTION_FIELD_NUMBER: _ClassVar[int]
    subregion_n: int
    max_grid: int
    min_valid_fraction: float
    def __init__(self, subregion_n: _Optional[int] = ..., max_grid: _Optional[int] = ..., min_valid_fraction: _Optional[float] = ...) -> None: ...

class PreprocessResult(_message.Message):
    __slots__ = ("series", "series_csv", "grid", "value_cube", "anomaly_cube")
    SERIES_FIELD_NUMBER: _ClassVar[int]
    SERIES_CSV_FIELD_NUMBER: _ClassVar[int]
    GRID_FIELD_NUMBER: _ClassVar[int]
    VALUE_CUBE_FIELD_NUMBER: _ClassVar[int]
    ANOMALY_CUBE_FIELD_NUMBER: _ClassVar[int]
    series: _common_pb2.BlobRef
    series_csv: _common_pb2.BlobRef
    grid: _common_pb2.BlobRef
    value_cube: _common_pb2.BlobRef
    anomaly_cube: _common_pb2.BlobRef
    def __init__(self, series: _Optional[_Union[_common_pb2.BlobRef, _Mapping]] = ..., series_csv: _Optional[_Union[_common_pb2.BlobRef, _Mapping]] = ..., grid: _Optional[_Union[_common_pb2.BlobRef, _Mapping]] = ..., value_cube: _Optional[_Union[_common_pb2.BlobRef, _Mapping]] = ..., anomaly_cube: _Optional[_Union[_common_pb2.BlobRef, _Mapping]] = ...) -> None: ...
