"""Reading and writing objects in SeaweedFS through its S3 API."""

from __future__ import annotations

from typing import Protocol

import boto3
from botocore.config import Config
from botocore.exceptions import BotoCoreError, ClientError

from trenddetective.common.v1 import common_pb2

from . import errors


class Storage(Protocol):
    def exists(self, bucket: str, key: str) -> bool: ...
    def get(self, bucket: str, key: str) -> bytes: ...
    def put(self, bucket: str, key: str, data: bytes, content_type: str) -> common_pb2.BlobRef: ...


class S3Storage:
    def __init__(self, endpoint: str, access_key: str, secret_key: str) -> None:
        self._s3 = boto3.client(
            "s3",
            endpoint_url=endpoint,
            aws_access_key_id=access_key,
            aws_secret_access_key=secret_key,
            # SeaweedFS ignores the region, but the signature needs one.
            region_name="us-east-1",
            config=Config(
                signature_version="s3v4",
                s3={"addressing_style": "path"},
                retries={"max_attempts": 3, "mode": "standard"},
                # Newer botocore adds CRC checksums SeaweedFS may not accept.
                request_checksum_calculation="when_required",
                response_checksum_validation="when_required",
            ),
        )

    def exists(self, bucket: str, key: str) -> bool:
        try:
            self._s3.head_object(Bucket=bucket, Key=key)
            return True
        except ClientError as err:
            if err.response.get("Error", {}).get("Code") in ("404", "NoSuchKey", "NotFound"):
                return False
            raise errors.storage(f"head {bucket}/{key}: {err}") from err
        except BotoCoreError as err:
            raise errors.storage(f"head {bucket}/{key}: {err}") from err

    def get(self, bucket: str, key: str) -> bytes:
        try:
            return self._s3.get_object(Bucket=bucket, Key=key)["Body"].read()
        except (ClientError, BotoCoreError) as err:
            raise errors.storage(f"get {bucket}/{key}: {err}") from err

    def put(self, bucket: str, key: str, data: bytes, content_type: str) -> common_pb2.BlobRef:
        try:
            self._s3.put_object(Bucket=bucket, Key=key, Body=data, ContentType=content_type)
        except (ClientError, BotoCoreError) as err:
            raise errors.storage(f"put {bucket}/{key}: {err}") from err
        return common_pb2.BlobRef(bucket=bucket, key=key, size_bytes=len(data), content_type=content_type)


class MemoryStorage:
    """An in-memory Storage for tests."""

    def __init__(self) -> None:
        self.objects: dict[tuple[str, str], tuple[bytes, str]] = {}
        self.puts: list[tuple[str, str]] = []

    def exists(self, bucket: str, key: str) -> bool:
        return (bucket, key) in self.objects

    def get(self, bucket: str, key: str) -> bytes:
        if (bucket, key) not in self.objects:
            raise errors.storage(f"get {bucket}/{key}: not found")
        return self.objects[(bucket, key)][0]

    def put(self, bucket: str, key: str, data: bytes, content_type: str) -> common_pb2.BlobRef:
        self.objects[(bucket, key)] = (data, content_type)
        self.puts.append((bucket, key))
        return common_pb2.BlobRef(bucket=bucket, key=key, size_bytes=len(data), content_type=content_type)
