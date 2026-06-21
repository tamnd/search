---
title: "Installation"
description: "Install the sx binary from Go, Homebrew, Scoop, a Linux package, or the container image, or add the search library to a Go module."
weight: 20
---

search ships as a single binary, `sx`, and as a Go library, `github.com/tamnd/search`. Pick whichever channel suits you. If you only want to use the engine from Go code, skip to [as a Go library](#as-a-go-library).

## Go

```bash
go install github.com/tamnd/search/cmd/sx@latest
```

This puts `sx` in `$(go env GOPATH)/bin`. The core is pure Go, so a `CGO_ENABLED=0` install works and cross-compiles like any other Go binary.

## Homebrew (macOS and Linux)

```bash
brew install tamnd/tap/search
```

The formula installs the prebuilt `sx` binary.

## Scoop (Windows)

```bash
scoop bucket add tamnd https://github.com/tamnd/scoop-bucket
scoop install search
```

## Linux (apt and dnf)

A signed apt and dnf repository tracks every release, so `apt upgrade` and `dnf upgrade` keep `sx` current.

```bash
# Debian, Ubuntu
curl -fsSL https://tamnd.github.io/linux-repo/gpg.key \
  | sudo gpg --dearmor -o /usr/share/keyrings/tamnd.gpg
echo "deb [signed-by=/usr/share/keyrings/tamnd.gpg] https://tamnd.github.io/linux-repo/apt stable main" \
  | sudo tee /etc/apt/sources.list.d/tamnd.list
sudo apt update && sudo apt install search

# Fedora, RHEL
sudo dnf config-manager --add-repo https://tamnd.github.io/linux-repo/dnf/tamnd.repo
sudo dnf install search
```

## Release archives and Linux packages

Every [release](https://github.com/tamnd/search/releases) attaches `tar.gz` archives (and a `.zip` for Windows) for Linux, macOS, Windows, and FreeBSD, plus `.deb`, `.rpm`, and `.apk` packages and a `checksums.txt`. Download the one for your platform, extract `sx`, and put it on your `PATH`. To install a package directly without the repo above:

```bash
# Debian, Ubuntu
sudo dpkg -i search_*_amd64.deb

# Fedora, RHEL
sudo rpm -i search-*.x86_64.rpm
```

## Container

```bash
docker run -v "$PWD:/data" ghcr.io/tamnd/search query /data/books.sx --field title go
```

The image is the bare `sx` binary, so mount the directory holding your `.sx` file and pass paths inside the container.

## Build from source

Clone the repository and build with the Makefile:

```bash
git clone https://github.com/tamnd/search
cd search
make build
```

`make build` produces the `sx` binary in the working tree. To run the full test suite first:

```bash
go test ./...
```

## As a Go library

To use the engine from Go code, add the module to your project:

```bash
go get github.com/tamnd/search
```

The packages you will reach for most are the root `search` (the `DB` lifecycle, indexing, and search), `schema` (field mappings and types), and `query` (the query tree). The [quick start](/getting-started/quick-start/) uses all three.

When you need the engine from another language, build the C shared library and its header:

```bash
go build -buildmode=c-shared -o libsearch.so ./cabi
```

Next: [the quick start](/getting-started/quick-start/).
