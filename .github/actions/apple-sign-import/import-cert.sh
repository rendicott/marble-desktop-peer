#!/usr/bin/env bash
# Decode and import a Developer ID Application .p12 into a throwaway keychain.
#
# Called from .github/actions/apple-sign-import/action.yml on a macOS runner.
# Keep this bash 3.2 compatible: macOS runners run /bin/bash 3.2, so no `+=` on
# arrays, no associative arrays, no mapfile.
#
# CONTRACT: this script never exits non-zero for a signing problem. It reports
# signed=false and says why, so a release publishes an unsigned build rather
# than failing outright. (v0.2.0 was lost because `security import` aborted the
# macOS job and the release job therefore never ran.)
#
# Inputs, all optional, via the environment:
#   APPLE_CERTIFICATE           base64 of the .p12
#   APPLE_CERTIFICATE_PASSWORD  password for that .p12
#   APPLE_SIGNING_IDENTITY      preferred codesign identity
# Outputs, via $GITHUB_OUTPUT: signed, identity
set -uo pipefail

TEMP="${RUNNER_TEMP:-${TMPDIR:-/tmp}}"
CERT="$TEMP/cert.p12"
CERT_FIXED="$TEMP/cert-reencoded.p12"
CERT_PEM="$TEMP/cert.pem"
KEYCHAIN="$TEMP/build.keychain-db"
IDENTITIES_FILE="$TEMP/build-identities.txt"
LOG="$TEMP/apple-sign-import.log"
SUMMARY="${GITHUB_STEP_SUMMARY:-/dev/null}"
OUTPUT="${GITHUB_OUTPUT:-/dev/null}"
PW_ORIG="${APPLE_CERTIFICATE_PASSWORD:-}"

# emit <true|false> [identity]: the step outputs, written exactly once.
emit() {
  echo "signed=$1" >> "$OUTPUT"
  echo "identity=${2:-}" >> "$OUTPUT"
}

# skip <reason>: warn, explain, report unsigned, and stop successfully.
skip() {
  emit false
  echo "::warning title=macOS signing skipped::$1"
  {
    echo "### ⚠️ macOS signing skipped"
    echo
    echo "$1"
    echo
    echo "This run publishes **unsigned** macOS artifacts: Gatekeeper warns on first open, and"
    echo "the Screen Recording / Accessibility grants have to be re-granted after every upgrade."
    echo "Fix the secrets and re-run — the tag does not need to move:"
    echo
    echo '```bash'
    echo "openssl pkcs12 -in cert.p12 -noout          # verify the pair locally first"
    echo "base64 -i cert.p12 | gh secret set APPLE_CERTIFICATE -R ${GITHUB_REPOSITORY:-OWNER/REPO}"
    echo "gh secret set APPLE_CERTIFICATE_PASSWORD -R ${GITHUB_REPOSITORY:-OWNER/REPO}"
    echo "gh run rerun ${GITHUB_RUN_ID:-RUN_ID} --failed -R ${GITHUB_REPOSITORY:-OWNER/REPO}"
    echo '```'
    echo
    echo "Run the **Signing check** workflow (Actions → Signing check → Run workflow) to test the"
    echo "secret pair without cutting a tag."
  } >> "$SUMMARY"
  exit 0
}

if [ -z "${APPLE_CERTIFICATE:-}" ]; then
  emit false
  echo "APPLE_CERTIFICATE is not set: building unsigned (supported, and no release is lost)."
  exit 0
fi

# --- decode ------------------------------------------------------------------
# python3 rather than `base64 -d`: it behaves identically on every runner, and it
# lets us reject garbage instead of writing it to cert.p12 the way the old step
# did (it fell back to writing the raw secret).
if ! DECODE_MSG=$(python3 - "$CERT" <<'PY'
import base64, os, sys

def fail(msg):
    print(msg)
    raise SystemExit(1)

raw = "".join(os.environ.get("APPLE_CERTIFICATE", "").split())
if not raw:
    fail("APPLE_CERTIFICATE is empty")
try:
    data = base64.b64decode(raw, validate=True)
except Exception:
    fail("APPLE_CERTIFICATE is not valid base64: the secret was truncated, or it holds the "
         ".p12 pasted raw instead of base64-encoded")
if len(data) < 128:
    fail("APPLE_CERTIFICATE decoded to only %d bytes, far too small for a .p12" % len(data))
if data[0] != 0x30:
    fail("APPLE_CERTIFICATE decoded to %d bytes but they are not DER/PKCS#12 (first byte "
         "0x%02x, expected 0x30)" % (len(data), data[0]))
with open(sys.argv[1], "wb") as fh:
    fh.write(data)
print("decoded %d bytes of PKCS#12" % len(data))
PY
); then
  skip "${DECODE_MSG:-APPLE_CERTIFICATE could not be decoded into a .p12}"
