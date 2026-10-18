#!/bin/sh
# Runs the go tests, and the integration tests on real file systems, in a
# privileged container (the tests mount loop devices): sh test/run.sh [go test args]
# The first run builds the image with the tools.
set -e
cd "$(dirname "$0")/.."
docker build -q -t bootmanager-it test >/dev/null
exec docker run --rm --privileged \
	-v "$PWD/..":/src -v "${GOMODCACHE:-$(go env GOMODCACHE)}":/go/pkg/mod \
	-w /src/bootmanager bootmanager-it sh -c '
		set -e
		M=$(mktemp -d)
		bash test/fixtures.sh "$M"
		E=$(mktemp -d)
			bash test/edge.sh "$E" "$M"
			INTEGRATION_MEDIA=$M INTEGRATION_EDGE=$E go test "$@" ./...
	' sh "$@"
