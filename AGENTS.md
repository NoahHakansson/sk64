# AGENTS.md

Orientation for anyone — human or agent — changing this codebase.

sk64 is a Bubble Tea TUI for editing Kubernetes Secrets and ConfigMaps.

## Commands

```sh
go build ./...
go test -race ./...
go vet ./...
(cd e2e && go test -race ./...)    # hermetic: starts and tears down its own apiserver
go test ./internal/tui -update     # regenerate golden files
go test -run FuzzX -fuzz FuzzX ./internal/resyaml   # fuzz targets live in resyaml and k8s
golangci-lint run                  # must be 0 issues; also run it in ./e2e
./hack/covergate.sh                # ≥85% on k8s, store, project, editor, diff
gofmt -l .                         # must print nothing
```

Go 1.26+. `helm` and `kustomize` are optional at runtime and in tests. Linux and macOS only — **do not add Windows build tags, fallbacks, or CI targets.**

## No test ever touches a real cluster

This is absolute. There is no opt-in, no environment variable, no documented destructive
mode, no `--yes-i-mean-it` flag. An escape hatch is itself the bug: no gate that points
tests at an existing cluster may exist, and none may be added.

- **Unit tests** use `k8s.io/client-go/kubernetes/fake`, with reactors injecting 409s,
  403s, timeouts and dry-run rejections. Every test binary whose package imports
  `internal/k8s` calls `kubetest.IsolateAmbientCluster` from `TestMain`; the positive
  guard in `internal/k8s/kubeconfig_isolation_test.go` fails when a package omits it.
  The helper redirects both kubeconfig loading paths and disables client-go's in-cluster
  fallback before any package tests run.
- **The e2e suite provisions its own throwaway cluster.** `e2e/harness_test.go` starts a
  local `kube-apiserver` + `etcd` pair with `sigs.k8s.io/controller-runtime/pkg/envtest`,
  bound to loopback on a random port, and stops it when the suite ends. Nothing survives
  the test process, because nothing outside it ever existed.
- **The ambient kubeconfig is unreachable by construction, not by policy.** The suite
  talks to the apiserver only through `k8s.NewForConfig(restConfig, namespace)`, using the
  `*rest.Config` the harness itself produced. It never calls `k8s.New`, never imports
  `k8s.io/client-go/tools/clientcmd`, and `TestSuiteNeverLoadsKubeconfig` fails if it
  starts to. `startControlPlane` repoints `HOME` and `KUBECONFIG` at an empty temp
  directory, clears the in-cluster environment and `USE_EXISTING_CLUSTER`, pins
  `envtest.Environment.UseExistingCluster` to false, and `requireLoopback` aborts the
  suite unless the resolved API host is a loopback address.
- **`e2e/` is a separate Go module on purpose.** The shipped module graph carries none of
  the test-only dependencies, and `go test ./...` at the repo root cannot compile the
  suite, let alone run it. Its `replace github.com/NoahHakansson/sk64 => ../` is a
  same-repo path replacement and is permitted; replacements to forks are not. Lint and
  `go mod verify` must be run inside `e2e/` too.
- The envtest control-plane binaries are downloaded once, sha512-verified against a
  **pinned** `controller-tools` index commit, and cached; after the first run the suite
  needs no network. Bump `envtestVersion` and `envtestIndexURL` together, by hand, when
  the `k8s.io/*` minor moves.

If a future assertion genuinely needs controllers — a ReplicaSet appearing, a Deployment
status, namespace garbage collection, a Pod actually running — swap `startControlPlane`
for the testcontainers-go k3s module (`github.com/testcontainers/testcontainers-go/modules/k3s`).
Nothing else in the suite changes: it only ever sees a `*rest.Config`, and the loopback
guard and environment scrubbing still apply. Do not reintroduce a knob that points the
suite at a cluster someone else owns.

## Layout

