#!/bin/sh
set -eu

GO="${GO:-go}"
TIMEOUT="${TIMEOUT:-timeout}"
SERVER_TIMEOUT="${SERVER_TIMEOUT:-5s}"

examples="
basic
telemetry
endpoint-controls
custom-encoding
readiness
logging
cors
external-server
advanced-composition
streaming
reverse-proxy
response-capture
"

server_examples="
basic
telemetry
endpoint-controls
custom-encoding
readiness
logging
cors
external-server
advanced-composition
streaming
reverse-proxy
response-capture
"

check_no_matches() {
	pattern="$1"
	shift
	if rg -U -n "$pattern" "$@"
	then
		echo "unexpected stale docs/examples reference matched: $pattern" >&2
		exit 1
	fi
}

check_local_markdown_links() {
	find README.md docs examples -name '*.md' -print | while IFS= read -r file
	do
		sed -n 's/.*](\([^)]*\)).*/\1/p' "$file" | while IFS= read -r link
		do
			case "$link" in
				""|\#*|http://*|https://*|mailto:*)
					continue
					;;
			esac

			target="${link%%#*}"
			[ -n "$target" ] || continue

			case "$target" in
				/*)
					echo "absolute local markdown link in $file: $link" >&2
					exit 1
					;;
				*)
					base="$(dirname "$file")"
					test -e "$base/$target" || {
						echo "missing local markdown link target in $file: $link" >&2
						exit 1
					}
					;;
			esac
		done
	done
}

run_server_example() {
	example="$1"
	echo "==> go run ./examples/$example (bounded by $SERVER_TIMEOUT)"
	set +e
	"$TIMEOUT" "$SERVER_TIMEOUT" "$GO" run "./examples/$example"
	status="$?"
	set -e
	if [ "$status" -eq 124 ]
	then
		return 0
	fi
	if [ "$status" -ne 0 ]
	then
		echo "server example failed before timeout: $example" >&2
		exit "$status"
	fi
	echo "server example exited before timeout: $example" >&2
	exit 1
}

echo "==> checking required tools"
command -v rg >/dev/null 2>&1 || {
	echo "rg is required for docs/examples verification" >&2
	exit 1
}

echo "==> checking example directories"
for example in $examples
do
	test -d "examples/$example" || {
		echo "missing examples/$example" >&2
		exit 1
	}
done

echo "==> go test ./..."
"$GO" test ./...

echo "==> go build ./examples/..."
"$GO" build ./examples/...

if command -v "$TIMEOUT" >/dev/null 2>&1
then
	echo "==> server example startup checks"
	for example in $server_examples
	do
		run_server_example "$example"
	done
else
	echo "==> skipping server example startup checks; timeout command not found"
fi

echo "==> stale API and link scans"
check_local_markdown_links
check_no_matches '\bWithTelemetry\b|\bWithTracing\b|\bWithMetrics\b|\bWithRequestID\b|\bWithCorrelationID\b|\bWithAccessLog\b|\bWithRecovery\b|\bservekit\.(Start|Shutdown)\(' README.md docs examples
check_no_matches 'examples/(hello|middleware|otel|health|proxy|server|composition)([^a-zA-Z0-9_-]|$)' README.md docs examples

echo "==> docs/examples verification passed"
