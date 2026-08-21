# sk64 — Super Kube 64

A terminal UI for browsing and editing Kubernetes **Secrets** and **ConfigMaps**, organized around **projects**. Open a resource, edit a value in `$EDITOR` with base64 handled transparently, preview the diff, save. Start it inside a repo and it opens that repo's project automatically.

sk64 is a single static binary. The only runtime dependency is your editor.

```
sk64
```

## Why

`kubectl edit secret` hands you base64 and hopes for the best. sk64 decodes values for editing, shows a plaintext diff, runs a server-side dry-run before it writes, guards the write with the original `resourceVersion` so a concurrent change can never be silently clobbered, and tells you which workloads consume the secret **before** you change it.

## Scope

sk64 deliberately keeps a narrow trust model and edit surface:

- It works with native Secrets and ConfigMaps, not Sealed Secrets, External Secrets, Vault, or other external-secret systems.
- One cluster is active at a time. Context switching is first-class, but there is no federated multi-cluster view.
- It follows native pod-spec references, including CSI volume secret references, but does not interpret vendor-specific SecretProviderClass resources.
- Project configuration is per-user metadata in SQLite, not a team-shared file committed to the repository.
- A value is the unit of editing; sk64 does not edit nested fields inside JSON, YAML, or another structured value.

## Install

```sh
go install github.com/NoahHakansson/sk64/cmd/sk64@latest
```

