package task

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
	"github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
	"gorm.io/gorm"
)

type binanceTransaction struct {
	OrderType string `json:"orderType"`
	ID        string `json:"transactionId"`
	Time      int64  `json:"transactionTime"`
	Amount    string `json:"amount"`
	Currency  string `json:"currency"`
	Receiver  struct {
		UID string `json:"binanceId"`
	} `json:"receiverInfo"`
}

type binanceClient struct {
	client            *http.Client
	base, key, secret string
}

func (b binanceClient) history(ctx context.Context, start, end int64) ([]binanceTransaction, error) {
	q := url.Values{"startTime": {strconv.FormatInt(start, 10)}, "endTime": {strconv.FormatInt(end, 10)}, "limit": {"100"}, "recvWindow": {"5000"}, "timestamp": {strconv.FormatInt(time.Now().UnixMilli(), 10)}}
	mac := hmac.New(sha256.New, []byte(b.secret))
	mac.Write([]byte(q.Encode()))
	req, err := http.NewRequestWithContext(ctx, "GET", b.base+"/sapi/v1/pay/transactions?"+q.Encode()+"&signature="+hex.EncodeToString(mac.Sum(nil)), nil)
	if err != nil {
		return nil, fmt.Errorf("invalid Binance request")
	}
	req.Header.Set("X-MBX-APIKEY", b.key)
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Binance request failed")
	} // never log signed URLs
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Binance HTTP status %d", resp.StatusCode)
	}
	var result struct {
		Code    string               `json:"code"`
		Success bool                 `json:"success"`
		Data    []binanceTransaction `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("invalid Binance response")
	}
	if !result.Success || result.Code != "000000" {
		return nil, fmt.Errorf("Binance API rejected request")
	}
	// A full page might omit records. Split the time range, including tied timestamps.
	if len(result.Data) >= 100 {
		if start >= end {
			return nil, fmt.Errorf("Binance history saturated at one millisecond")
		}
		mid := start + (end-start)/2
		left, err := b.history(ctx, start, mid)
		if err != nil {
			return nil, err
		}
		right, err := b.history(ctx, mid+1, end)
		if err != nil {
			return nil, err
		}
		return append(left, right...), nil
	}
	return result.Data, nil
}

func init() { Register(Task{Duration: time.Minute, Callback: pollBinance}) }

func pollBinance(ctx context.Context) {
	key, secret := os.Getenv("BINANCE_API_KEY"), os.Getenv("BINANCE_API_SECRET")
	if !model.BinanceConfigured() {
		return
	}
	pollBinanceClient(ctx, binanceClient{client: &http.Client{Timeout: 15 * time.Second}, base: "https://api.binance.com", key: key, secret: secret})
}

func pollBinanceClient(ctx context.Context, b binanceClient) {
	var orders []model.Order
	if err := model.Db.Where("trade_type = ? AND status IN ? AND expired_at > ?", model.Binance, []int{model.OrderStatusWaiting, model.OrderStatusExpired}, time.Now().Add(model.GetLookbackHour())).Order("created_at asc").Find(&orders).Error; err != nil || len(orders) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	records, err := b.history(ctx, orders[0].CreatedAt.Time().UnixMilli(), time.Now().UnixMilli())
	if err != nil {
		log.Task.Warn("Binance Pay: ", err)
		return
	}
	for _, record := range records {
		for _, order := range orders {
			if !binanceMatches(order, record) {
				continue
			}
			paid, err := settleBinance(order, record)
			if err != nil {
				log.Task.Warn("Binance settlement failed: ", err)
				break
			}
			if paid {
				if err := model.Db.First(&order, order.ID).Error; err == nil {
					notifyOrderSuccess(order)
				}
			}
			break
		}
	}
}

func binanceMatches(o model.Order, r binanceTransaction) bool {
	amount, err := decimal.NewFromString(r.Amount)
	target, e := decimal.NewFromString(o.Amount)
	return err == nil && e == nil && amount.IsPositive() && amount.Equal(target) && !o.AddressLocked && o.TradeType == model.Binance && (r.OrderType == "C2C" || r.OrderType == "PAY") && r.ID != "" && r.Currency == "USDT" && r.Receiver.UID == orderMatchAddress(o) && o.CreatedAt != nil && r.Time >= o.CreatedAt.Time().UnixMilli() && r.Time < o.ExpiredAt.UnixMilli()
}

func settleBinance(o model.Order, r binanceTransaction) (bool, error) {
	paid := false
	err := model.Db.Transaction(func(tx *gorm.DB) error {
		receipt := model.BinanceReceipt{TransactionID: r.ID, OrderID: o.ID}
		// Unique transaction and order constraints, plus conditional status update, make replay safe.
		var count int64
		if err := tx.Model(&model.BinanceReceipt{}).Where("transaction_id = ? OR order_id = ?", r.ID, o.ID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		if err := tx.Create(&receipt).Error; err != nil {
			return err
		}
		at := time.UnixMilli(r.Time)
		res := tx.Model(&model.Order{}).Where("id = ? AND status IN ? AND trade_type = ? AND amount = ? AND match_address = ? AND expired_at = ?", o.ID, []int{model.OrderStatusWaiting, model.OrderStatusExpired}, model.Binance, o.Amount, orderMatchAddress(o), o.ExpiredAt).Updates(map[string]any{"status": model.OrderStatusSuccess, "ref_hash": "binance:" + r.ID, "confirmed_at": at})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("order changed during settlement")
		}
		paid = true
		return nil
	})
	return paid && err == nil, err
}
