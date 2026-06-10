# Go OTEL Audit

<p align="center">
  <img src="./audit/docs/img/detective.jpeg" width="50%"/>
</p>

The Go OTEL Audit provides a package for auditing Go code for Microsoft compliance purposes. This package
uses Geneva Monitoring format to send audit events to the Geneva Monitoring service.

While everything in this is publically available, the main audience is Microsoft first party developers.

## Usage

Using this package will require some service setup that is beyond the scope of this guide.
If you are a Microsoft first party developer, please see our internal guides.

The package is designed to be asyncronous and non-blocking. A blocked send will return an error.
You can then decide what to do with the message that was sent. This allows you to build services that
do not slow down or block on audit messages.

The `audit` package provides a smart client that sends messages to the audit server and also handles
the connection (and any reconnections) to the audit server.

Here is a quick example of how to use the `audit` package with a domain socket listener:

```go

// Create a function that will create a new connection to the remote audit server.
// We use this function to create a new connection when the connection is broken.
cc := func() (conn.Audit, error) {
	return conn.NewDomainSocket() // You can pass an option here for a non-standard path.
}

// Creates the smart client to the remote audit server.
// You should only create one of these, preferrably in main().
//
// audit.WithDeferredConnection() makes New() return a working client even if the initial
// connection cannot be established: it responds as a no-op (messages are accepted and dropped,
// with drops recorded in the drop metrics) while it retries the connection in the background,
// then sends normally once connected. Omit this option if you want New() to return an error
// when the initial connection fails.
c, err := audit.New(ctx, serviceTreeID, cc, audit.WithDeferredConnection())
if err != nil {
	// Handle error.
}

// This is optional if you want to get notifications of logging problems or do something
// with messages that are not sent.
go func() {
	for notifyMsg := range c.Notify() {
		// Handle error notification.
		// You can log them or whatever you want to do.
	}
}()

// Send a message to the remote audit server.
if err := c.Send(context.Background(), msgs.Msg{<add record information>}); err != nil {
	// Handle error.
	// Errors here will either be:
	// 1. audit.ErrValidation, which means your message is invalid.
	// 2. audit.ErrQueueFull, which means the queue is full and you are responsible for the message.
	// 3. audit.ErrClientDead, which means the client is dead and will not recover. This is due to connections not closing for some reason.
	// 4. A standard error, which means there is an error condition we haven't categorized yet.
	// If #4 happens, please file a bug report as we shouldn't send non-categorized errors to the user.
	// You need to use errors.Is() or errors.As() to check for these errors.
}
```
