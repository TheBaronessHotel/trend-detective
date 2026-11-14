import datetime

from google.protobuf import timestamp_pb2 as _timestamp_pb2
from trenddetective.common.v1 import common_pb2 as _common_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class Source(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    SOURCE_UNSPECIFIED: _ClassVar[Source]
    SOURCE_HARMONY_SUBSET: _ClassVar[Source]
    SOURCE_GIOVANNI_AREA: _ClassVar[Source]
    SOURCE_GIOVANNI_POINT: _ClassVar[Source]
    SOURCE_POWER: _ClassVar[Source]
    SOURCE_CSV_UPLOAD: _ClassVar[Source]
    SOURCE_BUNDLE: _ClassVar[Source]
    SOURCE_APPEEARS: _ClassVar[Source]

class Format(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    FORMAT_UNSPECIFIED: _ClassVar[Format]
    FORMAT_NETCDF4: _ClassVar[Format]
    FORMAT_GIOVANNI_CSV: _ClassVar[Format]
    FORMAT_POWER_JSON: _ClassVar[Format]
    FORMAT_USER_CSV: _ClassVar[Format]
    FORMAT_GEOTIFF: _ClassVar[Format]
    FORMAT_GIOVANNI_POINT_CSV: _ClassVar[Format]
SOURCE_UNSPECIFIED: Source
SOURCE_HARMONY_SUBSET: Source
SOURCE_GIOVANNI_AREA: Source
SOURCE_GIOVANNI_POINT: Source
SOURCE_POWER: Source
SOURCE_CSV_UPLOAD: Source
SOURCE_BUNDLE: Source
SOURCE_APPEEARS: Source
FORMAT_UNSPECIFIED: Format
FORMAT_NETCDF4: Format
FORMAT_GIOVANNI_CSV: Format
FORMAT_POWER_JSON: Format
FORMAT_USER_CSV: Format
FORMAT_GEOTIFF: Format
FORMAT_GIOVANNI_POINT_CSV: Format

class IngestTask(_message.Message):
    __slots__ = ("variable_id", "bbox", "dates", "earthdata_token_key", "harmony_subset", "giovanni_area", "giovanni_point", "power", "csv_upload", "bundle", "appeears")
    VARIABLE_ID_FIELD_NUMBER: _ClassVar[int]
    BBOX_FIELD_NUMBER: _ClassVar[int]
    DATES_FIELD_NUMBER: _ClassVar[int]
    EARTHDATA_TOKEN_KEY_FIELD_NUMBER: _ClassVar[int]
    HARMONY_SUBSET_FIELD_NUMBER: _ClassVar[int]
    GIOVANNI_AREA_FIELD_NUMBER: _ClassVar[int]
    GIOVANNI_POINT_FIELD_NUMBER: _ClassVar[int]
    POWER_FIELD_NUMBER: _ClassVar[int]
    CSV_UPLOAD_FIELD_NUMBER: _ClassVar[int]
    BUNDLE_FIELD_NUMBER: _ClassVar[int]
    APPEEARS_FIELD_NUMBER: _ClassVar[int]
    variable_id: str
    bbox: _common_pb2.BBox
    dates: _common_pb2.DateRange
    earthdata_token_key: str
    harmony_subset: HarmonySubset
    giovanni_area: GiovanniArea
    giovanni_point: GiovanniPoint
    power: Power
    csv_upload: CsvUpload
    bundle: Bundle
    appeears: Appeears
    def __init__(self, variable_id: _Optional[str] = ..., bbox: _Optional[_Union[_common_pb2.BBox, _Mapping]] = ..., dates: _Optional[_Union[_common_pb2.DateRange, _Mapping]] = ..., earthdata_token_key: _Optional[str] = ..., harmony_subset: _Optional[_Union[HarmonySubset, _Mapping]] = ..., giovanni_area: _Optional[_Union[GiovanniArea, _Mapping]] = ..., giovanni_point: _Optional[_Union[GiovanniPoint, _Mapping]] = ..., power: _Optional[_Union[Power, _Mapping]] = ..., csv_upload: _Optional[_Union[CsvUpload, _Mapping]] = ..., bundle: _Optional[_Union[Bundle, _Mapping]] = ..., appeears: _Optional[_Union[Appeears, _Mapping]] = ...) -> None: ...

class HarmonySubset(_message.Message):
    __slots__ = ("collection_id", "variables", "max_granules")
    COLLECTION_ID_FIELD_NUMBER: _ClassVar[int]
    VARIABLES_FIELD_NUMBER: _ClassVar[int]
    MAX_GRANULES_FIELD_NUMBER: _ClassVar[int]
    collection_id: str
    variables: _containers.RepeatedScalarFieldContainer[str]
    max_granules: int
    def __init__(self, collection_id: _Optional[str] = ..., variables: _Optional[_Iterable[str]] = ..., max_granules: _Optional[int] = ...) -> None: ...

class GiovanniArea(_message.Message):
    __slots__ = ("data_field_id", "collection_id")
    DATA_FIELD_ID_FIELD_NUMBER: _ClassVar[int]
    COLLECTION_ID_FIELD_NUMBER: _ClassVar[int]
    data_field_id: str
    collection_id: str
    def __init__(self, data_field_id: _Optional[str] = ..., collection_id: _Optional[str] = ...) -> None: ...

class GiovanniPoint(_message.Message):
    __slots__ = ("data_field_id", "point")
    DATA_FIELD_ID_FIELD_NUMBER: _ClassVar[int]
    POINT_FIELD_NUMBER: _ClassVar[int]
    data_field_id: str
    point: _common_pb2.LatLon
    def __init__(self, data_field_id: _Optional[str] = ..., point: _Optional[_Union[_common_pb2.LatLon, _Mapping]] = ...) -> None: ...

class Power(_message.Message):
    __slots__ = ("parameters", "community", "point")
    PARAMETERS_FIELD_NUMBER: _ClassVar[int]
    COMMUNITY_FIELD_NUMBER: _ClassVar[int]
    POINT_FIELD_NUMBER: _ClassVar[int]
    parameters: _containers.RepeatedScalarFieldContainer[str]
    community: str
    point: _common_pb2.LatLon
    def __init__(self, parameters: _Optional[_Iterable[str]] = ..., community: _Optional[str] = ..., point: _Optional[_Union[_common_pb2.LatLon, _Mapping]] = ...) -> None: ...

class CsvUpload(_message.Message):
    __slots__ = ("upload",)
    UPLOAD_FIELD_NUMBER: _ClassVar[int]
    upload: _common_pb2.BlobRef
    def __init__(self, upload: _Optional[_Union[_common_pb2.BlobRef, _Mapping]] = ...) -> None: ...

class Bundle(_message.Message):
    __slots__ = ("name", "variables")
    NAME_FIELD_NUMBER: _ClassVar[int]
    VARIABLES_FIELD_NUMBER: _ClassVar[int]
    name: str
    variables: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, name: _Optional[str] = ..., variables: _Optional[_Iterable[str]] = ...) -> None: ...

class Appeears(_message.Message):
    __slots__ = ("product", "layers")
    PRODUCT_FIELD_NUMBER: _ClassVar[int]
    LAYERS_FIELD_NUMBER: _ClassVar[int]
    product: str
    layers: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, product: _Optional[str] = ..., layers: _Optional[_Iterable[str]] = ...) -> None: ...

