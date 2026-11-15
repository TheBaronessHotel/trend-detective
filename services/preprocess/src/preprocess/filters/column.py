"""column_average: collapses data on pressure levels into one value per pixel.

The result is the average over the air column above each pixel, each level
weighted by the air it holds. Air per unit area is proportional to the
pressure difference across a layer, so the weights are pressure thicknesses.
For a mole fraction, like AIRS methane, that's the column-average mole
fraction.

Layers, for levels sorted from the ground up (highest pressure first):

- level i's layer runs from halfway to the level below it up to halfway to
  the level above it;
- the lowest layer starts at the surface pressure, and every layer is cut
  there, so a level under the ground weighs nothing;
- the top layer ends at 0 hPa.

A pixel-step counts only if the levels with a value hold at least
min_coverage of the column's air. AIRS leaves levels under the ground empty,
and those are already out of the column, so they don't count against it.

The surface pressure is a helper variable the source lists after the
measurement. Without it, the column would start at the lowest level, and over
mountains it would average in air that isn't there.
"""

from __future__ import annotations

import numpy as np
import xarray as xr

from trenddetective.preprocess.v1 import data_pb2, preprocess_pb2

from .. import canonical, errors
from ..canonical import REASON, VALUE, Decoded

DEFAULT_MIN_COVERAGE = 0.9


def layer_bounds(levels: np.ndarray) -> tuple[np.ndarray, np.ndarray]:
    """Each level's layer as (bottom, top) pressures, for levels in any order.
    The lowest layer's bottom is +inf, for the surface pressure to cut."""
    order = np.argsort(-levels)
    p = levels[order]
    mid = (p[:-1] + p[1:]) / 2
    bottom = np.concatenate([[np.inf], mid])
    top = np.concatenate([mid, [0.0]])
    out_bottom, out_top = np.empty_like(p), np.empty_like(p)
    out_bottom[order], out_top[order] = bottom, top
    return out_bottom, out_top


def column_average(dec: Decoded, op: preprocess_pb2.ColumnAverage) -> None:
    if "level" not in dec.data.dims:
        raise errors.invalid(
            "This variable's preparation steps are set up wrong.",
            "filter chain: column_average needs data on pressure levels, and this data has none",
        )
    helper = canonical.helper_name(op.surface_pressure)
    if not op.surface_pressure or helper not in dec.data:
        raise errors.invalid(
            "This variable's preparation steps are set up wrong.",
            f"filter chain: column_average needs the surface pressure variable {op.surface_pressure!r} "
            "listed after the measurement in the source's variables",
        )
    min_coverage = op.min_coverage or DEFAULT_MIN_COVERAGE

    v = dec.data[VALUE]
    ps = dec.data[helper]
    levels = dec.data["level"].values.astype("float64")
    bottom, top = layer_bounds(levels)
    bottom_da = xr.DataArray(bottom, dims="level", coords={"level": levels})
    top_da = xr.DataArray(top, dims="level", coords={"level": levels})

    # Thickness of each layer above the ground, per pixel-step.
    weight = (np.minimum(bottom_da, ps) - top_da).clip(min=0)
    total = weight.sum("level")
    valid = v.notnull()
    covered = weight.where(valid, 0).sum("level")
    mean = (v * weight).where(valid, 0).sum("level") / covered.where(covered > 0)

    enough = ps.notnull() & (total > 0) & (covered >= min_coverage * total)
    any_value = valid.any("level")
    ds = xr.Dataset({VALUE: mean.where(enough)})
    ds[REASON] = xr.where(
        enough,
        0,
        xr.where(any_value, data_pb2.MISSING_REASON_QC_FAILED, data_pb2.MISSING_REASON_FILL_VALUE),
    ).astype("int8")
    for name in dec.data.data_vars:
        if str(name).startswith(canonical.HELPER_PREFIX):
            ds[name] = dec.data[name]
    dec.data = canonical.normalize_axes(ds)
