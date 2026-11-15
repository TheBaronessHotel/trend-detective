.PHONY: up down logs ps clean smoke smoke-harmony smoke-power smoke-giovanni smoke-appeears warm-examples test test-preprocess test-analysis gen lint fmt breaking web-dev web-check

# Runs on Linux, macOS and Windows.
#
# On Windows, install GNU make (for example `winget install ezwinports.make`)
# and Git for Windows, then run make from PowerShell, cmd or Git Bash. Recipes
# run in cmd.exe, so they stick to commands that mean the same in cmd and sh:
# no single quotes, no `$$(...)`, no `[ ]`, no `>/dev/null`. Anything that
# needs a real shell lives in a script under deploy/ and runs through $(SH),
# which is Git for Windows' bash there and sh everywhere else.
ifeq ($(OS),Windows_NT)
  SHELL := cmd.exe
  # Git's install folder, from where its helpers live, like
  # D:/Program Files/Git/mingw64/libexec/git-core. Set GIT_BASH to override.
  GIT_EXEC := $(shell git --exec-path 2>NUL)
  GIT_ROOT := $(subst /mingw64/libexec/git-core,,$(subst /mingw32/libexec/git-core,,$(subst /clangarm64/libexec/git-core,,$(GIT_EXEC))))
  GIT_BASH ?= $(GIT_ROOT)/bin/bash.exe
  ifneq ($(shell if exist "$(GIT_BASH)" echo yes),yes)
    $(error Git for Windows' bash wasn't found at "$(GIT_BASH)". Install Git for Windows, or run make with GIT_BASH=<path to bash.exe>)
  endif
  SH := "$(GIT_BASH)"
  COPY := copy /Y
else
  SH := sh
  COPY := cp
endif

# Start the stack in the background and wait until bootstrap has finished.
# The first run creates .env from .env.example.
up: .env
	$(SH) deploy/up.sh

# Stop the stack. Data stays in the Docker volumes.
down:
	docker compose down

# Follow the logs of every container.
logs:
	docker compose logs -f --tail=100

# List the containers, including bootstrap after it exits.
ps:
	docker compose ps -a

# Stop the stack and delete its data: streams, job state and every bucket.
clean:
	docker compose down -v

# End-to-end checks against the running stack. Run `make up` first.
smoke:
	$(SH) deploy/smoke/ingest.sh
	$(SH) deploy/smoke/gateway.sh
	$(SH) deploy/smoke/preprocess.sh
	$(SH) deploy/smoke/analysis.sh

# Live check against real NASA services. Needs EARTHDATA_TOKEN in .env.
smoke-harmony:
	$(SH) deploy/smoke/harmony.sh

# Live check against NASA POWER. No token needed, but CI skips it.
smoke-power:
	$(SH) deploy/smoke/power.sh

# Live check against Giovanni's time-series API. Needs EARTHDATA_TOKEN in .env.
smoke-giovanni:
	$(SH) deploy/smoke/giovanni.sh

# Live check against NASA AppEEARS. Needs EARTHDATA_USERNAME and
# EARTHDATA_PASSWORD in .env, and can take 5 to 15 minutes.
smoke-appeears:
	$(SH) deploy/smoke/appeears.sh

# Run every "Try an example" job once, so a demo doesn't wait on NASA. Run
# `make up` first. Needs EARTHDATA_TOKEN, and EARTHDATA_USERNAME and
# EARTHDATA_PASSWORD for the AppEEARS one, in .env. New ones can take a while.
warm-examples:
	$(SH) deploy/warm-examples.sh

# The web app with hot reload on http://localhost:5173, against the running
# stack's gateway. Needs Bun (https://bun.sh).
web-dev:
	bun install
	cd services/web && bun run dev

# The web app's type check, unit tests and production build.
web-check:
	bun install --frozen-lockfile
	cd services/web && bun run typecheck && bun test && bun run build

# Go unit tests.
test:
	go test ./...

# The preprocess checks CI runs: lockfile, lint and unit tests. Needs uv.
test-preprocess:
	cd services/preprocess && uv lock --check && uv run --frozen ruff check . && uv run --frozen pytest -q

# The analysis checks CI runs: lockfile, lint and unit tests. Needs uv.
test-analysis:
	cd services/analysis && uv lock --check && uv run --frozen ruff check . && uv run --frozen pytest -q

.env:
	$(COPY) .env.example .env
	@echo Created .env from .env.example. Set EARTHDATA_TOKEN in it before fetching NASA data.

# Regenerate gen/ from proto/. Commit the result together with the proto change.
gen:
	buf generate

# Lint and format-check the protos, the same way CI does.
lint:
	buf lint
	buf format --diff --exit-code

# Rewrite the protos in buf's standard format.
fmt:
	buf format --write

# Check the protos for changes that would break clients built against main.
# Double quotes, because cmd.exe passes single quotes through as part of the
# argument.
breaking:
	buf breaking --against ".git#branch=main"
