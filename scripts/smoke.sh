#!/usr/bin/env bash
# End-to-end smoke test: build the binary, generate a synthetic library, boot
# the server against a throwaway database, and drive it through first-run
# setup, login and a catalog read.
#
# Usage:
#   scripts/smoke.sh                     SQLite in a temporary directory
#   scripts/smoke.sh --driver postgres   Postgres, using GOBOOKSHELF_DB_DSN
#
# The Postgres run needs an empty, throwaway database: it drops nothing itself,
# but it does migrate and write into whatever the DSN points at. Without a DSN
# the flag is a no-op and the run is skipped rather than failed, so the same
# command works on a machine with no database server.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
port="${SMOKE_PORT:-18080}"
base="http://127.0.0.1:${port}"
# The URL import needs somewhere on this machine to fetch from; gen-samples
# doubles as that file server.
files_port="$((port + 1))"
files_base="http://127.0.0.1:${files_port}"
server_pid=""
files_pid=""
driver="sqlite"

while [ $# -gt 0 ]; do
  case "$1" in
    --driver)
      driver="${2:-}"
      shift 2
      ;;
    --driver=*)
      driver="${1#--driver=}"
      shift
      ;;
    *)
      echo "usage: $0 [--driver sqlite|postgres]" >&2
      exit 2
      ;;
  esac
done

case "${driver}" in
  sqlite) ;;
  postgres)
    if [ -z "${GOBOOKSHELF_DB_DSN:-}" ]; then
      echo "SKIP: --driver postgres needs GOBOOKSHELF_DB_DSN pointing at a throwaway database"
      exit 0
    fi
    ;;
  *)
    echo "unknown driver: ${driver}" >&2
    exit 2
    ;;
esac

