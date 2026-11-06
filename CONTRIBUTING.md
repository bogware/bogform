# Contributing to Bogform

Bogform is an independent fork of OpenTofu, focused on cheap, secure infrastructure
management from your laptop, your repository's pipelines, or a small self-hosted web app.

- Found a bug or have an idea? [Open an issue](https://github.com/bogware/bogform/issues/new/choose).
- Found a security problem? Follow [SECURITY.md](SECURITY.md) and don't open a public issue.

## Development

You need the Go version named in `.go-version` or `go.mod`.

```sh
go build -o bin/bogform ./cmd/bogform   # build the CLI
go test ./internal/... ./cmd/...        # run unit tests
go generate ./...                       # regenerate generated code after changing its sources
```

Keep pull requests focused, and add a test for any behavior change. Dependency updates are
made by hand, in their own pull request.

## License

Bogform is licensed under the [Mozilla Public License 2.0](LICENSE). Keep existing copyright
and license headers intact when you change a file, including those of OpenTofu and HashiCorp.
