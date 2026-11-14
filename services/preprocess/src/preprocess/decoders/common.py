"""Helpers the decoders share."""

from __future__ import annotations

from trenddetective.ingest.v1 import ingest_pb2

from .. import errors

# Spellings of the same unit, so a file saying "C" and a catalog saying "°C"
# aren't reported as a mismatch.
_UNIT_ALIASES = {
    "c": "°c",
    "degc": "°c",
    "deg c": "°c",
    "degrees c": "°c",
    "celsius": "°c",
    "k": "k",
    "kelvin": "k",
    "kg m-2": "kg/m²",
    "kg/m2": "kg/m²",
    "kg m**-2": "kg/m²",
    "mm hr-1": "mm/hr",
    "mm/h": "mm/hr",
    "mm day-1": "mm/day",
    "mm/d": "mm/day",
}


def _norm(units: str) -> str:
    u = units.strip().lower()
    return _UNIT_ALIASES.get(u, u)


def resolve_units(declared: str, source_units: str, notes: list[str]) -> str:
    """Picks the units of the decoded values.

    The task's source_units wins when it's set, because the catalog writes its
    unit-conversion filters against it. A file that says something else is
    noted, so a wrong catalog entry shows up in the logs.
    """
    declared = (declared or "").strip()
    if source_units:
        if declared and _norm(declared) != _norm(source_units):
            notes.append(f"file says units {declared!r}, task says {source_units!r}; using the task's")
        return source_units
    return declared


def single_variable(raw: ingest_pb2.RawDataset) -> str:
    """The one variable a task decodes. A job is about one variable."""
    if len(raw.variables) != 1:
        raise errors.invalid(
            "The data for this job couldn't be matched to one variable.",
            f"RawDataset lists {len(raw.variables)} variables: {list(raw.variables)}",
        )
    return raw.variables[0]
