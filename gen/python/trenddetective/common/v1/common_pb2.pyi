from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class TimeStep(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    TIME_STEP_UNSPECIFIED: _ClassVar[TimeStep]
    TIME_STEP_DAILY: _ClassVar[TimeStep]
    TIME_STEP_WEEKLY: _ClassVar[TimeStep]
    TIME_STEP_MONTHLY: _ClassVar[TimeStep]
TIME_STEP_UNSPECIFIED: TimeStep
TIME_STEP_DAILY: TimeStep
TIME_STEP_WEEKLY: TimeStep
TIME_STEP_MONTHLY: TimeStep

class BBox(_message.Message):
    __slots__ = ("west", "south", "east", "north")
    WEST_FIELD_NUMBER: _ClassVar[int]
    SOUTH_FIELD_NUMBER: _ClassVar[int]
    EAST_FIELD_NUMBER: _ClassVar[int]
    NORTH_FIELD_NUMBER: _ClassVar[int]
    west: float
    south: float
    east: float
    north: float
    def __init__(self, west: _Optional[float] = ..., south: _Optional[float] = ..., east: _Optional[float] = ..., north: _Optional[float] = ...) -> None: ...

class LatLon(_message.Message):
    __slots__ = ("lat", "lon")
    LAT_FIELD_NUMBER: _ClassVar[int]
    LON_FIELD_NUMBER: _ClassVar[int]
    lat: float
    lon: float
    def __init__(self, lat: _Optional[float] = ..., lon: _Optional[float] = ...) -> None: ...

class Date(_message.Message):
    __slots__ = ("year", "month", "day")
    YEAR_FIELD_NUMBER: _ClassVar[int]
    MONTH_FIELD_NUMBER: _ClassVar[int]
    DAY_FIELD_NUMBER: _ClassVar[int]
    year: int
    month: int
    day: int
    def __init__(self, year: _Optional[int] = ..., month: _Optional[int] = ..., day: _Optional[int] = ...) -> None: ...

class DateRange(_message.Message):
    __slots__ = ("start", "end")
    START_FIELD_NUMBER: _ClassVar[int]
    END_FIELD_NUMBER: _ClassVar[int]
    start: Date
    end: Date
    def __init__(self, start: _Optional[_Union[Date, _Mapping]] = ..., end: _Optional[_Union[Date, _Mapping]] = ...) -> None: ...

class YearRange(_message.Message):
    __slots__ = ("start_year", "end_year")
    START_YEAR_FIELD_NUMBER: _ClassVar[int]
    END_YEAR_FIELD_NUMBER: _ClassVar[int]
    start_year: int
    end_year: int
    def __init__(self, start_year: _Optional[int] = ..., end_year: _Optional[int] = ...) -> None: ...

class BlobRef(_message.Message):
    __slots__ = ("bucket", "key", "size_bytes", "content_type")
    BUCKET_FIELD_NUMBER: _ClassVar[int]
    KEY_FIELD_NUMBER: _ClassVar[int]
    SIZE_BYTES_FIELD_NUMBER: _ClassVar[int]
    CONTENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    bucket: str
    key: str
    size_bytes: int
    content_type: str
    def __init__(self, bucket: _Optional[str] = ..., key: _Optional[str] = ..., size_bytes: _Optional[int] = ..., content_type: _Optional[str] = ...) -> None: ...
