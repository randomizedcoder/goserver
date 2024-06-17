#
# github.com/network-quality/goserver/Makefile
#
MAKEFLAGS += --no-builtin-rules
.SUFFIXES:

APP             := networkqualityd
GIT_VERSION     := $(shell git describe --always --long)
COMMIT          := $(shell /usr/bin/git describe --always)
DATE            := $(shell /bin/date -u +"%Y-%m-%d-%H:%M")
PKG             := github.com/network-quality/goserver
LDFLAGS         := -ldflags "-s -w -X main.GitVersion=$(GIT_VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"
GO              ?= go

COMMON_GO_FILES := *.go go.mod go.sum

CMD_SOURCES     := $(shell find cmd -name networkqualityd.go)
DEV_TARGETS     := $(patsubst cmd/%/networkqualityd.go,%,$(CMD_SOURCES))

all: networkqualityd

ci: networkqualityd.darwin networkqualityd.windows networkqualityd.linux

test: $(APP)
	$(GO) test -cover ./...

vet:
	$(GO) vet ./...

test-race: $(APP)
	$(GO) test -race -cover ./...

lint:
	golangci-lint run

clean:
	[ -f $(DEV_TARGETS) ] && /bin/rm -f ./$(DEV_TARGETS) || true

%: CWD=$(PWD)
%: cmd/%/*.go $(COMMON_GO_FILES)
	cd cmd/$@ && CGO_ENABLED=0 $(GO) build -o $(CWD)/$@ $(LDFLAGS) .

$(APP).darwin: GOOS=darwin
$(APP).darwin:
	cd cmd/$(APP) && env GOOS=$(GOOS) CGO_ENABLED=0 $(GO) build -o $(APP).$(GOOS) $(LDFLAGS) .

$(APP).windows: GOOS=windows
$(APP).windows:
	cd cmd/$(APP) && env GOOS=$(GOOS) CGO_ENABLED=0 $(GO) build -o $(APP).$(GOOS) $(LDFLAGS) .

$(APP).linux: GOOS=linux
$(APP).linux:
	cd cmd/$(APP) && env GOOS=$(GOOS) CGO_ENABLED=0 $(GO) build -o $(APP).$(GOOS) $(LDFLAGS) .

.PHONY: all test vet test-race lint clean

docker:
	docker build --build-arg GIT_COMMIT=$(COMMIT) --build-arg DATE=$(DATE) -t rpmserver .

daveRun:
	/home/das/Downloads/goserver/networkqualityd --enable-prom --profile cpu --insecure-public-port 4081

# end