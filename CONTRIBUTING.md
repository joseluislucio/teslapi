# Contributing

Bug reports and pull requests are welcome. For a security problem, read [SECURITY.md](SECURITY.md)
first and do not open a public issue.

## Development

You need Go (see `daemon/go.mod`), Node 22 and, for the end-to-end tests, Python 3 and Chrome.

```sh
make test   # go vet + tests with -race, translations
make dev    # daemon with fake hardware and the clips in .devdata → http://localhost:8080
make e2e EVENT=<folder in .devdata/TeslaCam/SavedClips>   # with make dev running
```

Development mode only exists in builds with `-tags dev` and never reaches the Pi.

## Rules of thumb

- **Never commit real clips, `event.json` files, locations, addresses, MACs or credentials**, not
  even in tests. `.devdata/` is ignored for that reason. Use made-up data.
- A failure has to reach the portal with its reason, translated. Add new messages to all six
  languages in `web/src/locales/` (`make i18n` checks them).
- Input from the portal, the USB drive and the NAS is untrusted. Run system tools with separate
  arguments, never through a shell, and do not put secrets in arguments or logs.
- Every security fix comes with a test that would have caught it (see `internal/api/api_test.go`).
- Anything that touches mounts, Wi-Fi or the USB gadget needs testing on a real Pi: some tests only
  run there (`TESLAPI_NM_TEST`, `TESLAPI_AP_TEST`, `TESLAPI_NAS_TEST`).