class RawDataset(_message.Message):
    __slots__ = ("source", "format", "variable_id", "variables", "bbox", "point", "dates", "files", "fetched_at", "step")
    SOURCE_FIELD_NUMBER: _ClassVar[int]
    FORMAT_FIELD_NUMBER: _ClassVar[int]
    VARIABLE_ID_FIELD_NUMBER: _ClassVar[int]
    VARIABLES_FIELD_NUMBER: _ClassVar[int]
    BBOX_FIELD_NUMBER: _ClassVar[int]
    POINT_FIELD_NUMBER: _ClassVar[int]
    DATES_FIELD_NUMBER: _ClassVar[int]
    FILES_FIELD_NUMBER: _ClassVar[int]
    FETCHED_AT_FIELD_NUMBER: _ClassVar[int]
    STEP_FIELD_NUMBER: _ClassVar[int]
    source: Source
    format: Format
    variable_id: str
    variables: _containers.RepeatedScalarFieldContainer[str]
    bbox: _common_pb2.BBox
    point: _common_pb2.LatLon
    dates: _common_pb2.DateRange
    files: _containers.RepeatedCompositeFieldContainer[RawFile]
    fetched_at: _timestamp_pb2.Timestamp
    step: _common_pb2.TimeStep
    def __init__(self, source: _Optional[_Union[Source, str]] = ..., format: _Optional[_Union[Format, str]] = ..., variable_id: _Optional[str] = ..., variables: _Optional[_Iterable[str]] = ..., bbox: _Optional[_Union[_common_pb2.BBox, _Mapping]] = ..., point: _Optional[_Union[_common_pb2.LatLon, _Mapping]] = ..., dates: _Optional[_Union[_common_pb2.DateRange, _Mapping]] = ..., files: _Optional[_Iterable[_Union[RawFile, _Mapping]]] = ..., fetched_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., step: _Optional[_Union[_common_pb2.TimeStep, str]] = ...) -> None: ...

class RawFile(_message.Message):
    __slots__ = ("blob", "coverage")
    BLOB_FIELD_NUMBER: _ClassVar[int]
    COVERAGE_FIELD_NUMBER: _ClassVar[int]
    blob: _common_pb2.BlobRef
    coverage: _common_pb2.DateRange
    def __init__(self, blob: _Optional[_Union[_common_pb2.BlobRef, _Mapping]] = ..., coverage: _Optional[_Union[_common_pb2.DateRange, _Mapping]] = ...) -> None: ...
