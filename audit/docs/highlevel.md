# OTEL Security Logging For Go Client

<p align="center">
  <img src="detective.jpeg" width="50%"/>
</p>

## Introduction

Azure internal services are moving towards a unified way of logging security events. Today there are clients for both C++ and C#. Message types are loosely based on ifFix types.

This package is loosely based on the C# client. Loosely in that the client attempts to provide the capabilities in that client in a Go manner with various enhancements.

While this package is labelled OTEL (Open Telemetry), it is not an OTEL client. This name might have something to do with the agent doing OTEL stuff, but the client does not. This is a Geneva client that is used to log security events to a Geneva backend via an agent. Messages are sent in MsgPack format. The client does not use the Go OTEL packages. So trying to understand the client in terms of OTEL would be confusing. This does not log to spans and doesn't create traces. The best way to understand it is as a simple logging client to an agent that goes to Geneva. That agent, at this time, is either a Unix domain socket or a TCP connection.

### Goals

- Providing security logging messages to Geneva
- Avoid as many allocations as possible
- Client should be asyncronous with maximum queue size
- Allow the user to deal with any message sending issues
- Provide a smart client for most users that handles issues with the security agent without interrupting program flow
- Connection agnostic, based on net.Conn
- Support domain sockets and TCP on Linux (Windows support is not a goal)
- Allow custom logging based on the new standard library `slog` package
- Message validation so that bad messages are never transmitted
- Support for heartbeat messages after the first successful send
- Eventual support for diagnostic message type

## Structure (as of 5/3/2024)

```bash
├── audit
│   ├── audit.go
│   ├── base
│   │   ├── README.md
│   │   ├── base.go
│   │   ├── base_other.go
│   │   ├── base_test.go
│   │   ├── base_unix.go
│   │   ├── base_unix_test.go
│   │   └── errorcategory_string.go
│   ├── conn
│   │   ├── conn.go
│   │   ├── conn_test.go
│   │   ├── domainsocket.go
│   │   ├── internal
│   │   │   ├── server
│   │   │   │   └── server.go
│   │   │   └── writer
│   │   │       ├── writer.go
│   │   │       └── writer_test.go
│   │   ├── noop.go
│   │   ├── tcp.go
│   │   └── type_string.go
│   ├── docs
│   │   └── highlevel.md
│   ├── internal
│   │   └── version
│   │       └── version.go
│   └── msgs
│       ├── README.md
│       ├── calleridentitytype_string.go
│       ├── diagnostic.go
│       ├── heatbeat.go
│       ├── msgs.go
│       ├── msgs_test.go
│       ├── operationcategory_string.go
│       ├── operationresult_string.go
│       └── operationtype_string.go
├── go.mod
├── go.sum
```

- `audit/` contains our smart audit client with all the automatic connection handling and retries.
- `base/` contains our basic client that gives a user more control, but requires more management
- `conn/` contains the `conn.Audit` interface that all connection types must implement along with a domain socket and TCP implementation
- `conn/internal/server` contains a fake server for receiving messages from a `conn.Audit` connection, useful for testing purposes.
- `conn/internal/writer` provides a generic `net.Conn` writer that can be used by upper-level packages in `conn`
- `msgs` contains the messages that are sent to the audit server and msgpack serialization utilities

## Enhancements

The biggest enhancement is the automatic reconnect handling with exponential backoff capabilites that are in the `audit` package. This along with a notification channel lets the user not worry too much about logging but still allow for event notifications if they care about them.

This means less burden for the user on figuring out how to fix these problems. And they can centralize problem handling for messages that fill the queue in a single location.

We also provide a hook to allow messages to be captured or manipulated before being sent. This can allow pushing the record to a logging system that already exists for a project by simply wrapping what exists in the hook. Or it can provide some automatic population of fields to ease development.

## Optimizations

The client for C# was bulk based that sent when messages either it reached some number of messages or some amount of time passed.

This is basically Nagel's algorithm for TCP. Like Nagel's algorithm, it is not always the best choice.

While I initially implemented it this way, it dawned on me that with Go channels, we could be more efficient and remove unneeded options. `Send()` writes to a channel that has buffer set to whatever the queue length the user wants. A single `goroutine` reads from that channel and writes to a `bufio.Writer()` that wraps whatever `io.Writer` we have. That means we are filling a buffer full of bytes and the conn object is reading those bytes out as fast as it can.

This gives us buffered sends that will make the network drivers happy and we always have a constant stream of messages instead of thrashing when we reach either a time or size limit. It also means we don't kick off timers all the time to check empty buffers.

