import datetime

from google.protobuf import timestamp_pb2 as _timestamp_pb2
from trenddetective.analysis.v1 import analysis_pb2 as _analysis_pb2
from trenddetective.common.v1 import common_pb2 as _common_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class CheckStatus(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    CHECK_STATUS_UNSPECIFIED: _ClassVar[CheckStatus]
    CHECK_STATUS_PASS: _ClassVar[CheckStatus]
    CHECK_STATUS_WARN: _ClassVar[CheckStatus]
CHECK_STATUS_UNSPECIFIED: CheckStatus
CHECK_STATUS_PASS: CheckStatus
CHECK_STATUS_WARN: CheckStatus

class SeriesAnalysis(_message.Message):
    __slots__ = ("variable_id", "units", "alpha", "field", "n_valid", "n_total", "regression", "mann_kendall", "agreement", "pettitt", "residual_checks", "outliers", "shuffle", "trend_triangle", "monthly_trends", "years_to_significance", "warnings")
    VARIABLE_ID_FIELD_NUMBER: _ClassVar[int]
    UNITS_FIELD_NUMBER: _ClassVar[int]
    ALPHA_FIELD_NUMBER: _ClassVar[int]
    FIELD_FIELD_NUMBER: _ClassVar[int]
    N_VALID_FIELD_NUMBER: _ClassVar[int]
    N_TOTAL_FIELD_NUMBER: _ClassVar[int]
    REGRESSION_FIELD_NUMBER: _ClassVar[int]
    MANN_KENDALL_FIELD_NUMBER: _ClassVar[int]
    AGREEMENT_FIELD_NUMBER: _ClassVar[int]
    PETTITT_FIELD_NUMBER: _ClassVar[int]
    RESIDUAL_CHECKS_FIELD_NUMBER: _ClassVar[int]
    OUTLIERS_FIELD_NUMBER: _ClassVar[int]
    SHUFFLE_FIELD_NUMBER: _ClassVar[int]
    TREND_TRIANGLE_FIELD_NUMBER: _ClassVar[int]
    MONTHLY_TRENDS_FIELD_NUMBER: _ClassVar[int]
    YEARS_TO_SIGNIFICANCE_FIELD_NUMBER: _ClassVar[int]
    WARNINGS_FIELD_NUMBER: _ClassVar[int]
    variable_id: str
    units: str
    alpha: float
    field: _analysis_pb2.SeriesField
    n_valid: int
    n_total: int
    regression: RegressionResult
    mann_kendall: MannKendallResult
    agreement: Agreement
    pettitt: PettittResult
    residual_checks: ResidualChecksResult
    outliers: OutliersResult
    shuffle: ShuffleResult
    trend_triangle: TrendTriangleResult
    monthly_trends: _containers.RepeatedCompositeFieldContainer[MonthlyTrend]
    years_to_significance: YearsToSignificanceResult
    warnings: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, variable_id: _Optional[str] = ..., units: _Optional[str] = ..., alpha: _Optional[float] = ..., field: _Optional[_Union[_analysis_pb2.SeriesField, str]] = ..., n_valid: _Optional[int] = ..., n_total: _Optional[int] = ..., regression: _Optional[_Union[RegressionResult, _Mapping]] = ..., mann_kendall: _Optional[_Union[MannKendallResult, _Mapping]] = ..., agreement: _Optional[_Union[Agreement, _Mapping]] = ..., pettitt: _Optional[_Union[PettittResult, _Mapping]] = ..., residual_checks: _Optional[_Union[ResidualChecksResult, _Mapping]] = ..., outliers: _Optional[_Union[OutliersResult, _Mapping]] = ..., shuffle: _Optional[_Union[ShuffleResult, _Mapping]] = ..., trend_triangle: _Optional[_Union[TrendTriangleResult, _Mapping]] = ..., monthly_trends: _Optional[_Iterable[_Union[MonthlyTrend, _Mapping]]] = ..., years_to_significance: _Optional[_Union[YearsToSignificanceResult, _Mapping]] = ..., warnings: _Optional[_Iterable[str]] = ...) -> None: ...

