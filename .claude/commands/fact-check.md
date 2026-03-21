# Fact-Check Code Claims

Fact-check the user's question by verifying every claim through test execution. Uses parallel worktrees for speed.

**User's question**: $ARGUMENTS

## Phase 1: Capture Uncommitted Changes

Before anything else, capture the user's uncommitted work so worktree agents can reproduce it. Generate two patch files capturing staged, unstaged, and untracked changes:

```bash
# Capture staged + unstaged changes (tracked files)
git diff HEAD --binary > /tmp/fact-check-tracked.patch

# Capture list of untracked files and their contents
git ls-files --others --exclude-standard -z | xargs -0 tar cf /tmp/fact-check-untracked.tar 2>/dev/null || true
```

Save the paths to these files — they will be passed to each worktree agent. If both are empty (no diff output, no untracked files), the working tree is clean and no patch step is needed.

## Phase 2: Claim Decomposition (main context)

Use a single Agent (subagent_type=Explore) to investigate the question and break it into discrete factual claims about code behavior. This agent runs in the **main context** (not a worktree), so it can read uncommitted and untracked files in the working tree — this is intentional, as claims may reference code that only exists locally. The agent should:

1. Read relevant source code to understand the functions/behaviors being asked about. **Include uncommitted and untracked files** — the user's question may reference new code that hasn't been committed yet. If a claimed function or type only exists in uncommitted work, note this so the worktree agents know to apply the patches before investigating.
2. Classify each claim as:
   - **TESTABLE**: Can be verified by writing and running a Go test
   - **UNTESTABLE**: Subjective, stylistic, or architectural opinion
3. For each TESTABLE claim, identify:
   - The target package path (e.g., `./x/thorchain/`)
   - The test framework used in that package (GoCheck vs standard `testing`)
   - Key function signatures and test helpers available
4. **Group claims by target package** — claims targeting the same package should be batched together so they can share a single worktree agent and test file. Each group gets one worktree.
5. For each TESTABLE claim, assign a **confidence tier** prediction:
   - **HIGH**: The test directly calls the function under test and asserts the exact behavior claimed
   - **MEDIUM**: The test exercises the behavior indirectly (e.g., through a handler or manager) or asserts a proxy
   - **LOW**: The test can only partially verify the claim (e.g., tests one code path but the claim is about all paths)
6. For claims that span multiple packages (e.g., "Bifrost sends X to THORNode which does Y"), identify all involved packages and flag as **CROSS-PACKAGE**. These get their own worktree with tests in the highest-level package that can import the others.
7. Return a structured list of claims grouped by package, with their classifications, framework info, and confidence tiers

Process all claims found — no cap on number.

## Phase 3: Parallel Verification (worktree agents)

Spawn one Agent per **claim group** (package), all in parallel, each with `isolation: "worktree"`. Each agent receives:
- The list of claims to verify for that package group
- The patch file paths from Phase 1 (if any)
- The target package path and test framework info from Phase 2
- The confidence tier for each claim
- Whether `--verbose` mode is active (see Guardrails)

For CROSS-PACKAGE claims, spawn a separate worktree agent that receives all involved package paths.

Each worktree agent follows this protocol:

### a. Apply uncommitted changes
If patch files were provided:
```bash
# Apply tracked changes
git apply --allow-empty /tmp/fact-check-tracked.patch 2>/dev/null || true

# Restore untracked files
tar xf /tmp/fact-check-untracked.tar 2>/dev/null || true
```
If the patch apply fails (e.g., conflicts), note this but continue — the committed code is still available for testing.

### b. Investigate
Read the relevant code to understand the function/behavior being claimed about. Identify function signatures and test helpers available.

### c. Write transient tests
Create `factcheck_transient_test.go` in the target package directory. Write **one test method per claim** in the group — this avoids redundant worktree overhead.

Rules:
- Before writing: check the target package's existing test files to determine test framework (GoCheck via `gopkg.in/check.v1` vs standard `testing`)
- If GoCheck: use suite name `FactCheckTransientSuite` with `var _ = Suite(&FactCheckTransientSuite{})`. No new `TestPackage` needed — the existing one picks it up. Name each method descriptively (e.g., `(s *FactCheckTransientSuite) TestClaim1_ReturnsErrorOnNilInput`).
- If standard `testing`: use regular `Test` functions
- For `x/thorchain` package: use `setupKeeperForTest(c)` for keeper access
- For panic/crash claims: use `recover()` or GoCheck's `PanicMatches`
- Each test method must include a comment with the exact claim being tested
- Reference helpers: `GetRandom*` generators in `x/thorchain/types/test_common.go`, `NewDummyMgrWithKeeper()` in `x/thorchain/managers_dummy_test.go`
- For CROSS-PACKAGE claims: write tests in the highest-level package that can import the others, or use integration-style tests that exercise the full call chain

### d. Build check
Before running tests, verify compilation:
```bash
go build -tags=mocknet ./path/to/package/
```
If this fails, fix the compilation error (up to 2 attempts). This catches errors faster than waiting for the full test runner.

### e. Run tests
```bash
go test -tags=mocknet -parallel=1 -count=1 -timeout <TIMEOUT> ./path/to/package/ -check.f "FactCheckTransientSuite"
```
If using standard `testing` framework, use `-run` instead of `-check.f`.

**Timeout rules** (scale by number of claims in the group):
- `x/thorchain` package (requires keeper setup): `-timeout <N * 120>s` where N is the number of claims in the group, capped at 600s
- All other packages: `-timeout <N * 30>s` where N is the number of claims in the group, capped at 300s

