// Package release holds tests that guard the release-pipeline contract: the
// three commands cross-compile to static (CGO_ENABLED=0) binaries whose
// SHA256SUMS are cosign-signed (keyless) such that they actually verify. The
// pipeline lives in .github/workflows/ci.yml, scripts/verify-release.sh and the
// docs; these tests are the regression guard for the release-verify task
// (a fix must ship tests, and nothing else exercises the release wiring).
package release
