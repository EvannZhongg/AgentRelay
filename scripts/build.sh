#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
output=${1:-dist}
for os in windows darwin linux; do
  for arch in amd64 arm64; do
    for component in DevFreight DevFreight-mcp DevFreight-worker; do
      name=$component
      flags='-s -w'
      if [ "$os" = windows ]; then
        name="$name.exe"
        if [ "$component" = DevFreight-mcp ]; then flags="$flags -H=windowsgui"; fi
      fi
      CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "$flags" -o "$output/$os-$arch/$name" "./cmd/$component"
    done
    echo "Built $os/$arch"
  done
done
