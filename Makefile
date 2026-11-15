CC=g++
CFLAGS=-I./HCNetSDKCom/incEn -Wall -O2 -std=c++11
LDFLAGS=-L./HCNetSDKCom/lib -lhcnetsdk -lpthread -ldl -Wl,-rpath,'$$ORIGIN/HCNetSDKCom/lib'

SRC=ptz_control.cpp
BIN=ptz_control

COMPOSE?=docker compose
GO?=go
GO_DIR=frigate-onvif-go

all: $(BIN)

$(BIN): $(SRC)
	$(CC) $(CFLAGS) -o $@ $^ $(LDFLAGS)

clean:
	rm -f $(BIN)

# --- Frigate / ONVIF bridge (see docker-compose.yml and .env.example) --------
image:
	$(COMPOSE) build

up:
	$(COMPOSE) up -d --build

down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f --tail=100

# End-to-end ONVIF test with a fake Hikvision SDK (needs Docker and builds the
# linux/amd64 Go bridge image).
test:
	./frigate-onvif/tests/run.sh

# Go bridge (needs a Go toolchain; CI and the image build use these too).
go-build:
	cd $(GO_DIR) && $(GO) build ./...

go-test:
	cd $(GO_DIR) && $(GO) test ./...

go-vet:
	cd $(GO_DIR) && $(GO) vet ./...

go-fmt-check:
	cd $(GO_DIR) && test -z "$$(gofmt -l .)"

.PHONY: all clean image up down logs test go-build go-test go-vet go-fmt-check
