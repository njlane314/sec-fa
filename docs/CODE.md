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
15. No plan or value function is allowed to submit, stage, or mutate broker state.

## 3. Go rules for service shell

Go is used for orchestration and data movement. It is not the ultimate risk authority.

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

Go code may use dynamic allocation, libraries, SQLite, XML parsing, and network I/O. It must remain outside the pure C++ core.

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

The check script builds the C++ core and Go CLI, runs tests, initializes a database, runs a synthetic model/risk path, and verifies that stale reconciliation blocks approval.
