# internal/defaults
Parses `control-plane/defaults/defaults.yaml` (R11), embedded in the binary: `Get()` for Go consumers (typed),
`Document()`/`JSON()` for `defaults.get` and the MCP `defaults://` resource (keys as in the file). `Parse` refuses a
value without description or source, a value outside its range and unknown keys.
