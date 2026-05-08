## Summary

What changed?

## Verification

Paste the commands you ran:

```sh
go test ./... -count=1
go vet ./...
go test -race ./... -count=1
```

## Provider Status

- [ ] No provider status changed.
- [ ] Provider status changed and `docs/provider-status.md` was updated.
- [ ] Live provider behavior changed and the relevant opt-in integration test was run.

## Safety Checklist

- [ ] No credentials, `.env` files, generated artifacts, or local runtime state are committed.
- [ ] New provider claims are documented honestly.
- [ ] Failure paths return explicit errors.
- [ ] Tests cover the changed behavior.
