#!/bin/sh
# Installe Hugo HUGO_VERSION (archive officielle, somme sha256 vérifiée) et l'ajoute au PATH du job.
set -eu

version=${HUGO_VERSION:?HUGO_VERSION manquante}
tarball="hugo_${version}_linux-amd64.tar.gz"
url="https://github.com/gohugoio/hugo/releases/download/v${version}"
dir=$(mktemp -d)
curl -sSfL --retry 3 -o "$dir/$tarball" "$url/$tarball"
curl -sSfL --retry 3 -o "$dir/checksums.txt" "$url/hugo_${version}_checksums.txt"
(cd "$dir" && grep " $tarball\$" checksums.txt | sha256sum -c -)
mkdir -p "$RUNNER_TEMP/hugo"
tar xzf "$dir/$tarball" -C "$RUNNER_TEMP/hugo" hugo
echo "$RUNNER_TEMP/hugo" >>"$GITHUB_PATH"