cleanup() {
  for pid in "${server_pid}" "${files_pid}"; do
    if [ -n "${pid}" ] && kill -0 "${pid}" 2>/dev/null; then
      kill "${pid}" 2>/dev/null || true
      wait "${pid}" 2>/dev/null || true
    fi
  done
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
# Built rather than "go run": the file server below has to be killable by pid,
# and "go run" would leave its child behind.
samples="${repo_root}/bin/gen-samples"
go build -o "${samples}" ./scripts/gen-samples
"${samples}" -dir "${work}/media" -inbox "${work}/inbox"

step "starting a file server on ${files_base} for the URL import"
"${samples}" -inbox "${work}/inbox" -serve "127.0.0.1:${files_port}" >"${work}/files.log" 2>&1 &
files_pid=$!
for _ in $(seq 1 100); do
  if curl -fsS "${files_base}/morning.epub" -o /dev/null 2>/dev/null; then break; fi
  sleep 0.1
done
curl -fsS "${files_base}/morning.epub" -o /dev/null || fail "the sample file server never came up"

step "generating a secrets key"
# The key encrypts the credentials the app stores in its own database. There is
# no default and no way to start without one; this is the same command the
# README tells an operator to run.
if command -v openssl >/dev/null 2>&1; then
  secrets_key="$(openssl rand -base64 32)"
else
  secrets_key="$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
fi

# The database settings differ per driver, and nothing else in the script does.
# On Postgres the data directory is deliberately left unset: the run then proves
# the server needs no local disk, covers included.
if [ "${driver}" = "postgres" ]; then
  db_env=(
    "GOBOOKSHELF_DB_DRIVER=postgres"
    "GOBOOKSHELF_DB_DSN=${GOBOOKSHELF_DB_DSN}"
    "GOBOOKSHELF_DB_PATH="
    "GOBOOKSHELF_DATA_DIR="
  )
else
  mkdir -p "${work}/data"
  db_env=(
    "GOBOOKSHELF_DB_DRIVER=sqlite"
    "GOBOOKSHELF_DB_PATH=${work}/data/go-bookshelf.db"
    "GOBOOKSHELF_DATA_DIR=${work}/data"
  )
fi

step "refusing to start without a secrets key"
if env "${db_env[@]}" GOBOOKSHELF_LISTEN="127.0.0.1:${port}" \
   "${binary}" >"${work}/nokey.log" 2>&1; then
  fail "the server started without GOBOOKSHELF_SECRETS_KEY"
fi
grep -q 'openssl rand -base64 32' "${work}/nokey.log" \
  || { cat "${work}/nokey.log" >&2; fail "the missing-key error does not say how to generate one"; }

step "refusing a database configuration that cannot mean anything"
if env GOBOOKSHELF_DB_DRIVER=postgres GOBOOKSHELF_DB_PATH="${work}/data/stray.db" \
   GOBOOKSHELF_DB_DSN="postgres://books@db.example.com:5432/books" \
   GOBOOKSHELF_SECRETS_KEY="${secrets_key}" GOBOOKSHELF_LISTEN="127.0.0.1:${port}" \
   "${binary}" >"${work}/baddb.log" 2>&1; then
  fail "the server accepted a db_path alongside driver=postgres"
fi
grep -q 'GOBOOKSHELF_DB_PATH' "${work}/baddb.log" \
  || { cat "${work}/baddb.log" >&2; fail "the bad-database error does not name the offending variable"; }

step "starting the server on ${base} (driver: ${driver})"
env "${db_env[@]}" \
  GOBOOKSHELF_LISTEN="127.0.0.1:${port}" \
  GOBOOKSHELF_SECRETS_KEY="${secrets_key}" \
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

# ---------------------------------------------------------------------------
# The first-run wizard. Every application setting is entered here rather than
# in the environment, so this sequence is also the configuration.
# ---------------------------------------------------------------------------

step "POST /api/v1/setup/token (checked, not spent)"
jar="${work}/cookies"
curl -sS -o /dev/null -w '%{http_code}' -X POST "${base}/api/v1/setup/token" \
  -H 'Content-Type: application/json' -d '{"token":"wrong"}' | grep -q '^403$' \
  || fail "a wrong setup token was not refused"
checked="$(curl -fsS -X POST "${base}/api/v1/setup/token" \
  -H 'Content-Type: application/json' -d "{\"token\":\"${token}\"}")"
echo "${checked}"
echo "${checked}" | grep -q '"ok":true' || fail "the setup token was rejected"
echo "${checked}" | grep -q '"suggested_base_url"' || fail "the token step did not suggest a base URL"

step "POST /api/v1/setup/admin"
setup="$(curl -fsS -c "${jar}" -X POST "${base}/api/v1/setup/admin" \
  -H 'Content-Type: application/json' \
  -d "{\"token\":\"${token}\",\"username\":\"smoke\",\"password\":\"smoke-test-password\",\"display_name\":\"Smoke\"}")"
echo "${setup}"
echo "${setup}" | grep -q '"role":"admin"' || fail "setup did not return an admin account"

step "the ordinary API stays closed until the wizard finishes"
code="$(curl -s -o /dev/null -w '%{http_code}' -b "${jar}" "${base}/api/v1/items")"
[ "${code}" = "403" ] || fail "items during an unfinished setup returned ${code}, want 403"

step "POST /api/v1/setup/base-url"
baseurl="$(curl -fsS -b "${jar}" -X POST "${base}/api/v1/setup/base-url" \
  -H 'Content-Type: application/json' -d "{\"base_url\":\"${base}/\"}")"
echo "${baseurl}"
echo "${baseurl}" | grep -q "\"base_url\":\"${base}\"" || fail "the base URL was not stored without its trailing slash"
echo "${baseurl}" | grep -q '/api/v1/auth/oidc/callback' || fail "the base URL step did not report the redirect URI"

step "POST /api/v1/setup/oidc (skipped)"
curl -fsS -b "${jar}" -X POST "${base}/api/v1/setup/oidc" \
  -H 'Content-Type: application/json' -d '{"skip":true}' | grep -q '"oidc_enabled":false' \
  || fail "skipping the OIDC step did not leave OIDC off"

step "POST /api/v1/setup/library"
curl -s -o /dev/null -w '%{http_code}' -b "${jar}" -X POST "${base}/api/v1/setup/library" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"Nowhere\",\"kind\":\"mixed\",\"path\":\"${work}/not-a-directory\"}" \
  | grep -q '^400$' || fail "a library on a missing path was accepted"
library="$(curl -fsS -b "${jar}" -X POST "${base}/api/v1/setup/library" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"Smoke\",\"kind\":\"mixed\",\"path\":\"${work}/media\"}")"
echo "${library}"
library_id="$(echo "${library}" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')"
[ -n "${library_id}" ] || fail "no library id in the response"

