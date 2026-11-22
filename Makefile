VERSION := $(shell tr -d '[:space:]' < VERSION)
LDFLAGS := -s -w -X main.version=$(VERSION)
export CGO_ENABLED := 0

.PHONY: build run test vet fmt check dist clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/nixtui .

run:
	go run . $(ARGS)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

# What CI runs.
check: vet test
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "gofmt needed" && exit 1)

# Release binaries, same names as the GitHub Release.
dist:
	mkdir -p dist
	GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/nixtui-linux-x86_64 .
	GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/nixtui-linux-aarch64 .
	cd dist && sha256sum nixtui-linux-x86_64 nixtui-linux-aarch64 > sha256sums.txt

clean:
	rm -rf bin dist
