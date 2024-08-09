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

The package is designed to by asyncronous and non-blocking. A blocked send will return an error.
You can then decide what to do with the message that was sent. This allows you to build services that
do not slow down or block on audit messages.

Here is a quick example of how to use the package with a domain socket listener:

```go

// Create a function that will create a new connection to the remote audit server.
// We use this function to create a new connection when the connection is broken.
cc := func() (conn.Audit, error) {
	return conn.NewDomainSocket() // You can pass an option here for a non-standard path.
}

// Creates the smart client to the remote audit server.
// You should only create one of these, preferrably in main().
c, err := audit.New(cc)
if err != nil {
	// Handle error.
}
defer c.Close(context.Background())

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
	// 1. base.ErrValidation , which means your message is invalid.
	// 2. base.ErrQueueFull, which means the queue is full and you are responsible for the message.
	// 3. A standard error, which means there is an error condition we haven't categorized yet.
	// If #3 happens, please file a bug report as we shouldn't send non-categorized errors to the user.
}
```
