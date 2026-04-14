BINARY=s3gw
BUILD_DIR=bin
GO=go
GOFLAGS=-trimpath
LDFLAGS=-s -w

.PHONY: all build clean install uninstall

all: build

build:
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/s3gw

clean:
	rm -rf $(BUILD_DIR)

install: build
	install -d /etc/s3gw
	install -m 755 $(BUILD_DIR)/$(BINARY) /usr/local/bin/$(BINARY)
	install -m 644 configs/s3gw.example.yaml /etc/s3gw/config.yaml.example
	install -m 644 init/s3gw.service /etc/systemd/system/s3gw.service

uninstall:
	rm -f /usr/local/bin/$(BINARY)
	rm -f /etc/systemd/system/s3gw.service
	systemctl daemon-reload
