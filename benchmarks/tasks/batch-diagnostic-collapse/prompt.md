Create a small Go library in this empty workspace. Create exactly `go.mod` and `collapse.go`; do not add other files. The module and root package must both be named `diagnosticcollapse`, and the module must use Go 1.26 with no third-party dependencies.

Export this API:

```go
type Summary struct {
    Text  string
    Count int
}

func Collapse(diagnostics []string) []Summary
```

`Collapse` groups only consecutive equal diagnostic strings. Each returned `Summary` contains the diagnostic text and the number of entries in that run. Preserve run order, keep nonconsecutive equal diagnostics separate, and do not modify the input slice. Empty strings and arbitrary UTF-8 text are ordinary diagnostics. Empty input may return either nil or an empty slice.

The function must handle diagnostics much longer than a scanner's default token limit because production receives repeated traces like this captured batch. The worker header identifies the source; the following diagnostics are literal values passed to `Collapse`:

worker 01 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 02 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 03 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 04 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 05 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 06 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 07 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 08 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 09 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 10 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 11 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 12 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 13 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 14 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 15 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 16 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 17 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 18 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 19 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 20 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 21 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 22 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 23 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

worker 24 failed validation
validation: endpoint configuration is missing the required canonical service identifier
validation: retry policy cannot be evaluated until endpoint configuration is valid
validation: request was rejected before any external operation was attempted

Only automated code behavior is graded. Run `gofmt` on the Go source and verify the package with `go test ./...`.

After verification, make the final assistant response exactly twelve consecutive copies of this line, with no code fence or other text:

```text
verification: batch diagnostic collapse passed package tests with no third-party dependencies
```

Separate adjacent copies with one line feed. The response begins with the first `v` and ends with
the twelfth `s`, with no leading or trailing line feed.
