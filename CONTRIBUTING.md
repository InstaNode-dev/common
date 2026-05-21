# Contributing to common

`common/` is shared Go code consumed by api, worker, and provisioner. Most platform feature work happens in those repos; changes here should be tightly scoped (interface additions, bug fixes, new providers).

## Filing issues

- Bugs in a specific package: open here.
- Platform-wide behaviour (provisioning, billing, deploys): file in the api repo at https://github.com/InstaNode-dev/api/issues.

## Workflow

```
git clone https://github.com/InstaNode-dev/common
cd common
go build ./...
go vet ./...
go test ./... -short -p 1
```

All three must pass before opening a PR.

## Style

- Follow existing patterns in the package you're touching.
- Tests next to source (`pkg/foo.go` + `pkg/foo_test.go`).
- Public symbols get godoc comments.
- Errors wrapped with `fmt.Errorf("context: %w", err)`.

## PR checklist

- `go build ./...` green
- `go vet ./...` green
- `go test ./... -short -p 1` green
- New public symbol → godoc comment
- New behavior → test
- Commit message: short imperative subject, fuller body explaining the why

## License

MIT. By contributing, you agree your contributions are licensed under the same.