```
cmd/sk64/          flags, kubeconfig loading, pre-TUI connectivity probe, TUI entry
internal/
  config/          ghostty-style user config: keybind grammar, actions, reserved-set validation
  k8s/             everything that talks to the apiserver
    contexts.go      context+server identity resolution and per-process switching
    resource.go      Resource interface (Secret + ConfigMap behind one type)
    save.go          dry-run → RV-guarded Update → conflict → GET-and-verify retry
    create_delete.go Create, and Delete with UID+RV preconditions
    refwalk.go       pure pod-spec walker: every secret/configmap reference location
    reverse.go       RefIndex: resource → consumers (workloads, pods, ServiceAccounts)
    workloads.go     list Deploy/STS/DS/Job/CronJob, pod template per kind
    rollout.go       restartedAt annotation patch
    validate.go      well-known Secret type checks
  tui/             one file per screen, plus app.go (root model + screen stack)
    project_context_confirm.go project switch/rebind confirmation and probing
  store/           SQLite: projects, context identities, link origins, ui_state. Metadata only, never values
  project/         cwd→project resolution, repo scanner, helm/kustomize exec boundary
  editor/          $EDITOR invocation, secure tempdirs, hex rendering
  resyaml/         whole-resource YAML: round-trip-exact serialize/parse
  diff/            plaintext diff rendering
  undo/            in-memory ring of recent saves
  debuglog/        opt-in log; API physically cannot accept value bytes
  kubetest/        IsolateAmbientCluster: kubeconfig + in-cluster isolation for test binaries
  natsort/         natural ordering for names with numeric runs
e2e/               separate module; hermetic envtest control plane, no ambient kubeconfig
```

## The two absolute rules

Both are enforced by reflection tests (`internal/store/novalues_test.go`, `internal/debuglog/novalues_test.go`) that fail if any exported method can accept value bytes:

1. **No secret value reaches the SQLite store.**
2. **No secret value reaches the debug log.**

A change that needs to break either rule needs a different design.

Project-store availability never blocks ordinary cluster browsing. A corrupt database and
its WAL files are moved aside before a fresh store is created; other open failures disable
project features for that process and surface an error without blocking cluster access.

## TUI conventions

**Screens** implement `screen`: `Init/Update/View/SetSize/Title/Hints/Help/CapturesInput/WantsEsc`. `app` owns a screen stack and pops on `esc`.

**Navigation happens only through messages** — `pushScreenMsg`, `popScreen`, and `replaceScreenMsg` for an atomic pop+push. Never use `tea.Sequence`: the golden-test harness flattens `tea.Batch` only. If you need ordering, use one message that does both things.

**Key messages go to the overlay or top screen; data messages broadcast to the whole stack.** Staleness is handled by the loader, not by routing. Bubbles `list.FilterMatchesMsg` has no owner identity. Every filterable screen, overlay and form list scopes update and `SetItems` commands with `scopeListFilterCmd` and routes messages through `updateListModel`; never forward raw filter results to `list.Update`.

**Every cluster and SQLite call runs through `loader`** (`internal/tui/loader.go`): a package-global atomic request ID plus a cancel function, with contexts parented on the program context. This gives in-flight dedup for `ctrl+r`, `esc` cancellation, and stale-result rejection for free. Never block the event loop; never poll with `time.Sleep`.

**List data is paginated and refreshed, never watched.** Screens render `Limit`/`Continue`
pages as they arrive and refresh on entry or `ctrl+r`. Do not add informers or watches: the
resourceVersion save guard does not need a cache, and a cache would add another staleness model.

**Background commands must not read shared mutable state.** Snapshot first — `res.Clone()` for resources, copied locals for everything else. The edit flow's dry-run and save paths do this deliberately.

**Hint lines are ASCII-only, at most 78 columns, and non-capturing browsing screens end with `? help`.** Footer, help, and actionable state-line copy derive from the `key.Binding` keymaps in `internal/tui/keymap.go`, with user config overrides applied before screen construction. `Hints()` returns binding-backed `footerHints`; use `hintDesc` for state-specific wording, `bindingAction` for state-line instructions, `displayHint` for non-dispatchable affordances, and `hintStatus` for cannot-cancel states. The app injects navigation and help groups, and disabled bindings vanish from both the footer and binding-backed `Help()` entries. `TestHintLinesFitEightyColumns` enforces the budget; detailed keys belong in `Help()`, not the footer.

**Quitting is deliberate.** `Q` is the default rebindable quit key. `ctrl+c` always requires two presses within two seconds; the first press replaces the footer with a warning, any other key disarms it and still runs normally, and an unsaved edit supplies the more specific warning and cleanup while the app owns the arm state.

