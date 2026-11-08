#!/bin/sh
# Downloads one Anchore release (Syft or Grype), verifies it and extracts the binary. It fails closed:
# any unverified byte is an error. Runs in the scanner image's fetch stage (Alpine, busybox ash) and locally
# wherever wget, sha256sum, cosign, tar and file are installed.
set -euo pipefail

usage() {
  cat <<USAGE
usage: tools/scanner-verify.sh <project> <version> <checksums-sha256> <identity> <arch> <outdir>

  project           syft or grype
  version           release version without the leading v, for example 1.57.0
  checksums-sha256  expected sha256 of <project>_<version>_checksums.txt
  identity          exact cosign certificate identity (SAN) of the release workflow
  arch              amd64 or arm64
  outdir            directory that receives the verified binary <outdir>/<project>

Downloads <project>_<version>_checksums.txt, its .sigstore.json bundle and the linux/<arch> tarball from
https://github.com/anchore/<project>/releases/download/v<version>/, then:
  1. checks the sha256 of the checksums file against <checksums-sha256>
  2. cosign verify-blob --bundle with <identity> and the GitHub Actions OIDC issuer
  3. checks the tarball against the signed checksums file
  4. extracts the binary and requires 'statically linked' in its 'file' output

Exit codes: 0 ok, 1 verification or download failed, 2 usage error.
USAGE
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 0
fi
if [ "$#" -ne 6 ]; then
  usage >&2
  exit 2
fi

project="$1"
version="$2"
expected_checksums_sha256="$3"
identity="$4"
arch="$5"
outdir="$6"

case "$project" in
  syft | grype) ;;
  *) echo "scanner-verify: project must be syft or grype, got '$project'" >&2; exit 2 ;;
esac
case "$arch" in
  amd64 | arm64) ;;
  *) echo "scanner-verify: arch must be amd64 or arm64, got '$arch'" >&2; exit 2 ;;
esac
case "$version" in
  v* | '') echo "scanner-verify: version must be given without a leading v, got '$version'" >&2; exit 2 ;;
esac
if [ -z "$expected_checksums_sha256" ] || [ -z "$identity" ]; then
  echo "scanner-verify: checksums-sha256 and identity must not be empty" >&2
  exit 2
fi

fail() {
  echo "scanner-verify: FAIL: $1" >&2
  exit 1
}

base_url="https://github.com/anchore/${project}/releases/download/v${version}"
checksums_name="${project}_${version}_checksums.txt"
bundle_name="${checksums_name}.sigstore.json"
tarball_name="${project}_${version}_linux_${arch}.tar.gz"
oidc_issuer="https://token.actions.githubusercontent.com"

mkdir -p "$outdir"
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

for name in "$checksums_name" "$bundle_name" "$tarball_name"; do
  wget -q -O "${workdir}/${name}" "${base_url}/${name}" || fail "download of ${base_url}/${name} failed"
done

actual_checksums_sha256="$(sha256sum "${workdir}/${checksums_name}" | cut -d ' ' -f 1)"
[ "$actual_checksums_sha256" = "$expected_checksums_sha256" ] ||
  fail "${checksums_name} sha256 is ${actual_checksums_sha256}, want ${expected_checksums_sha256}"
echo "OK: ${checksums_name} sha256 matches the pin"

cosign verify-blob \
  --bundle "${workdir}/${bundle_name}" \
  --certificate-identity "$identity" \
  --certificate-oidc-issuer "$oidc_issuer" \
  "${workdir}/${checksums_name}" || fail "cosign could not verify ${checksums_name} for identity ${identity}"
echo "OK: ${checksums_name} signature verified for ${identity}"

# --ignore-missing is not used: the tarball must be listed, otherwise grep finds nothing and the check fails.
grep -F " ${tarball_name}" "${workdir}/${checksums_name}" >"${workdir}/tarball.sha256" ||
  fail "${tarball_name} is not listed in ${checksums_name}"
[ "$(wc -l <"${workdir}/tarball.sha256")" -eq 1 ] || fail "${tarball_name} is listed more than once"
(cd "$workdir" && sha256sum -c tarball.sha256) || fail "${tarball_name} does not match the signed checksums"
echo "OK: ${tarball_name} matches the signed checksums"

mkdir "${workdir}/extract"
tar -xzf "${workdir}/${tarball_name}" -C "${workdir}/extract" "$project" || fail "${tarball_name} has no ${project} binary"
file_output="$(file -b "${workdir}/extract/${project}")"
case "$file_output" in
  *"statically linked"*) ;;
  *) fail "${project} is not statically linked: ${file_output}" ;;
esac
echo "OK: ${project} is statically linked (${file_output})"

install -m 0755 "${workdir}/extract/${project}" "${outdir}/${project}"
echo "OK: ${outdir}/${project} verified"
