package session

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/protocol"
)

type mockTransport struct {
	execFn  func(ctx context.Context, req *adu.Request) (*adu.Response, error)
	closeFn func() error
}

func (m *mockTransport) ExecuteRequest(ctx context.Context, req *adu.Request) (*adu.Response, error) {
	if m.execFn != nil {
		return m.execFn(ctx, req)
	}
	return &adu.Response{UnitID: req.UnitID, FunctionCode: req.FunctionCode}, nil
}

func (m *mockTransport) Close() error {
	if m.closeFn != nil {
		return m.closeFn()
	}
	return nil
}

func TestNewEngine(t *testing.T) {
	e := NewEngine(Config{
		Dial: func() (Transport[*adu.Request, *adu.Response], error) {
			return &mockTransport{}, nil
		},
	})
	if e == nil {
		t.Fatal("NewEngine returned nil")
	}

	e2 := NewEngine(Config{
		Dial:   func() (Transport[*adu.Request, *adu.Response], error) { return &mockTransport{}, nil },
		Logger: nil,
	})
	if e2 == nil {
		t.Fatal("NewEngine with nil logger returned nil")
	}
}

func TestEngine_OpenClose_SingleTransport(t *testing.T) {
	dialCount := 0
	dial := func() (Transport[*adu.Request, *adu.Response], error) {
		dialCount++
		return &mockTransport{}, nil
	}
	e := NewEngine(Config{Dial: dial, UsePool: false})
	if err := e.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if dialCount != 1 {
		t.Errorf("Dial called %d times, want 1", dialCount)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestEngine_Open_DialError(t *testing.T) {
	dialErr := errors.New("dial failed")
	dial := func() (Transport[*adu.Request, *adu.Response], error) {
		return nil, dialErr
	}
	e := NewEngine(Config{Dial: dial, UsePool: false})
	err := e.Open()
	if err == nil {
		t.Fatal("Open expected error")
	}
	if !errors.Is(err, dialErr) {
		t.Errorf("got %v, want %v", err, dialErr)
	}
}

func TestEngine_Open_Idempotent(t *testing.T) {
	dialCount := 0
	dial := func() (Transport[*adu.Request, *adu.Response], error) {
		dialCount++
		return &mockTransport{}, nil
	}
	e := NewEngine(Config{Dial: dial, UsePool: false})
	if err := e.Open(); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := e.Open(); err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if dialCount != 1 {
		t.Errorf("Dial called %d times, want 1 (second Open should be no-op)", dialCount)
	}
}

func TestEngine_Execute_Success(t *testing.T) {
	dial := func() (Transport[*adu.Request, *adu.Response], error) {
		return &mockTransport{}, nil
	}
	e := NewEngine(Config{Dial: dial, UsePool: false})
	if err := e.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = e.Close() }()

	ctx := context.Background()
	req := &adu.Request{UnitID: 1, FunctionCode: 0x03}
	res, err := e.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil response")
	}
	if res.UnitID != 1 || res.FunctionCode != 0x03 {
		t.Errorf("response UnitID=%d FunctionCode=0x%02x, want 1, 0x03", res.UnitID, res.FunctionCode)
	}
}

func TestEngine_Execute_NotOpen(t *testing.T) {
	dial := func() (Transport[*adu.Request, *adu.Response], error) {
		return &mockTransport{}, nil
	}
	e := NewEngine(Config{Dial: dial, UsePool: false})
	// do not call Open

	ctx := context.Background()
	_, err := e.Execute(ctx, &adu.Request{})
	if err == nil {
		t.Fatal("Execute without Open expected error")
	}
	if !errors.Is(err, protocol.ErrClientNotOpen) {
		t.Errorf("got %v, want ErrClientNotOpen", err)
	}
}

func TestEngine_Execute_AfterClose(t *testing.T) {
	dial := func() (Transport[*adu.Request, *adu.Response], error) {
		return &mockTransport{}, nil
	}
	e := NewEngine(Config{Dial: dial, UsePool: false})
	if err := e.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := e.Execute(context.Background(), &adu.Request{})
	if err == nil {
		t.Fatal("Execute after Close expected error")
	}
	if !errors.Is(err, protocol.ErrClientNotOpen) {
		t.Errorf("got %v, want ErrClientNotOpen", err)
	}
}

func TestEngine_Execute_TransportError(t *testing.T) {
	transportErr := errors.New("transport failed")
	dial := func() (Transport[*adu.Request, *adu.Response], error) {
		return &mockTransport{
			execFn: func(_ context.Context, _ *adu.Request) (*adu.Response, error) {
				return nil, transportErr
			},
		}, nil
	}
	e := NewEngine(Config{Dial: dial, UsePool: false, Retry: NoRetry()})
	if err := e.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = e.Close() }()

	_, err := e.Execute(context.Background(), &adu.Request{})
	if err == nil {
		t.Fatal("Execute expected error")
	}
	if !errors.Is(err, transportErr) {
		t.Errorf("got %v, want %v", err, transportErr)
	}
}

func TestEngine_Execute_RetrySuccess(t *testing.T) {
	var execCount atomic.Int32
	dial := func() (Transport[*adu.Request, *adu.Response], error) {
		return &mockTransport{
			execFn: func(_ context.Context, _ *adu.Request) (*adu.Response, error) {
				n := execCount.Add(1)
				if n == 1 {
					return nil, io.EOF
				}
				return &adu.Response{UnitID: 1, FunctionCode: 0x03}, nil
			},
		}, nil
	}
	e := NewEngine(Config{
		Dial:  dial,
		Retry: ExponentialBackoff(1*time.Millisecond, 10*time.Millisecond, 2),
	})
	if err := e.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = e.Close() }()

	res, err := e.Execute(context.Background(), &adu.Request{UnitID: 1, FunctionCode: 0x03})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil response")
	}
	if execCount.Load() != 2 {
		t.Errorf("ExecuteRequest called %d times, want 2 (fail then success)", execCount.Load())
	}
}

func TestEngine_Execute_ContextCanceled(t *testing.T) {
	dial := func() (Transport[*adu.Request, *adu.Response], error) {
		return &mockTransport{
			execFn: func(ctx context.Context, _ *adu.Request) (*adu.Response, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		}, nil
	}
	e := NewEngine(Config{Dial: dial, UsePool: false})
	if err := e.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = e.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := e.Execute(ctx, &adu.Request{})
	if err == nil {
		t.Fatal("Execute expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

func TestEngine_Close_NoOp(t *testing.T) {
	e := NewEngine(Config{
		Dial: func() (Transport[*adu.Request, *adu.Response], error) {
			return &mockTransport{}, nil
		},
	})
	// do not call Open
	err := e.Close()
	if err != nil {
		t.Errorf("Close without Open: %v", err)
	}
}
