# Gradual Go SDK

[![CI](https://github.com/elijahnikov/gradual-go/actions/workflows/ci.yml/badge.svg)](https://github.com/elijahnikov/gradual-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/elijahnikov/gradual-go.svg)](https://pkg.go.dev/github.com/elijahnikov/gradual-go)

The official Go SDK for [Gradual](https://github.com/elijahnikov/gradual) feature flags.

## Installation

```bash
go get github.com/elijahnikov/gradual-go
```

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    gradual "github.com/elijahnikov/gradual-go"
)

func main() {
    ctx := context.Background()
    client := gradual.NewClient(ctx, gradual.Options{
        APIKey:      "gra_xxx",
        Environment: "production",
    })

    if err := client.WaitUntilReady(ctx); err != nil {
        panic(err)
    }
    defer client.Close()

    if client.IsEnabled("new-feature") {
        fmt.Println("Feature is on!")
    }

    theme := client.Get("theme", "dark")
    fmt.Println("Theme:", theme)
}
```

## API Reference

| Method | Description |
|--------|-------------|
| `NewClient(ctx, Options)` | Create a new Gradual client |
| `WaitUntilReady(ctx)` | Block until the client has fetched its initial flag configuration |
| `Close()` | Shut down the client and flush any pending events |
| `IsEnabled(flag)` | Check whether a boolean feature flag is enabled |
| `Get(flag, fallback)` | Get a flag's value, returning the fallback if the flag is not found |

## Development

Run the test suite:

```bash
go test -v ./...
```

## License

MIT
