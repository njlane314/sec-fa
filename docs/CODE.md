# Coding Standard

## 1. Scope

This standard applies to production code in this repository. Class A code receives the strictest interpretation.

Class A currently includes:

```text
abi/core.h
engine/*.cpp
broker submission code when implemented
reconciliation code when connected to a live broker
ledger mutation code for order/risk state
```

## 2. C++ rules for Class A

1. No recursion.
2. No `goto`, `setjmp`, or `longjmp`.
3. No exceptions across the ABI boundary. The current build uses `-fno-exceptions`.
4. No RTTI in the core build.
5. No network, filesystem, environment, or wall-clock access inside the C++ model/risk core.
6. Every exported function returns an explicit status code.
7. Every output object has a matching free function.
8. No unchecked external response in broker code.
9. No direct broker access outside the broker submitter.
10. All warnings are errors.
11. Sanitizer builds must pass before release.
12. Static analysis must pass before release.
13. Public structs are versioned by `abi_version`.
14. No STL type is exposed across the C ABI.
15. No portfolio-plan or valuation-plan function is allowed to submit, stage, or mutate broker state.

## 3. Rust CLI and Go service rules

Rust is used for the CLI/parser and Go is used for network-facing service work. Neither is the ultimate risk authority.

Rules:

```text
stdout is machine-readable
stderr is diagnostic
all commands have explicit exit codes
mutating commands emit append-only events
no broker credentials outside broker submitter
no arbitrary SQL in CLI arguments
network calls require explicit User-Agent where required
```

Rust and Go code may use dynamic allocation, libraries, SQLite, XML parsing, and network I/O. They must remain outside the pure C++ core.

The accession XBRL parser and canonical observation resolver in `filings/` are explicit Class B data logic, not mere orchestration. They may remain in Rust because XBRL parsing and source-fact normalization are messy data-ingestion work, but their resolver rules must stay named, tested, and auditable. They must not stage, submit, or approve orders. If canonicalization becomes part of execution authority, it must move behind a separate reviewed engine boundary instead of being hidden inside CLI command flow.

The Rust `value` command is orchestration only for valuation inputs: it loads canonical observations, calls `fa_build_statement_snapshots_v1`, then persists the returned statement snapshots. It must not rebuild TTM flows, choose statement anchors, or assign statement-quality flags in Rust.

## 4. Exit-code convention

```text
0 success
1 user/input error
2 external dependency failure
3 invariant/risk failure
4 unavailable/stale state
5 internal software fault
```

## 5. Build discipline

The repository is acceptable only if this passes:

```sh
./check
```

The check script builds the C++ core, Rust CLI/parser, and Go service, runs tests, initializes a database, runs a synthetic model/risk path, and verifies that stale reconciliation blocks approval.
