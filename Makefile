# Change these variables as necessary.
main_package_path  = ./cmd/coder
binary_name        = coder
shell_package_path = ./cmd/coder-shell
shell_name         = coder-shell.exe
addr               = 127.0.0.1:8090
llm                = http://127.0.0.1:8080/v1

# The accent colour loom's generated theme is built around; baked into
# loom.css by `make css`.
accent = indigo

# The compiled stylesheet, embedded into the binary and committed, so a
# fresh clone builds before it has ever run `make css`.
stylesheet = assets/static/styles.css

# The CLIs live in their own module so the app does not inherit their
# dependency trees. GOWORK=off because -modfile and workspaces exclude each
# other, and the tools do not need the workspace.
tools = GOWORK=off go tool -modfile=tools/go.mod

# The Datastar bundle shuttle is pinned to, vendored so the app works offline.
datastar_version = 1.0.2

# ==================================================================================== #
# HELPERS
# ==================================================================================== #

## help: print this help message
.PHONY: help
help:
	@echo 'Usage:'
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' | sed -e 's/^/ /'

.PHONY: confirm
confirm:
	@echo -n 'Are you sure? [y/N] ' && read ans && [ $${ans:-N} = y ]

.PHONY: no-dirty
no-dirty:
	@test -z "$(shell git status --porcelain)"

## work: create go.work over the sibling checkouts (loom, shuttle, spectacle, colibri-go)
.PHONY: work
work:
	go work init . && go work use ../loom ../shuttle ../spectacle ../colibri-go

# ==================================================================================== #
# QUALITY CONTROL
# ==================================================================================== #

## audit: run quality control checks
.PHONY: audit
audit: test
	go mod tidy -diff
	go mod verify
	test -z "$(shell $(tools) gofumpt -l .)"
	go vet ./...
	GOOS=windows go vet ./...
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o /dev/null $(shell_package_path)
	$(tools) golangci-lint run ./...
	$(tools) govulncheck ./...
	cd tools && go mod tidy -diff
	cd tools && go mod verify

## test: run all tests
.PHONY: test
test: generate
	go test -race -buildvcs ./...

## test/cover: run all tests and display coverage
.PHONY: test/cover
test/cover: generate
	go test -race -buildvcs -coverprofile=/tmp/coverage.out ./...
	go tool cover -html=/tmp/coverage.out

# ==================================================================================== #
# DEVELOPMENT
# ==================================================================================== #

## tidy: tidy modfiles and format .go files
.PHONY: tidy
tidy:
	go mod tidy -v
	cd tools && go mod tidy -v
	$(tools) gofumpt -l -w .

## generate: generate Go from the .templ views
.PHONY: generate
generate:
	$(tools) templ generate

## css: compile the stylesheet
# cmd/css writes loom.css (Tailwind import, loom's theme and structural CSS,
# an @source at loom's own directory); input.css imports that and adds this
# repository as a source.
.PHONY: css
css:
	go run github.com/pietjan/loom/cmd/css -accent $(accent) -o assets/css/loom.css
	tailwindcss -i assets/css/input.css -o $(stylesheet) --minify

## vendor/datastar: fetch the pinned Datastar bundle into assets/static
.PHONY: vendor/datastar
vendor/datastar:
	curl -fsSL -o assets/static/datastar.js https://cdn.jsdelivr.net/gh/starfederation/datastar@v$(datastar_version)/bundles/datastar.js

## build: build the daemon
.PHONY: build
build: generate css
	CGO_ENABLED=0 go build -o=bin/$(binary_name) $(main_package_path)

## run: run the daemon on $(addr) against $(llm)
.PHONY: run
run: build
	./bin/$(binary_name) serve --listen $(addr) --llm $(llm)

## run/live: run the daemon with reloading on file changes
.PHONY: run/live
run/live:
	$(tools) templ generate --watch --proxy="http://$(addr)" --open-browser=false \
		--cmd="go run $(main_package_path) serve --listen $(addr) --llm $(llm)"

# ==================================================================================== #
# OPERATIONS
# ==================================================================================== #

## build/windows: cross-compile the Windows shell and stage it on the Windows side
.PHONY: build/windows
build/windows: generate css
	bash scripts/windows-stage.sh

## push: audit, then push to the tracking branch
.PHONY: push
push: confirm audit no-dirty
	git push
