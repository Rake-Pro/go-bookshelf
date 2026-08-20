#!/usr/bin/env bash
# End-to-end smoke test: build the binary, generate a synthetic library, boot
# the server against a throwaway data directory, and drive it through first-run
# setup, login and a catalog read.
#
# Usage: scripts/smoke.sh   (or: make smoke)
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
port="${SMOKE_PORT:-18080}"
base="http://127.0.0.1:${port}"
server_pid=""

cleanup() {
  if [ -n "${server_pid}" ] && kill -0 "${server_pid}" 2>/dev/null; then
    kill "${server_pid}" 2>/dev/null || true
    wait "${server_pid}" 2>/dev/null || true
  fi
  rm -rf "${work}"
}
trap cleanup EXIT

step() { printf '\n== %s\n' "$1"; }
fail() { printf '\nFAIL: %s\n' "$1" >&2; exit 1; }

step "building"
# The binary is built into bin/ rather than the temp directory: a hardened
# host often mounts the temp filesystem noexec.
binary="${repo_root}/bin/go-bookshelf"
mkdir -p "${repo_root}/bin"
go build -o "${binary}" ./cmd/go-bookshelf

step "generating a sample library"
go run ./scripts/gen-samples -dir "${work}/media"

step "starting the server on ${base}"
mkdir -p "${work}/data"
GOBOOKSHELF_LISTEN="127.0.0.1:${port}" \
GOBOOKSHELF_DB_PATH="${work}/data/go-bookshelf.db" \
GOBOOKSHELF_DATA_DIR="${work}/data" \
GOBOOKSHELF_BASE_URL="${base}" \
GOBOOKSHELF_LOG_LEVEL="info" \
  "${binary}" >"${work}/server.log" 2>&1 &
server_pid=$!

for _ in $(seq 1 100); do
  if curl -fsS "${base}/healthz" >/dev/null 2>&1; then break; fi
  if ! kill -0 "${server_pid}" 2>/dev/null; then
    cat "${work}/server.log" >&2
    fail "the server exited during startup"
  fi
  sleep 0.1
done

step "GET /healthz"
health="$(curl -fsS "${base}/healthz")"
echo "${health}"
echo "${health}" | grep -q '"status":"ok"' || fail "healthz did not report ok"

step "GET /readyz"
curl -fsS "${base}/readyz" | grep -q '"status":"ready"' || fail "readyz did not report ready"

step "unauthenticated GET /api/v1/items must be refused"
code="$(curl -s -o /dev/null -w '%{http_code}' "${base}/api/v1/items")"
[ "${code}" = "401" ] || fail "unauthenticated items returned ${code}, want 401"

step "reading the one-time setup token from the log"
token="$(grep -o 'one-time token: [0-9a-f]*' "${work}/server.log" | tail -1 | awk '{print $3}')"
[ -n "${token}" ] || { cat "${work}/server.log" >&2; fail "no setup token in the log"; }
echo "token: ${token:0:8}..."

step "POST /api/v1/auth/setup"
jar="${work}/cookies"
setup="$(curl -fsS -c "${jar}" -X POST "${base}/api/v1/auth/setup" \
  -H 'Content-Type: application/json' \
  -d "{\"token\":\"${token}\",\"username\":\"smoke\",\"password\":\"smoke-test-password\",\"display_name\":\"Smoke\"}")"
echo "${setup}"
echo "${setup}" | grep -q '"role":"admin"' || fail "setup did not return an admin account"

step "POST /api/v1/auth/login"
login="$(curl -fsS -c "${jar}" -X POST "${base}/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"smoke","password":"smoke-test-password"}')"
echo "${login}"
echo "${login}" | grep -q '"username":"smoke"' || fail "login did not return the account"

step "POST /api/v1/libraries"
library="$(curl -fsS -b "${jar}" -X POST "${base}/api/v1/libraries" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"Smoke\",\"kind\":\"mixed\",\"paths\":[\"${work}/media\"]}")"
echo "${library}"
library_id="$(echo "${library}" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')"
[ -n "${library_id}" ] || fail "no library id in the response"

step "POST /api/v1/libraries/${library_id}/scan"
scan="$(curl -fsS -b "${jar}" -X POST "${base}/api/v1/libraries/${library_id}/scan")"
echo "${scan}"
echo "${scan}" | grep -q '"added":3' || fail "the scan did not ingest the three sample items"

step "GET /api/v1/items"
items="$(curl -fsS -b "${jar}" "${base}/api/v1/items?sort=title")"
echo "${items}" | head -c 600
echo
echo "${items}" | grep -q '"total":3' || fail "items did not return the three sample items"
echo "${items}" | grep -q 'The Long Afternoon' || fail "the sample ebook is missing from the catalog"
echo "${items}" | grep -q 'The Long Evening' || fail "the sample audiobook is missing from the catalog"
echo "${items}" | grep -q 'The Long Night' || fail "the multi-file audiobook is missing from the catalog"

