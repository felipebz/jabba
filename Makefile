SHELL := /bin/bash -o pipefail
VERSION := $(shell git describe --tags --abbrev=0)

fetch:
	go get \
	github.com/modocache/gover \
	github.com/aktau/github-release

clean:
	rm -f ./jabba
	rm -rf ./build

fmt:
	gofmt -l -s -w `find . -type f -name '*.go' -not -path "./vendor/*"`

test:
	go vet `go list ./... | grep -v /vendor/`
	SRC=`find . -type f -name '*.go' -not -path "./vendor/*"` && gofmt -l -s $$SRC | read && gofmt -l -s -d $$SRC && exit 1 || true
	go test `go list ./... | grep -v /vendor/`

test-coverage:
	go list ./... | grep -v /vendor/ | xargs -L1 -I{} sh -c 'go test -coverprofile `basename {}`.coverprofile {}' && \
	gover && \
	go tool cover -html=gover.coverprofile -o coverage.html && \
	rm *.coverprofile

build:
	go build -ldflags "-s -w -X main.version=${VERSION}"

build-release:
	mkdir -p release
	# Windows
	GOOS=windows GOARCH=amd64 go build -ldflags "-s -w -X main.version=${VERSION}" -o release/jabba-${VERSION}-windows-amd64.exe
	GOOS=windows GOARCH=arm64 go build -ldflags "-s -w -X main.version=${VERSION}" -o release/jabba-${VERSION}-windows-arm64.exe

	# Linux
	GOOS=linux GOARCH=amd64 go build -ldflags "-s -w -X main.version=${VERSION}" -o release/jabba-${VERSION}-linux-amd64
	GOOS=linux GOARCH=arm64 go build -ldflags "-s -w -X main.version=${VERSION}" -o release/jabba-${VERSION}-linux-arm64

	# macOS
	GOOS=darwin GOARCH=amd64 go build -ldflags "-s -w -X main.version=${VERSION}" -o release/jabba-${VERSION}-darwin-amd64
	GOOS=darwin GOARCH=arm64 go build -ldflags "-s -w -X main.version=${VERSION}" -o release/jabba-${VERSION}-darwin-arm64

install: build
	JABBA_MAKE_INSTALL=true JABBA_VERSION=${VERSION} sh install.sh

publish: clean build-release
	test -n "$(GITHUB_TOKEN)" # $$GITHUB_TOKEN must be set
	github-release release --user shyiko --repo jabba --tag ${VERSION} \
	--name "${VERSION}" --description "${VERSION}" && \
	github-release upload --user shyiko --repo jabba --tag ${VERSION} \
	--name "jabba-${VERSION}-windows-amd64.exe" --file release/jabba-${VERSION}-windows-amd64.exe; \
	for qualifier in darwin-amd64 linux-386 linux-amd64 linux-arm linux-arm64; do \
		github-release upload --user shyiko --repo jabba --tag ${VERSION} \
		--name "jabba-${VERSION}-$$qualifier" --file release/jabba-${VERSION}-$$qualifier; \
	done