fi
echo "$DECODE_MSG"

# --- keychain ----------------------------------------------------------------
security delete-keychain "$KEYCHAIN" >/dev/null 2>&1 || true
if ! security create-keychain -p ci "$KEYCHAIN"; then
  skip "could not create the CI keychain at $KEYCHAIN"
fi
security set-keychain-settings -lut 21600 "$KEYCHAIN" || true
if ! security unlock-keychain -p ci "$KEYCHAIN"; then
  skip "could not unlock the CI keychain at $KEYCHAIN"
fi

# --- helpers -----------------------------------------------------------------
attempt_import() { # $1 p12, $2 password, $3 "quiet" to swallow the first failure
  if security import "$1" -k "$KEYCHAIN" -P "$2" \
       -T /usr/bin/codesign -T /usr/bin/security >"$LOG" 2>&1; then
    return 0
  fi
  if [ "${3:-}" != "quiet" ]; then
    echo "security import said:"
    cat "$LOG"
  fi
  return 1
}

# Real OpenSSL 3 is needed to re-encode; LibreSSL cannot.
OSSL3=""
for cand in /opt/homebrew/opt/openssl@3/bin/openssl \
            /usr/local/opt/openssl@3/bin/openssl \
            "$(command -v openssl || true)"; do
  if [ -n "$cand" ] && [ -x "$cand" ] && "$cand" version 2>/dev/null | grep -q '^OpenSSL 3'; then
    OSSL3="$cand"
    break
  fi
done

# pkcs12 -export reads PEM, not another .p12, so round-trip through PEM.
# -legacy is only needed to read old RC2/40-bit exports.
reencode() { # $1 password, $2 optional -legacy
  "$OSSL3" pkcs12 ${2:-} -in "$CERT" -passin "pass:$1" -nodes -out "$CERT_PEM" \
    >"$LOG" 2>&1 || return 1
  "$OSSL3" pkcs12 -export -in "$CERT_PEM" -passout "pass:$1" \
    -keypbe PBE-SHA1-3DES -certpbe PBE-SHA1-3DES -macalg sha1 \
    -out "$CERT_FIXED" >>"$LOG" 2>&1
}

