#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "Usage: $0 <operator-tag>" >&2
  echo "Example: $0 v0.2.0" >&2
  exit 1
fi

operator_tag="$1"
if [[ ! "$operator_tag" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
  echo "Error: invalid operator tag '$operator_tag'. Expected stable semver tag like v0.2.0." >&2
  exit 1
fi
chart_version="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.${BASH_REMATCH[3]}"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/../.." && pwd)"
chart_yaml="${repo_root}/charts/redis-operator/Chart.yaml"
values_yaml="${repo_root}/charts/redis-operator/values.yaml"

if [ ! -f "$chart_yaml" ] || [ ! -f "$values_yaml" ]; then
  echo "Error: chart files are missing." >&2
  exit 1
fi

awk -v chart_version="$chart_version" -v app_version="$operator_tag" '
BEGIN { version_set = 0; app_set = 0 }
$1 == "version:" { print "version: " chart_version; version_set = 1; next }
$1 == "appVersion:" { print "appVersion: \"" app_version "\""; app_set = 1; next }
{ print }
END {
  if (!version_set) {
    print "Error: missing version in Chart.yaml" > "/dev/stderr"
    exit 1
  }
  if (!app_set) {
    print "Error: missing appVersion in Chart.yaml" > "/dev/stderr"
    exit 1
  }
}
' "$chart_yaml" > "${chart_yaml}.tmp"
mv "${chart_yaml}.tmp" "$chart_yaml"

awk -v app_version="$operator_tag" '
BEGIN { in_image = 0; tag_set = 0 }
$0 ~ /^image:[[:space:]]*$/ { in_image = 1; print; next }
in_image && $0 ~ /^[^[:space:]]/ { in_image = 0 }
in_image && $0 ~ /^[[:space:]]{2}tag:[[:space:]]*/ {
  print "  tag: " app_version
  tag_set = 1
  next
}
{ print }
END {
  if (!tag_set) {
    print "Error: missing image.tag in values.yaml" > "/dev/stderr"
    exit 1
  }
}
' "$values_yaml" > "${values_yaml}.tmp"
mv "${values_yaml}.tmp" "$values_yaml"

(
  cd "$repo_root"
  make helm-docs
)

echo "Updated chart metadata to ${operator_tag} (chart version ${chart_version})."
