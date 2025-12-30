# Binary
BIN = statiko

# Build loc
BUILDLOC = build

# Install location
INSTLOC = $(GOPATH)/bin

# Build flags
ncommits = $(shell git rev-list --count HEAD)
BUILDNUM = $(shell printf '%06d' $(ncommits))
COMMITHASH = $(shell git rev-parse HEAD)
LDFLAGS = -ldflags="-X main.build=$(BUILDNUM) -X main.commit=$(COMMITHASH)"

SOURCES = $(shell find . -type f -iname "*.go")

.PHONY: statiko install clean uninstall

statiko: $(BUILDLOC)/$(BIN)

install: statiko
	install $(BUILDLOC)/$(BIN) $(INSTLOC)/$(BIN)

clean:
	rm -r $(BUILDLOC)

uninstall:
	rm $(INSTLOC)/$(BIN)

$(BUILDLOC)/$(BIN): $(SOURCES)
	go build $(LDFLAGS) -o $(BUILDLOC)/$(BIN)
