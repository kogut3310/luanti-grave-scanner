.PHONY: image test build
image:
	./scripts/build-image.sh

test:
	go test ./...

build:
	go build ./...
