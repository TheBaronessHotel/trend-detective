from trenddetective.analysis.v1 import analysis_pb2 as _analysis_pb2
from trenddetective.common.v1 import common_pb2 as _common_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class Role(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    ROLE_UNSPECIFIED: _ClassVar[Role]
    ROLE_USER: _ClassVar[Role]
    ROLE_ASSISTANT: _ClassVar[Role]

class Provider(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    PROVIDER_UNSPECIFIED: _ClassVar[Provider]
    PROVIDER_OPENAI_CHAT: _ClassVar[Provider]
    PROVIDER_ANTHROPIC_MESSAGES: _ClassVar[Provider]
ROLE_UNSPECIFIED: Role
ROLE_USER: Role
ROLE_ASSISTANT: Role
PROVIDER_UNSPECIFIED: Provider
PROVIDER_OPENAI_CHAT: Provider
PROVIDER_ANTHROPIC_MESSAGES: Provider

class NarrateRequest(_message.Message):
    __slots__ = ("result", "variable", "bbox", "dates", "question", "history", "llm")
    RESULT_FIELD_NUMBER: _ClassVar[int]
    VARIABLE_FIELD_NUMBER: _ClassVar[int]
    BBOX_FIELD_NUMBER: _ClassVar[int]
    DATES_FIELD_NUMBER: _ClassVar[int]
    QUESTION_FIELD_NUMBER: _ClassVar[int]
    HISTORY_FIELD_NUMBER: _ClassVar[int]
    LLM_FIELD_NUMBER: _ClassVar[int]
    result: _analysis_pb2.AnalysisResult
    variable: VariableContext
    bbox: _common_pb2.BBox
    dates: _common_pb2.DateRange
    question: str
    history: _containers.RepeatedCompositeFieldContainer[ChatMessage]
    llm: LlmConfig
    def __init__(self, result: _Optional[_Union[_analysis_pb2.AnalysisResult, _Mapping]] = ..., variable: _Optional[_Union[VariableContext, _Mapping]] = ..., bbox: _Optional[_Union[_common_pb2.BBox, _Mapping]] = ..., dates: _Optional[_Union[_common_pb2.DateRange, _Mapping]] = ..., question: _Optional[str] = ..., history: _Optional[_Iterable[_Union[ChatMessage, _Mapping]]] = ..., llm: _Optional[_Union[LlmConfig, _Mapping]] = ...) -> None: ...

class NarrateResponse(_message.Message):
    __slots__ = ("event",)
    EVENT_FIELD_NUMBER: _ClassVar[int]
    event: NarrateEvent
    def __init__(self, event: _Optional[_Union[NarrateEvent, _Mapping]] = ...) -> None: ...

class NarrateEvent(_message.Message):
    __slots__ = ("text_delta", "done")
    TEXT_DELTA_FIELD_NUMBER: _ClassVar[int]
    DONE_FIELD_NUMBER: _ClassVar[int]
    text_delta: str
    done: NarrateDone
    def __init__(self, text_delta: _Optional[str] = ..., done: _Optional[_Union[NarrateDone, _Mapping]] = ...) -> None: ...

class NarrateDone(_message.Message):
    __slots__ = ("unverified_numbers", "model")
    UNVERIFIED_NUMBERS_FIELD_NUMBER: _ClassVar[int]
    MODEL_FIELD_NUMBER: _ClassVar[int]
    unverified_numbers: _containers.RepeatedScalarFieldContainer[str]
    model: str
    def __init__(self, unverified_numbers: _Optional[_Iterable[str]] = ..., model: _Optional[str] = ...) -> None: ...

class VariableContext(_message.Message):
    __slots__ = ("id", "name", "definition", "units", "llm_hints")
    ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    DEFINITION_FIELD_NUMBER: _ClassVar[int]
    UNITS_FIELD_NUMBER: _ClassVar[int]
    LLM_HINTS_FIELD_NUMBER: _ClassVar[int]
    id: str
    name: str
    definition: str
    units: str
    llm_hints: str
    def __init__(self, id: _Optional[str] = ..., name: _Optional[str] = ..., definition: _Optional[str] = ..., units: _Optional[str] = ..., llm_hints: _Optional[str] = ...) -> None: ...

class ChatMessage(_message.Message):
    __slots__ = ("role", "text")
    ROLE_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    role: Role
    text: str
    def __init__(self, role: _Optional[_Union[Role, str]] = ..., text: _Optional[str] = ...) -> None: ...

class LlmConfig(_message.Message):
    __slots__ = ("provider", "base_url", "model")
    PROVIDER_FIELD_NUMBER: _ClassVar[int]
    BASE_URL_FIELD_NUMBER: _ClassVar[int]
    MODEL_FIELD_NUMBER: _ClassVar[int]
    provider: Provider
    base_url: str
    model: str
    def __init__(self, provider: _Optional[_Union[Provider, str]] = ..., base_url: _Optional[str] = ..., model: _Optional[str] = ...) -> None: ...