step "GET /api/v1/home"
curl -fsS -b "${jar}" "${base}/api/v1/home" | grep -q '"recent"' || fail "home did not return a recent row"

step "GET /api/v1/items/1/cover"
curl -fsS -b "${jar}" -o "${work}/cover.jpg" "${base}/api/v1/items/1/cover"
[ -s "${work}/cover.jpg" ] || fail "the cover response was empty"

# ---------------------------------------------------------------------------
# From here on the script walks the same sequence the PWA performs, so a
# backend change that breaks a view fails the smoke test rather than the user.
# ---------------------------------------------------------------------------

# json_field <json> <key> - first value of a flat "key":value pair.
json_field() {
  echo "$1" | sed -n "s/.*\"$2\":\\?\"\{0,1\}\([^,\"}]*\).*/\1/p" | head -1
}

step "identifying the sample items"
ebook_id="$(curl -fsS -b "${jar}" "${base}/api/v1/items?kind=ebook" | sed -n 's/.*"items":\[{"id":\([0-9]*\).*/\1/p')"
[ -n "${ebook_id}" ] || fail "could not find the sample ebook"
echo "ebook=${ebook_id}"

step "GET /api/v1/items/{id} for every audiobook (files, chapters, progress)"
file_id=""
for id in $(curl -fsS -b "${jar}" "${base}/api/v1/items?kind=audiobook&sort=title" \
    | tr '{' '\n' | sed -n 's/^"id":\([0-9]*\),"library_id".*/\1/p'); do
  detail="$(curl -fsS -b "${jar}" "${base}/api/v1/items/${id}")"
  echo "${detail}" | grep -q '"files":\[{' || fail "item ${id} carries no files"
  echo "${detail}" | grep -q '"chapters":\[{' || fail "item ${id} carries no top-level chapters"
  echo "${detail}" | grep -q '"file_id":' || fail "item ${id} chapters do not name their file"
  # Every file must carry a duration, or the player cannot map chapter starts
  # onto absolute positions across a multi-file audiobook.
  if echo "${detail}" | tr ',' '\n' | grep -q '"duration_ms":0'; then
    echo "${detail}" | head -c 600; echo
    fail "item ${id} has a file with duration_ms 0"
  fi
  echo "item ${id}: $(echo "${detail}" | tr ',' '\n' | grep -c '"stream_url"') file(s), \
$(echo "${detail}" | tr '{' '\n' | grep -c '"file_id"') chapter(s)"
  if [ -z "${file_id}" ]; then
    file_id="$(echo "${detail}" | sed -n 's/.*"files":\[{"id":\([0-9]*\).*/\1/p' | head -1)"
    audio_id="${id}"
  fi
done
[ -n "${file_id}" ] || fail "no audiobook file id found"