class RegressionResult(_message.Message):
    __slots__ = ("slope_per_decade", "slope_ci_low", "slope_ci_high", "p_value", "significant", "r_squared", "n", "n_effective", "model")
    SLOPE_PER_DECADE_FIELD_NUMBER: _ClassVar[int]
    SLOPE_CI_LOW_FIELD_NUMBER: _ClassVar[int]
    SLOPE_CI_HIGH_FIELD_NUMBER: _ClassVar[int]
    P_VALUE_FIELD_NUMBER: _ClassVar[int]
    SIGNIFICANT_FIELD_NUMBER: _ClassVar[int]
    R_SQUARED_FIELD_NUMBER: _ClassVar[int]
    N_FIELD_NUMBER: _ClassVar[int]
    N_EFFECTIVE_FIELD_NUMBER: _ClassVar[int]
    MODEL_FIELD_NUMBER: _ClassVar[int]
    slope_per_decade: float
    slope_ci_low: float
    slope_ci_high: float
    p_value: float
    significant: bool
    r_squared: float
    n: int
    n_effective: float
    model: FittedModel
    def __init__(self, slope_per_decade: _Optional[float] = ..., slope_ci_low: _Optional[float] = ..., slope_ci_high: _Optional[float] = ..., p_value: _Optional[float] = ..., significant: _Optional[bool] = ..., r_squared: _Optional[float] = ..., n: _Optional[int] = ..., n_effective: _Optional[float] = ..., model: _Optional[_Union[FittedModel, _Mapping]] = ...) -> None: ...

class FittedModel(_message.Message):
    __slots__ = ("time_origin", "intercept", "intercept_stderr", "slope", "slope_stderr", "residual_sd", "lag1_autocorrelation")
    TIME_ORIGIN_FIELD_NUMBER: _ClassVar[int]
    INTERCEPT_FIELD_NUMBER: _ClassVar[int]
    INTERCEPT_STDERR_FIELD_NUMBER: _ClassVar[int]
    SLOPE_FIELD_NUMBER: _ClassVar[int]
    SLOPE_STDERR_FIELD_NUMBER: _ClassVar[int]
    RESIDUAL_SD_FIELD_NUMBER: _ClassVar[int]
    LAG1_AUTOCORRELATION_FIELD_NUMBER: _ClassVar[int]
    time_origin: _timestamp_pb2.Timestamp
    intercept: float
    intercept_stderr: float
    slope: float
    slope_stderr: float
    residual_sd: float
    lag1_autocorrelation: float
    def __init__(self, time_origin: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., intercept: _Optional[float] = ..., intercept_stderr: _Optional[float] = ..., slope: _Optional[float] = ..., slope_stderr: _Optional[float] = ..., residual_sd: _Optional[float] = ..., lag1_autocorrelation: _Optional[float] = ...) -> None: ...

class MannKendallResult(_message.Message):
    __slots__ = ("variant", "tau", "z", "p_value", "significant", "sens_slope_per_decade", "sens_ci_low", "sens_ci_high", "n")
    VARIANT_FIELD_NUMBER: _ClassVar[int]
    TAU_FIELD_NUMBER: _ClassVar[int]
    Z_FIELD_NUMBER: _ClassVar[int]
    P_VALUE_FIELD_NUMBER: _ClassVar[int]
    SIGNIFICANT_FIELD_NUMBER: _ClassVar[int]
    SENS_SLOPE_PER_DECADE_FIELD_NUMBER: _ClassVar[int]
    SENS_CI_LOW_FIELD_NUMBER: _ClassVar[int]
    SENS_CI_HIGH_FIELD_NUMBER: _ClassVar[int]
    N_FIELD_NUMBER: _ClassVar[int]
    variant: _analysis_pb2.MannKendallVariant
    tau: float
    z: float
    p_value: float
    significant: bool
    sens_slope_per_decade: float
    sens_ci_low: float
    sens_ci_high: float
    n: int
    def __init__(self, variant: _Optional[_Union[_analysis_pb2.MannKendallVariant, str]] = ..., tau: _Optional[float] = ..., z: _Optional[float] = ..., p_value: _Optional[float] = ..., significant: _Optional[bool] = ..., sens_slope_per_decade: _Optional[float] = ..., sens_ci_low: _Optional[float] = ..., sens_ci_high: _Optional[float] = ..., n: _Optional[int] = ...) -> None: ...

class Agreement(_message.Message):
    __slots__ = ("same_direction", "same_significance")
    SAME_DIRECTION_FIELD_NUMBER: _ClassVar[int]
    SAME_SIGNIFICANCE_FIELD_NUMBER: _ClassVar[int]
    same_direction: bool
    same_significance: bool
    def __init__(self, same_direction: _Optional[bool] = ..., same_significance: _Optional[bool] = ...) -> None: ...

class PettittResult(_message.Message):
    __slots__ = ("change_time", "p_value", "significant", "mean_before", "mean_after")
    CHANGE_TIME_FIELD_NUMBER: _ClassVar[int]
    P_VALUE_FIELD_NUMBER: _ClassVar[int]
    SIGNIFICANT_FIELD_NUMBER: _ClassVar[int]
    MEAN_BEFORE_FIELD_NUMBER: _ClassVar[int]
    MEAN_AFTER_FIELD_NUMBER: _ClassVar[int]
    change_time: _timestamp_pb2.Timestamp
    p_value: float
    significant: bool
    mean_before: float
    mean_after: float
    def __init__(self, change_time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., p_value: _Optional[float] = ..., significant: _Optional[bool] = ..., mean_before: _Optional[float] = ..., mean_after: _Optional[float] = ...) -> None: ...

