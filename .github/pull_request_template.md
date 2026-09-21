<!-- What changes, and why. With "Squash and merge" this text becomes the commit on main. -->

- [ ] A user-facing change is noted under `## [Unreleased]` in `CHANGELOG.md`
- [ ] New logic in the core comes with a test; `make test` passes
- [ ] The core stays cgo-free (nothing outside `internal/ui` needs GTK)
- [ ] UI change: a screenshot is attached, and `make screenshots` was run if the README's images change
- [ ] `README.md` and `README.pt-BR.md` agree
