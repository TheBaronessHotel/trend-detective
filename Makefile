.PHONY: gen lint fmt breaking

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