class ResidualChecksResult(_message.Message):
    __slots__ = ("autocorrelation", "lag1_autocorrelation", "normality", "normality_p_value")
    AUTOCORRELATION_FIELD_NUMBER: _ClassVar[int]
    LAG1_AUTOCORRELATION_FIELD_NUMBER: _ClassVar[int]
    NORMALITY_FIELD_NUMBER: _ClassVar[int]
    NORMALITY_P_VALUE_FIELD_NUMBER: _ClassVar[int]
    autocorrelation: CheckStatus
    lag1_autocorrelation: float
    normality: CheckStatus
    normality_p_value: float
    def __init__(self, autocorrelation: _Optional[_Union[CheckStatus, str]] = ..., lag1_autocorrelation: _Optional[float] = ..., normality: _Optional[_Union[CheckStatus, str]] = ..., normality_p_value: _Optional[float] = ...) -> None: ...

class OutliersResult(_message.Message):
    __slots__ = ("robust_z_threshold", "outliers", "highest", "lowest")
    ROBUST_Z_THRESHOLD_FIELD_NUMBER: _ClassVar[int]
    OUTLIERS_FIELD_NUMBER: _ClassVar[int]
    HIGHEST_FIELD_NUMBER: _ClassVar[int]
    LOWEST_FIELD_NUMBER: _ClassVar[int]
    robust_z_threshold: float
    outliers: _containers.RepeatedCompositeFieldContainer[Outlier]
    highest: Outlier
    lowest: Outlier
    def __init__(self, robust_z_threshold: _Optional[float] = ..., outliers: _Optional[_Iterable[_Union[Outlier, _Mapping]]] = ..., highest: _Optional[_Union[Outlier, _Mapping]] = ..., lowest: _Optional[_Union[Outlier, _Mapping]] = ...) -> None: ...

class Outlier(_message.Message):
    __slots__ = ("time", "value", "robust_z")
    TIME_FIELD_NUMBER: _ClassVar[int]
    VALUE_FIELD_NUMBER: _ClassVar[int]
    ROBUST_Z_FIELD_NUMBER: _ClassVar[int]
    time: _timestamp_pb2.Timestamp
    value: float
    robust_z: float
    def __init__(self, time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., value: _Optional[float] = ..., robust_z: _Optional[float] = ...) -> None: ...

class ShuffleResult(_message.Message):
    __slots__ = ("permutations", "block_length", "observed_slope_per_decade", "p_value", "shuffled_slopes_per_decade")
    PERMUTATIONS_FIELD_NUMBER: _ClassVar[int]
    BLOCK_LENGTH_FIELD_NUMBER: _ClassVar[int]
    OBSERVED_SLOPE_PER_DECADE_FIELD_NUMBER: _ClassVar[int]
    P_VALUE_FIELD_NUMBER: _ClassVar[int]
    SHUFFLED_SLOPES_PER_DECADE_FIELD_NUMBER: _ClassVar[int]
    permutations: int
    block_length: int
    observed_slope_per_decade: float
    p_value: float
    shuffled_slopes_per_decade: _containers.RepeatedScalarFieldContainer[float]
    def __init__(self, permutations: _Optional[int] = ..., block_length: _Optional[int] = ..., observed_slope_per_decade: _Optional[float] = ..., p_value: _Optional[float] = ..., shuffled_slopes_per_decade: _Optional[_Iterable[float]] = ...) -> None: ...

class TrendTriangleResult(_message.Message):
    __slots__ = ("min_years", "start_years", "end_years", "slopes_per_decade", "p_values")
    MIN_YEARS_FIELD_NUMBER: _ClassVar[int]
    START_YEARS_FIELD_NUMBER: _ClassVar[int]
    END_YEARS_FIELD_NUMBER: _ClassVar[int]
    SLOPES_PER_DECADE_FIELD_NUMBER: _ClassVar[int]
    P_VALUES_FIELD_NUMBER: _ClassVar[int]
    min_years: int
    start_years: _containers.RepeatedScalarFieldContainer[int]
    end_years: _containers.RepeatedScalarFieldContainer[int]
    slopes_per_decade: _containers.RepeatedScalarFieldContainer[float]
    p_values: _containers.RepeatedScalarFieldContainer[float]
    def __init__(self, min_years: _Optional[int] = ..., start_years: _Optional[_Iterable[int]] = ..., end_years: _Optional[_Iterable[int]] = ..., slopes_per_decade: _Optional[_Iterable[float]] = ..., p_values: _Optional[_Iterable[float]] = ...) -> None: ...

