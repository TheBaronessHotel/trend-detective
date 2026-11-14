.PHONY: up down logs ps clean smoke smoke-harmony smoke-power smoke-giovanni test gen lint fmt breaking

# Start the stack in the background and wait until bootstrap has finished.
# The first run creates .env from .env.example. `up --wait` would treat
# bootstrap exiting as a failure, so this waits on its exit code instead.
up: .env
	docker compose up -d --build
	@docker compose wait bootstrap >/dev/null; \
	code=$$(docker compose ps -a bootstrap --format '{{.ExitCode}}'); \
	if [ "$$code" = 0 ]; then \
		echo "Stack is up. Streams and buckets are ready."; \
	else \
		echo "Bootstrap failed with exit code $$code:"; \
		docker compose logs --no-log-prefix bootstrap; \
		exit 1; \
	fi

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
	sh deploy/smoke/ingest.sh
	sh deploy/smoke/gateway.sh
	sh deploy/smoke/preprocess.sh

# Live check against real NASA services. Needs EARTHDATA_TOKEN in .env.
smoke-harmony:
	sh deploy/smoke/harmony.sh

# Live check against NASA POWER. No token needed, but CI skips it.
smoke-power:
	sh deploy/smoke/power.sh

# Live check against Giovanni's time-series API. Needs EARTHDATA_TOKEN in .env.
smoke-giovanni:
	sh deploy/smoke/giovanni.sh

# Go unit tests.
test:
	go test ./...

# Python unit tests for preprocess. Needs uv.
test-preprocess:
	cd services/preprocess && uv run --frozen pytest -q

.env:
	cp .env.example .env
	@echo "Created .env from .env.example. Set EARTHDATA_TOKEN in it before fetching NASA data."

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
breaking:
	buf breaking --against '.git#branch=main'
