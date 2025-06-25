/*
Package audit implements a smart client to a remote audit server. This handles any connection problems silently,
while providing ways to detect problems through whatever alerting method is desired. It also handles an exponential
backoff when trying to reconnect to the remote audit server as to prevent any overwhelming of the remote audit server.

This is the preferred way to connect to a remote audit server. There is a more low-level way to connect to a remote
audit server in the base package. This one allows for you to completely customize how you want to handle issues,
but it is more work to do so.

Example using the domainsocket package:

	// Create a function that will create a new connection to the remote audit server.
	// We use this function to create a new connection when the connection is broken.
	cc := func() (conn.Audit, error) {
		return conn.NewDomainSocket()
	}

	// Creates the smart client to the remote audit server.
	// You should only create one of these, preferrably in main().
	c, err := audit.New(cc)
	if err != nil {
		// Handle error.
	}
	defer c.Close(context.Background())

	// This is optional if you want to get notifications of logging problems.
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
		// 1. ErrValidation , which means your message is invalid.
		// 2. ErrQueueFull, which means the queue is full and you are responsible for the message.
		// 3. A standard error, which means there is an error condition we haven't categorized yet.
		// If #3 happens, please file a bug report as we shouldn't send non-categorized errors to the user.
	}
*/
package audit

import (
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gostdlib/base/concurrency/sync"
	"github.com/gostdlib/base/context"
	"github.com/microsoft/go-otel-audit/v2/audit/conn"
	"github.com/microsoft/go-otel-audit/v2/audit/internal/version"
	"github.com/microsoft/go-otel-audit/v2/audit/msgs"

	"github.com/Azure/retry/exponential"
)

// DefaultQueueSize is the number of audit records that can be queued by default.
const DefaultQueueSize = 2048

var (
	// ErrValidation is an error that occurred during validation of an audit record.
	// This means the audit record is invalid and was not sent.
	ErrValidation = errors.New("validation error")
	// ErrClientDead is an error that indicates the audit client is dead and will not recover.
	// This is set when the connection manager detects that the connection is dead and hasn't been able to recover.
	// This can be due to continuous send timeouts because the agent isn't listening, a bad file descriptor
	// due to a broken connection, or other unrecoverable errors. If on K8, this can be because mouting the socket via
	// a host mount. Whatever the reason, we are going to stop trying to prevent runaways goroutines.
	ErrClientDead = fmt.Errorf("audit client is dead: %w", exponential.ErrPermanent)
	// ErrQueueFull is an error that occurred because the queue is full. The message was not sent or
	// requeued.
	ErrQueueFull = errors.New("queue full")
)

// NotifyError is an error that is sent to the Notify channel when the connection to the remote audit server is broken.
type NotifyError struct {
	// Time is the time the error occurred.
	Time time.Time
	// Err is the error that happened. This will be a an ErrConnection or ErrQueueFull.
	Err error
}

// Client is an audit server smart client. This handles any connection problems silently, while providing ways to
// detect problems through whatever alerting method is desired.
type Client struct {
	// kver is the kernel version of the host
	kver string
	// goos is the operating system of the host. This is set to runtime.GOOS.
	goos string
	// queueSize is the size of the queue for sending audit records.
	queueSize int
	// sendCh is the channel for sending audit records to our async sender. It has a buffer
	// capacity of MaxQueueSize.
	sendCh chan msgs.Msg

	// create is the function that creates a new connection to the remote audit server. This is used when the
	// connection is broken.
	create CreateConn

	// backoff is the exponential backoff for making a new connection to the remote audit server.
	backoff *exponential.Backoff

	// closers is a group used to track the number of conn objects that are currently having their CloseSend method called.
	// This is used to prevent runaway goroutines that can cause the system to run out of resources.
	closers *sync.Group
	// maxClosers is the maximum number of closers that can be running at the same time. This is used to prevent runaway goroutines.
	maxClosers int

	// notifier is the channel that will receive errors when the connection to the remote audit server is broken or
	// the queue is full. If this channel is full, the error will be dropped.
	notifier chan NotifyError

	// clientDead indicates the client is dead and will not recover. This is set to true when the
	// connection manager detects that the connection is dead and hasn't been able to recover.
	clientDead atomic.Bool

	// metrics stores the metrics for the audit client.
	metrics *metrics

	// log is the logger for the audit client. If not set, the default logger is used.
	log *slog.Logger

	// below here are used for testing purposes only.

	testContext context.Context
	testparams  *testParams
}

