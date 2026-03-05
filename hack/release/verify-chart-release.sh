#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/../.." && pwd)"
chart_yaml="${repo_root}/charts/redis-operator/Chart.yaml"
values_yaml="${repo_root}/charts/redis-operator/values.yaml"

if [ ! -f "$chart_yaml" ] || [ ! -f "$values_yaml" ]; then
  echo "Error: chart files are missing." >&2
  exit 1
fi

current_chart_version="$(awk '$1 == "version:" { print $2; exit }' "$chart_yaml")"
current_app_version="$(awk -F': *' '$1 == "appVersion" { gsub(/"/, "", $2); print $2; exit }' "$chart_yaml")"
current_image_tag="$({
  awk '
  BEGIN { in_image = 0 }
  $0 ~ /^image:[[:space:]]*$/ { in_image = 1; next }
  in_image && $0 ~ /^[^[:space:]]/ { in_image = 0 }
  in_image && $0 ~ /^[[:space:]]{2}tag:[[:space:]]*/ {
    sub(/^[[:space:]]{2}tag:[[:space:]]*/, "")
    print
    exit
  }
  ' "$values_yaml"
})"

if [[ ! "$current_chart_version" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
  echo "Error: invalid chart version '$current_chart_version'. Expected stable semver like 0.2.0." >&2
  exit 1
fi

operator_tag="v${current_chart_version}"

if [ "$current_app_version" != "$operator_tag" ]; then
  echo "Error: Chart.yaml appVersion '$current_app_version' must be '$operator_tag'." >&2
  exit 1
fi

if [ "$current_image_tag" != "$operator_tag" ]; then
  echo "Error: values.yaml image.tag '$current_image_tag' must be '$operator_tag'." >&2
  exit 1
fi

if ! git rev-parse -q --verify "refs/tags/${operator_tag}" >/dev/null; then
  echo "Error: operator tag '${operator_tag}' does not exist. chart release is only allowed after operator release." >&2
  exit 1
fi

echo "Chart release metadata verification passed for chart version ${current_chart_version}."
