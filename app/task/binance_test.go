package task

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/v03413/bepusdt/app/model"
)

func TestBinanceHistorySignature(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		sig := q.Get("signature")
		q.Del("signature")
		mac := hmac.New(sha256.New, []byte("secret"))
		mac.Write([]byte(q.Encode()))
		if sig != hex.EncodeToString(mac.Sum(nil)) || r.Header.Get("X-MBX-APIKEY") != "key" {
			t.Error("invalid authentication")
		}
		w.Write([]byte(`{"success":true,"code":"000000","data":[]}`))
	}))
	defer srv.Close()
	c := binanceClient{srv.Client(), srv.URL, "key", "secret"}
	if _, err := c.history(context.Background(), 1, 2); err != nil {
		t.Fatal(err)
	}
}

func TestBinanceMatchingAndReplay(t *testing.T) {
	initTaskTestEnv(t, nil)
	now := time.Now().Truncate(time.Second)
	o := createTestOrder(t, model.Order{TradeId: "binance-test", OrderId: "merchant", TradeType: model.Binance, Address: "12345678", Amount: "10.01", Status: model.OrderStatusWaiting, ExpiredAt: now.Add(time.Hour)}, now.Add(-time.Minute))
	r := binanceTrade{OrderType: "C2C", ID: "receipt-1", Time: now.UnixMilli(), Amount: "10.010", Currency: "USDT"}
	r.Receiver.UID = o.Address
	if !binanceMatches(o, r, o.Address) {
		t.Fatal("valid receipt rejected")
	}
	for _, change := range []func(*binanceTrade){func(r *binanceTrade) { r.Amount = "-10.01" }, func(r *binanceTrade) { r.Amount = "10.02" }, func(r *binanceTrade) { r.Currency = "USDC" }, func(r *binanceTrade) { r.OrderType = "PAY_REFUND" }, func(r *binanceTrade) { r.Receiver.UID = "87654321" }, func(r *binanceTrade) { r.Time = now.Add(2 * time.Hour).UnixMilli() }} {
		invalid := r
		change(&invalid)
		if binanceMatches(o, invalid, o.Address) {
			t.Fatal("invalid receipt accepted")
		}
	}
	if paid, err := settleBinance(&o, r); err != nil || !paid {
		t.Fatalf("settle: %v", err)
	}
	second := createTestOrder(t, model.Order{TradeId: "second", OrderId: "second", TradeType: model.Binance, Address: o.Address, Amount: o.Amount, Status: model.OrderStatusWaiting, ExpiredAt: now.Add(time.Hour)}, now.Add(-time.Minute))
	if paid, _ := settleBinance(&second, r); paid {
		t.Fatal("replayed receipt paid another order")
	}
	if got := getTestOrder(t, "second"); got.Status != model.OrderStatusWaiting {
		t.Fatal("replay changed order")
	}
}

func TestBinanceScanSplitsFullPage(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("startTime") == "1" && r.URL.Query().Get("endTime") == "4" {
			w.Write([]byte(`{"success":true,"code":"000000","data":[`))
			for i := 0; i < 100; i++ {
				if i > 0 {
					w.Write([]byte(","))
				}
				w.Write([]byte(`{}`))
			}
			w.Write([]byte(`]}`))
			return
		}
		w.Write([]byte(`{"success":true,"code":"000000","data":[{"transactionId":"record"}]}`))
	}))
	defer srv.Close()
	c := binanceClient{srv.Client(), srv.URL, "key", "secret"}
	rows, err := c.scan(context.Background(), 1, 4)
	if err != nil || len(rows) != 2 || calls != 3 {
		t.Fatalf("rows=%d calls=%d err=%v", len(rows), calls, err)
	}
}

func TestBinanceHistoryFailsClosed(t *testing.T) {
	for _, body := range []string{`{"success":false,"code":"000000","data":[]}`, `{"success":true,"code":"-1","data":[]}`, `invalid`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		c := binanceClient{srv.Client(), srv.URL, "key", "secret"}
		if _, err := c.history(context.Background(), 1, 2); err == nil {
			t.Error("invalid response accepted")
		}
		srv.Close()
	}
}
