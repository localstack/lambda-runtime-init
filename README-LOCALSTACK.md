# Customized lambda-runtime-init for LocalStack

This customized version of the Lambda Runtime Interface Emulator (RIE) is designed to work with [LocalStack](https://github.com/localstack/localstack).

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
| `cmd/ls-api`                             | Mock LocalStack component for testing (likely outdated)                                                                                                                                                                                                          |
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

## Custom LocalStack Changes

Document all custom changes with the following comment prefix `# LOCALSTACK CHANGES yyyy-mm-dd:`

* Everything in `cmd/localstack`, `cmd/ls-api`, and `.github`
* `Makefile` for debugging and building with Docker
* 2023-10-17: `lambda/rapidcore/server.go` pass request metadata into .Reserve(invoke.ID, invoke.TraceID, invoke.LambdaSegmentID)
