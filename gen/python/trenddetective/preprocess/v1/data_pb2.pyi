import datetime

from google.protobuf import timestamp_pb2 as _timestamp_pb2
from trenddetective.common.v1 import common_pb2 as _common_pb2
from trenddetective.ingest.v1 import ingest_pb2 as _ingest_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class AnomalyKind(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    ANOMALY_KIND_UNSPECIFIED: _ClassVar[AnomalyKind]
    ANOMALY_KIND_ABSOLUTE: _ClassVar[AnomalyKind]
    ANOMALY_KIND_PERCENT: _ClassVar[AnomalyKind]
    ANOMALY_KIND_STANDARDIZED: _ClassVar[AnomalyKind]

class ClimatologyMethod(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    CLIMATOLOGY_METHOD_UNSPECIFIED: _ClassVar[ClimatologyMethod]
    CLIMATOLOGY_METHOD_MONTHLY_MEANS: _ClassVar[ClimatologyMethod]
    CLIMATOLOGY_METHOD_HARMONIC: _ClassVar[ClimatologyMethod]

class MissingReason(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    MISSING_REASON_UNSPECIFIED: _ClassVar[MissingReason]
    MISSING_REASON_FILL_VALUE: _ClassVar[MissingReason]
    MISSING_REASON_QC_FAILED: _ClassVar[MissingReason]
    MISSING_REASON_CLOUD: _ClassVar[MissingReason]
    MISSING_REASON_POLAR_NIGHT: _ClassVar[MissingReason]
    MISSING_REASON_INSTRUMENT_OUTAGE: _ClassVar[MissingReason]
    MISSING_REASON_BEFORE_MISSION: _ClassVar[MissingReason]
    MISSING_REASON_LOW_COVERAGE: _ClassVar[MissingReason]
    MISSING_REASON_TOO_FEW_DAYS: _ClassVar[MissingReason]
    MISSING_REASON_NOT_POSITIVE: _ClassVar[MissingReason]
    MISSING_REASON_NO_NORMAL: _ClassVar[MissingReason]

class CubeKind(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    CUBE_KIND_UNSPECIFIED: _ClassVar[CubeKind]
    CUBE_KIND_VALUES: _ClassVar[CubeKind]
    CUBE_KIND_ANOMALIES: _ClassVar[CubeKind]
ANOMALY_KIND_UNSPECIFIED: AnomalyKind
ANOMALY_KIND_ABSOLUTE: AnomalyKind
ANOMALY_KIND_PERCENT: AnomalyKind
ANOMALY_KIND_STANDARDIZED: AnomalyKind
CLIMATOLOGY_METHOD_UNSPECIFIED: ClimatologyMethod
CLIMATOLOGY_METHOD_MONTHLY_MEANS: ClimatologyMethod
CLIMATOLOGY_METHOD_HARMONIC: ClimatologyMethod
MISSING_REASON_UNSPECIFIED: MissingReason
MISSING_REASON_FILL_VALUE: MissingReason
MISSING_REASON_QC_FAILED: MissingReason
MISSING_REASON_CLOUD: MissingReason
MISSING_REASON_POLAR_NIGHT: MissingReason
MISSING_REASON_INSTRUMENT_OUTAGE: MissingReason
MISSING_REASON_BEFORE_MISSION: MissingReason
MISSING_REASON_LOW_COVERAGE: MissingReason
MISSING_REASON_TOO_FEW_DAYS: MissingReason
MISSING_REASON_NOT_POSITIVE: MissingReason
MISSING_REASON_NO_NORMAL: MissingReason
CUBE_KIND_UNSPECIFIED: CubeKind
CUBE_KIND_VALUES: CubeKind
CUBE_KIND_ANOMALIES: CubeKind

class Series(_message.Message):
    __slots__ = ("variable_id", "units", "bbox", "source", "step", "times", "values", "anomalies", "display_values", "coverage", "missing", "anomaly_kind", "baseline", "normals")
    VARIABLE_ID_FIELD_NUMBER: _ClassVar[int]
    UNITS_FIELD_NUMBER: _ClassVar[int]
    BBOX_FIELD_NUMBER: _ClassVar[int]
    SOURCE_FIELD_NUMBER: _ClassVar[int]
    STEP_FIELD_NUMBER: _ClassVar[int]
    TIMES_FIELD_NUMBER: _ClassVar[int]
    VALUES_FIELD_NUMBER: _ClassVar[int]
    ANOMALIES_FIELD_NUMBER: _ClassVar[int]
    DISPLAY_VALUES_FIELD_NUMBER: _ClassVar[int]
    COVERAGE_FIELD_NUMBER: _ClassVar[int]
    MISSING_FIELD_NUMBER: _ClassVar[int]
    ANOMALY_KIND_FIELD_NUMBER: _ClassVar[int]
    BASELINE_FIELD_NUMBER: _ClassVar[int]
    NORMALS_FIELD_NUMBER: _ClassVar[int]
    variable_id: str
    units: str
    bbox: _common_pb2.BBox
    source: _ingest_pb2.Source
    step: _common_pb2.TimeStep
    times: _containers.RepeatedCompositeFieldContainer[_timestamp_pb2.Timestamp]
    values: _containers.RepeatedScalarFieldContainer[float]
    anomalies: _containers.RepeatedScalarFieldContainer[float]
    display_values: _containers.RepeatedScalarFieldContainer[float]
    coverage: _containers.RepeatedScalarFieldContainer[float]
    missing: _containers.RepeatedCompositeFieldContainer[MissingValue]
    anomaly_kind: AnomalyKind
    baseline: _common_pb2.YearRange
    normals: _containers.RepeatedCompositeFieldContainer[MonthlyNormal]
    def __init__(self, variable_id: _Optional[str] = ..., units: _Optional[str] = ..., bbox: _Optional[_Union[_common_pb2.BBox, _Mapping]] = ..., source: _Optional[_Union[_ingest_pb2.Source, str]] = ..., step: _Optional[_Union[_common_pb2.TimeStep, str]] = ..., times: _Optional[_Iterable[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]]] = ..., values: _Optional[_Iterable[float]] = ..., anomalies: _Optional[_Iterable[float]] = ..., display_values: _Optional[_Iterable[float]] = ..., coverage: _Optional[_Iterable[float]] = ..., missing: _Optional[_Iterable[_Union[MissingValue, _Mapping]]] = ..., anomaly_kind: _Optional[_Union[AnomalyKind, str]] = ..., baseline: _Optional[_Union[_common_pb2.YearRange, _Mapping]] = ..., normals: _Optional[_Iterable[_Union[MonthlyNormal, _Mapping]]] = ...) -> None: ...

class MissingValue(_message.Message):
    __slots__ = ("index", "reason")
    INDEX_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    index: int
    reason: MissingReason
    def __init__(self, index: _Optional[int] = ..., reason: _Optional[_Union[MissingReason, str]] = ...) -> None: ...

class MonthlyNormal(_message.Message):
    __slots__ = ("month", "mean", "sd", "years", "usable")
    MONTH_FIELD_NUMBER: _ClassVar[int]
    MEAN_FIELD_NUMBER: _ClassVar[int]
    SD_FIELD_NUMBER: _ClassVar[int]
    YEARS_FIELD_NUMBER: _ClassVar[int]
    USABLE_FIELD_NUMBER: _ClassVar[int]
    month: int
    mean: float
    sd: float
    years: int
    usable: bool
    def __init__(self, month: _Optional[int] = ..., mean: _Optional[float] = ..., sd: _Optional[float] = ..., years: _Optional[int] = ..., usable: _Optional[bool] = ...) -> None: ...

class Grid(_message.Message):
    __slots__ = ("variable_id", "units", "bbox", "rows", "cols", "lats", "lons", "step", "times", "values", "anomalies", "block_pixels", "cell_pixels", "lat_edges", "lon_edges")
    VARIABLE_ID_FIELD_NUMBER: _ClassVar[int]
    UNITS_FIELD_NUMBER: _ClassVar[int]
    BBOX_FIELD_NUMBER: _ClassVar[int]
    ROWS_FIELD_NUMBER: _ClassVar[int]
    COLS_FIELD_NUMBER: _ClassVar[int]
    LATS_FIELD_NUMBER: _ClassVar[int]
    LONS_FIELD_NUMBER: _ClassVar[int]
    STEP_FIELD_NUMBER: _ClassVar[int]
    TIMES_FIELD_NUMBER: _ClassVar[int]
    VALUES_FIELD_NUMBER: _ClassVar[int]
    ANOMALIES_FIELD_NUMBER: _ClassVar[int]
    BLOCK_PIXELS_FIELD_NUMBER: _ClassVar[int]
    CELL_PIXELS_FIELD_NUMBER: _ClassVar[int]
    LAT_EDGES_FIELD_NUMBER: _ClassVar[int]
    LON_EDGES_FIELD_NUMBER: _ClassVar[int]
    variable_id: str
    units: str
    bbox: _common_pb2.BBox
    rows: int
    cols: int
    lats: _containers.RepeatedScalarFieldContainer[float]
    lons: _containers.RepeatedScalarFieldContainer[float]
    step: _common_pb2.TimeStep
    times: _containers.RepeatedCompositeFieldContainer[_timestamp_pb2.Timestamp]
    values: _common_pb2.BlobRef
    anomalies: _common_pb2.BlobRef
    block_pixels: int
    cell_pixels: _containers.RepeatedScalarFieldContainer[int]
    lat_edges: _containers.RepeatedScalarFieldContainer[float]
    lon_edges: _containers.RepeatedScalarFieldContainer[float]
    def __init__(self, variable_id: _Optional[str] = ..., units: _Optional[str] = ..., bbox: _Optional[_Union[_common_pb2.BBox, _Mapping]] = ..., rows: _Optional[int] = ..., cols: _Optional[int] = ..., lats: _Optional[_Iterable[float]] = ..., lons: _Optional[_Iterable[float]] = ..., step: _Optional[_Union[_common_pb2.TimeStep, str]] = ..., times: _Optional[_Iterable[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]]] = ..., values: _Optional[_Union[_common_pb2.BlobRef, _Mapping]] = ..., anomalies: _Optional[_Union[_common_pb2.BlobRef, _Mapping]] = ..., block_pixels: _Optional[int] = ..., cell_pixels: _Optional[_Iterable[int]] = ..., lat_edges: _Optional[_Iterable[float]] = ..., lon_edges: _Optional[_Iterable[float]] = ...) -> None: ...

class CubeHeader(_message.Message):
    __slots__ = ("variable_id", "units", "kind", "bbox", "rows", "cols", "lats", "lons", "step", "times", "scale", "offset", "missing_value", "color_min", "color_max", "diverging", "data")
    VARIABLE_ID_FIELD_NUMBER: _ClassVar[int]
    UNITS_FIELD_NUMBER: _ClassVar[int]
    KIND_FIELD_NUMBER: _ClassVar[int]
    BBOX_FIELD_NUMBER: _ClassVar[int]
    ROWS_FIELD_NUMBER: _ClassVar[int]
    COLS_FIELD_NUMBER: _ClassVar[int]
    LATS_FIELD_NUMBER: _ClassVar[int]
    LONS_FIELD_NUMBER: _ClassVar[int]
    STEP_FIELD_NUMBER: _ClassVar[int]
    TIMES_FIELD_NUMBER: _ClassVar[int]
    SCALE_FIELD_NUMBER: _ClassVar[int]
    OFFSET_FIELD_NUMBER: _ClassVar[int]
    MISSING_VALUE_FIELD_NUMBER: _ClassVar[int]
    COLOR_MIN_FIELD_NUMBER: _ClassVar[int]
    COLOR_MAX_FIELD_NUMBER: _ClassVar[int]
    DIVERGING_FIELD_NUMBER: _ClassVar[int]
    DATA_FIELD_NUMBER: _ClassVar[int]
    variable_id: str
    units: str
    kind: CubeKind
    bbox: _common_pb2.BBox
    rows: int
    cols: int
    lats: _containers.RepeatedScalarFieldContainer[float]
    lons: _containers.RepeatedScalarFieldContainer[float]
    step: _common_pb2.TimeStep
    times: _containers.RepeatedCompositeFieldContainer[_timestamp_pb2.Timestamp]
    scale: float
    offset: float
    missing_value: int
    color_min: float
    color_max: float
    diverging: bool
    data: _common_pb2.BlobRef
    def __init__(self, variable_id: _Optional[str] = ..., units: _Optional[str] = ..., kind: _Optional[_Union[CubeKind, str]] = ..., bbox: _Optional[_Union[_common_pb2.BBox, _Mapping]] = ..., rows: _Optional[int] = ..., cols: _Optional[int] = ..., lats: _Optional[_Iterable[float]] = ..., lons: _Optional[_Iterable[float]] = ..., step: _Optional[_Union[_common_pb2.TimeStep, str]] = ..., times: _Optional[_Iterable[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]]] = ..., scale: _Optional[float] = ..., offset: _Optional[float] = ..., missing_value: _Optional[int] = ..., color_min: _Optional[float] = ..., color_max: _Optional[float] = ..., diverging: _Optional[bool] = ..., data: _Optional[_Union[_common_pb2.BlobRef, _Mapping]] = ...) -> None: ...