The `-count=1` flag ensures tests are never served from cache.

- Exit 0 = test passed
- Exit non-zero = capture stderr for failure details

### f. Meta-verify
After the test runs, evaluate your own test **for each claim**:
1. Re-read the test method and the claim it targets
2. Does this test actually prove/disprove the claim? Could it pass regardless of the claim's truth?
3. Check for false-positive patterns: tautologies, circular mocks, hardcoded values, tests that don't call the function under test
4. For critical claims (error handling, security, consensus): write `factcheck_adversarial_test.go` with a test that **would pass if the claim were false**. For example, if the claim is "handler rejects nil messages", the adversarial test sends a nil message and asserts it is accepted. Run the adversarial test:
   - If the adversarial test **fails** (as expected): the original claim is supported — the behavior truly differs from the opposite assertion
   - If the adversarial test **passes**: the original test is FLAWED — both a claim and its negation appear true, indicating the test doesn't actually exercise the behavior
5. Assign a final **confidence tier**:
   - **HIGH**: Test directly calls the function and asserts exact claimed behavior; adversarial test (if run) failed as expected
   - **MEDIUM**: Test exercises behavior indirectly or asserts a proxy; no adversarial contradictions
   - **LOW**: Test only partially covers the claim, or adversarial test was not feasible
6. Determine verdict: CONFIRMED or FLAWED with explanation

### g. Record and return result
Return a structured result **per claim** with:
- The claim text
- Status: one of `[VERIFIED]`, `[FALSIFIED]`, `[UNVERIFIED]`
- Confidence: `HIGH`, `MEDIUM`, or `LOW`
- Determination rules:
  - Test passed + meta-verified CONFIRMED -> `[VERIFIED]`
  - Test failed + failure reason clear -> `[FALSIFIED]` with notes on why
  - Meta-verification says FLAWED -> re-examine claim, attempt once more, or mark `[UNVERIFIED]`
  - Compilation error after 2 fix attempts -> `[UNVERIFIED]`
  - Timeout -> `[UNVERIFIED]`
  - Symbol/function not found -> `[FALSIFIED]` ("referenced code does not exist")
- Method description (how the test verified/falsified it)
- If falsified: expected vs actual behavior and explanation
- If `--verbose` mode: include the full test source code and complete test output

No cleanup is needed — the worktree is automatically discarded.

## Phase 4: Handle UNTESTABLE Claims (main context)

For each UNTESTABLE claim from Phase 2:
- Fall back to reading code
- Mark as `[UNVERIFIED - <reason>]` (e.g., "subjective", "architectural opinion")

## Phase 5: Assemble Output

Collect results from all worktree agents and the untestable claims. Present in this exact format:

```
## Fact-Check Results

**Question**: [user's question]

**Summary**: N/M claims VERIFIED, X FALSIFIED, Y UNVERIFIED

**Verdict**: [One-line overall assessment based on the results]
- All VERIFIED, 0 FALSIFIED → "**Fully accurate** — all testable claims confirmed."
- ≥75% VERIFIED, 0 FALSIFIED → "**Largely accurate** — most claims confirmed, some could not be verified."
- ≥50% VERIFIED, any FALSIFIED → "**Partially incorrect** — some claims are wrong (see Falsified Claims below)."
- <50% VERIFIED or majority FALSIFIED → "**Mostly wrong** — the majority of claims could not be confirmed or were disproven."

### Answer
[Full answer with inline annotations]
Function X returns Y when given Z [VERIFIED ↑HIGH]. The pool balance check
happens before the swap [VERIFIED ↑MEDIUM]. This is the most efficient
approach [UNVERIFIED - subjective].

### Verification Details
| # | Claim | Status | Confidence | Method |
|---|-------|--------|------------|--------|
| 1 | "Function X returns Z when given Y" | VERIFIED | HIGH | Asserted return value matches |
| 2 | "Handler rejects unsigned msgs" | FALSIFIED | HIGH | Unsigned msg was accepted |
| 3 | "Bifrost relays X to THORNode" | VERIFIED | MEDIUM | Cross-package integration test |
| 4 | "Most efficient approach" | UNVERIFIED | — | Subjective claim |

### Falsified Claims
[Only include this section if any claims were falsified]

**Claim N**: "[claim text]"
- **Expected**: [what the claim stated]
- **Actual**: [what the test revealed]
- **Why**: [explanation of the discrepancy]
```

If `--verbose` mode is active, append an additional section:

```
### Test Details
[For each claim, show the test source code and full test output]

**Claim N**: "[claim text]"
<test source code>
<full test output>
```

## Guardrails

- **No modifications to existing files** — neither the main context nor worktree agents should modify existing source files. Only create `factcheck_*_test.go` files (in worktrees, which are discarded).
- Max 2 compilation fix attempts per test
- Always use `-count=1` to bypass test cache
- Timeout: scales by claim count — `N * 120s` (cap 600s) for `x/thorchain`, `N * 30s` (cap 300s) for others
- Worktree agents do NOT need to clean up — worktrees are automatically removed
- If patch files from Phase 1 are empty, skip the apply step in agents
- **Verbose mode**: If the user's question contains `--verbose`, include full test source code and test output in the final report. Otherwise, show only the summary table.
- **Claim grouping**: Always batch claims by target package to minimize worktree count. Only create separate worktrees for distinct packages or CROSS-PACKAGE claims.
