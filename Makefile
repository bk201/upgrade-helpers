VALIDATOR_IMAGE ?= rancher/harvester-precheck:dev

.PHONY: build test image

build:
	go build -trimpath -ldflags "-X main.defaultValidatorImage=$(VALIDATOR_IMAGE)" -o harvester-precheck .

test:
	go test ./...

image:
	docker build --build-arg VALIDATOR_IMAGE=$(VALIDATOR_IMAGE) -t $(VALIDATOR_IMAGE) .