// CreateConn is a function that creates a connection to a remote audit server.
type CreateConn func() (conn.Audit, error)

// Option is an option for the smart client.
type Option func(*Client) error

// WithQueueSize sets the size of the queue for sending audit records. Once the queue is full, Send() calls
// will return ErrQueueFull immediately. The default queue size is DefaultQueueSize (2048).
func WithQueueSize(size int) Option {
	return func(c *Client) error {
		if size <= 0 {
			return fmt.Errorf("queue size must be greater than 0, got %d", size)
		}
		c.queueSize = size
		return nil
	}
}

// WithLogger sets the logger for the audit client. If not set, slog.Default() is used.
func WithLogger(l *slog.Logger) Option {
	return func(c *Client) error {
		c.log = l
		return nil
	}
}

// New creates a new smart client to the remote audit server. You can get a CreateConn
// from conn.NewDomainSocket(), conn.NewTCP() or conn.NewNoop(). The last one is useful for testing
// when you don't want to send logs anywhere.
func New(ctx context.Context, create CreateConn, options ...Option) (*Client, error) {
	kver, err := kernelVer()
	if err != nil {
		return nil, fmt.Errorf("could not determine kernel version on platform: %w", err)
	}

	backoff, err := exponential.New()
	if err != nil {
		return nil, fmt.Errorf("failed to create exponential backoff: %w", err)
	}

	g := context.Pool(ctx).Sub(ctx, "AuditClientConnClosers").Group()

	c := &Client{
		kver:       kver,
		goos:       runtime.GOOS,
		queueSize:  DefaultQueueSize,
		create:     create,
		notifier:   make(chan NotifyError, 1),
		closers:    &g,
		maxClosers: 1000,
		metrics:    newMetrics(),
		backoff:    backoff,
		log:        slog.Default(),
	}

	for _, o := range options {
		o(c)
	}
	c.sendCh = make(chan msgs.Msg, c.queueSize)

	if err := c.connManager(); err != nil {
		return nil, fmt.Errorf("failed to start connection manager: %w", err)
	}

	return c, nil
}

// connManager starts the initial connection and calls the newMsgSender function to create a message sender.
// Once the initial connection is established, it will manage the sender in a separate goroutine. If it cannot
// setup the initial connection, this will return an error.
func (c *Client) connManager() error {
	sender, err := c.newSender()
	if err != nil {
		return fmt.Errorf("failed to create message sender: %w", err)
	}

	go c.manageSender(sender)
	return nil
}

type msgSenderer interface {
	start(ctx context.Context) <-chan error
}

// manage sender starts a msgSender and manages its lifecycle. When it dies we send a notification. If we build up a lot of clients
// that aren't closing, which can happen because of a bad file descriptor that causes system calls to fail, we will stop trying to reconnect
// after c.maxCloser attempts. This is to prevent runaway goroutines that can cause the system to run out of resources. This is wrapped in
// an exponential backoff to prevent overwhelming the remote audit server with connection attempts.
func (c *Client) manageSender(sender msgSenderer) {
	ctx := context.Background()
	if c.testContext != nil && testing.Testing() {
		ctx = c.testContext
	}

	for {
		err := <-sender.start(ctx) // DO NOT used a passed context here that can be cancelled except in tests.
		if err == nil {
			if !testing.Testing() {
				c.sendNotify(errors.New("bug: audit.Client connection looks to be closed by user, but we don't have a Close() method"))
			}
			return
		}
		c.sendNotify(fmt.Errorf("audit.Client connection error: %w", err))

		if c.closers.Running() > c.maxClosers {
			c.sendNotify(errors.New("audit.Client connection error: too many closers running, stopping connection manager"))
			c.clientDead.Store(true)
			return
		}

		err = c.backoff.Retry(
			context.Background(),
			func(ctx context.Context, r exponential.Record) error {
				if c.closers.Running() > c.maxClosers {
					return exponential.ErrPermanent
				}
				var err error
				sender, err = c.newSender()
				if err != nil {
					c.sendNotify(fmt.Errorf("audit.Client connection error: %w", err))
					return err
				}
				return nil
			},
		)
		if errors.Is(err, exponential.ErrPermanent) {
			c.sendNotify(errors.New("audit.Client connection error: too many closers running, stopping connection manager"))
			c.clientDead.Store(true)
			return
		}
	}
}

