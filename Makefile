BINARY := outpost
PREFIX := .

.PHONY: build test vet fmt potato web web-check clean

build:
	go build -o $(BINARY) ./cmd/outpost

# The Le Potato is linux/arm64. Pure-Go SQLite means no cgo and no toolchain
# gymnastics: this compiles on the Mac and runs on the box.
potato:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o $(BINARY)-arm64 ./cmd/outpost

# The built frontend is committed (web/build), so `go build` alone produces a
# complete binary and the Le Potato needs no Node. Run this after changing
# anything under web/src, then commit web/build with it.
#
# The build is reproducible — two runs on one tree produce byte-identical output,
# because `version.name` is a hash of web/src rather than a timestamp — so the
# CI job in .github/workflows/web.yml can rebuild and diff. `make web-check`
# runs that check locally.
web:
	cd web && pnpm install --frozen-lockfile && pnpm build

web-check:
	$(MAKE) web
	@if [ -n "$$(git status --porcelain -- web/build)" ]; then \
		git status --porcelain -- web/build; \
		echo; \
		echo "web/build is out of date: run \`make web\` and commit it with the source change."; \
		exit 1; \
	fi

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -f $(BINARY) $(BINARY)-arm64
