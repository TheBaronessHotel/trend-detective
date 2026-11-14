from __future__ import annotations

import numpy as np
import pytest
from scipy import stats

from trenddetective.analysis.v1 import analysis_pb2
from trenddetective.common.v1 import common_pb2
from trenddetective.gateway.v1 import task_pb2
from trenddetective.preprocess.v1 import data_pb2

from conftest import ar1_noise, catalog_plan, delhi, make_series, month_starts, seasonal_series, series_plan
from analysis import series as series_mod
from analysis import suite
from analysis.errors import TaskFailure

SEASONAL = analysis_pb2.MANN_KENDALL_VARIANT_SEASONAL


def failure(fn, *args) -> TaskFailure:
    with pytest.raises(TaskFailure) as info:
        fn(*args)
    return info.value


def test_delhi_temperature_with_the_catalog_plan():
    out = suite.run(delhi("T2M"), catalog_plan())

    assert out.variable_id == "T2M" and out.units == "C"
    assert out.n_valid == out.n_total == 528
    assert out.field == analysis_pb2.SERIES_FIELD_ANOMALIES
    # Preprocess doesn't make anomalies yet, so analysis did.
    assert list(out.warnings) == [series_mod.OWN_ANOMALIES]

    reg, mk = out.regression, out.mann_kendall
    assert reg.slope_ci_low < reg.slope_per_decade < reg.slope_ci_high
    assert mk.sens_ci_low < mk.sens_slope_per_decade < mk.sens_ci_high
    assert mk.variant == analysis_pb2.MANN_KENDALL_VARIANT_HAMED_RAO
    assert out.HasField("agreement") and out.agreement.same_direction

    # The time origin is the middle of 1981-2024, and the anomalies average
    # out to zero there.
    assert reg.model.time_origin.ToJsonString() == "2002-12-16T12:00:00Z"
    assert reg.model.intercept == pytest.approx(0, abs=1e-9)

    # Tests from later tasks stay unset.
    for name in ("pettitt", "residual_checks", "outliers", "shuffle", "trend_triangle", "years_to_significance"):
        assert not out.HasField(name)
    assert len(out.monthly_trends) == 0


def test_autocorrelation_correction_changes_the_delhi_verdict():
    # POWER's Delhi temperature cools slightly. The months are correlated, so
    # the plain t-test overstates how sure that is.
    plain = suite.run(delhi("T2M"), series_plan(correct=False)).regression
    corrected = suite.run(delhi("T2M"), series_plan(correct=True)).regression
    assert plain.slope_per_decade == corrected.slope_per_decade < 0
    assert plain.significant and not corrected.significant
    assert corrected.n_effective < plain.n_effective == 528


def test_delhi_rain_has_no_trend():
    # The demo's story: 44 years of Delhi rain show no significant trend yet.
    out = suite.run(delhi("PRECTOTCORR"), catalog_plan())
    assert out.units == "mm/day"
    assert not out.regression.significant
    assert not out.mann_kendall.significant
    assert out.agreement.same_significance


def test_one_test_significant_gets_a_caveat():
    # On Delhi temperature, Seasonal Kendall finds the cooling and the
    # corrected regression doesn't.
    out = suite.run(delhi("T2M"), series_plan(variant=SEASONAL))
    assert out.mann_kendall.significant and not out.regression.significant
    assert not out.agreement.same_significance
    assert suite.ONE_TEST_SIGNIFICANT in out.warnings


def test_preprocess_anomalies_are_used_when_present():
    n = 240
    values = seasonal_series(20, per_decade=0.0, seed=2)
    anomalies = 0.5 / 10 * np.arange(n) / 12 + np.random.default_rng(2).normal(0, 0.3, n)
    s = make_series(values, anomalies=anomalies, anomaly_kind=data_pb2.ANOMALY_KIND_PERCENT, units="mm/day")

    out = suite.run(s, series_plan())

    assert out.units == "%"
    assert series_mod.OWN_ANOMALIES not in out.warnings
    assert out.regression.slope_per_decade == pytest.approx(0.5, abs=0.15)
    assert out.regression.significant


def test_seasonal_kendall_finds_the_trend_under_the_seasons():
    s = make_series(seasonal_series(30, per_decade=0.5, phi=0.3, seed=6))
    out = suite.run(s, series_plan(variant=SEASONAL))
    assert out.mann_kendall.variant == SEASONAL
    assert out.mann_kendall.sens_slope_per_decade == pytest.approx(0.5, abs=0.15)
    assert out.regression.slope_per_decade == pytest.approx(0.5, abs=0.15)
    assert out.mann_kendall.significant and out.regression.significant


def test_seasonal_kendall_on_anomalies_is_the_test_on_values():
    values = seasonal_series(20, per_decade=0.3, seed=13) + 30  # all positive, for percent anomalies
    values[[4, 50, 51]] = np.nan
    on_values = suite.run(make_series(values), series_plan(variant=SEASONAL, field=analysis_pb2.SERIES_FIELD_VALUES))

    # Absolute anomalies, the ones analysis makes itself: the same test and slope.
    absolute = suite.run(make_series(values), series_plan(variant=SEASONAL)).mann_kendall
    assert absolute.z == pytest.approx(on_values.mann_kendall.z)
    assert absolute.p_value == pytest.approx(on_values.mann_kendall.p_value)
    assert absolute.sens_slope_per_decade == pytest.approx(on_values.mann_kendall.sens_slope_per_decade)

    # Percent anomalies from preprocess: the same test, the slope in percent.
    m = series_mod.prepare(make_series(values), analysis_pb2.SERIES_FIELD_ANOMALIES)
    normals = np.array([np.nanmean(values[m.calendar_month == c]) for c in range(12)])[m.calendar_month]
    percent = (values - normals) / normals * 100
    s = make_series(values, anomalies=percent, anomaly_kind=data_pb2.ANOMALY_KIND_PERCENT)
    out = suite.run(s, series_plan(variant=SEASONAL))
    assert out.units == "%"
    assert out.mann_kendall.z == pytest.approx(on_values.mann_kendall.z)
    assert out.mann_kendall.sens_slope_per_decade == pytest.approx(
        on_values.mann_kendall.sens_slope_per_decade / normals.mean() * 100, rel=0.1
    )


