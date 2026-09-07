# Development

## Layout

```
cmd/argo9s/          CLI entrypoint (cobra)
internal/config/     ~/.argo9s/config.yaml
internal/kube/       client-go helpers
internal/argo/       CRD kinds, store, watch, operations
internal/ui/         Bubble Tea TUI
```

## Principles

- Talk to the **Kubernetes API** only (dynamic client + unstructured) — avoid pulling the full Argo CD module tree.
- Keep the UI responsive: watchers run in goroutines, ops run as Bubble Tea commands with timeouts.
- Read-only by default; mutating ops must check `Operator.guard()`.
- Follow K9s-style keybinding conventions where they map cleanly.

## Commands

```bash
make tidy
make test
make build
./bin/argo9s
```

## Adding a resource kind

1. Add `Kind` + GVR in `internal/argo/resource.go`
2. Extend `FromUnstructured`
3. Include in `WatchKinds` / `ParseKind`
4. Add list column / command aliases in `internal/ui/app.go`
5. Add a unit test for field extraction
