package events

import (
    "context"
    "reflect"
    "sync"
    "time"

    "github.com/mafredri/cdp/protocol/dom"
    "github.com/mafredri/cdp/protocol/page"

    "github.com/MontFerret/ferret/pkg/drivers"
    "github.com/MontFerret/ferret/pkg/runtime/core"

    "github.com/pkg/errors"
)

type (
    Event int

    EventListener func(ctx context.Context, message interface{})

    EventBroker struct {
        mu                      sync.Mutex
        listeners               map[Event][]EventListener
        cancel                  context.CancelFunc
        onLoad                  page.LoadEventFiredClient
        onReload                dom.DocumentUpdatedClient
        onAttrModified          dom.AttributeModifiedClient
        onAttrRemoved           dom.AttributeRemovedClient
        onChildNodeCountUpdated dom.ChildNodeCountUpdatedClient
        onChildNodeInserted     dom.ChildNodeInsertedClient
        onChildNodeRemoved      dom.ChildNodeRemovedClient
    }
)

const (
    //revive:disable-next-line:var-declaration
    EventError = Event(iota)
    EventLoad
    EventReload
    EventAttrModified
    EventAttrRemoved
    EventChildNodeCountUpdated
    EventChildNodeInserted
    EventChildNodeRemoved
)

func NewEventBroker(
    onLoad page.LoadEventFiredClient,
    onReload dom.DocumentUpdatedClient,
    onAttrModified dom.AttributeModifiedClient,
    onAttrRemoved dom.AttributeRemovedClient,
    onChildNodeCountUpdated dom.ChildNodeCountUpdatedClient,
    onChildNodeInserted dom.ChildNodeInsertedClient,
    onChildNodeRemoved dom.ChildNodeRemovedClient,
) *EventBroker {
    broker := new(EventBroker)
    broker.listeners = make(map[Event][]EventListener)
    broker.onLoad = onLoad
    broker.onReload = onReload
    broker.onAttrModified = onAttrModified
    broker.onAttrRemoved = onAttrRemoved
    broker.onChildNodeCountUpdated = onChildNodeCountUpdated
    broker.onChildNodeInserted = onChildNodeInserted
    broker.onChildNodeRemoved = onChildNodeRemoved

    return broker
}

func (broker *EventBroker) AddEventListener(event Event, listener EventListener) {
    broker.mu.Lock()
    defer broker.mu.Unlock()

    listeners, ok := broker.listeners[event]

    if !ok {
        listeners = make([]EventListener, 0, 5)
    }

    broker.listeners[event] = append(listeners, listener)
}

func (broker *EventBroker) RemoveEventListener(event Event, listener EventListener) {
    broker.mu.Lock()
    defer broker.mu.Unlock()

    idx := -1

    listeners, ok := broker.listeners[event]

    if !ok {
        return
    }

    listenerPointer := reflect.ValueOf(listener).Pointer()

    for i, l := range listeners {
        itemPointer := reflect.ValueOf(l).Pointer()
        if itemPointer == listenerPointer {
            idx = i
            break
        }
    }

    if idx < 0 {
        return
    }

    var modifiedListeners []EventListener

    if len(listeners) > 1 {
        modifiedListeners = append(listeners[:idx], listeners[idx+1:]...)
    } else {
        modifiedListeners = make([]EventListener, 0, 5)
    }

    broker.listeners[event] = modifiedListeners
}

func (broker *EventBroker) ListenerCount(event Event) int {
    broker.mu.Lock()
    defer broker.mu.Unlock()

    listeners, ok := broker.listeners[event]

    if !ok {
        return 0
    }

    return len(listeners)
}

func (broker *EventBroker) Start() error {
    broker.mu.Lock()
    defer broker.mu.Unlock()

    if broker.cancel != nil {
        return core.Error(core.ErrInvalidOperation, "broker is already started")
    }

    ctx, cancel := context.WithCancel(context.Background())

    broker.cancel = cancel

    go broker.runLoop(ctx)

    return nil
}

func (broker *EventBroker) Stop() error {
    broker.mu.Lock()
    defer broker.mu.Unlock()

    if broker.cancel == nil {
        return core.Error(core.ErrInvalidOperation, "broker is already stopped")
    }

    broker.cancel()
    broker.cancel = nil

    return nil
}

func (broker *EventBroker) Close() error {
    broker.mu.Lock()
    defer broker.mu.Unlock()

    if broker.cancel != nil {
        broker.cancel()
        broker.cancel = nil
    }

    broker.onLoad.Close()
    broker.onReload.Close()
    broker.onAttrModified.Close()
    broker.onAttrRemoved.Close()
    broker.onChildNodeCountUpdated.Close()
    broker.onChildNodeInserted.Close()
    broker.onChildNodeRemoved.Close()

    return nil
}

func (broker *EventBroker) StopAndClose() error {
    err := broker.Stop()

    if err != nil {
        return err
    }

    return broker.Close()
}

func (broker *EventBroker) runLoop(ctx context.Context) {
	for {
		select {
		case <-broker.onLoad.Ready():
			reply, err := broker.onLoad.Recv()

			if ctxDone(err) {
				return
			}

			broker.emit(ctx, EventLoad, reply, err)
		case <-broker.onReload.Ready():
			reply, err := broker.onReload.Recv()

			if ctxDone(err) {
				return
			}

			broker.emit(ctx, EventReload, reply, err)
		case <-broker.onAttrModified.Ready():
			reply, err := broker.onAttrModified.Recv()

			if ctxDone(err) {
				return
			}

			broker.emit(ctx, EventAttrModified, reply, err)
		case <-broker.onAttrRemoved.Ready():
			reply, err := broker.onAttrRemoved.Recv()

			if ctxDone(err) {
				return
			}

			broker.emit(ctx, EventAttrRemoved, reply, err)
		case <-broker.onChildNodeCountUpdated.Ready():
			reply, err := broker.onChildNodeCountUpdated.Recv()

			if ctxDone(err) {
				return
			}

			broker.emit(ctx, EventChildNodeCountUpdated, reply, err)
		case <-broker.onChildNodeInserted.Ready():
			reply, err := broker.onChildNodeInserted.Recv()

			if ctxDone(err) {
				return
			}

			broker.emit(ctx, EventChildNodeInserted, reply, err)
		case <-broker.onChildNodeRemoved.Ready():
			reply, err := broker.onChildNodeRemoved.Recv()

			if ctxDone(err) {
				return
			}

			broker.emit(ctx, EventChildNodeRemoved, reply, err)
		}
	}
}

func ctxDone(cdpError error) bool {
	if cdpError != nil {
	 	return errors.Cause(cdpError) == context.Canceled
	}
	return false
}

func (broker *EventBroker) emit(ctx context.Context, event Event, message interface{}, err error) {
    if err != nil {
        event = EventError
        message = err
    }

    broker.mu.Lock()

    listeners, ok := broker.listeners[event]

    if !ok {
        broker.mu.Unlock()
        return
    }

    snapshot := make([]EventListener, len(listeners))
    copy(snapshot, listeners)

    broker.mu.Unlock()

    for _, listener := range snapshot {
        select {
        case <-ctx.Done():
            return
        default:
            ctx2, fn := context.WithTimeout(ctx, time.Duration(drivers.DefaultTimeout)*time.Millisecond)

            listener(ctx2, message)

            fn()
        }
    }
}
