# Skill: refactor-without-breaking

## Purpose

Restructure code (rename, split, merge, simplify) while keeping behaviour identical and tests green at every step.

## When To Use

- You want to clean up a function, package, or module.
- You want to extract or inline.
- You want to rename something exported.

## Steps

### 1. Confirm the baseline is green

```sh
make test
make lint
```

If the baseline is red, **stop**. Fix it first. Refactoring on top of a broken baseline destroys your safety net.

### 2. Take small steps

A refactor is a sequence of small, reversible changes. Each change keeps tests green.

Bad: "I will rename, split, and move this function in one commit."
Good: "I will rename in one commit. Then split in the next. Then move in the third."

### 3. Lean on the compiler

For renames, deletions, signature changes — let the compiler tell you what else needs to change. Run `make build` after each step. Address compile errors before doing anything else.

### 4. Run tests after every step

```sh
make test
```

Not every couple of steps. Every step. The cost is seconds; the value is certainty.

### 5. Commit each step separately

Each step gets its own commit:

```
refactor(application): rename CreateNotification.Run to Execute
refactor(application): extract validateInput to private method
refactor(application): move idempotency check before validation
```

Reviewers can review each step in isolation. If one step turns out to be wrong, it is one revert, not a rewrite.

### 6. Do not change behaviour during refactor

A refactor that adds a feature or fixes a bug is **not a refactor**. It is two things. Split them:

1. Refactor first (commits prefixed `refactor:`).
2. Then change behaviour (commits prefixed `feat:` or `fix:`).

If you discover a bug while refactoring, write it down. Finish the refactor. Then fix the bug as a separate commit.

### 7. Run linter after every step

Some refactors introduce dead code or unused imports. Catch them as you go:

```sh
make lint
```

### 8. Run the full test suite at the end

```sh
make test-all
```

Not just the unit tests. Integration tests catch things unit tests miss — especially repository refactors.

## Verification

- [ ] Every commit individually has green tests.
- [ ] `make test-all` and `make lint` pass at HEAD.
- [ ] No behaviour change snuck in (no new conditions, no new defaults, no new fields).
- [ ] The diff is reviewable: each commit does one named thing.

## Common Mistakes

- Big-bang refactor: one giant commit that "rewrites a package." Impossible to review, impossible to revert cleanly.
- Mixing refactor and behaviour change. The reviewer cannot distinguish "I improved this" from "I changed what this does."
- Skipping the lint and test cycle between steps because "this step is trivial." Trivial steps add up to subtle breakage.
- Renaming exported symbols without updating all callers in the same commit. Compile error in `main`.
- Refactoring code that nobody calls anymore. Delete it instead; that is a separate commit.
- Refactoring "while in the area" during a feature PR. Bundle the changes; reviewers cannot tell what was the feature and what was the cleanup.
