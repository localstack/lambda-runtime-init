# Customized lambda-runtime-init for LocalStack

This customized version of the Lambda Runtime Interface Emulator (RIE) is designed to work with [LocalStack for AWS](https://www.localstack.cloud/localstack-for-aws)).

Refer to [debugging/README.md](./debugging/README.md) for instructions on how to build and test the customized RIE with LocalStack.

## Branches

* `localstack` main branch with the latest custom LocalStack changes
* `develop` and `main` are mirror branches of the upstream AWS repository [lambda-runtime-init](https://github.com/aws/aws-lambda-runtime-interface-emulator)

## Structure

| Directory                                | Description                                                                                                                                                                                                                                                      |
|------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `.github`                                | Build and release workflows                                                                                                                                                                                                                                      |
| `bin/`                                   | Target directory for binary builds (e.g., `aws-lambda-rie-x86_64`)                                                                                                                                                                                               | 
| `cmd/localstack`                         | LocalStack customizations                                                                                                                                                                                                                                        |
| ├── `main.go`                            | Main entrypoint                                                                                                                                                                                                                                                  |
| ├── `custom_interop.go`                  | Custom server interface between the Lambda runtime API and this Go init. Implements the `Server` interface from `lambda/interop/model.go:Server` but forwards most calls to the original implementation in `lambda/rapidcore/server.go` available as `delegate`. |
| `cmd/ls-mock`                             | Mock LocalStack component for smoke testing                                                                                                                                                                                                                      |
| ├── [`README.md`](./cmd/ls-mock/README.md) | Instructions for LS API<->RIE smoke testing                                                                                                                                                                                                                     |
| `debugging/`                             | Debug and test this Go init with LocalStack                                                                                                                                                                                                                      |
| ├── [`README.md`](./debugging/README.md) | Instructions for building and debugging with LocalStack                                                                                                                                                                                                          |
| `lambda`                                 | Original AWS implementation of the runtime emulator ideally kept untouched                                                                                                                                                                                       |

## Integrate Upstream Changes

Follow these steps to integrate upstream changes from the official AWS [lambda-runtime-init](https://github.com/aws/aws-lambda-runtime-interface-emulator) repository:

1. Open the [develop](https://github.com/localstack/lambda-runtime-init/tree/develop) branch on GitHub.
2. Click "🔁Sync fork" to pull the upstream changes from AWS into the develop branch.
3. Create a new branch based on the branch localstack `git checkout localstack && git checkout -b integrate-upstream-changes`.
4. Merge the upstream changes from develop into the new branch `git merge develop` and resolve any potential conflicts.
5. If needed, add a single commit with minimal changes to adjust the localstack customizations to the new changes.
6. Create a PR on Github against `localstack/lambda-runtime-init localstack` (️not against AWS as by default ⚠️).
7. **MERGING:** Manually merge the approved PR using `git checkout localstack && git merge --ff integrate-upstream-changes` and add the PR number as a suffix to the commit message. Example: `(#24)`. Do not squash any upstream commits!
8. Manually push `git push origin localstack` and close the PR on GitHub

Example PR that integrates upstream changes: https://github.com/localstack/lambda-runtime-init/pull/24

## Releases

Releases are cut from the `localstack` branch (not `main`/`develop`, which mirror upstream AWS).
The resulting binaries are consumed by localstack-pro via `LAMBDA_INIT_RELEASE_VERSION=<tag>` and
by lambda-images.

### Regular release

Regular releases are **automated**: pushing a version tag on `localstack` triggers the
[`build.yml`](./.github/workflows/build.yml) workflow, which runs the tests, builds the binaries
(`make compile-lambda-linux-all`), and publishes a GitHub release with auto-generated release notes
and the `bin/*` binaries attached. Versioning follows `vX.Y.Z` (e.g. `v0.2.0`), continuing from the
previous release (e.g. [`v0.1.47`](https://github.com/localstack/lambda-runtime-init/releases/tag/v0.1.47)).

1. Create a **lightweight** tag for the new version, on the commit you want to release. Match the
   prior convention
   ```bash
   git tag v0.2.0
   ```
2. Push the tag to trigger the release build:
   ```bash
   git push origin v0.2.0
   ```
   `build.yml` matches the `v*.*` tag pattern and publishes the release automatically. A tag ending
   in `-pre` is published as a pre-release.

> The `.github/workflows/release.yml` ("Release") workflow is **legacy** and is not used for
> LocalStack releases — it is a manual `workflow_dispatch` that checks out `main` (the upstream
> mirror) rather than `localstack`, so it would not include the LocalStack customizations.

### Weekly auto-release

[`weekly-release.yml`](./.github/workflows/weekly-release.yml) runs every Friday at 06:00 UTC. If
`localstack` has new commits since the highest existing release, it patch-bumps the version and calls
`build.yml` to run the tests, build the binaries, push the tag, and publish the release. Together with
Renovate automerge, this is what carries dependency and CVE fixes downstream without manual work.

The release is published as a **pre-release**, and only reaches consumers once it has been validated:

1. `weekly-release.yml` publishes `vX.Y.Z`, marked as a pre-release.
2. localstack-pro opens a PR bumping `LAMBDA_RUNTIME_DEFAULT_VERSION` to that version; its CI is the
   quality gate.
3. On merge, localstack-pro flips the same release to a full release through the GitHub API — no new
   tag and no rebuild, so the binaries that were validated are the binaries that ship.
4. lambda-images ignores pre-releases, so Renovate only opens a bump PR there after the promotion.

Run it manually via the **Weekly Release** workflow (`workflow_dispatch`); `dryRun` reports the next
version without releasing. A failed run posts to Slack.

### RC (release candidate) pre-release

RC pre-releases let an **unmerged** PR be tested against localstack-pro CI without cutting a real
release. They are fully automated via `.github/workflows/rc-release.yml`:

1. Add the label `trigger:rc-release` to the PR.
2. The workflow builds the binaries from the PR head, publishes a throwaway GitHub **pre-release**
   tagged `v0.0.0-rc.pr<N>-<sha>`, and comments the tag + download URLs back on the PR.
3. Test the PR by setting `LAMBDA_INIT_RELEASE_VERSION=v0.0.0-rc.pr<N>-<sha>` in localstack-pro CI.
4. The pre-release and its tag are **deleted automatically** when the PR is closed.

Unlike a regular release, an RC is built from an **unmerged PR head** and published as a throwaway
pre-release that is auto-deleted on close — localstack-pro CI is the real test. Both flows run the
unit tests (`make tests-with-docker`); neither runs integ-tests.

## Custom LocalStack Changes

Document all custom changes with the following comment prefix `# LOCALSTACK CHANGES yyyy-mm-dd:`

* Everything in `cmd/localstack`, `cmd/ls-mock`, and `.github`
* `Makefile` for debugging and building with Docker
* `internal/lsapi` LocalStack-only package with the request/response types of the LocalStack <-> RIE HTTP API
* 2023-10-17: `lambda/rapidcore/server.go` pass request metadata into .Reserve(invoke.ID, invoke.TraceID, invoke.LambdaSegmentID)