Or download an archive (`sk64_<version>_<os>_<arch>.tar.gz`) from the [releases page](https://github.com/NoahHakansson/sk64/releases). Each release ships per-archive SPDX SBOMs and a `checksums.txt` whose keyless [cosign](https://docs.sigstore.dev/) signature proves the checksums were produced by this repository's release workflow:

```sh
cosign verify-blob \
  --bundle checksums.txt.cosign.bundle \
  --certificate-identity-regexp 'https://github\.com/NoahHakansson/sk64/\.github/workflows/release\.yml@refs/tags/v.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
shasum -a 256 -c --ignore-missing checksums.txt
```

Linux and macOS, amd64 and arm64. Windows is not supported. `go install` builds report `sk64 dev` from `--version`; release binaries carry the real version.

## Quick start

| | |
|---|---|
| `sk64` | Browse the current context: namespaces → resources → keys |
| `sk64 -n production` | Start in a namespace |
| `sk64 --read-only` | Look, don't touch: every mutating path is disabled |
| `sk64 --project myapp` | Open a saved project |

Press `?` on any screen for keys in context.

## Editing

Pick a key, press `enter`, and sk64 opens the decoded value in `$EDITOR`. On save it shows a plaintext diff (never base64, on either side), runs `Update` with `DryRun: ["All"]` so admission webhooks and validation reject the change *before* anything is written, then asks you to type `YES` in a dialog stating exactly what is about to happen, and writes with the original `resourceVersion`.

If an admission webhook does not support server-side dry-run, sk64 stops before writing and explains that preflight validation is unavailable. `Y` proceeds to the typed confirmation; `e` reopens the editor and `esc` aborts.

If the resource changed in the cluster while you were editing, the apiserver returns 409 and sk64 keeps your work: you get a diff of cluster-current versus your edit, and can re-apply your value against the fresh `resourceVersion`. If the network fails ambiguously mid-write, sk64 re-reads the resource and checks whether the write actually landed before retrying.

Other keys on the key screen: `e` edits every key at once as a single YAML document, `N` adds a key, `D` deletes one, `x` exports raw bytes to a file, `i` imports a file as a value, and `ctrl+z` reverts the last save to that resource through the same dry-run-and-diff flow.

**Binary values** are handled, not refused: `enter` opens a read-only hex viewer, and `x`/`i` move raw bytes to and from disk.

**Well-known Secret types** are checked before save — `kubernetes.io/tls` (both halves present, PEM-parseable, and a matching pair), `dockerconfigjson`, `basic-auth`, `ssh-auth`, and `service-account-token`. Failures are warnings you can override, never hard blocks; clusters contain creative uses of standard types.

**Immutable resources** are marked and blocked in the UI, so you never discover the problem as a save-time error.

## Blast radius

Press `r` on any resource to see everything that consumes it: Deployments, StatefulSets, DaemonSets, Jobs, CronJobs, standalone Pods created by operators, and ServiceAccounts. sk64 walks containers, init containers, and ephemeral containers across `env`, `envFrom`, volumes, projected volumes, `imagePullSecrets`, and the ServiceAccount chain, tagging each reference — including `↻ rollout` when a value only propagates after a restart, and `⚠ subPath` for mounts that never update in place.

The edit flow shows a one-line summary of this before the diff. After saving a resource whose values only propagate on restart — environment references and `subPath` mounts — sk64 offers to restart the affected workloads: a per-workload opt-in checklist, confirmed by typing `YES`, that patches the same `kubectl.kubernetes.io/restartedAt` annotation `kubectl rollout restart` uses.

If RBAC blocks part of the walk, the affected section is tagged `no access` (`[no access]` in ASCII mode) and the reverse lookup tells you which sources it could not check, rather than quietly reporting a smaller blast radius.

## Projects

A project binds a repo to a Kubernetes API server through a kube context, plus namespaces, linked workloads, and linked resources. Launch sk64 inside the repo and it opens that project automatically. If this sk64 window is using another context, a dialog shows the current and project context/server identities: `Y` chooses to switch once, `A` to always switch for that project — either way you type `YES` to confirm — and `esc` keeps the current client unchanged.

When creating a project, sk64 first offers to scan the detected repo or directory. The scan can fill in repository-derived namespaces and a matching local context, while ambiguous or unavailable context hints require an explicit choice. You can skip the scan, and every inferred field remains editable. The context itself is never free text: pressing `enter` on that row opens the contexts available from your kubeconfig. Selecting one records the context name and its API server identity without switching the running client or changing kubeconfig's `current-context`.

Context switching is local to the current sk64 process. It builds and probes a new client without changing kubeconfig's `current-context`, so `kubectl` and other open sk64 windows are unaffected. Because a context name can later be repointed, sk64 also stores the API server as the project's cluster identity: a server mismatch always requires an explicit rebind, while a renamed context is offered only when exactly one context points to the saved server. If sk64 cannot identify that server unambiguously, it refuses to switch automatically.

Workload and resource links remember the context and server where they were created. The project view marks links whose origin differs from the project's current binding instead of silently treating them as resources from another cluster.

Press `ctrl+p` to switch or create projects, `L` to link the item under the cursor, and `s` in a project view to **scan the repo** for things worth linking. The scanner reads raw manifests (including multi-doc files), and renders Helm charts and kustomize overlays properly when `helm` or `kustomize` is on your `$PATH` — falling back to literal name extraction when they aren't, and always saying which mode produced a suggestion. CI files are mined for `--namespace` and context hints. Every suggestion shows its provenance (`file:line`) and whether the resource currently exists in the cluster, and nothing is linked without your confirmation. The scanner never modifies the repo.

Projects live in a per-user SQLite database at `${XDG_DATA_HOME:-~/.local/share}/sk64/sk64.db`. It stores **project and link metadata only** — including repo paths, context names, API server URLs, namespaces, and resource/workload names — **never secret values.**

If the database is corrupt, sk64 moves it and its WAL files aside with a timestamp, creates a fresh database, and shows a notice. If project storage is otherwise unavailable, ordinary cluster browsing still works without project features.

## Configuration

sk64 reads `$XDG_CONFIG_HOME/sk64/config` when `XDG_CONFIG_HOME` is absolute, or `~/.config/sk64/config` otherwise. A missing file uses the defaults.

The format is `key = value`. Blank lines and lines beginning with `#` are ignored. `keybind` is the only recognized key; bindings use `keybind = <keys>=<action>`, and repeating the line gives one action multiple keys. A rebind replaces all defaults for that action, so include a default key explicitly if you want to retain it. An invalid config never half-applies: sk64 reports the offending lines and exits (or shows them in a dialog) until the file is fixed.

```text
# Move refresh away from a terminal-intercepted chord.
keybind = ctrl+e=refresh
```

The rebindable actions are `up`, `down`, `top`, `bottom`, `page-up`, `page-down`, `half-page-up`, `half-page-down`, `refresh`, `filter`, `all-namespaces`, `type-cycle`, `values`, `wrap`, `help`, and `quit`.

Representable keys are single printable ASCII characters and these named keys: `up`, `down`, `left`, `right`, `home`, `end`, `pgup`, `pgdown`, `tab`, `enter`, `esc`, `space`, `backspace`, `delete`, `insert`, `f1`, `f2`, `f3`, `f4`, `f5`, `f6`, `f7`, `f8`, `f9`, `f10`, `f11`, and `f12`. Modifiers are written in `ctrl+alt+shift+` order before the key; omit modifiers that do not apply. Modifier chords accept lowercase letters, digits, and the named keys as their base (`ctrl+5`, `alt+up`); punctuation cannot be modified, and `shift+letter` or `shift+digit` chords are not representable (use a standalone uppercase letter for a shifted letter).

The safety keys `N`, `D`, `R`, `Y`, `esc`, and `enter` are reserved so mutation initiators, cancellation, and typed or selected acceptance cannot be rebound. The chords `ctrl+c`, `ctrl+f`, `ctrl+k`, and `ctrl+p` are fixed on screens where those global features are reachable, and `ctrl+z` is fixed on the key list where undo is available. A handful of screen-local keys (`e`, `i`, `r`, `s`, `u`, `x`, `L`, `w` for workloads, and the filepicker's navigation keys) are also fixed; a rebind that collides with one on a shared screen is rejected with the conflict named.

## Keys

| Key | Action |
|---|---|
| `?` | Help for the current screen |
| `esc` | Back / close overlay / cancel the in-flight request |
| `/` | Filter the current list |
| `ctrl+f` | Search resource and key names across all namespaces |
| `ctrl+r` | Refresh |
| `ctrl+k` | Switch kube context |
| `ctrl+p` | Switch project |
| `ctrl+z` | Undo the last save to this resource (session only) |
| `L` | Link the item under the cursor to a project |
| `Y` | Proceed with a mutating action — the final commit is typing `YES` |
| `h/j/k/l` | Same as the arrow keys |
| `Q` | Quit |
| `ctrl+c` twice within two seconds | Quit |

Anything that creates, deletes or overwrites starts with an uppercase key — `N`, `D`, `Y`, `R` — and commits only after a dialog states exactly what is about to happen and you type `YES` and press `enter`. Deleting a resource asks you to type its exact name instead. `esc` always closes a confirmation with nothing done, so a stray keystroke into a focused terminal can never fire a write.

Screen-specific keys — `a` all namespaces, `w` workloads, `t` filter by kind, `N` create, `D` delete, `r` consumers, `v` value search — are listed in `?` on the screen that offers them.

Value search (`v`) matches decoded values of the **current resource only**. There is no cluster-wide value search, because that would mean fetching every secret in the cluster.

## Flags

```
--kubeconfig PATH      Override kubeconfig (default: $KUBECONFIG merged, or ~/.kube/config)
--context NAME         Start context (default: current, or the project's)
--namespace, -n NAME   Start namespace
--project NAME         Open this project (overrides cwd resolution)
--no-project           Skip cwd project resolution
--db PATH              Database location override
--read-only            Disable all mutating paths (save, create, delete, rollout)
--editor CMD           Override $EDITOR
--no-configmaps        Secrets only
--ascii                Force ASCII markers ([S]/[C]) instead of emoji
--scan-depth N         Scanner max directory depth (default 12)
--scan-max-files N     Scanner max file count (default 20000)
--debug-log PATH       Opt-in debug log (names and sizes only — never values)
-v, --version          Print version
-h, --help             Print usage
```

`NO_COLOR` is respected, and sk64 falls back to ASCII markers automatically on terminals that won't render emoji.

## Security

Read this before using sk64 on secrets you care about.

- **Plaintext secrets touch disk briefly.** Editing writes the decoded value to a temporary file so your editor can open it. sk64 creates it in a `0700` per-invocation directory — preferring `/dev/shm`, then `$XDG_RUNTIME_DIR`, then `os.TempDir()` — verifies ownership and mode, and removes it on exit and on SIGINT/SIGTERM/SIGHUP.
- **Editor-artifact mitigation is vim-best-effort.** sk64 passes `-n -i NONE` to vi, vim, and nvim to suppress swapfiles and viminfo. If you enable `undofile`, vim still writes undo history under `~/.local/state`. Behavior of other editors is undefined and undocumented by sk64.
- **The database stores names, never values.** Project names, repo paths, contexts, namespaces, and resource/workload names are mildly sensitive metadata and are persisted. Secret values are never written to it. The same rule applies to `--debug-log`. Both are enforced by tests that reject any API able to accept value bytes.
- **This is not for environments where any of the above is unacceptable.** If plaintext must never reach local disk, sk64 is the wrong tool.

To report a vulnerability, see [SECURITY.md](SECURITY.md).

## Development

```sh
go test -race ./...     # tests
golangci-lint run       # lint
./hack/covergate.sh     # coverage gate (≥85% on the core packages)
go test ./internal/tui -update   # regenerate TUI golden files
```

See [AGENTS.md](AGENTS.md) for the architecture map and conventions, [CONTRIBUTING.md](CONTRIBUTING.md) to contribute, and [RELEASING.md](RELEASING.md) for the maintainer release procedure.

## License

MIT — see [LICENSE](LICENSE).