# The secret as-is, then the obvious paste damage: trailing whitespace from a
# file that was piped in, and quotes from a shell one-liner. If a variant opens
# the .p12 then that variant *is* the password.
CANDIDATES=("$PW_ORIG")
add_candidate() {
  local c
  for c in "${CANDIDATES[@]}"; do
    [ "$c" = "$1" ] && return 0
  done
  CANDIDATES[${#CANDIDATES[@]}]="$1"
}
TRIMMED="$(printf '%s' "$PW_ORIG" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
add_candidate "$TRIMMED"
add_candidate "$(printf '%s' "$TRIMMED" | sed -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'\$//")"
add_candidate "$PW_ORIG
"

# --- import, with password cleanup and one algorithm retry -------------------
IMPORTED=0
PW="$PW_ORIG"
FIRST=1
for cand in "${CANDIDATES[@]}"; do
  # The first attempt prints the runner's own error; the retries stay quiet.
  QUIET="quiet"
  if [ "$FIRST" = "1" ]; then
    QUIET=""
    FIRST=0
  fi
  if attempt_import "$CERT" "$cand" "$QUIET"; then
    PW="$cand"
    IMPORTED=1
    break
  fi
  if [ -z "$OSSL3" ]; then
    continue
  fi
  # OpenSSL 3 writes AES-256-CBC/PBKDF2 by default and some macOS `security`
  # builds refuse it with "MAC verification failed during PKCS12 import".
  if reencode "$cand" || reencode "$cand" -legacy; then
    echo "re-encoded the .p12 as 3DES/SHA-1 (some OpenSSL 3 exports do not import):"
    cat "$LOG"
    if attempt_import "$CERT_FIXED" "$cand" quiet; then
      PW="$cand"
      IMPORTED=1
      break
    fi
  fi
done

if [ "$IMPORTED" = "1" ] && [ "$PW" != "$PW_ORIG" ]; then
  echo "::warning title=APPLE_CERTIFICATE_PASSWORD needs cleaning::The secret only opens the .p12 after dropping leading/trailing whitespace or quotes, so it was stored with paste damage. The import succeeded, but re-set it from a file: gh secret set APPLE_CERTIFICATE_PASSWORD < password.txt"
fi

# --- diagnostics, when nothing worked ----------------------------------------
DIAG=""
diagnose() {
  local ossl="${OSSL3:-$(command -v openssl || true)}"
  ls -l "$CERT" || true
  if [ -z "$ossl" ]; then
    DIAG="No OpenSSL on this runner, so the .p12 could not be inspected further."
    return 0
  fi
  local info rc subj key
  info="$("$ossl" pkcs12 -info -in "$CERT" -passin "pass:$PW_ORIG" -noout 2>&1)"
  rc=$?
  echo "$info"
  # OpenSSL 3 prints no "MAC verified OK", so judge by what it complains about:
  # a Mac verify error is a password mismatch, a parsed-but-failing file is an
  # algorithm this runner cannot handle.
  if printf '%s' "$info" | grep -qi 'mac verify error\|invalid password\|bad decrypt\|mac verification failed'; then
    DIAG="APPLE_CERTIFICATE_PASSWORD does not open this .p12, byte for byte and in the cleaned-up forms this step tried. Re-export the Developer ID Application certificate with a known password and set APPLE_CERTIFICATE and APPLE_CERTIFICATE_PASSWORD together."
    return 0
  fi
  if ! printf '%s' "$info" | grep -qi 'pkcs7 encrypted data\|certificate bag\|mac:'; then
    DIAG="This is not a readable PKCS#12 file, so APPLE_CERTIFICATE is probably the wrong export, or it holds something other than a .p12."
    return 0
  fi
  if [ "$rc" != "0" ]; then
    DIAG="The password opened this .p12 but its contents would not decrypt (openssl exit $rc) and the 3DES re-encode retry did not rescue it. Re-export the .p12 from Keychain Access as a PKCS#12 with a fresh password."
  else
    DIAG="The password opens this .p12 cleanly, so the import failure is not the password: the export is missing its private key, or the identity is not a Developer ID Application one."
  fi
  subj="$("$ossl" pkcs12 -in "$CERT" -nokeys -clcerts -passin "pass:$PW_ORIG" 2>/dev/null \
          | "$ossl" x509 -noout -subject -issuer -fingerprint -sha1 -enddate 2>/dev/null || true)"
  if [ -n "$subj" ]; then
    echo "certificate in the .p12:"
    echo "$subj"
    DIAG="$DIAG Certificate: $(printf '%s' "$subj" | tr '\n' ' ' | tr -s ' ')."
  else
    DIAG="$DIAG No certificate could be read out of it either."
  fi
  if "$ossl" pkcs12 -in "$CERT" -nocerts -nodes -passin "pass:$PW_ORIG" 2>/dev/null \
       | grep -q 'BEGIN .*PRIVATE KEY'; then
    key="private key present"
  else
    key="NO private key — a certificate alone imports without a signing identity"
  fi
  echo "key material: $key"
  DIAG="$DIAG Key material: $key."
}

if [ "$IMPORTED" != "1" ]; then
  echo "---- Developer ID diagnostics ----"
  diagnose
  echo "----------------------------------"
  skip "Could not import APPLE_CERTIFICATE into the keychain. ${DIAG}"
fi

# --- verify a signing identity really is in the keychain ---------------------
security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k ci "$KEYCHAIN" >/dev/null || true
security list-keychain -d user -s "$KEYCHAIN" login.keychain || true

security find-identity -v -p codesigning "$KEYCHAIN" 2>/dev/null >"$IDENTITIES_FILE" || true
cat "$IDENTITIES_FILE"

if [ -n "${APPLE_SIGNING_IDENTITY:-}" ] &&
   grep -qF "$APPLE_SIGNING_IDENTITY" "$IDENTITIES_FILE"; then
  ID="$APPLE_SIGNING_IDENTITY"
else
  ID="$(awk -F'"' '/Developer ID Application:/ {print $2; exit}' "$IDENTITIES_FILE")"
  if [ -n "${APPLE_SIGNING_IDENTITY:-}" ] && [ -n "$ID" ]; then
    echo "::warning title=Signing identity mismatch::APPLE_SIGNING_IDENTITY ('${APPLE_SIGNING_IDENTITY}') is not in the imported keychain; signing with '$ID' instead. Update the secret to match the certificate."
  fi
fi

if [ -z "$ID" ]; then
  skip "The .p12 imported, but the keychain holds no 'Developer ID Application' signing identity. That usually means the export contained the certificate without its private key, or with a different one."
fi

echo "code signing identity: $ID"
emit true "$ID"
exit 0