step "POST /api/v1/setup/complete"
curl -fsS -b "${jar}" -X POST "${base}/api/v1/setup/complete" \
  -H 'Content-Type: application/json' -d '{}' | grep -q '"setup_complete":true' \
  || fail "the wizard did not close"

step "GET /api/v1/auth/status reports a finished setup"
status="$(curl -fsS "${base}/api/v1/auth/status")"
echo "${status}"
echo "${status}" | grep -q '"setup_complete":true' || fail "auth status does not report setup_complete"
echo "${status}" | grep -q '"local_login":true' || fail "auth status does not report local_login"

step "POST /api/v1/auth/login"
login="$(curl -fsS -c "${jar}" -X POST "${base}/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"smoke","password":"smoke-test-password"}')"
echo "${login}"
echo "${login}" | grep -q '"username":"smoke"' || fail "login did not return the account"

# ---------------------------------------------------------------------------
# The admin settings page: read the document, change one value, see it applied.
# ---------------------------------------------------------------------------

step "GET /api/v1/admin/settings"
settings_doc="$(curl -fsS -b "${jar}" "${base}/api/v1/admin/settings")"
echo "${settings_doc}" | head -c 500; echo
echo "${settings_doc}" | grep -q '"has_client_secret":false' || fail "the OIDC section does not report the secret state"
echo "${settings_doc}" | grep -q '"client_secret"' && fail "the settings response carries the client secret"
echo "${settings_doc}" | grep -q '"user_group"' || fail "the OIDC section is missing the user group mapping"

step "PUT /api/v1/admin/settings applies live"
curl -fsS -b "${jar}" -X PUT "${base}/api/v1/admin/settings" \
  -H 'Content-Type: application/json' -d '{"general":{"session_ttl":"48h","scan_interval":"2h"}}' \
  | grep -q '"session_ttl":"48h0m0s"' || fail "the session lifetime was not stored"
curl -fsS -b "${jar}" "${base}/api/v1/admin/settings" | grep -q '"scan_interval":"2h0m0s"' \
  || fail "the scan interval did not survive the write"

step "PUT /api/v1/admin/settings rejects an invalid document"
code="$(curl -s -o /dev/null -w '%{http_code}' -b "${jar}" -X PUT "${base}/api/v1/admin/settings" \
  -H 'Content-Type: application/json' -d '{"metrics":{"allow":["192.0.2.0/33"]}}')"
[ "${code}" = "400" ] || fail "an unparseable CIDR was accepted (${code})"

step "POST /api/v1/admin/settings/oidc/test reports an unreachable issuer"
probe="$(curl -fsS -b "${jar}" -X POST "${base}/api/v1/admin/settings/oidc/test" \
  -H 'Content-Type: application/json' \
  -d '{"issuer":"http://127.0.0.1:1/nowhere","client_id":"smoke","client_secret":"x","groups_claim":"groups"}')"
echo "${probe}"
echo "${probe}" | grep -q '"ok":false' || fail "the OIDC test claimed success against a dead issuer"

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

step "GET /api/v1/items/1/cover (served from the database)"
curl -fsS -b "${jar}" -o "${work}/cover.jpg" "${base}/api/v1/items/1/cover"
[ -s "${work}/cover.jpg" ] || fail "the cover response was empty"
head -c 2 "${work}/cover.jpg" | od -An -tx1 | tr -d ' \n' | grep -q '^ffd8$' \
  || fail "the cover is not a JPEG"
curl -fsS -b "${jar}" -o "${work}/thumb.jpg" "${base}/api/v1/items/1/cover?size=thumb"
[ -s "${work}/thumb.jpg" ] || fail "the thumbnail response was empty"
head -c 2 "${work}/thumb.jpg" | od -An -tx1 | tr -d ' \n' | grep -q '^ffd8$' \
  || fail "the thumbnail is not a JPEG"
