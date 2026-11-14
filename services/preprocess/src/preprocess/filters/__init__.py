"""The filter chain: the catalog's ordered list of steps, carried out on the
decoded data.

The steps act at three points, whatever their place in the list:

1. **Pixel steps** change the values in list order: qc_range, linear,
   monthly_mean, log10, coarsen.
2. **anomaly** adds a per-pixel anomaly layer next to the values. It has to
   come after every pixel step.
3. **gap_fill_display** works on the finished region series, so it only
   records its max_gap here. output.py fills Series.display_values.

The chain is checked before any work, and a chain that can't run fails with
INVALID_REQUEST, which points at the catalog entry.

Daily or weekly data whose chain has no monthly_mean gets one, with
AUTO_MIN_VALID_DAYS, before its first step other than qc_range or linear.
Anomalies need calendar months, and a daily user CSV can't say so in the
catalog.
"""

from __future__ import annotations

from collections.abc import Callable

from google.protobuf.message import Message

from trenddetective.common.v1 import common_pb2
from trenddetective.preprocess.v1 import data_pb2, preprocess_pb2

from .. import canonical, errors
from ..canonical import ANOMALY, VALUE, Decoded
from . import anomaly, pixel

AUTO_MIN_VALID_DAYS = 20
DEFAULT_MAX_GAP = 2

PIXEL_STEPS: dict[str, Callable[[Decoded, Message], None]] = {
    "qc_range": pixel.qc_range,
    "linear": pixel.linear,
    "monthly_mean": pixel.monthly_mean,
    "log10": pixel.log10,
    "coarsen": pixel.coarsen,
}

Step = tuple[str, Message]


def plan(chain: preprocess_pb2.FilterChain, step: int) -> list[Step]:
    """The steps to run, in order, after checking the chain."""
    steps: list[Step] = []
    for i, op in enumerate(chain.ops):
        name = op.WhichOneof("op")
        if name is None:
            raise _bad(f"step {i} is empty")
        steps.append((name, getattr(op, name)))

    for name, op in steps:
        # proto3 can't tell an unset limit from 0, so {min: 0} alone arrives
        # as min 0, max 0 and would drop every positive value. A range with
        # its max below its min, or both 0, is refused instead.
        if name == "qc_range" and (op.max < op.min or op.min == op.max == 0):
            raise _bad(f"qc_range needs both min and max, with max above min; got min {op.min}, max {op.max}")

    names = [n for n, _ in steps]
    for once in ("anomaly", "monthly_mean", "gap_fill_display"):
        if names.count(once) > 1:
            raise _bad(f"{once} appears {names.count(once)} times")
    if "anomaly" in names:
        after = [n for n in names[names.index("anomaly") + 1 :] if n in PIXEL_STEPS]
        if after:
            raise _bad(f"anomaly must come after every data step, but {', '.join(after)} follows it")

    if step in (common_pb2.TIME_STEP_DAILY, common_pb2.TIME_STEP_WEEKLY) and "monthly_mean" not in names:
        at = next((i for i, n in enumerate(names) if n not in ("qc_range", "linear")), len(names))
        auto = preprocess_pb2.MonthlyMean(min_valid_days=AUTO_MIN_VALID_DAYS)
        steps.insert(at, ("monthly_mean", auto))
    return steps


def apply(dec: Decoded, chain: preprocess_pb2.FilterChain) -> Decoded:
    """Runs the chain on dec in place and returns it."""
    steps = plan(chain, dec.step)
    if dec.step == common_pb2.TIME_STEP_MONTHLY:
        dec.data = canonical.regularize_monthly(dec.data)

    for name, op in steps:
        if name in PIXEL_STEPS:
            PIXEL_STEPS[name](dec, op)
            if name == "monthly_mean":
                dec.data = canonical.regularize_monthly(dec.data)
        elif name == "anomaly":
            _anomaly(dec, op)
        elif name == "gap_fill_display":
            dec.gap_fill_max = op.max_gap or DEFAULT_MAX_GAP
        dec.applied.append(name)
    return dec


def _anomaly(dec: Decoded, given: preprocess_pb2.Anomaly) -> None:
    if dec.step != common_pb2.TIME_STEP_MONTHLY:
        raise _bad("anomaly needs monthly data; add monthly_mean before it")
    # Unset kind and method mean the defaults, written out so the Series says
    # what was done.
    op = preprocess_pb2.Anomaly()
    op.CopyFrom(given)
    if op.kind == data_pb2.ANOMALY_KIND_UNSPECIFIED:
        op.kind = data_pb2.ANOMALY_KIND_ABSOLUTE
    if op.method == data_pb2.CLIMATOLOGY_METHOD_UNSPECIFIED:
        op.method = data_pb2.CLIMATOLOGY_METHOD_MONTHLY_MEANS
    anomaly.check(op, dec.units)
    times = dec.data["time"].values
    base = anomaly.baseline_years(times, op)

    v = dec.data[VALUE]
    flat = v.values.reshape(v.shape[0], -1)
    n = anomaly.normals(flat, times, op, base)
    anom = anomaly.anomalies(flat, times, op, n).reshape(v.shape)

    ds = dec.data.copy()
    ds[ANOMALY] = v.copy(data=anom)
    dec.data = ds

    used = preprocess_pb2.Anomaly()
    used.CopyFrom(op)
    used.baseline.CopyFrom(anomaly.year_range(base))
    dec.anomaly = used


def _bad(detail: str) -> errors.TaskFailure:
    return errors.invalid("This variable's preparation steps are set up wrong.", f"filter chain: {detail}")
