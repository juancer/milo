# Contributing to Nexo

Nexo is open source under Apache-2.0. You are free to use, modify and fork it.

**Please notify the project when you improve or fork it.** The easiest way is to open a
pull request (preferred) or an issue at https://github.com/juancer/nexo describing what you
changed. This is a polite request that keeps the community informed; it is not a condition
of the license, and you must keep the `LICENSE` and `NOTICE` files intact.

## Ground rules

- Everything in the repository is English: code, comments, docs, UI, schema, logs.
- Never commit secrets, real Telegram IDs, family names or private messages.
- Keep it buildable and tested: `make test` must pass. Development runs in Docker
  (see [docs/development.md](docs/development.md)); no Go or Node is needed on your machine.
- All SQL lives in `internal/store`. Hardware and external providers stay behind interfaces
  with a mock. The LLM may only call registered, validated actions.
- Security-sensitive changes (auth, secrets, Action Engine, Telegram authorization) need
  tests for the failure paths.
