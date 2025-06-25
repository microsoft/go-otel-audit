# OTEL Security Logging For Go Client v2

<p align="center">
  <img src="./img/detective.jpeg" width="50%"/>
</p>

## Document scope

This document simply covers the changes in v2.

## Structure (as of 6/25/2025)

```bash
├── v2
    ├── audit
        ├── audit_other.go
        ├── audit_test.go
        ├── audit_unix_test.go
        ├── audit_unix.go
        ├── audit.go
        ├── conn
        │   ├── conn_test.go
        │   ├── conn.go
        │   ├── domainsocket_test.go
        │   ├── domainsocket.go
        │   ├── hang_conn.go
        │   ├── internal
        │   │   ├── server
        │   │   │   └── server.go
        │   │   └── writer
        │   │       ├── writer_test.go
        │   │       └── writer.go
        │   ├── noop.go
        │   ├── tcp.go
        │   └── type_string.go
        ├── docs
        │   ├── highlevel.md
        │   └── img
        │       └── detective.jpeg
        ├── internal
        │   ├── scenarios
        │   │   └── scenarios_test.go
        │   └── version
        │       └── version.go
        ├── metrics.go
        ├── msgs
        │   ├── calleridentitytype_string.go
        │   ├── diagnostic.go
        │   ├── heatbeat.go
        │   ├── msgs_test.go
        │   ├── msgs.go
        │   ├── operationcategory_string.go
        │   ├── operationresult_string.go
        │   ├── operationtype_string.go
        │   ├── README.md
        │   └── type_string.go
        ├── msgsender_test.go
        ├── msgsender.go
        └── testing.go
```

- `v2/audit/` contains our smart audit client with all the automatic connection handling and retries.
- `v2/conn/` contains the `conn.Audit` interface that all connection types must implement along with a domain socket and TCP implementation
- `v2/conn/internal/server` contains a fake server for receiving messages from a `conn.Audit` connection, useful for testing purposes.
- `v2/conn/internal/writer` provides a generic `net.Conn` writer that can be used by upper-level packages in `conn`
- `m2/msgs` contains the messages that are sent to the audit server and msgpack serialization utilities

## Changes from v1

* Removed the `base/` set of packages.
* After some number of `Close()` calls hang, it will not allow new connections.
* Simplified structure.
* `Close()` calls are handled in goroutines to prevent any blocking.
