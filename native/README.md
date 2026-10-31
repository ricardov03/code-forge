# Native accelerator

`native/` contains the optional Go accelerator and compatibility packages for code-forge.
The build is pure Go, uses only the standard library, and keeps CGO off (`CGO_ENABLED=0`).
This also avoids the macOS 26 LC_UUID linker error during local tests.
Test from the repository root:

```sh
cd native && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
```
