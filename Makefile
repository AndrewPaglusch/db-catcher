S3_TEST_PORT ?= 18333

.PHONY: build test test-integration docker

build:
	CGO_ENABLED=0 go build -o db-catcher .

test:
	go vet ./...
	go test -race ./...

# Runs the integration test against a throwaway SeaweedFS S3 server.
test-integration:
	docker run -d --rm --name db-catcher-s3 -p $(S3_TEST_PORT):8333 chrislusf/seaweedfs server -s3
	until curl -s -o /dev/null http://127.0.0.1:$(S3_TEST_PORT)/; do sleep 1; done
	DBCATCHER_TEST_S3_ENDPOINT=http://127.0.0.1:$(S3_TEST_PORT) go test -race -count=1 -run Integration -v ./...; \
		status=$$?; docker stop db-catcher-s3; exit $$status

docker:
	docker build -t db-catcher .
