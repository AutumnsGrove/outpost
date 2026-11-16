BINARY := outpost
PREFIX := .

.PHONY: build web test vet fmt potato clean

# web/build is not committed (PLAN 38). The image build runs `pnpm build` itself
# and copies the result into the Go build, so nothing here has to be kept in
# sync by hand. Locally it exists for one reason: `go:embed all:build` matches
# nothing when the directory is absent, so a cold clone must run `make web`
# before `go build` or `go test`. CI does the same before its Go steps.
#
# The build is reproducible — two runs on one tree produce byte-identical output,
# because `version.name` is a hash of web/src rather than a timestamp — which is
# what .github/workflows/web.yml's deterministic job checks.
web:
	cd web && pnpm install --frozen-lockfile && pnpm build

build:
	go build -o $(BINARY) ./cmd/outpost

# The Le Potato is linux/arm64. Pure-Go SQLite means no cgo and no toolchain
# gymnastics: this compiles on the Mac and runs on the box. It is no longer the
# deploy — the box runs the published image — but it is still how a one-off
# binary gets built for debugging on the host.
potato:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o $(BINARY)-arm64 ./cmd/outpost

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -f $(BINARY) $(BINARY)-arm64
