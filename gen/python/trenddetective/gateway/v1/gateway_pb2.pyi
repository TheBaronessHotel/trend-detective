import datetime

from google.protobuf import timestamp_pb2 as _timestamp_pb2
from trenddetective.analysis.v1 import analysis_pb2 as _analysis_pb2
from trenddetective.common.v1 import common_pb2 as _common_pb2
from trenddetective.gateway.v1 import task_pb2 as _task_pb2
from trenddetective.ingest.v1 import ingest_pb2 as _ingest_pb2
from trenddetective.narrator.v1 import narrator_pb2 as _narrator_pb2
from trenddetective.preprocess.v1 import preprocess_pb2 as _preprocess_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class Domain(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    DOMAIN_UNSPECIFIED: _ClassVar[Domain]
    DOMAIN_LAND: _ClassVar[Domain]
    DOMAIN_OCEAN: _ClassVar[Domain]
    DOMAIN_GLOBAL: _ClassVar[Domain]

class JobState(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    JOB_STATE_UNSPECIFIED: _ClassVar[JobState]
    JOB_STATE_PENDING: _ClassVar[JobState]
    JOB_STATE_RUNNING: _ClassVar[JobState]
    JOB_STATE_SUCCEEDED: _ClassVar[JobState]
    JOB_STATE_FAILED: _ClassVar[JobState]
    JOB_STATE_CANCELED: _ClassVar[JobState]

class TaskState(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    TASK_STATE_UNSPECIFIED: _ClassVar[TaskState]
    TASK_STATE_QUEUED: _ClassVar[TaskState]
    TASK_STATE_RUNNING: _ClassVar[TaskState]
    TASK_STATE_SUCCEEDED: _ClassVar[TaskState]
    TASK_STATE_FAILED: _ClassVar[TaskState]
    TASK_STATE_CANCELED: _ClassVar[TaskState]

class Artifact(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    ARTIFACT_UNSPECIFIED: _ClassVar[Artifact]
    ARTIFACT_SERIES: _ClassVar[Artifact]
    ARTIFACT_SERIES_CSV: _ClassVar[Artifact]
    ARTIFACT_VALUE_CUBE: _ClassVar[Artifact]
    ARTIFACT_ANOMALY_CUBE: _ClassVar[Artifact]
    ARTIFACT_SERIES_ANALYSIS: _ClassVar[Artifact]
    ARTIFACT_GRID_ANALYSIS: _ClassVar[Artifact]
    ARTIFACT_GRID: _ClassVar[Artifact]
DOMAIN_UNSPECIFIED: Domain
DOMAIN_LAND: Domain
DOMAIN_OCEAN: Domain
DOMAIN_GLOBAL: Domain
JOB_STATE_UNSPECIFIED: JobState
JOB_STATE_PENDING: JobState
JOB_STATE_RUNNING: JobState
JOB_STATE_SUCCEEDED: JobState
JOB_STATE_FAILED: JobState
JOB_STATE_CANCELED: JobState
TASK_STATE_UNSPECIFIED: TaskState
TASK_STATE_QUEUED: TaskState
TASK_STATE_RUNNING: TaskState
TASK_STATE_SUCCEEDED: TaskState
TASK_STATE_FAILED: TaskState
TASK_STATE_CANCELED: TaskState
ARTIFACT_UNSPECIFIED: Artifact
ARTIFACT_SERIES: Artifact
ARTIFACT_SERIES_CSV: Artifact
ARTIFACT_VALUE_CUBE: Artifact
ARTIFACT_ANOMALY_CUBE: Artifact
ARTIFACT_SERIES_ANALYSIS: Artifact
ARTIFACT_GRID_ANALYSIS: Artifact
ARTIFACT_GRID: Artifact

class ListVariablesRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class ListVariablesResponse(_message.Message):
    __slots__ = ("variables",)
    VARIABLES_FIELD_NUMBER: _ClassVar[int]
    variables: _containers.RepeatedCompositeFieldContainer[Variable]
    def __init__(self, variables: _Optional[_Iterable[_Union[Variable, _Mapping]]] = ...) -> None: ...

class Variable(_message.Message):
    __slots__ = ("id", "name", "definition", "units", "step", "available", "grid", "domain", "default_source", "sources", "dataset_url", "pixel_lat_deg", "pixel_lon_deg", "default_block_pixels")
    ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    DEFINITION_FIELD_NUMBER: _ClassVar[int]
    UNITS_FIELD_NUMBER: _ClassVar[int]
    STEP_FIELD_NUMBER: _ClassVar[int]
    AVAILABLE_FIELD_NUMBER: _ClassVar[int]
    GRID_FIELD_NUMBER: _ClassVar[int]
    DOMAIN_FIELD_NUMBER: _ClassVar[int]
    DEFAULT_SOURCE_FIELD_NUMBER: _ClassVar[int]
    SOURCES_FIELD_NUMBER: _ClassVar[int]
    DATASET_URL_FIELD_NUMBER: _ClassVar[int]
    PIXEL_LAT_DEG_FIELD_NUMBER: _ClassVar[int]
    PIXEL_LON_DEG_FIELD_NUMBER: _ClassVar[int]
    DEFAULT_BLOCK_PIXELS_FIELD_NUMBER: _ClassVar[int]
    id: str
    name: str
    definition: str
    units: str
    step: _common_pb2.TimeStep
    available: _common_pb2.DateRange
    grid: bool
    domain: Domain
    default_source: _ingest_pb2.Source
    sources: _containers.RepeatedScalarFieldContainer[_ingest_pb2.Source]
    dataset_url: str
    pixel_lat_deg: float
    pixel_lon_deg: float
    default_block_pixels: int
    def __init__(self, id: _Optional[str] = ..., name: _Optional[str] = ..., definition: _Optional[str] = ..., units: _Optional[str] = ..., step: _Optional[_Union[_common_pb2.TimeStep, str]] = ..., available: _Optional[_Union[_common_pb2.DateRange, _Mapping]] = ..., grid: _Optional[bool] = ..., domain: _Optional[_Union[Domain, str]] = ..., default_source: _Optional[_Union[_ingest_pb2.Source, str]] = ..., sources: _Optional[_Iterable[_Union[_ingest_pb2.Source, str]]] = ..., dataset_url: _Optional[str] = ..., pixel_lat_deg: _Optional[float] = ..., pixel_lon_deg: _Optional[float] = ..., default_block_pixels: _Optional[int] = ...) -> None: ...

class CreateJobRequest(_message.Message):
    __slots__ = ("spec",)
    SPEC_FIELD_NUMBER: _ClassVar[int]
    spec: JobSpec
    def __init__(self, spec: _Optional[_Union[JobSpec, _Mapping]] = ...) -> None: ...

class CreateJobResponse(_message.Message):
    __slots__ = ("job",)
    JOB_FIELD_NUMBER: _ClassVar[int]
    job: Job
    def __init__(self, job: _Optional[_Union[Job, _Mapping]] = ...) -> None: ...

class JobSpec(_message.Message):
    __slots__ = ("variable_id", "bbox", "dates", "source", "stop_after", "upload", "filters", "aggregation", "tests")
    VARIABLE_ID_FIELD_NUMBER: _ClassVar[int]
    BBOX_FIELD_NUMBER: _ClassVar[int]
    DATES_FIELD_NUMBER: _ClassVar[int]
    SOURCE_FIELD_NUMBER: _ClassVar[int]
    STOP_AFTER_FIELD_NUMBER: _ClassVar[int]
    UPLOAD_FIELD_NUMBER: _ClassVar[int]
    FILTERS_FIELD_NUMBER: _ClassVar[int]
    AGGREGATION_FIELD_NUMBER: _ClassVar[int]
    TESTS_FIELD_NUMBER: _ClassVar[int]
    variable_id: str
    bbox: _common_pb2.BBox
    dates: _common_pb2.DateRange
    source: _ingest_pb2.Source
    stop_after: _task_pb2.Stage
    upload: _common_pb2.BlobRef
    filters: _preprocess_pb2.FilterChain
    aggregation: _preprocess_pb2.Aggregation
    tests: _analysis_pb2.TestPlan
    def __init__(self, variable_id: _Optional[str] = ..., bbox: _Optional[_Union[_common_pb2.BBox, _Mapping]] = ..., dates: _Optional[_Union[_common_pb2.DateRange, _Mapping]] = ..., source: _Optional[_Union[_ingest_pb2.Source, str]] = ..., stop_after: _Optional[_Union[_task_pb2.Stage, str]] = ..., upload: _Optional[_Union[_common_pb2.BlobRef, _Mapping]] = ..., filters: _Optional[_Union[_preprocess_pb2.FilterChain, _Mapping]] = ..., aggregation: _Optional[_Union[_preprocess_pb2.Aggregation, _Mapping]] = ..., tests: _Optional[_Union[_analysis_pb2.TestPlan, _Mapping]] = ...) -> None: ...

class Job(_message.Message):
    __slots__ = ("id", "state", "spec", "tasks", "tasks_total", "tasks_succeeded", "tasks_failed", "results", "created_at", "updated_at")
    ID_FIELD_NUMBER: _ClassVar[int]
    STATE_FIELD_NUMBER: _ClassVar[int]
    SPEC_FIELD_NUMBER: _ClassVar[int]
    TASKS_FIELD_NUMBER: _ClassVar[int]
    TASKS_TOTAL_FIELD_NUMBER: _ClassVar[int]
    TASKS_SUCCEEDED_FIELD_NUMBER: _ClassVar[int]
    TASKS_FAILED_FIELD_NUMBER: _ClassVar[int]
    RESULTS_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_FIELD_NUMBER: _ClassVar[int]
    UPDATED_AT_FIELD_NUMBER: _ClassVar[int]
    id: str
    state: JobState
    spec: JobSpec
    tasks: _containers.RepeatedCompositeFieldContainer[TaskStatus]
    tasks_total: int
    tasks_succeeded: int
    tasks_failed: int
    results: JobResults
    created_at: _timestamp_pb2.Timestamp
    updated_at: _timestamp_pb2.Timestamp
    def __init__(self, id: _Optional[str] = ..., state: _Optional[_Union[JobState, str]] = ..., spec: _Optional[_Union[JobSpec, _Mapping]] = ..., tasks: _Optional[_Iterable[_Union[TaskStatus, _Mapping]]] = ..., tasks_total: _Optional[int] = ..., tasks_succeeded: _Optional[int] = ..., tasks_failed: _Optional[int] = ..., results: _Optional[_Union[JobResults, _Mapping]] = ..., created_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., updated_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class TaskStatus(_message.Message):
    __slots__ = ("meta", "state", "attempts", "cache_hit", "error", "started_at", "finished_at", "percent", "activity")
    META_FIELD_NUMBER: _ClassVar[int]
    STATE_FIELD_NUMBER: _ClassVar[int]
    ATTEMPTS_FIELD_NUMBER: _ClassVar[int]
    CACHE_HIT_FIELD_NUMBER: _ClassVar[int]
    ERROR_FIELD_NUMBER: _ClassVar[int]
    STARTED_AT_FIELD_NUMBER: _ClassVar[int]
    FINISHED_AT_FIELD_NUMBER: _ClassVar[int]
    PERCENT_FIELD_NUMBER: _ClassVar[int]
    ACTIVITY_FIELD_NUMBER: _ClassVar[int]
    meta: _task_pb2.TaskMeta
    state: TaskState
    attempts: int
    cache_hit: bool
    error: _task_pb2.TaskError
    started_at: _timestamp_pb2.Timestamp
    finished_at: _timestamp_pb2.Timestamp
    percent: int
    activity: str
    def __init__(self, meta: _Optional[_Union[_task_pb2.TaskMeta, _Mapping]] = ..., state: _Optional[_Union[TaskState, str]] = ..., attempts: _Optional[int] = ..., cache_hit: _Optional[bool] = ..., error: _Optional[_Union[_task_pb2.TaskError, _Mapping]] = ..., started_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., finished_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., percent: _Optional[int] = ..., activity: _Optional[str] = ...) -> None: ...

class JobResults(_message.Message):
    __slots__ = ("raw", "preprocess", "analysis")
    RAW_FIELD_NUMBER: _ClassVar[int]
    PREPROCESS_FIELD_NUMBER: _ClassVar[int]
    ANALYSIS_FIELD_NUMBER: _ClassVar[int]
    raw: _containers.RepeatedCompositeFieldContainer[_ingest_pb2.RawDataset]
    preprocess: _preprocess_pb2.PreprocessResult
    analysis: _analysis_pb2.AnalysisResult
    def __init__(self, raw: _Optional[_Iterable[_Union[_ingest_pb2.RawDataset, _Mapping]]] = ..., preprocess: _Optional[_Union[_preprocess_pb2.PreprocessResult, _Mapping]] = ..., analysis: _Optional[_Union[_analysis_pb2.AnalysisResult, _Mapping]] = ...) -> None: ...

class GetJobRequest(_message.Message):
    __slots__ = ("job_id",)
    JOB_ID_FIELD_NUMBER: _ClassVar[int]
    job_id: str
    def __init__(self, job_id: _Optional[str] = ...) -> None: ...

class GetJobResponse(_message.Message):
    __slots__ = ("job",)
    JOB_FIELD_NUMBER: _ClassVar[int]
    job: Job
    def __init__(self, job: _Optional[_Union[Job, _Mapping]] = ...) -> None: ...

class CancelJobRequest(_message.Message):
    __slots__ = ("job_id",)
    JOB_ID_FIELD_NUMBER: _ClassVar[int]
    job_id: str
    def __init__(self, job_id: _Optional[str] = ...) -> None: ...

class CancelJobResponse(_message.Message):
    __slots__ = ("job",)
    JOB_FIELD_NUMBER: _ClassVar[int]
    job: Job
    def __init__(self, job: _Optional[_Union[Job, _Mapping]] = ...) -> None: ...

class WatchJobRequest(_message.Message):
    __slots__ = ("job_id",)
    JOB_ID_FIELD_NUMBER: _ClassVar[int]
    job_id: str
    def __init__(self, job_id: _Optional[str] = ...) -> None: ...

class WatchJobResponse(_message.Message):
    __slots__ = ("job",)
    JOB_FIELD_NUMBER: _ClassVar[int]
    job: Job
    def __init__(self, job: _Optional[_Union[Job, _Mapping]] = ...) -> None: ...

class GetResultURLRequest(_message.Message):
    __slots__ = ("job_id", "artifact")
    JOB_ID_FIELD_NUMBER: _ClassVar[int]
    ARTIFACT_FIELD_NUMBER: _ClassVar[int]
    job_id: str
    artifact: Artifact
    def __init__(self, job_id: _Optional[str] = ..., artifact: _Optional[_Union[Artifact, str]] = ...) -> None: ...

class GetResultURLResponse(_message.Message):
    __slots__ = ("url", "data_url", "expires_at")
    URL_FIELD_NUMBER: _ClassVar[int]
    DATA_URL_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    url: str
    data_url: str
    expires_at: _timestamp_pb2.Timestamp
    def __init__(self, url: _Optional[str] = ..., data_url: _Optional[str] = ..., expires_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class CreateUploadURLRequest(_message.Message):
    __slots__ = ("filename", "size_bytes")
    FILENAME_FIELD_NUMBER: _ClassVar[int]
    SIZE_BYTES_FIELD_NUMBER: _ClassVar[int]
    filename: str
    size_bytes: int
    def __init__(self, filename: _Optional[str] = ..., size_bytes: _Optional[int] = ...) -> None: ...

class CreateUploadURLResponse(_message.Message):
    __slots__ = ("url", "upload", "expires_at")
    URL_FIELD_NUMBER: _ClassVar[int]
    UPLOAD_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    url: str
    upload: _common_pb2.BlobRef
    expires_at: _timestamp_pb2.Timestamp
    def __init__(self, url: _Optional[str] = ..., upload: _Optional[_Union[_common_pb2.BlobRef, _Mapping]] = ..., expires_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class NarrateRequest(_message.Message):
    __slots__ = ("job_id", "question", "history", "llm")
    JOB_ID_FIELD_NUMBER: _ClassVar[int]
    QUESTION_FIELD_NUMBER: _ClassVar[int]
    HISTORY_FIELD_NUMBER: _ClassVar[int]
    LLM_FIELD_NUMBER: _ClassVar[int]
    job_id: str
    question: str
    history: _containers.RepeatedCompositeFieldContainer[_narrator_pb2.ChatMessage]
    llm: _narrator_pb2.LlmConfig
    def __init__(self, job_id: _Optional[str] = ..., question: _Optional[str] = ..., history: _Optional[_Iterable[_Union[_narrator_pb2.ChatMessage, _Mapping]]] = ..., llm: _Optional[_Union[_narrator_pb2.LlmConfig, _Mapping]] = ...) -> None: ...

class NarrateResponse(_message.Message):
    __slots__ = ("event",)
    EVENT_FIELD_NUMBER: _ClassVar[int]
    event: _narrator_pb2.NarrateEvent
    def __init__(self, event: _Optional[_Union[_narrator_pb2.NarrateEvent, _Mapping]] = ...) -> None: ...

class GetQuotaRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class GetQuotaResponse(_message.Message):
    __slots__ = ("giovanni_used_fraction", "giovanni_cutoff", "harmony_in_flight", "harmony_limit")
    GIOVANNI_USED_FRACTION_FIELD_NUMBER: _ClassVar[int]
    GIOVANNI_CUTOFF_FIELD_NUMBER: _ClassVar[int]
    HARMONY_IN_FLIGHT_FIELD_NUMBER: _ClassVar[int]
    HARMONY_LIMIT_FIELD_NUMBER: _ClassVar[int]
    giovanni_used_fraction: float
    giovanni_cutoff: float
    harmony_in_flight: int
    harmony_limit: int
    def __init__(self, giovanni_used_fraction: _Optional[float] = ..., giovanni_cutoff: _Optional[float] = ..., harmony_in_flight: _Optional[int] = ..., harmony_limit: _Optional[int] = ...) -> None: ...
