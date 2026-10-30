# Agent instructions

Before modifying code, read [docs/best-practices.md](docs/best-practices.md).
Docent is a public project: never add names of private organizations,
repositories, teams, hosts, or people to code, docs, tests, commits, or pull
requests.

Validate changes with:

```sh
go build ./apps/... && go vet ./... && go test ./...
```

For changes under `apps/docentd/web`, also run `npm run typecheck` there.
