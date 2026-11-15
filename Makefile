CC=g++
CFLAGS=-I./HCNetSDKCom/incEn -Wall -O2 -std=c++11
LDFLAGS=-L./HCNetSDKCom/lib -lhcnetsdk -lpthread -ldl -Wl,-rpath,'$$ORIGIN/HCNetSDKCom/lib'

SRC=ptz_control.cpp
BIN=ptz_control

COMPOSE?=docker compose

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

# End-to-end ONVIF test with a fake Hikvision SDK (needs Docker).
test:
	./frigate-onvif/tests/run.sh

.PHONY: all clean image up down logs test