# A second read must be byte-identical whether it came from the cache or the
# database. (That the two variants are rendered at different sizes is asserted
# in the Go tests, where the source artwork is large enough for it to show.)
curl -fsS -b "${jar}" -o "${work}/cover-again.jpg" "${base}/api/v1/items/1/cover"
cmp -s "${work}/cover.jpg" "${work}/cover-again.jpg" \
  || fail "two reads of the same cover returned different bytes"

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
status_body="$(curl -fsS -b "${jar}" "${base}/api/v1/system/status")"
echo "${status_body}" | grep -q '"counts":{"audiobooks":2,"ebooks":1}' \
  || fail "system status does not report the counts the admin page reads"
echo "${status_body}" | grep -q "\"db_driver\":\"${driver}\"" \
  || fail "system status does not report the database driver in use"
echo "${status_body}" | grep -qi 'password' && fail "system status leaked a password"

step "GET /opds with an API token"
token_body="$(curl -fsS -b "${jar}" -X POST "${base}/api/v1/me/tokens" \
  -H 'Content-Type: application/json' -d '{"name":"smoke","scopes":["read"]}')"
secret="$(json_field "${token_body}" secret)"
[ -n "${secret}" ] || fail "no token secret in the response"
opds_code="$(curl -s -o /dev/null -w '%{http_code}' "${base}/opds")"
[ "${opds_code}" = "401" ] || fail "anonymous /opds returned ${opds_code}, want 401"
curl -fsS -u "smoke:${secret}" "${base}/opds" | grep -q '<feed' || fail "/opds did not return a feed"

# ---------------------------------------------------------------------------
# Adding books: an upload from this machine, and an import from a URL. Both end
# in the same validation path, so both are worth driving end to end.
# ---------------------------------------------------------------------------

step "POST /api/v1/libraries/${library_id}/upload"
upload="$(curl -fsS -b "${jar}" -X POST "${base}/api/v1/libraries/${library_id}/upload" \
  -F "files=@${work}/inbox/morning.epub")"
echo "${upload}"
echo "${upload}" | grep -q '"status":"complete"' || fail "the upload did not finish"
echo "${upload}" | grep -q '"filename":"A. Writer - The Long Morning.epub"' \
  || fail "the uploaded book was not filed under a name derived from its metadata"
uploaded_item="$(echo "${upload}" | sed -n 's/.*"item_id":\([0-9]*\).*/\1/p' | head -1)"
[ -n "${uploaded_item}" ] && [ "${uploaded_item}" != "0" ] \
  || fail "the upload response carries no item id"
[ -f "${work}/media/A. Writer - The Long Morning.epub" ] \
  || fail "the uploaded file is not in the library directory"
curl -fsS -b "${jar}" "${base}/api/v1/items/${uploaded_item}" | grep -q '"title":"The Long Morning"' \
  || fail "the uploaded book was not ingested by the scan that followed"

step "the client's filename never decides the name on disk"
# The name in the request is a traversal; the name on disk comes from the
# book's own metadata, so the traversal is not so much refused as irrelevant.
named="$(curl -fsS -b "${jar}" -X POST "${base}/api/v1/libraries/${library_id}/upload" \
  -F "files=@${work}/inbox/dusk.epub;filename=../../evil.epub")"
echo "${named}"
echo "${named}" | grep -q '"filename":"A. Writer - The Long Dusk.epub"' \
  || fail "a traversal in the client's filename reached the on-disk name"
[ ! -e "${work}/evil.epub" ] || fail "an upload wrote outside the library"

step "a subfolder that is a path is refused"
traversal="$(curl -s -o /dev/null -w '%{http_code}' -b "${jar}" \
  -X POST "${base}/api/v1/libraries/${library_id}/upload" \
  -F "subdir=../escape" -F "files=@${work}/inbox/dusk.epub")"
[ "${traversal}" = "400" ] || fail "a traversal in the subfolder returned ${traversal}, want 400"
[ ! -e "${work}/escape" ] || fail "an upload created a directory outside the library"

step "an upload whose bytes are not a book is refused"
badmagic="$(curl -s -o /dev/null -w '%{http_code}' -b "${jar}" \
  -X POST "${base}/api/v1/libraries/${library_id}/upload" \
  -F "files=@${work}/inbox/not-a-book.epub")"
[ "${badmagic}" = "400" ] || fail "a text file called .epub returned ${badmagic}, want 400"

