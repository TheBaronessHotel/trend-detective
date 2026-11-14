"""Failures that end a task, mapped to the TaskError the gateway shows."""

from __future__ import annotations

from trenddetective.gateway.v1 import task_pb2


class TaskFailure(Exception):
    """A task failed with a reason the user can read.

    transient means another attempt might succeed, like an S3 hiccup. The
    worker retries those and reports anything else at once.
    """

    def __init__(
        self,
        code: int,
        user_message: str,
        detail: str = "",
        *,
        retryable: bool = False,
        transient: bool = False,
    ) -> None:
        super().__init__(detail or user_message)
        self.code = code
        self.user_message = user_message
        self.detail = detail
        self.retryable = retryable
        self.transient = transient

    def task_error(self) -> task_pb2.TaskError:
        return task_pb2.TaskError(
            code=self.code,
            retryable=self.retryable,
            user_message=self.user_message,
            detail=self.detail,
        )


def invalid(user_message: str, detail: str = "") -> TaskFailure:
    return TaskFailure(task_pb2.ERROR_CODE_INVALID_REQUEST, user_message, detail)


def no_data(user_message: str, detail: str = "") -> TaskFailure:
    return TaskFailure(task_pb2.ERROR_CODE_NO_DATA, user_message, detail)


def unreadable(user_message: str, detail: str = "") -> TaskFailure:
    """A NASA file we couldn't read. Retrying gets the same file, so it fails at once."""
    return TaskFailure(task_pb2.ERROR_CODE_UPSTREAM, user_message, detail)


def unsupported(user_message: str, detail: str = "") -> TaskFailure:
    return TaskFailure(task_pb2.ERROR_CODE_UNSUPPORTED, user_message, detail)


def storage(detail: str) -> TaskFailure:
    """SeaweedFS failed. Worth another attempt."""
    return TaskFailure(
        task_pb2.ERROR_CODE_INTERNAL,
        "Storage failed while preparing the data. Try again in a minute.",
        detail,
        retryable=True,
        transient=True,
    )


def as_failure(err: BaseException) -> TaskFailure:
    """Wraps an unexpected exception as an internal error."""
    if isinstance(err, TaskFailure):
        return err
    return TaskFailure(
        task_pb2.ERROR_CODE_INTERNAL,
        "Something went wrong while preparing the data.",
        f"{type(err).__name__}: {err}",
    )
