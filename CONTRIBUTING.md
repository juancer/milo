# Contributing to Milo

Milo is source-available under the [PolyForm Noncommercial License 1.0.0](LICENSE): free for
personal, family, hobby and educational use; commercial use needs the author's permission
(open an issue at https://github.com/juancer/milo).

## How changes get in

`main` is protected: nobody pushes to it directly, not even the author. Every change is a pull request.

1. Fork the repo (or branch, if you have access) and make your change.
2. Run `make test`. The same check runs on the pull request and must pass.
3. Open a pull request that says what changed and why, and how you tested it (which Pi, if hardware is involved).
4. The author reviews it. Merged branches are deleted automatically.

By sending a pull request you confirm that you wrote the change (or may share it) and agree that
it is distributed under this project's license. Forks are welcome under the license terms; please
keep the `LICENSE` and `NOTICE` files, and tell the project about improvements by opening a pull request or issue.

## Ground rules

- English is the source language: code, comments, docs, schema and logs are English, and so are the
  source translation catalogs. Other languages live only in the catalogs; see [docs/i18n.md](docs/i18n.md)
  and [docs/development.md](docs/development.md) to add one. Never hardcode user-facing text: use a key.
- Never commit secrets, real Telegram IDs, family names or private messages.
- Keep it buildable and tested: `make test` must pass. Development runs in Docker
  (see [docs/development.md](docs/development.md)); no Go or Node is needed on your machine.
- All SQL lives in `internal/store`. Hardware and external providers stay behind interfaces
  with a mock. The LLM may only call registered, validated actions.
- Security-sensitive changes (auth, secrets, Action Engine, Telegram authorization) need
  tests for the failure paths.
