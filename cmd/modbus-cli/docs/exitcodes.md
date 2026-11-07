# modbus-cli exit codes

| Code | Meaning |
|---|---|
| 0 | Success: every operation completed, or help or the version was printed |
| 1 | Failure of any kind: invalid flags or arguments, a configuration or TLS file error, a connection failure, or at least one operation failed (the first one with `--fail-fast`) |

modbus-cli does not yet use the organisation exit-code table
(otfabric/engineering `standards/api-design.md`, CLI-001), which gives usage
errors, transport failures and protocol errors codes of their own.