**Every mutation ends in a typed YES gate; `esc` always cancels.** Uppercase keys initiate or
advance mutating flows — `N` new, `D` delete, `R` restart, `Y` proceed — so a stray keystroke
into a focused terminal cannot fire one, but no keystroke ever commits. The commit itself is
the shared `confirmGate` (`internal/tui/typed_confirm.go`): a dialog stating exactly what is
about to happen, an input that accepts only the literal uppercase word `YES`, and `enter`. This
gates cluster writes (save, create, key delete, restart), SQLite writes (project create/edit,
link, unlink, context rebind), and plaintext export. Two deliberate exceptions: resource delete
keeps its stronger exact-name gate instead of YES, and passive bookkeeping writes with no
triggering user action (`BackfillProjectKubeServer`, `SetLastProject`) stay silent. `esc` at a
gate closes it with nothing dispatched; once dispatched, the existing cannot-cancel states are
unchanged. `enter` otherwise accepts only where the user typed or selected the answer
(`deleteConfirm`'s exact-name gate, `keyNamePrompt`, `filePromptScreen`, `createPrompt`, any
list row); it is never an alias for `Y`. A near miss is answered, not ignored: lowercase
`y`/`enter` on a `Y` prompt nudges with `pressYToConfirm`, and a lowercase `yes` at a gate gets
`type YES in capitals to confirm`. Lowercase `n` survives only where "no" differs from
"cancel" — the exec-plugin offers return to the list while `esc` closes the overlay.

**Confirmations are centered dialogs; evidence screens are not.** A screen whose whole job is
answering a question renders through `dialog.render` (`internal/tui/dialog.go`): a centered,
bordered panel of title, body, warnings, prompt and message, which drops whole body entries and then
truncates warnings when the body rectangle is short, and never drops the prompt. A screen whose
job is showing evidence — the edit flow's `phaseDiff` and `phaseConflict` — stays full-width and
full-height with a styled header pinned above the scrolling viewport. Do not box a diff: a centered
panel leaves 66 usable columns, which is worse than the terminal width for the long single-line
values these diffs exist to show.

**`w` toggles wrapping in the diff views, and it must stay `viewport.SoftWrap`.** Wrapping is off by
default so the rendered diff matches the stored bytes. It is the viewport's own soft wrap, applied
after `styledDiff` has classified whole logical lines — never a pre-wrap of the content. Two reasons,
both measured: the viewport wraps with `ansi.Cut`, which is lossless and re-emits the active SGR on
every fragment, whereas `ansi.Wrap`, `ansi.Wordwrap` and `lipgloss`-width wrapping consume the
whitespace at the break (121 chars in, 120 out on a real secret value) and leave continuation
fragments unstyled once the first fragment scrolls away. Dropping a byte from a secret diff means
showing the user a value that is not in the cluster. Pre-wrapping would also let a fragment that
happens to begin with `-` or `+` be mis-styled as a deletion or addition; classifying whole lines
first makes that impossible rather than merely guarded.

**Golden tests** drive `Update` with synthetic messages and assert on `View()`. The harness isolates `KUBECONFIG` via `t.Setenv` so tests never touch a real cluster or a developer's config. Regenerate with `-update` and **read the diff** before committing.

## Project context safety

**Project creation is one staged form state machine.** It starts with scan-or-manual selection, then fields, a selection-only context list, identity resolution, the typed YES gate, and the non-cancellable store write. The repository scan and kubeconfig reads run through the form's `loader`; stale results are rejected by request ID. Scan hints may auto-select a context only when exactly one hint matches a locally available context. Ambiguous or unavailable hints fail closed. Context is never a text input: the chooser uses `k8s.ListContexts`, and final validation uses `k8s.ResolveContextIdentity`; neither path probes a cluster, switches the app client, or writes kubeconfig. Create the project and its additional namespaces atomically with `CreateProjectWithNamespaces`.

**A context switch is local to one sk64 process.** `k8s.SwitchContext` builds and probes a new client with kubeconfig overrides; it never writes kubeconfig or changes `current-context`. Install the client through the existing messages so `kubectl` and other sk64 processes remain unaffected.

