from trenddetective.common.v1 import common_pb2 as _common_pb2
from trenddetective.preprocess.v1 import preprocess_pb2 as _preprocess_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class SeriesField(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    SERIES_FIELD_UNSPECIFIED: _ClassVar[SeriesField]
    SERIES_FIELD_ANOMALIES: _ClassVar[SeriesField]
    SERIES_FIELD_VALUES: _ClassVar[SeriesField]

class MannKendallVariant(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    MANN_KENDALL_VARIANT_UNSPECIFIED: _ClassVar[MannKendallVariant]
    MANN_KENDALL_VARIANT_ORIGINAL: _ClassVar[MannKendallVariant]
    MANN_KENDALL_VARIANT_HAMED_RAO: _ClassVar[MannKendallVariant]
    MANN_KENDALL_VARIANT_SEASONAL: _ClassVar[MannKendallVariant]
SERIES_FIELD_UNSPECIFIED: SeriesField
SERIES_FIELD_ANOMALIES: SeriesField
SERIES_FIELD_VALUES: SeriesField
MANN_KENDALL_VARIANT_UNSPECIFIED: MannKendallVariant
MANN_KENDALL_VARIANT_ORIGINAL: MannKendallVariant
MANN_KENDALL_VARIANT_HAMED_RAO: MannKendallVariant
MANN_KENDALL_VARIANT_SEASONAL: MannKendallVariant

class AnalysisTask(_message.Message):
    __slots__ = ("input", "tests")
    INPUT_FIELD_NUMBER: _ClassVar[int]
    TESTS_FIELD_NUMBER: _ClassVar[int]
    input: _preprocess_pb2.PreprocessResult
    tests: TestPlan
    def __init__(self, input: _Optional[_Union[_preprocess_pb2.PreprocessResult, _Mapping]] = ..., tests: _Optional[_Union[TestPlan, _Mapping]] = ...) -> None: ...

class TestPlan(_message.Message):
    __slots__ = ("alpha", "field", "series", "grid", "fdr_q")
    ALPHA_FIELD_NUMBER: _ClassVar[int]
    FIELD_FIELD_NUMBER: _ClassVar[int]
    SERIES_FIELD_NUMBER: _ClassVar[int]
    GRID_FIELD_NUMBER: _ClassVar[int]
    FDR_Q_FIELD_NUMBER: _ClassVar[int]
    alpha: float
    field: SeriesField
    series: _containers.RepeatedCompositeFieldContainer[SeriesTest]
    grid: _containers.RepeatedCompositeFieldContainer[GridTest]
    fdr_q: float
    def __init__(self, alpha: _Optional[float] = ..., field: _Optional[_Union[SeriesField, str]] = ..., series: _Optional[_Iterable[_Union[SeriesTest, _Mapping]]] = ..., grid: _Optional[_Iterable[_Union[GridTest, _Mapping]]] = ..., fdr_q: _Optional[float] = ...) -> None: ...

class SeriesTest(_message.Message):
    __slots__ = ("regression", "mann_kendall", "pettitt", "residual_checks", "outliers", "shuffle", "trend_triangle", "monthly_trends", "years_to_significance")
    REGRESSION_FIELD_NUMBER: _ClassVar[int]
    MANN_KENDALL_FIELD_NUMBER: _ClassVar[int]
    PETTITT_FIELD_NUMBER: _ClassVar[int]
    RESIDUAL_CHECKS_FIELD_NUMBER: _ClassVar[int]
    OUTLIERS_FIELD_NUMBER: _ClassVar[int]
    SHUFFLE_FIELD_NUMBER: _ClassVar[int]
    TREND_TRIANGLE_FIELD_NUMBER: _ClassVar[int]
    MONTHLY_TRENDS_FIELD_NUMBER: _ClassVar[int]
    YEARS_TO_SIGNIFICANCE_FIELD_NUMBER: _ClassVar[int]
    regression: Regression
    mann_kendall: MannKendall
    pettitt: Pettitt
    residual_checks: ResidualChecks
    outliers: Outliers
    shuffle: Shuffle
    trend_triangle: TrendTriangle
    monthly_trends: MonthlyTrends
    years_to_significance: YearsToSignificance
    def __init__(self, regression: _Optional[_Union[Regression, _Mapping]] = ..., mann_kendall: _Optional[_Union[MannKendall, _Mapping]] = ..., pettitt: _Optional[_Union[Pettitt, _Mapping]] = ..., residual_checks: _Optional[_Union[ResidualChecks, _Mapping]] = ..., outliers: _Optional[_Union[Outliers, _Mapping]] = ..., shuffle: _Optional[_Union[Shuffle, _Mapping]] = ..., trend_triangle: _Optional[_Union[TrendTriangle, _Mapping]] = ..., monthly_trends: _Optional[_Union[MonthlyTrends, _Mapping]] = ..., years_to_significance: _Optional[_Union[YearsToSignificance, _Mapping]] = ...) -> None: ...

class GridTest(_message.Message):
    __slots__ = ("regression", "mann_kendall")
    REGRESSION_FIELD_NUMBER: _ClassVar[int]
    MANN_KENDALL_FIELD_NUMBER: _ClassVar[int]
    regression: Regression
    mann_kendall: MannKendall
    def __init__(self, regression: _Optional[_Union[Regression, _Mapping]] = ..., mann_kendall: _Optional[_Union[MannKendall, _Mapping]] = ...) -> None: ...

class Regression(_message.Message):
    __slots__ = ("correct_autocorrelation",)
    CORRECT_AUTOCORRELATION_FIELD_NUMBER: _ClassVar[int]
    correct_autocorrelation: bool
    def __init__(self, correct_autocorrelation: _Optional[bool] = ...) -> None: ...

class MannKendall(_message.Message):
    __slots__ = ("variant",)
    VARIANT_FIELD_NUMBER: _ClassVar[int]
    variant: MannKendallVariant
    def __init__(self, variant: _Optional[_Union[MannKendallVariant, str]] = ...) -> None: ...

class Pettitt(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class ResidualChecks(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class Outliers(_message.Message):
    __slots__ = ("robust_z_threshold",)
    ROBUST_Z_THRESHOLD_FIELD_NUMBER: _ClassVar[int]
    robust_z_threshold: float
    def __init__(self, robust_z_threshold: _Optional[float] = ...) -> None: ...

class Shuffle(_message.Message):
    __slots__ = ("permutations", "block_length")
    PERMUTATIONS_FIELD_NUMBER: _ClassVar[int]
    BLOCK_LENGTH_FIELD_NUMBER: _ClassVar[int]
    permutations: int
    block_length: int
    def __init__(self, permutations: _Optional[int] = ..., block_length: _Optional[int] = ...) -> None: ...

class TrendTriangle(_message.Message):
    __slots__ = ("min_years",)
    MIN_YEARS_FIELD_NUMBER: _ClassVar[int]
    min_years: int
    def __init__(self, min_years: _Optional[int] = ...) -> None: ...

class MonthlyTrends(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class YearsToSignificance(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class AnalysisResult(_message.Message):
    __slots__ = ("series", "grid")
    SERIES_FIELD_NUMBER: _ClassVar[int]
    GRID_FIELD_NUMBER: _ClassVar[int]
    series: _common_pb2.BlobRef
    grid: _common_pb2.BlobRef
    def __init__(self, series: _Optional[_Union[_common_pb2.BlobRef, _Mapping]] = ..., grid: _Optional[_Union[_common_pb2.BlobRef, _Mapping]] = ...) -> None: ...
