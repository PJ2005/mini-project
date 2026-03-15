BINARY   := edgemesh-gateway
PKG      := ./cmd/gateway
PROTO    := proto/canonical.proto
PB_OUT   := internal/canonical
CONFIG   := config/config.yaml

.PHONY: build run proto clean

build:
	go build -o $(BINARY) $(PKG)

run: build
	./$(BINARY) -config $(CONFIG)

proto:
	protoc --go_out=$(PB_OUT) --go_opt=module=edgemesh/$(PB_OUT) --proto_path=. $(PROTO)

clean:
	rm -f $(BINARY) *.db
