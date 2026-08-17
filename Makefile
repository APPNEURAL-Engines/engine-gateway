.PHONY: build run test vet docker

build:
	go build -o bin/gateway ./cmd/gateway

run: build
	./bin/gateway

test:
	go test ./... -cover

vet:
	go vet ./...

docker:
	docker build -f deploy/Dockerfile -t appneural-engines/engine-gateway .
