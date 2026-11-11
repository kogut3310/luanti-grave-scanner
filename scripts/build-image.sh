#!/bin/sh
set -eu
project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
version=$(tr -d '\r\n' < "$project_dir/VERSION")
case "$version" in
  ''|*[!a-zA-Z0-9_.-]*) echo "Nieprawidłowa wersja w VERSION" >&2; exit 1 ;;
esac
image_name=${IMAGE_NAME:-luanti-grave-scanner}
exec docker build --build-arg "APP_VERSION=$version" --tag "$image_name:$version" "$@" "$project_dir"