**A project's cluster identity is its context name plus API server URL.** A stored-server mismatch requires an explicit rebind even when automatic switching was previously selected; a missing context may be rebound only when exactly one context points to the saved server, and ambiguous identity fails closed. Re-probe the target and compare it with the server shown in `projectContextConfirm` before persisting a binding, because kubeconfig can change while the dialog is open.

**Links retain their origin context and server.** Mark an origin mismatch in the project view, and never open cluster-backed workload or resource rows while the project context is inactive or its server identity mismatches the active client. Legacy links with empty origins remain associated with the project for migration compatibility.

**Changing API servers invalidates undo history.** Every app route that accepts a replacement client resets the in-memory undo ring when `Client.Server` changes, even if the context name stayed the same, so values from one cluster cannot be replayed onto another.

## Edit flow

`internal/tui/edit_flow.go` is one state machine — one phase enum, one switch — shared by all flow targets (single key, whole resource, import, undo, create). Extend it; do not fork it.

Its invariant: **an aborted edit must leave no trace in the shared resource object.** Edits are applied to the resource before dry-run, so `abort()` and the conflict path restore the original value when the `applied` flag is set. Any new exit path must maintain this, or the key screen will display unsaved values as if they were cluster state.

A second invariant: **the YAML-document path never touches a binary key.** Binary values are
omitted from the editable document, so `originalMap` carries no record of their bytes — a
collision that slips into `applyChanges` cannot be restored or undone, only deleted, which turns
abort and ctrl+z into silent data loss. `editedMap` is therefore validated with `binaryCollision`
at every route into `applyChanges`: `finishEditor` after parsing, `confirm()` (the choke point the
revert flow enters through), and `enterConflict` against the **refreshed** `binaryKeys`, because a
key can become binary on the cluster mid-edit. A new flow target or path into `phaseDiff` must
keep hitting one of these guards; rejections land in `phaseBinaryCollision`, whose dialog and
hints stay mode-aware (`e` reopens the editor, except in proposed mode where it returns to the diff).

## Save flow

The originally fetched object is the source of truth; there is deliberately **no pre-fetch before saving** — that would refresh the `resourceVersion` and mask the very concurrent edit the guard exists to catch.

1. Apply the edit to the original object, run `Validate()` (warnings with override, never hard blocks).
2. `Update` with `DryRun: ["All"]` — catches webhook and validation rejections before anything is written.
3. The typed YES gate (`phaseCommitGate`) — the final human decision sits after the dry-run so no
   typing is wasted on a rejected save and the dialog can state precisely what will be written.
   `esc` here returns to the diff without dispatching; every save path (plain, validation
   override, no-dry-run override, conflict re-apply) passes exactly one gate per write.
4. Real `Update` with the original `resourceVersion` → the apiserver 409s on conflict.
5. On 409: show cluster-vs-mine, offer to re-apply the user's value against the fresh version. Never discard their work.
6. On ambiguous network failure: GET and check whether the write landed before retrying (≤3, backoff).

Secrets write `data`, never `stringData`, and mutate only `data` on the original object so type, labels, and annotations survive by construction.

## Testing expectations

Tests ship with the change. Table-driven, named cases, `t.Helper()` in helpers, no sleeps where synchronization belongs. Cluster logic is tested against `k8s.io/client-go/kubernetes/fake` with reactors injecting 409s, 403s, timeouts, and dry-run rejections — note that the fake tracker ignores delete preconditions, so precondition tests use a `PrependReactor` that inspects the delete options.

Every root-module test binary that imports `internal/k8s` must call
`kubetest.IsolateAmbientCluster` from `TestMain`; individual `t.Setenv` overrides remain
valid for fixtures. No test in `e2e/` may use kubeconfig loading rules at all — see
"No test ever touches a real cluster".

Don't test other people's code: no tests of the fuzzy-filter library's ranking, no permutation padding.

## Style

Smallest clear implementation. No speculative abstraction, no single-call-site wrappers, no options structs nobody asked for. Self-describing names over comments. Errors wrapped with `%w` and context, matched with `errors.Is`/`errors.As`. No `TODO`s, no stubbed error paths, no `panic` outside genuinely unreachable code.

New dependencies need real justification and vetting; copy import paths verbatim rather than from memory.
