# Enkodo

Enkodo is a compact encoder/decoder focused on speed and simplicity.

## Why this fork

This repository is a maintained fork that adds a code generation script for structs. This script relies on "go generate" and NOT reflection (good gophers despise reflection).

## Quick usage
Add a `go:generate` directive to the top of a file in the package you want to generate code for:

```go
//go:generate go run github.com/nullmonk/enkodo/cmd/enkodo .

package main

type User struct {
    Email string `enkodo:""`
    Age   uint8  `enkodo:""`
}
```

Running `go generate` in that directory will produce `*_enkodo.go` with `MarshalEnkodo` and `UnmarshalEnkodo` implementations for the exported fields tagged with `enkodo:""`.