step "the same book twice is refused as a duplicate"
dup_body="${work}/dup.json"
dup="$(curl -s -o "${dup_body}" -w '%{http_code}' -b "${jar}" \
  -X POST "${base}/api/v1/libraries/${library_id}/upload" \
  -F "files=@${work}/inbox/morning.epub")"
[ "${dup}" = "409" ] || fail "a duplicate upload returned ${dup}, want 409"
grep -q "\"item_id\":${uploaded_item}" "${dup_body}" \
  || { cat "${dup_body}"; fail "the conflict does not name the book already in the library"; }

step "URL imports refuse a private address before anything is queued"
for blocked in "http://169.254.169.254/latest/meta-data/" "file:///etc/passwd"; do
  code="$(curl -s -o /dev/null -w '%{http_code}' -b "${jar}" \
    -X POST "${base}/api/v1/libraries/${library_id}/import" \
    -H 'Content-Type: application/json' -d "{\"url\":\"${blocked}\"}")"
  [ "${code}" = "400" ] || fail "importing ${blocked} returned ${code}, want 400"
done

step "allowing the guarded fetcher to reach this machine"
# The file server is on the loopback address, which the SSRF guard refuses by
# default. This is the same switch an operator flips for a metadata provider on
# their own network, and it is what makes the next step reachable at all.
curl -fsS -b "${jar}" -X PUT "${base}/api/v1/admin/settings" \
  -H 'Content-Type: application/json' -d '{"metadata":{"provider":"none","allow_private":true}}' \
  >/dev/null || fail "could not allow private addresses"

step "POST /api/v1/libraries/{id}/import"
mkdir -p "${work}/imports"
import_lib="$(curl -fsS -b "${jar}" -X POST "${base}/api/v1/libraries" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"Imports\",\"kind\":\"mixed\",\"paths\":[\"${work}/imports\"]}")"
import_lib_id="$(echo "${import_lib}" | sed -n 's/.*"id":\([0-9]*\).*/\1/p' | head -1)"
[ -n "${import_lib_id}" ] || fail "could not create the import library"

job="$(curl -fsS -b "${jar}" -X POST "${base}/api/v1/libraries/${import_lib_id}/import" \
  -H 'Content-Type: application/json' -d "{\"url\":\"${files_base}/noon.epub\"}")"
echo "${job}"
job_id="$(echo "${job}" | sed -n 's/.*"id":\([0-9]*\).*/\1/p' | head -1)"
[ -n "${job_id}" ] || fail "the import was not queued"
echo "${job}" | grep -q '"status":"queued"' || fail "a new import is not queued"

step "waiting for import ${job_id}"
import_status=""
for _ in $(seq 1 300); do
  job="$(curl -fsS -b "${jar}" "${base}/api/v1/imports/${job_id}")"
  case "${job}" in
    *'"status":"done"'*) import_status="done"; break ;;
    *'"status":"failed"'*) echo "${job}"; fail "the import failed" ;;
  esac
  sleep 0.2
done
[ "${import_status}" = "done" ] || { echo "${job}"; fail "the import never finished"; }
echo "${job}"
import_item="$(echo "${job}" | sed -n 's/.*"item_id":\([0-9]*\).*/\1/p' | head -1)"
[ -n "${import_item}" ] && [ "${import_item}" != "0" ] || fail "the finished import names no item"
curl -fsS -b "${jar}" "${base}/api/v1/items/${import_item}" | grep -q '"title":"The Long Noon"' \
  || fail "the imported book is not in the catalog"
[ -f "${work}/imports/A. Writer - The Long Noon.epub" ] \
  || fail "the imported book is not in the import library's directory"

step "GET /api/v1/me/imports"
curl -fsS -b "${jar}" "${base}/api/v1/me/imports" | grep -q "\"id\":${job_id}" \
  || fail "the import is missing from the job list"

step "DELETE /api/v1/imports/${job_id}"
curl -fsS -b "${jar}" -X DELETE "${base}/api/v1/imports/${job_id}" | grep -q '"status":"cancelled"' \
  || fail "the finished import could not be cleared"

step "SPA fallback"
for route in / /library/1 /item/1 /read/1 /listen/1 /authors /series /search /settings /admin /admin/settings /login /setup; do
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

printf '\nSMOKE OK (%s)\n' "${driver}"