def test_values_field_keeps_the_seasons_and_says_so():
    s = make_series(seasonal_series(20, seed=3))
    out = suite.run(s, series_plan(field=analysis_pb2.SERIES_FIELD_VALUES))
    assert out.field == analysis_pb2.SERIES_FIELD_VALUES
    assert out.units == "°C"
    assert series_mod.RAW_VALUES in out.warnings


def test_band_and_interval_come_from_the_model():
    # What the web app needs for the trend line and its band: the line is
    # intercept + slope * t, t in years since time_origin, and the interval's
    # t quantile can be read back from the slope's interval.
    values = seasonal_series(25, per_decade=0.4, seed=12)
    values[[7, 8, 9, 120, 200]] = np.nan
    out = suite.run(make_series(values), series_plan())
    reg = out.regression
    t_crit = (reg.slope_ci_high - reg.slope_per_decade) / (10 * reg.model.slope_stderr)
    assert t_crit == pytest.approx(stats.t.ppf(0.975, max(reg.n_effective, 3) - 2))
    assert reg.model.slope * 10 == pytest.approx(reg.slope_per_decade)
    # Centered on the valid months, so the intercept is the mean anomaly.
    m = series_mod.prepare(make_series(values), analysis_pb2.SERIES_FIELD_ANOMALIES)
    assert reg.model.intercept == pytest.approx(np.nanmean(m.tested))


def test_short_gappy_record_gets_caveats():
    values = seasonal_series(8, seed=4)
    rng = np.random.default_rng(4)
    values[rng.choice(values.size, size=29, replace=False)] = np.nan  # 29 of 96 is 30%
    out = suite.run(make_series(values), series_plan())
    assert "Only 8 years of data. A trend in a record this short is uncertain." in out.warnings
    assert "30% of the months have no value." in out.warnings


def test_strong_autocorrelation_gets_a_caveat():
    values = 10 + ar1_noise(120, 0.97, 1.0, seed=5)
    out = suite.run(make_series(values), series_plan())
    assert out.regression.n_effective < suite.FEW_EFFECTIVE / 2
    assert any(w.startswith("Neighboring months are so alike") for w in out.warnings)


def test_months_missing_from_the_times_count_as_missing():
    values = seasonal_series(4, seed=5)
    s = make_series(values)
    keep = [i for i in range(values.size) if i not in (13, 14, 40)]
    sparse = data_pb2.Series(variable_id="v", units="°C", step=common_pb2.TIME_STEP_MONTHLY)
    sparse.times.extend([s.times[i] for i in keep])
    sparse.values.extend([s.values[i] for i in keep])
    out = suite.run(sparse, series_plan())
    assert out.n_total == 48
    assert out.n_valid == 45


def test_daily_series_is_refused():
    s = make_series(np.arange(800.0), step=common_pb2.TIME_STEP_DAILY)
    err = failure(suite.run, s, series_plan())
    assert err.code == task_pb2.ERROR_CODE_INVALID_REQUEST
    assert "daily" in err.user_message


def test_several_values_in_a_month_are_refused():
    s = make_series(np.arange(24.0), step=common_pb2.TIME_STEP_UNSPECIFIED)
    del s.times[:]
    s.times.extend(month_starts(2000, 1, 12) + month_starts(2000, 1, 12))
    err = failure(suite.run, s, series_plan())
    assert err.code == task_pb2.ERROR_CODE_INVALID_REQUEST


@pytest.mark.parametrize(
    "values",
    [
        np.arange(11.0),  # too few values
        np.r_[np.arange(12.0), np.full(11, np.nan)],  # 12 values, but within one year
    ],
)
def test_too_little_data_is_refused(values):
    err = failure(suite.run, make_series(values), series_plan())
    assert err.code == task_pb2.ERROR_CODE_NO_DATA


def test_bad_plans_are_refused():
    s = make_series(seasonal_series(10, seed=1))
    assert failure(suite.run, s, series_plan(alpha=0.7)).code == task_pb2.ERROR_CODE_INVALID_REQUEST
    plan = series_plan()
    plan.series[1].mann_kendall.variant = 99
    assert failure(suite.run, s, plan).code == task_pb2.ERROR_CODE_INVALID_REQUEST


def test_defaults_and_repeats():
    s = make_series(seasonal_series(10, seed=1))
    plan = analysis_pb2.TestPlan()
    plan.series.add().mann_kendall.SetInParent()
    plan.series.add().mann_kendall.variant = analysis_pb2.MANN_KENDALL_VARIANT_ORIGINAL
    out = suite.run(s, plan)
    assert out.alpha == 0.05
    # The first Mann-Kendall wins, and an unset variant means Hamed and Rao.
    assert out.mann_kendall.variant == analysis_pb2.MANN_KENDALL_VARIANT_HAMED_RAO
    assert not out.HasField("regression") and not out.HasField("agreement")


def test_empty_plan_still_describes_the_series():
    out = suite.run(make_series(seasonal_series(10, seed=1)), analysis_pb2.TestPlan())
    assert out.n_valid == 120
    assert not out.HasField("regression") and not out.HasField("mann_kendall")
