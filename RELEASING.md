# Releasing posthook-go

## Release repo

https://github.com/posthook/posthook-go

## Steps

1. **Update the version constant** in `posthook.go`:

   ```go
   const Version = "1.1.0"  // bump as needed
   ```

2. **Run tests**:

   ```bash
   go test ./...
   ```

3. **Commit and push**:

   ```bash
   git add -A
   git commit -m "Release v1.1.0"
   git push origin main
   ```

4. **Tag the release**:

   ```bash
   git tag v1.1.0
   git push origin v1.1.0
   ```

5. **Trigger Go module proxy indexing**:

   ```bash
   GOPROXY=https://proxy.golang.org go list -m github.com/posthook/posthook-go@v1.1.0
   ```

6. **Create GitHub release**:

   ```bash
   gh release create v1.1.0 --title "v1.1.0" --notes "Release notes here"
   ```

## Versioning

Follow [semver](https://semver.org/):

- **Patch** (1.0.x): Bug fixes, doc updates
- **Minor** (1.x.0): New features, backward-compatible changes
- **Major** (x.0.0): Breaking API changes
