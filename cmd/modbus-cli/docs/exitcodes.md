# modbus-cli exit codes

modbus-cli uses the organisation exit-code table (otfabric/engineering
`standards/api-design.md`, CLI-001). The codes are the `Exit…` constants in
[`exitcodes.go`](../exitcodes.go).

| Code | Meaning |
|---|---|
| 0 | Success: every operation completed, or help or the version was printed |
| 2 | Usage: invalid flags, arguments or operations, an invalid `--target` URL, or TLS requested without `--cert`, `--key` or `--ca` |
| 3 | Invalid input: a TLS certificate, key or CA file cannot be read or used |
| 4 | Transport: the connection cannot be opened, or a connection failure ended the operations |
| 5 | Timeout: the device did not answer within `--timeout` |
| 6 | Protocol: the device answered with a Modbus exception, or with a malformed or unexpected response |
| 7 | Partial: at least one read or write operation succeeded and at least one failed |
| 10 | Internal: the results could not be written to standard output, or an unclassified error |
| 130 | Interrupted: SIGINT or SIGTERM before the operations completed (also during `repeat`) |

Read and write operations count as succeeded or failed. Scans, `ping`, `sleep`,
`suid`/`sid`, `repeat` and `date` do not: a scan or ping that reaches no device
still exits 0. When every counted operation failed, the first failure decides
the code (4, 5 or 6). With `--fail-fast` the run stops at the first failure,
so it exits 7 when an earlier operation succeeded. The failures are printed on
standard output with the results; one `Error:` line on standard error
summarises them.

## Changes from earlier releases

Up to v1.11.1 every failure exited 1.

| Before | Now | Situation |
|---|---|---|
| 1 | 2 | Invalid flags, arguments or operations, invalid `--target`, missing TLS flags |
| 1 | 3 | Unreadable or invalid TLS certificate, key or CA file |
| 1 | 4 | Connection cannot be opened; connection failures in every operation |
| 1 | 5 | Every operation timed out |
| 1 | 6 | Every operation failed with a Modbus exception or a protocol error |
| 1 | 7 | Some operations succeeded, some failed |
| 1 | 10 | Results cannot be written |
| 1 | 130 | Interrupted (an interrupted `sleep` or `repeat`) |
