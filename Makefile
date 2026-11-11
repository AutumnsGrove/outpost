BINARY := outpost
PREFIX := .

.PHONY: build test vet fmt potato clean

build:
	go build -o $(BINARY) ./cmd/outpost

# The Le Potato is linux/arm64. Pure-Go SQLite means no cgo and no toolchain
# gymnastics: this compiles on the Mac and runs on the box.
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
