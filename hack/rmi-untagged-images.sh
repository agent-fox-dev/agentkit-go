#!/usr/bin/env bash
# Remove local Podman images that have no tag (REPOSITORY/TAG show "<none>").
set -euo pipefail

CONTAINER_TOOL="${CONTAINER_TOOL:-podman}"
DRY_RUN=false

usage() {
	cat <<EOF
Usage: $(basename "$0") [-n|--dry-run]

Remove untagged (dangling) Podman images.

Options:
  -n, --dry-run   List image IDs that would be removed without deleting them
  -h, --help      Show this help message

Environment:
  CONTAINER_TOOL  Container CLI to use (default: podman)
EOF
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	-n | --dry-run)
		DRY_RUN=true
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "Unknown option: $1" >&2
		usage >&2
		exit 1
		;;
	esac
done

image_ids=$(
	"$CONTAINER_TOOL" images --format '{{.ID}}\t{{.Tag}}' |
		awk -F'\t' '$2 == "<none>" {print $1}' |
		sort -u
)

if [[ -z "$image_ids" ]]; then
	echo "No untagged images found."
	exit 0
fi

count=$(echo "$image_ids" | wc -l | tr -d ' ')
echo "Found $count untagged image(s):"
echo "$image_ids" | sed 's/^/  /'

if [[ "$DRY_RUN" == true ]]; then
	echo "Dry run: no images removed."
	exit 0
fi

while IFS= read -r id; do
	[[ -z "$id" ]] && continue
	echo "Removing $id ..."
	"$CONTAINER_TOOL" rmi -f "$id"
done <<EOF
$image_ids
EOF

echo "Done."
