package task

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/v03413/bepusdt/app/model"
)

func TestBinanceHistorySigningAndPagination(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		sig := q.Get("signature")
		q.Del("signature")
		mac := hmac.New(sha256.New, []byte("secret"))
		mac.Write([]byte(q.Encode()))
		if sig != hex.EncodeToString(mac.Sum(nil)) || r.Header.Get("X-MBX-APIKEY") != "key" || r.URL.Path != "/sapi/v1/pay/transactions" {
			t.Error("invalid signed request")
		}
		start, _ := strconv.ParseInt(q.Get("startTime"), 10, 64)
		end, _ := strconv.ParseInt(q.Get("endTime"), 10, 64)
		data := []binanceTransaction{{ID: "record"}}
		if start == 1000 && end == 2000 {
			data = make([]binanceTransaction, 100)
		}
		json.NewEncoder(w).Encode(map[string]any{"code": "000000", "success": true, "data": data})
	}))
	defer server.Close()
	b := binanceClient{server.Client(), server.URL, "key", "secret"}
	records, err := b.history(context.Background(), 1000, 2000)
	if err != nil || len(records) != 2 || calls != 3 {
		t.Fatalf("pagination: %d records, %d calls, %v", len(records), calls, err)
	}
}

func TestBinanceHistoryRejectsErrors(t *testing.T) {
	for _, body := range []string{`{"code":"-1022","success":false}`, `invalid`, `{"code":"000000","data":[]}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer server.Close()
			_, err := (binanceClient{server.Client(), server.URL, "key", "secret"}).history(context.Background(), 1, 2)
			if err == nil {
				t.Fatal("accepted invalid response")
			}
		})
	}
}

func TestBinanceMatchingAndReplay(t *testing.T) {
	initTaskTestEnv(t, nil)
	now := time.Now().Truncate(time.Millisecond)
	o := createTestOrder(t, model.Order{OrderId: "binance-order", TradeId: "binance-order", TradeType: model.Binance, Amount: "10.01", Address: "12345678", MatchAddress: "12345678", Status: model.OrderStatusWaiting, ExpiredAt: now.Add(time.Minute)}, now.Add(-time.Minute))
	r := binanceTransaction{OrderType: "C2C", ID: "unique-transfer", Time: now.UnixMilli(), Amount: "10.01000000", Currency: "USDT"}
	r.Receiver.UID = "12345678"
	if !binanceMatches(o, r) {
		t.Fatal("valid payment rejected")
	}
	cases := []struct {
		name   string
		change func(*binanceTransaction)
	}{
		{"underpaid", func(r *binanceTransaction) { r.Amount = "10" }},
		{"overpaid", func(r *binanceTransaction) { r.Amount = "10.02" }},
		{"outgoing", func(r *binanceTransaction) { r.Amount = "-10.01" }},
		{"currency", func(r *binanceTransaction) { r.Currency = "USDC" }},
		{"receiver", func(r *binanceTransaction) { r.Receiver.UID = "87654321" }},
		{"refund", func(r *binanceTransaction) { r.OrderType = "PAY_REFUND" }},
		{"expired", func(r *binanceTransaction) { r.Time = o.ExpiredAt.UnixMilli() }},
		{"old", func(r *binanceTransaction) { r.Time = o.CreatedAt.Time().UnixMilli() }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bad := r
			c.change(&bad)
			if binanceMatches(o, bad) {
				t.Fatal("accepted invalid payment")
			}
		})
	}
	paid, err := settleBinance(o, r)
	if err != nil || !paid {
		t.Fatalf("settlement: %v %v", paid, err)
	}
	paid, err = settleBinance(o, r)
	if err != nil || paid {
		t.Fatalf("replay: %v %v", paid, err)
	}
	got := getTestOrder(t, o.TradeId)
	if got.Status != model.OrderStatusSuccess || got.RefHash != "binance:"+r.ID {
		t.Fatal("payment not persisted")
	}
	other := createTestOrder(t, model.Order{OrderId: "other", TradeId: "other", TradeType: model.Binance, Amount: "10.01", MatchAddress: "12345678", Status: model.OrderStatusWaiting, ExpiredAt: o.ExpiredAt}, now.Add(-time.Second))
	paid, err = settleBinance(other, r)
	if err != nil || paid {
		t.Fatal("transaction reused for another order")
	}
}

func TestBinancePollCallbacksOnce(t *testing.T) {
	initTaskTestEnv(t, nil)
	now := time.Now().Truncate(time.Millisecond)
	var calls atomic.Int64
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload["block_transaction_id"] != "binance:poll-transfer" || payload["status"] != float64(2) {
			t.Errorf("unexpected callback: %v", payload)
		}
		w.Write([]byte("ok"))
	}))
	defer callback.Close()
	createTestOrder(t, model.Order{OrderId: "poll-order", TradeId: "poll-order", TradeType: model.Binance, Amount: "2.01", Money: "2", Rate: "1", Address: "12345678", MatchAddress: "12345678", NotifyUrl: callback.URL, ApiType: model.OrderApiTypeEpusdt, Status: model.OrderStatusWaiting, ExpiredAt: now.Add(time.Minute)}, now.Add(-time.Minute))
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"success":true,"code":"000000","data":[{"orderType":"C2C","transactionId":"poll-transfer","transactionTime":%d,"currency":"USDT","amount":"2.01","receiverInfo":{"binanceId":"12345678"}}]}`, now.UnixMilli())
	}))
	defer api.Close()
	b := binanceClient{api.Client(), api.URL, "key", "secret"}
	pollBinanceClient(context.Background(), b)
	waitOrderNotified(t, "poll-order")
	pollBinanceClient(context.Background(), b)
	if calls.Load() != 1 {
		t.Fatalf("callback count %d", calls.Load())
	}
}

func TestBinanceOrderAllocationAndUID(t *testing.T) {
	initTaskTestEnv(t, nil)
	for _, uid := range []string{"", "0", "123abc", "0x12345678"} {
		if _, err := model.NewWallet(uid, model.Binance); err == nil {
			t.Fatalf("accepted UID %q", uid)
		}
	}
	wallet, err := model.NewWallet("12345678", model.Binance)
	if err != nil {
		t.Fatal(err)
	}
	wallet.Status = model.WaStatusEnable
	if err := model.Db.Create(&wallet).Error; err != nil {
		t.Fatal(err)
	}
	if err := model.Db.Create(&model.Rate{Rate: "1", RawRate: 1, Fiat: "USD", Crypto: "USDT"}).Error; err != nil {
		t.Fatal(err)
	}
	p := model.OrderParams{OrderId: "allocate-1", Money: decimal.NewFromInt(10), TradeType: model.Binance, Fiat: model.USD, Rate: "1"}
	first, err := model.StartBuildOrder(p)
	if err != nil {
		t.Fatal(err)
	}
	p.OrderId = "allocate-2"
	second, err := model.StartBuildOrder(p)
	if err != nil {
		t.Fatal(err)
	}
	if first.Amount == second.Amount || first.Address != "12345678" {
		t.Fatalf("amount allocation: %s, %s", first.Amount, second.Amount)
	}
	p.AddressLocked = true
	if _, err := model.BuildTrade(p); err == nil {
		t.Fatal("accepted address-locked Binance order")
	}
}
