package skills

// BuiltinDebugRegression is the investigation protocol for regressions and
// failed-fix follow-ups in Astonish Code. Keep this short: the agent already
// has the files; this teaches how to treat evidence.
const BuiltinDebugRegression = `# Debug regressions

Use this when something used to work, a previous patch did not stick, or the user says the UI still does not match (live vs restore, badge vs summary, "zero difference").

Do **not** blame the binary, build cache, or environment after the user said they rebuilt. Do **not** invent invisible events. The user's restatement is the spec.

## 1. Recover history before more production edits

On the failing files/symbols, via ` + "`shell_command`" + ` (pagers are already disabled):

` + "```" + `
git log --oneline -20 -- <paths>
git log -p -S '<symbol>' -- <paths>
git blame -L <range> <path>
git show <commit>
` + "```" + `

Find the last commit where the user-visible contract held, and which later commit undid it. Uncommitted diffs count.

## 2. Encode the user-visible sequence as a failing test

Write (or restore) a table test of the sequence the user sees, for example:

- live: user → routing/event → text → tool
- restore from history
- tool-only responses
- a later call must not restamp an earlier bubble

Confirm the test **fails on current HEAD**. Package tests of unrelated code are not proof.

## 3. Patch until that test passes

Change only what the test requires. Do not "fix" by deleting the test. Do not mix a new requirement into a rewrite of a working fix.

## 4. One source of truth

If two counters disagree (totals vs per-item badges, live vs restore), the UI is wrong — do not add a third. Related surfaces must read the same ordered per-call record.

## 5. Silent gates

User-visible text behind ` + "`if flag && value > threshold`" + ` needs a test that forces both sides. Log lookup misses at Debug; do not omit a clause with no test that the lookup succeeds for real IDs.

## 6. Stop using the user as the first harness

Logic that can be unit-tested must be unit-tested before asking the user to rebuild and screenshot again.
`
