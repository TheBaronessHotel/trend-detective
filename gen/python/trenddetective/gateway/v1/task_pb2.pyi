import datetime

from google.protobuf import timestamp_pb2 as _timestamp_pb2
from trenddetective.analysis.v1 import analysis_pb2 as _analysis_pb2
from trenddetective.ingest.v1 import ingest_pb2 as _ingest_pb2
from trenddetective.preprocess.v1 import preprocess_pb2 as _preprocess_pb2
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class Stage(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    STAGE_UNSPECIFIED: _ClassVar[Stage]
    STAGE_INGEST: _ClassVar[Stage]
    STAGE_PREPROCESS: _ClassVar[Stage]
    STAGE_ANALYSIS: _ClassVar[Stage]

class ErrorCode(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    ERROR_CODE_UNSPECIFIED: _ClassVar[ErrorCode]
    ERROR_CODE_INTERNAL: _ClassVar[ErrorCode]
    ERROR_CODE_INVALID_REQUEST: _ClassVar[ErrorCode]
    ERROR_CODE_NO_DATA: _ClassVar[ErrorCode]
    ERROR_CODE_QUOTA_EXCEEDED: _ClassVar[ErrorCode]
    ERROR_CODE_TOO_LARGE: _ClassVar[ErrorCode]
    ERROR_CODE_UNAUTHENTICATED: _ClassVar[ErrorCode]
    ERROR_CODE_UPSTREAM: _ClassVar[ErrorCode]
    ERROR_CODE_UNSUPPORTED: _ClassVar[ErrorCode]
    ERROR_CODE_RETRIES_EXHAUSTED: _ClassVar[ErrorCode]
STAGE_UNSPECIFIED: Stage
STAGE_INGEST: Stage
STAGE_PREPROCESS: Stage
STAGE_ANALYSIS: Stage
ERROR_CODE_UNSPECIFIED: ErrorCode
ERROR_CODE_INTERNAL: ErrorCode
ERROR_CODE_INVALID_REQUEST: ErrorCode
ERROR_CODE_NO_DATA: ErrorCode
ERROR_CODE_QUOTA_EXCEEDED: ErrorCode
ERROR_CODE_TOO_LARGE: ErrorCode
ERROR_CODE_UNAUTHENTICATED: ErrorCode
ERROR_CODE_UPSTREAM: ErrorCode
ERROR_CODE_UNSUPPORTED: ErrorCode
ERROR_CODE_RETRIES_EXHAUSTED: ErrorCode

class TaskMeta(_message.Message):
    __slots__ = ("job_id", "task_id", "stage")
    JOB_ID_FIELD_NUMBER: _ClassVar[int]
    TASK_ID_FIELD_NUMBER: _ClassVar[int]
    STAGE_FIELD_NUMBER: _ClassVar[int]
    job_id: str
    task_id: str
    stage: Stage
    def __init__(self, job_id: _Optional[str] = ..., task_id: _Optional[str] = ..., stage: _Optional[_Union[Stage, str]] = ...) -> None: ...

class Task(_message.Message):
    __slots__ = ("meta", "ingest", "preprocess", "analysis")
    META_FIELD_NUMBER: _ClassVar[int]
    INGEST_FIELD_NUMBER: _ClassVar[int]
    PREPROCESS_FIELD_NUMBER: _ClassVar[int]
    ANALYSIS_FIELD_NUMBER: _ClassVar[int]
    meta: TaskMeta
    ingest: _ingest_pb2.IngestTask
    preprocess: _preprocess_pb2.PreprocessTask
    analysis: _analysis_pb2.AnalysisTask
    def __init__(self, meta: _Optional[_Union[TaskMeta, _Mapping]] = ..., ingest: _Optional[_Union[_ingest_pb2.IngestTask, _Mapping]] = ..., preprocess: _Optional[_Union[_preprocess_pb2.PreprocessTask, _Mapping]] = ..., analysis: _Optional[_Union[_analysis_pb2.AnalysisTask, _Mapping]] = ...) -> None: ...

class StageDone(_message.Message):
    __slots__ = ("meta", "attempt", "cache_hit", "finished_at", "ingest", "preprocess", "analysis", "error")
    META_FIELD_NUMBER: _ClassVar[int]
    ATTEMPT_FIELD_NUMBER: _ClassVar[int]
    CACHE_HIT_FIELD_NUMBER: _ClassVar[int]
    FINISHED_AT_FIELD_NUMBER: _ClassVar[int]
    INGEST_FIELD_NUMBER: _ClassVar[int]
    PREPROCESS_FIELD_NUMBER: _ClassVar[int]
    ANALYSIS_FIELD_NUMBER: _ClassVar[int]
    ERROR_FIELD_NUMBER: _ClassVar[int]
    meta: TaskMeta
    attempt: int
    cache_hit: bool
    finished_at: _timestamp_pb2.Timestamp
    ingest: _ingest_pb2.RawDataset
    preprocess: _preprocess_pb2.PreprocessResult
    analysis: _analysis_pb2.AnalysisResult
    error: TaskError
    def __init__(self, meta: _Optional[_Union[TaskMeta, _Mapping]] = ..., attempt: _Optional[int] = ..., cache_hit: _Optional[bool] = ..., finished_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., ingest: _Optional[_Union[_ingest_pb2.RawDataset, _Mapping]] = ..., preprocess: _Optional[_Union[_preprocess_pb2.PreprocessResult, _Mapping]] = ..., analysis: _Optional[_Union[_analysis_pb2.AnalysisResult, _Mapping]] = ..., error: _Optional[_Union[TaskError, _Mapping]] = ...) -> None: ...

class TaskError(_message.Message):
    __slots__ = ("code", "retryable", "user_message", "detail")
    CODE_FIELD_NUMBER: _ClassVar[int]
    RETRYABLE_FIELD_NUMBER: _ClassVar[int]
    USER_MESSAGE_FIELD_NUMBER: _ClassVar[int]
    DETAIL_FIELD_NUMBER: _ClassVar[int]
    code: ErrorCode
    retryable: bool
    user_message: str
    detail: str
    def __init__(self, code: _Optional[_Union[ErrorCode, str]] = ..., retryable: _Optional[bool] = ..., user_message: _Optional[str] = ..., detail: _Optional[str] = ...) -> None: ...
