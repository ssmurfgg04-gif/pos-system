package mpesa

import (
        "context"
        "errors"
        "fmt"
        "sync"
        "time"
)

// Mock is an in-process provider used for demos (default) and tests.
// STK pushes "succeed" after a configurable delay; QuerySTK reports the
// outcome so the sweeper completes orders exactly like production.
type Mock struct {
        mu        sync.Mutex
        delay     time.Duration
        resultCode int
        seq       int
        pushes    map[string]*mockPush
}

type mockPush struct {
        createdAt time.Time
        deleted   bool
}

func NewMock(delay time.Duration, resultCode int) *Mock {
        return &Mock{delay: delay, resultCode: resultCode, pushes: map[string]*mockPush{}}
}

func (m *Mock) Name() string { return "mock" }

func (m *Mock) Configure(delay time.Duration, resultCode int) {
        m.mu.Lock()
        m.delay, m.resultCode = delay, resultCode
        m.mu.Unlock()
}

func (m *Mock) InitiateSTK(ctx context.Context, req STKRequest) (STKResponse, error) {
        m.mu.Lock()
        defer m.mu.Unlock()
        m.seq++
        id := fmt.Sprintf("ws_CO_MOCK_%d_%d", time.Now().UnixNano(), m.seq)
        m.pushes[id] = &mockPush{createdAt: time.Now()}
        return STKResponse{
                MerchantRequestID: fmt.Sprintf("%d-%d", time.Now().UnixMilli(), m.seq),
                CheckoutRequestID: id,
                CustomerMessage:   "Mock STK push sent. Approve on your (imaginary) phone.",
        }, nil
}

func (m *Mock) QuerySTK(ctx context.Context, checkoutRequestID string) (STKQueryResult, error) {
        m.mu.Lock()
        defer m.mu.Unlock()
        p, ok := m.pushes[checkoutRequestID]
        if !ok {
                return STKQueryResult{}, errors.New("mock: unknown checkout id")
        }
        if p.deleted {
                return STKQueryResult{}, errors.New("mock: push expired")
        }
        elapsed := time.Since(p.createdAt)
        if elapsed < m.delay {
                // Still pending — Daraja reports ResultCode 1032-ish "being processed";
                // we signal pending with rc=-1 and empty receipt.
                return STKQueryResult{ResultCode: -1, ResultDesc: "STK push in progress"}, nil
        }
        p.deleted = true
        if m.resultCode != 0 {
                return STKQueryResult{ResultCode: m.resultCode, ResultDesc: "Mock failure configured"}, nil
        }
        return STKQueryResult{
                ResultCode:         0,
                ResultDesc:         "The service request is processed successfully.",
                MpesaReceiptNumber: GenerateMockReceipt(m.seq),
        }, nil
}

// Age returns how long a push has existed (test helper).
func (m *Mock) Age(checkoutRequestID string) time.Duration {
        m.mu.Lock()
        defer m.mu.Unlock()
        if p, ok := m.pushes[checkoutRequestID]; ok {
                return time.Since(p.createdAt)
        }
        return 0
}
