# Run CI Checks and Fix Issues

Run all CI checks and automatically fix any issues found.

## Workflow Overview

Execute CI checks in logical order, fixing issues as they're discovered:

1. **Generate** - Regenerate code (proto, openapi, go generate)
2. **Format** - Fix formatting issues
3. **Unit Tests** - Run and fix failing tests
4. **Regression Tests** - Run and fix failing regression tests
5. **Lint** - Fix linting errors (last, to catch anything introduced by fixes)

## Phase 1: Code Generation

First, ensure all generated code is up to date:

1. Run `make generate` to regenerate all generated files
2. Run `make proto-format` to format protobuf files
3. Check `git status` for any generated file changes
4. If changes exist, report them - these should be committed

## Phase 2: Formatting

Fix code formatting issues:

1. Run `goimports -w .` on all Go files to fix imports and formatting
2. Check for any remaining formatting issues with `git diff`
3. Report files that were modified

## Phase 3: Unit Tests

Run unit tests and fix failures:

1. Run `make test` to execute all unit tests
2. If tests fail:
   - Analyze the failure output
   - Read the test file and the code being tested
   - Determine if it's a test bug or code bug
   - Apply the appropriate fix
   - Re-run the specific failing test to verify
3. Continue until all unit tests pass

### Running Specific Tests

To run a specific test:

```bash
go test -tags=mocknet -parallel=1 -run TestName ./...
```

## Phase 4: Regression Tests

Run regression tests and fix failures:

1. Run `make test-regression CI=1 EXPORT=1 DEBUG=1`
2. If tests fail:
   - Analyze the failure output and exported state
   - Check if the test expectations need updating
   - Or if the code logic needs fixing
   - Apply the fix
3. For specific regression tests, use `RUN=test-name`

### Updating Regression Test Expectations

If test output changed due to intentional behavior changes:

```bash
make test-regression RUN=test-name AUTO_UPDATE=1
```

## Phase 5: Linting

Run linter and fix issues (last to catch anything from previous fixes):

1. Run `make lint` to check for linting errors
2. For each linting error:
   - Read the affected file
   - Understand the issue
   - Apply the fix
   - Verify the fix resolves the lint error
3. Re-run `make lint` until it passes

### Common Lint Fixes

- **unused variable**: Remove or use the variable
- **ineffectual assignment**: Remove or restructure the code
- **shadowed variable**: Rename the inner variable
- **error not checked**: Add proper error handling
- **missing return**: Add missing return statement

## Error Resolution Strategy

When fixing errors:

1. **Read first**: Always read the affected code before making changes
2. **Understand context**: Check related code to understand the full picture
3. **Minimal changes**: Make the smallest change that fixes the issue
4. **Verify**: Re-run the specific check to confirm the fix works
5. **Don't break others**: Ensure fixes don't introduce new failures

## Reporting

After each phase, report:

- ✅ Checks that passed
- 🔧 Issues that were fixed (with file:line references)
- ❌ Issues that couldn't be automatically fixed (need human review)

## Final Summary

At the end, provide:

1. Total issues found and fixed
2. Any remaining issues requiring manual intervention
3. Files modified during the process
4. Recommendation on whether code is ready for commit

## Important Notes

- Always use `CI=1 EXPORT=1 DEBUG=1` flags for regression tests
- Use `goimports -w` not `go fmt` for Go formatting
- Run `make generate` before other checks (generated code affects tests)
- If proto files changed, always run `make proto-format`
- Commit generated file changes separately from manual fixes
