BINARY := outpost
PREFIX := .

.PHONY: build test vet fmt potato web clean

build:
	go build -o $(BINARY) ./cmd/outpost

# The Le Potato is linux/arm64. Pure-Go SQLite means no cgo and no toolchain
# gymnastics: this compiles on the Mac and runs on the box.
potato:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o $(BINARY)-arm64 ./cmd/outpost

# The built frontend is committed (web/build), so `go build` alone produces a
# complete binary and the Le Potato needs no Node. Run this after changing
# anything under web/src, then commit web/build with it.
web:
	cd web && pnpm install --frozen-lockfile && pnpm build

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -f $(BINARY) $(BINARY)-arm64
