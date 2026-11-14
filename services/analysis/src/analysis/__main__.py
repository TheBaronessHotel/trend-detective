"""Entry point: python -m analysis."""

from __future__ import annotations

import asyncio
import logging
import signal
import sys

from . import logs
from .settings import Settings
from .storage import S3Storage
from .worker import Worker


async def _serve(worker: Worker) -> None:
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGINT, signal.SIGTERM):
        try:
            loop.add_signal_handler(sig, worker.stop)
        except (NotImplementedError, RuntimeError):
            pass  # Windows; Ctrl+C still ends the process.
    await worker.run()


def main() -> int:
    try:
        settings = Settings.from_env()
        logs.setup(settings.log_level)
    except ValueError as err:
        print(f"analysis: {err}", file=sys.stderr)
        return 2
    store = S3Storage(settings.s3_endpoint, settings.s3_access_key, settings.s3_secret_key)
    try:
        asyncio.run(_serve(Worker(settings, store)))
    except Exception:
        logging.getLogger("analysis").exception("stopped")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
