// Package release holds tests that guard the release-pipeline contract: the
// three commands cross-compile to static (CGO_ENABLED=0) binaries whose
// SHA256SUMS are cosign-signed (keyless) such that they actually verify.
//
// Two parallel pipelines currently implement this contract, and both are
// guarded here: the pre-existing .github/workflows/ci.yml + scripts/
// verify-release.sh (release_test.go), and the self-hosted-Zuul job config
// (zuul.d/, playbooks/, scripts/build-release-artifacts.sh) this repo is
// migrating to per docs/tasks/release-verify.md — NOT GitHub Actions, NOT
// cosign-on-GitHub going forward (zuul_test.go). The GitHub Actions pipeline
// is left running until the Zuul one is proven live; cutover is a follow-up.
// These tests are the regression guard for the release-verify task family (a
// fix must ship tests, and nothing else exercises the release wiring).
package release