step "GET /api/v1/items/${ebook_id}/epub (reading manifest)"
manifest="$(curl -fsS -b "${jar}" "${base}/api/v1/items/${ebook_id}/epub")"
echo "${manifest}" | head -c 400; echo
echo "${manifest}" | grep -q '"spine":\[{' || fail "the manifest has an empty spine"
echo "${manifest}" | grep -q '"size":[1-9]' || fail "the manifest publishes no spine sizes"
container_url="$(json_field "${manifest}" container_url)"
spine_url="$(echo "${manifest}" | sed -n 's/.*"spine":\[{[^}]*"url":"\([^"]*\)".*/\1/p' | head -1)"
[ -n "${spine_url}" ] || fail "no spine url in the manifest"

step "GET ${container_url} (the renderer's first request)"
curl -fsS -b "${jar}" "${base}${container_url}" | grep -q 'rootfile' || fail "container.xml is not reachable at the container root"

step "GET ${spine_url} (one EPUB resource)"
headers="$(curl -fsS -D - -o "${work}/chapter.xhtml" -b "${jar}" "${base}${spine_url}")"
echo "${headers}" | grep -qi "content-security-policy:.*script-src 'none'" || fail "the EPUB resource CSP does not forbid scripts"
echo "${headers}" | grep -qi 'content-security-policy:.*sandbox' || fail "the EPUB resource CSP does not sandbox"
[ -s "${work}/chapter.xhtml" ] || fail "the EPUB resource was empty"

step "HEAD ${spine_url} (the reader's size probe)"
length="$(curl -fsS -I --compressed -b "${jar}" "${base}${spine_url}" | grep -i '^content-length:' | tr -d '\r' | awk '{print $2}')"
[ -n "${length}" ] && [ "${length}" -gt 0 ] || fail "HEAD returned no Content-Length; the reader falls back to equal weights"
echo "content-length: ${length}"

step "GET stream with Range (206)"
range_code="$(curl -s -o "${work}/range.bin" -w '%{http_code}' -b "${jar}" \
  -H 'Range: bytes=0-99' "${base}/api/v1/items/${audio_id}/files/${file_id}/stream")"
[ "${range_code}" = "206" ] || fail "a ranged stream request returned ${range_code}, want 206"
[ "$(wc -c <"${work}/range.bin")" = "100" ] || fail "the ranged response was not 100 bytes"

step "PUT /api/v1/me/progress/${audio_id}"
curl -fsS -b "${jar}" -X PUT "${base}/api/v1/me/progress/${audio_id}" \
  -H 'Content-Type: application/json' \
  -d '{"position_ms":450000,"percent":0.25,"finished":false,"device":"smoke"}' \
  | grep -q '"percent":0.25' || fail "progress was not stored"

step "GET /api/v1/home shows the continue row"
curl -fsS -b "${jar}" "${base}/api/v1/home" \
  | grep -q "\"continue\":\[{\"id\":${audio_id}" || fail "home does not surface the item just started"

step "PUT /api/v1/me/settings (partial writes must merge)"
curl -fsS -b "${jar}" -X PUT "${base}/api/v1/me/settings" \
  -H 'Content-Type: application/json' -d '{"player":{"speed":1.5}}' >/dev/null
curl -fsS -b "${jar}" -X PUT "${base}/api/v1/me/settings" \
  -H 'Content-Type: application/json' -d '{"reader":{"font_scale":1.3}}' >/dev/null
curl -fsS -b "${jar}" -X PUT "${base}/api/v1/me/settings" \
  -H 'Content-Type: application/json' -d '{"ui":{"theme":"hc-dark","text_scale":1.2}}' >/dev/null
settings="$(curl -fsS -b "${jar}" "${base}/api/v1/me/settings")"
echo "${settings}"
echo "${settings}" | grep -q '"speed":1.5' || fail "an earlier settings group was cleared by a later partial write"
echo "${settings}" | grep -q '"font_scale":1.3' || fail "reader.font_scale did not survive"
echo "${settings}" | grep -q '"text_scale":1.2' || fail "ui.text_scale did not survive"
echo "${settings}" | grep -q '"font_family":"publisher"' || fail "untouched keys lost their defaults"

step "POST and GET /api/v1/me/bookmarks"
curl -fsS -b "${jar}" -X POST "${base}/api/v1/me/bookmarks" \
  -H 'Content-Type: application/json' \
  -d "{\"item_id\":${audio_id},\"position_ms\":450000,\"note\":\"Smoke\"}" \
  | grep -q '"note":"Smoke"' || fail "the bookmark was not created"
curl -fsS -b "${jar}" "${base}/api/v1/me/bookmarks?item=${audio_id}" \
  | grep -q '"total":1' || fail "the bookmark was not listed back"

step "GET /api/v1/system/status"
curl -fsS -b "${jar}" "${base}/api/v1/system/status" \
  | grep -q '"counts":{"audiobooks":2,"ebooks":1}' || fail "system status does not report the counts the admin page reads"

step "GET /opds with an API token"
token_body="$(curl -fsS -b "${jar}" -X POST "${base}/api/v1/me/tokens" \
  -H 'Content-Type: application/json' -d '{"name":"smoke","scopes":["read"]}')"
secret="$(json_field "${token_body}" secret)"
[ -n "${secret}" ] || fail "no token secret in the response"
opds_code="$(curl -s -o /dev/null -w '%{http_code}' "${base}/opds")"
[ "${opds_code}" = "401" ] || fail "anonymous /opds returned ${opds_code}, want 401"
curl -fsS -u "smoke:${secret}" "${base}/opds" | grep -q '<feed' || fail "/opds did not return a feed"

step "SPA fallback"
for route in / /library/1 /item/1 /read/1 /listen/1 /authors /series /search /settings /admin /login /setup; do
  curl -fsS "${base}${route}" -o "${work}/route.html"
  grep -qi '<div id="app">' "${work}/route.html" || fail "${route} did not return the application shell"
done
for reserved in /sw.js /manifest.webmanifest /app/main.js /vendor/foliate-js/view.js /icons/icon.svg; do
  curl -fsS "${base}${reserved}" -o "${work}/asset"
  grep -qi '<div id="app">' "${work}/asset" && fail "${reserved} was shadowed by the application shell"
  [ -s "${work}/asset" ] || fail "${reserved} was empty"
done
for guarded in /healthz /readyz /metrics /opds; do
  curl -s "${base}${guarded}" -o "${work}/guarded"
  grep -qi '<div id="app">' "${work}/guarded" && fail "${guarded} was shadowed by the application shell"
done

step "POST /api/v1/auth/logout"
curl -fsS -b "${jar}" -c "${jar}" -X POST "${base}/api/v1/auth/logout" >/dev/null
after="$(curl -s -o /dev/null -w '%{http_code}' -b "${jar}" "${base}/api/v1/items")"
[ "${after}" = "401" ] || fail "the session still worked after logout (${after})"

step "static frontend check"
go run ./scripts/checkweb -dir "${repo_root}/web/dist"

printf '\nSMOKE OK\n'
