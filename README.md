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

## Advanced Usage

### Type Overrides
If you have a custom type that is a primitive under the hood, you can specify the primitive type in the tag:

```go
type SocialMedia string

type User struct {
    Twitter SocialMedia `enkodo:"string"`
}
```

### Custom Functions
You can specify custom functions for encoding and decoding. The tag format is:
`enkodo:"[type][,[encode][,[decode]]]"`

#### Package-level functions
```go
type User struct {
    Parent *CustomObj `enkodo:"string,EncodeCustomObj,DecodeCustomObj"`
}

func EncodeCustomObj(c *CustomObj) string {
    return c.Name
}

func DecodeCustomObj(name string) *CustomObj {
    return &CustomObj{Name: name}
}
```

#### Methods
If the function starts with a dot `.`, it is treated as a method on the field.

```go
type User struct {
    // Calls u.Parent.GetName() for encoding and u.Parent.SetName(v) for decoding
    Parent *CustomObj `enkodo:"string,.GetName(),.SetName"`
}
```