The mechanics of the C# library make sense if messages are smaller than an IP packet (that is never the case) or if the overhead of the protocol is high.

With `TCP` and `domain sockets` that are missing the overhead of an upper level protocol such as `http`, bulk messages didn't seem to make a lot of sense in the Go case. In those cases you might want to save sending headers on every message by bulking them up. But with TCP and domain sockets, it doesn't help us.

What this boils down to is that with less mechanics we can be more efficient and have a simpler client.

In addition, I did not keep any of the default timeouts. If a user wants to timeout messages, then they can set it via the `context.Context` object. This is the standard for Go in the modern era.  

However, timeout of security related messages for Go is just a bad idea. Relying on TCP backoff mechansims or domain socket blocking should be more reliable mechanism when paired with immediate returns when your buffered channel is full.  I almost removed honoring cancellation at all, because you can get subtle bugs by passing a parent Context that gets cancelled. I noted in the SDK that you need to use `context.WithoutCancel()` on calls to avoid this. We may want to change this in the future, because I'm not really sure of a valid use case in our model.

## The Geneva Message Format

Before you shoot the messenger, no one involved in any of the OTEL loggers is responsible for the Geneva Message format. I'm sure that this format is documented somewhere with some reasoning, I don't know what it is. Also, [msgpack](https://msgpack.org/), blah...

So you need to use `msgpack` for serialization. The format in Go, roughly comes out to be:

```go
[3]any{
    msgTypeAsString, // AuditType event, so AsmAuditDP, AsmAuditCP, ...
    [1][2]any{
        [2]any{
            time.Now().Unix(), // timestamp in seconds
            yourRecord, // Your record type goes here
        },
    },
    struct{ TimeFormat string }{TimeFormat: "DateTime"}, // required by Geneva Agent
}
```
Geneva isn't using strict arrays of a size as I am doing here, instead they are a vector of entries. I assume this is to allow bulk transmission. But the OTEL client always packs a message like this, so we can use strict arrays to avoid allocations (where slices will allocate).

As a note, this client is only responsible for sending messages to a server, not decoding them. 

However, I do decode messages in a fake server located in `audit/conn/internal/server`. Because of what I consider a bug in `msgpack`, you cannot unmarshal `any` fields into a concrete type, but instead must unmarshal to `map[string]any`. I have reported that to the `msgpack` author, but I don't expect this to be fixed. Here are various bugs I reported:

* https://github.com/vmihailenco/msgpack/issues/372
* https://github.com/vmihailenco/msgpack/issues/373
* https://github.com/vmihailenco/msgpack/issues/374

To convert back to a concrete type, I convert to a type with `map[string]any`, marshal to `json` and finally unmarshal to our concrete type.

Note to do this, I use the preview version of the `json` encoder for the Go standard library: `github.com/go-json-experiment/json`. This version is faster, more accurate in decoding, supports decoding `any` types into concrete types instead of `map[string]any`, etc... This module will become the stdlib `encoding/json/v2`.

Note, I found a bug there around typed `uint8` when stored in `any`: 

* https://github.com/go-json-experiment/json/issues/36

This is why all numeric constants are `uint16` instead of `uint8`. This is unlikely to affect anything measurable.

Also, don't let the word `experiment` fool you, the new json package is superior to the standard `encoding/json` package in pretty much every way. It is written by a Go author from the Go team (now at Tailscale) and will be the new `v2` package in the standard library.

## Message types

All messages here are based on:

* https://msazure.visualstudio.com/One/_git/ASM-OpenTelemetryAudit?path=/src/csharp/OpenTelemetry.Audit.Geneva/DataModel

These have numeric values and we continue that tradition here. However, we do not log messages with numeric values. All message values are converted to `string` representations based on their names. Aka, we are [`stringly`](https://blog.codinghorror.com/new-programming-jargon/) typed in our marshalled output.

This does mean that we must have custom marshal and unmarshal methods on all our numeric types. This is the minimum spec that must be included:

* `MarshalMsgpack() ([]byte, error)`
* `UnmarshalMsgpack(b []byte) error`
* `UnmarshalJSON(b []byte) error`

Each of these implements an `interface` that is present in the various data format packages and are called during the marshal/unmarshal phase by those packages.

These rely on `string` conversion generated by the `stringer` command line tool, which you must have installed. We do this through the `//go generate` directive in the files, so running `go generate ./...` at the top level will update all of these mappings in the repository. You only need to do this if adding values, but it is a good idea to do before commiting anything for safety.

`stringer` does not generate a reverse mapping, so I create a generic `unString` method to reverse any value. This is only used in tests.

The `UnmarshalJSON` is only included to handle internal testing.

It should be noted that `MarshalMsgpack` is the only method used outside of tests.

## Backoff and smart client features

The top level client created in the `audit` package contains smart features not available in the `base` client.

The smart client detects connection problems and creates a new connection and resume sending messages that were queued up.

We don't want to spam the server with new connection attempts if the server has a problem (be it local or remote). We use an exponential backoff package to handle reconnection events.

Our backoff mechanisms are completely related to reconnections and not sending. We expect TCP and domain socket backoff mechanisms to handle that.


## Adding a new connection method

Writing a new connection method is as simple as adding a sub-package in `audit/conn/` and implementing a `audit/conn.Audit` interface. If you can do this using a generic `net.Conn` wrapped in `audit/conn/writer` you should do that. Otherwise you need to construct what that package is doing for your own purposes.

All new connection methods must live in this package, as I have made `Audit` contain a private method that cannot be implemented by outside packages. This allows us to not worry about breaking changes when an interface is changed.

## Asyncronous vs Syncronous

So there might be some concerns about having an asyncronous client. My marching orders when making this was to do a asyncronous client as to not interfere with services.

My feeling is that this is the right decision, given the nature of the types of services using Go.

You have a couple of different scenarios that can happen that cause logging failures:

* The client is sending faster than the logging agent can handle it
* The logging agent gets stuck for some reason and will never process
* The logging agent gets disconnected from its remote end
* Networking issues that affect the agent, or in the case of TCP, connection to the agent
* Machine death before a log is written

In a syncronous client, each one of these is a series of problems for the service. Slowing down calls to K8 because the logger is slow is not great. And frankly, most services aren't doing continuous profiling that is going to catch this. You would have entire services slowing down because the loggers or its infra are too slow. Yikes.

You could try to handle this with timeouts, but that is a fairly crude way of doing this. You are just adding latency all over the place when a problem with logging occurs.

In any of the failure scenarios, if someone forgets a timeout (and you support timeouts), the service is stuck until logging works. That is a sev0 where heads are going to roll. 

This client is adjustable. We are async in nature, but we have a queue. You can make the queue as large as you want. If you want to start blocking on queue full errors for some time period, the service can do that. You can write the messages to disk and restart sending them when the client is sending again. You have a notifcation system to pop alerts to you that bad things are happening. 

But what never happens is that the serivce utilizing the logger blocks without the service owner making that decision explicitly. And the service owner is never in the dark when any event happens.

There are a few scenarios in which audit logs can be lost:

* The user tries to send, gets an error and doesn't resend (throws the message away). This only happens if the queue is full. But they have the opportunity to do something if they want.
* The user provides a timeout for logging the event and it cancels before it is sent. Those messages are always lost. [*Note*: the author thinks we should remove this, but I included it to be in sync with C#]

## Use of unsafe

This package uses the `unsafe` package. This sometimes causes undue alarm. Unsafe simply means your doing something that `C` and `C++` are doing in every function call.

Our use is limited to preventing a copy from occuring when doing string conversion from the `[]byte` types to `string`.

This prevents an extra allocation. 
 
We do this at the unmarshalling boundary to convert to a string and do the lookup of what `uint*` value the `string` stands for. The `[]byte` object is never touched again and we throw away both the `[]byte` and the `string` after the operation.

This is a safe operation and as pointed out above, basically every line of `C` and `C++` is unsafe, we are doing this in a single location once.

## Why not make it just mirror the C# client?

I think this a fallacy of client and API design thinking that has crept in, especially in the days of auto-generation clients to ease developer burden for a service. We say its better because of "consistency", which really means it was just easier for the client creators to maintain because they can autogenerate the clients.

Developers don't want clients that look the same, they want clients that feel native to their language. Having things named slightly differently as long as it eases use in their language of choice and follows the natural design guidelines of their language should be prefered.

Also, most developers are likely to do work with a service in at most 2 languages, the second language being a frontend langauge. C# and Go are direct competitors, so a project is likely to only use one of them. And this package is not really used in the frontend.

Where you can really help out developers would be to use something like protocol buffers for the message structure. Even if you are not going to use protocol buffer encoding. 

This is because it forces a more generic structure that works in all languages. It also tends to keep you from using language specific types that have to be recreated in other languages. You don't have to re-create message types when adding support for a new language. That gets you more consistency with much less burden for the internal dev team, but allows you to customize clients.

So, in less words, because it is better for the Go users.