class MonthlyTrend(_message.Message):
    __slots__ = ("month", "slope_per_decade", "p_value", "significant")
    MONTH_FIELD_NUMBER: _ClassVar[int]
    SLOPE_PER_DECADE_FIELD_NUMBER: _ClassVar[int]
    P_VALUE_FIELD_NUMBER: _ClassVar[int]
    SIGNIFICANT_FIELD_NUMBER: _ClassVar[int]
    month: int
    slope_per_decade: float
    p_value: float
    significant: bool
    def __init__(self, month: _Optional[int] = ..., slope_per_decade: _Optional[float] = ..., p_value: _Optional[float] = ..., significant: _Optional[bool] = ...) -> None: ...

class YearsToSignificanceResult(_message.Message):
    __slots__ = ("years_needed", "years_so_far", "noise_sd", "trend_per_year", "phi", "already_significant")
    YEARS_NEEDED_FIELD_NUMBER: _ClassVar[int]
    YEARS_SO_FAR_FIELD_NUMBER: _ClassVar[int]
    NOISE_SD_FIELD_NUMBER: _ClassVar[int]
    TREND_PER_YEAR_FIELD_NUMBER: _ClassVar[int]
    PHI_FIELD_NUMBER: _ClassVar[int]
    ALREADY_SIGNIFICANT_FIELD_NUMBER: _ClassVar[int]
    years_needed: float
    years_so_far: float
    noise_sd: float
    trend_per_year: float
    phi: float
    already_significant: bool
    def __init__(self, years_needed: _Optional[float] = ..., years_so_far: _Optional[float] = ..., noise_sd: _Optional[float] = ..., trend_per_year: _Optional[float] = ..., phi: _Optional[float] = ..., already_significant: _Optional[bool] = ...) -> None: ...

class GridAnalysis(_message.Message):
    __slots__ = ("variable_id", "units", "alpha", "fdr_q", "bbox", "rows", "cols", "lats", "lons", "valid_steps", "layers")
    VARIABLE_ID_FIELD_NUMBER: _ClassVar[int]
    UNITS_FIELD_NUMBER: _ClassVar[int]
    ALPHA_FIELD_NUMBER: _ClassVar[int]
    FDR_Q_FIELD_NUMBER: _ClassVar[int]
    BBOX_FIELD_NUMBER: _ClassVar[int]
    ROWS_FIELD_NUMBER: _ClassVar[int]
    COLS_FIELD_NUMBER: _ClassVar[int]
    LATS_FIELD_NUMBER: _ClassVar[int]
    LONS_FIELD_NUMBER: _ClassVar[int]
    VALID_STEPS_FIELD_NUMBER: _ClassVar[int]
    LAYERS_FIELD_NUMBER: _ClassVar[int]
    variable_id: str
    units: str
    alpha: float
    fdr_q: float
    bbox: _common_pb2.BBox
    rows: int
    cols: int
    lats: _containers.RepeatedScalarFieldContainer[float]
    lons: _containers.RepeatedScalarFieldContainer[float]
    valid_steps: _containers.RepeatedScalarFieldContainer[int]
    layers: _containers.RepeatedCompositeFieldContainer[GridLayer]
    def __init__(self, variable_id: _Optional[str] = ..., units: _Optional[str] = ..., alpha: _Optional[float] = ..., fdr_q: _Optional[float] = ..., bbox: _Optional[_Union[_common_pb2.BBox, _Mapping]] = ..., rows: _Optional[int] = ..., cols: _Optional[int] = ..., lats: _Optional[_Iterable[float]] = ..., lons: _Optional[_Iterable[float]] = ..., valid_steps: _Optional[_Iterable[int]] = ..., layers: _Optional[_Iterable[_Union[GridLayer, _Mapping]]] = ...) -> None: ...

class GridLayer(_message.Message):
    __slots__ = ("test", "slopes_per_decade", "p_values", "significant", "p_threshold")
    TEST_FIELD_NUMBER: _ClassVar[int]
    SLOPES_PER_DECADE_FIELD_NUMBER: _ClassVar[int]
    P_VALUES_FIELD_NUMBER: _ClassVar[int]
    SIGNIFICANT_FIELD_NUMBER: _ClassVar[int]
    P_THRESHOLD_FIELD_NUMBER: _ClassVar[int]
    test: _analysis_pb2.GridTest
    slopes_per_decade: _containers.RepeatedScalarFieldContainer[float]
    p_values: _containers.RepeatedScalarFieldContainer[float]
    significant: _containers.RepeatedScalarFieldContainer[bool]
    p_threshold: float
    def __init__(self, test: _Optional[_Union[_analysis_pb2.GridTest, _Mapping]] = ..., slopes_per_decade: _Optional[_Iterable[float]] = ..., p_values: _Optional[_Iterable[float]] = ..., significant: _Optional[_Iterable[bool]] = ..., p_threshold: _Optional[float] = ...) -> None: ...
