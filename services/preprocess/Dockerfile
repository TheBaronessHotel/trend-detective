# syntax=docker/dockerfile:1
# Build context: the repo root.

FROM python:3.14-slim
COPY --from=ghcr.io/astral-sh/uv:0.11.21 /uv /bin/uv
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy UV_PYTHON_DOWNLOADS=never
WORKDIR /app

# Dependencies first, so a code change doesn't reinstall them.
COPY services/preprocess/pyproject.toml services/preprocess/uv.lock ./
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-dev --no-install-project
COPY services/preprocess/src ./src
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-dev --no-editable

# The generated protobuf modules, which aren't a package of their own.
COPY gen/python ./gen
ENV PYTHONPATH=/app/gen PATH=/app/.venv/bin:$PATH PYTHONUNBUFFERED=1

USER 65532:65532
ENTRYPOINT ["python", "-m", "preprocess"]