// newSender simply creates a new msgSender with a new connection to the remote audit server.
func (c *Client) newSender() (msgSenderer, error) {
	if testing.Testing() && c.testparams != nil && len(c.testparams.senders) > 0 {
		return c.testparams.newSender()
	}

	auditConn, err := c.create()
	if err != nil {
		return nil, fmt.Errorf("failed to create audit connection: %w", err)
	}

	if c.goos != "linux" && !testing.Testing() {
		if auditConn.Type() != conn.TypeNoOP {
			c.closeAuditConn(auditConn)
			return nil, fmt.Errorf("audit: only linux clients can use Audit with a non-NoOp conn.Audit type")
		}
	}

	hb := msgs.Msg{
		Type: msgs.Heartbeat,
		Heartbeat: msgs.HeartbeatMsg{
			AuditVersion: version.Semantic,
			OsVersion:    c.kver,
			Language:     runtime.Version(),
			Destination:  auditConn.Type().String(),
		},
	}

	sender, err := newMsgSender(
		msgSenderArgs{
			conn:      auditConn,
			heartbeat: hb,
			client:    c,
		},
	)
	if err != nil {
		c.closeAuditConn(auditConn)
		return nil, fmt.Errorf("failed to create message sender: %w", err)
	}
	return sender, err
}

// Notify returns a channel that will receive errors when the connection to the remote audit server is broken or
// the queue is full. If this channel is full, the error will be dropped.
func (c *Client) Notify() <-chan NotifyError {
	return c.notifier
}

type sendOptions struct{}

// SendOption is an option for the Send function.
type SendOption func(sendOptions) (sendOptions, error)

// Send sends a message to the remote audit server. The only errors that will be returned are due to the Record being
// invalid, trying to send a Msg that doesn't validate, if the queue is full or the client being dead. Errors that cannot be
// recovered from are tagged with exponential.ErrPermanent that can be tested with errors.Is(). Errors other than this will not be returned,
// as this method is async and just pushes the message to a channel for sending later.
// Deadlines and cancellations of the context have no effect on this method.
func (c *Client) Send(ctx context.Context, msg msgs.Msg, options ...SendOption) error {
	if c == nil || c.clientDead.Load() {
		return ErrClientDead
	}

	// Some types of messages cannot be sent by the user, they can only be sent by the client automatically,
	// like a heartbeat.
	if msg.Type == msgs.ATUnknown || msg.Type > msgs.ControlPlane {
		return fmt.Errorf("audit type (%v) is invalid: %w", msg.Type, ErrValidation)
	}

	if err := msg.Record.Validate(); err != nil {
		return fmt.Errorf("%w: %w", err, ErrValidation)
	}

	select {
	case c.sendCh <- msg:
	default:
		return ErrQueueFull
	}
	return nil
}

// sendNotify sends a notification to the notifier channel. If the channel is full, the notification is dropped.
func (c *Client) sendNotify(err error) {
	if c.notifier == nil || err == nil {
		return
	}

	notice := NotifyError{
		Err:  err,
		Time: time.Now().UTC(),
	}

	select {
	case c.notifier <- notice:
	default:
		if c.log != nil {
			c.log.Error("audit.Client notifier channel is full, dropping notification", slog.Any("error", err))
		}
	}
}

// closeAuditConn closes the audit connection in a goroutine and sends a notification if it fails. It uses the closers group
// to allow the manager to track if this is running and to prevent runaway goroutines.
func (c *Client) closeAuditConn(auditConn conn.Audit) {
	_ = c.closers.Go(
		context.Background(),
		func(ctx context.Context) error {
			if err := auditConn.CloseSend(); err != nil {
				c.sendNotify(fmt.Errorf("failed to close audit connection: %w", err))
			}
			return nil
		},
	)
}
