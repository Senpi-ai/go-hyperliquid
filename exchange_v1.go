package hyperliquid

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

type ExchangeV1 struct {
	debug        bool
	vault        string
	accountAddr  string
	expiresAfter *int64
	lastNonce    atomic.Int64
	isMainnet    bool
}

func NewExchangeV1(
	ctx context.Context,
	baseURL string,
	vaultAddr, accountAddr string,
	isMainnet bool,
) *ExchangeV1 {
	ex := &ExchangeV1{
		vault:       vaultAddr,
		accountAddr: accountAddr,
		isMainnet:   isMainnet,
	}
	return ex
}

// nextNonce returns either the current timestamp in milliseconds or incremented by one to prevent duplicates
// Nonces must be within (T - 2 days, T + 1 day), where T is the unix millisecond timestamp on the block of the transaction.
// See https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/nonces-and-api-wallets#hyperliquid-nonces
func (e *ExchangeV1) nextNonce() int64 {
	// it's possible that at exactly the same time a nextNonce is requested
	for {
		last := e.lastNonce.Load()
		candidate := time.Now().UnixMilli()

		if candidate <= last {
			candidate = last + 1
		}

		// Try to publish our candidate; if someone beat us, retry.
		if e.lastNonce.CompareAndSwap(last, candidate) {
			return candidate
		}
	}
}

// SetExpiresAfter sets the expiration time for actions
// If expiresAfter is nil, actions will not have an expiration time
// If expiresAfter is set, actions will include this expiration nonce
func (e *ExchangeV1) SetExpiresAfter(expiresAfter *int64) {
	e.expiresAfter = expiresAfter
}

// SetLastNonce allows for resuming from a persisted nonce, e.g. the nonce was stored before a restart
// Only useful if a lot of increments happen for unique nonces. Most users do not need this.
func (e *ExchangeV1) SetLastNonce(n int64) {
	e.lastNonce.Store(n)
}

func (e *ExchangeV1) GetTypedData(ctx context.Context, action any) (*apitypes.TypedData, error) {
	nonce := e.nextNonce()

	// Step 1: Create action hash
	hash := actionHash(action, e.vault, nonce, e.expiresAfter)

	// Step 2: Construct phantom agent
	isMainnet := e.isMainnet
	phantomAgent := constructPhantomAgent(hash, isMainnet)

	// Step 3: Create l1 payload
	typedData := l1Payload(phantomAgent, isMainnet)

	// to avoid byte44 error
	typedData.Message["connectionId"] = "0x" + hex.EncodeToString(hash)

	return &typedData, nil
}


func (e *ExchangeV1) NewCreateOrderAction(
	orders []CreateOrderRequest,
	info *BuilderInfo,
) (OrderAction, error) {
	orderRequests := make([]OrderWire, len(orders))
	for i, order := range orders {
		priceWire, err := floatToWire(order.Price)
		if err != nil {
			return OrderAction{}, fmt.Errorf("failed to wire price for order %d: %w", i, err)
		}

		sizeWire, err := floatToWire(order.Size)
		if err != nil {
			return OrderAction{}, fmt.Errorf("failed to wire size for order %d: %w", i, err)
		}

		asset, err := strconv.Atoi(order.Coin)
		if err != nil {
			return OrderAction{}, fmt.Errorf("failed to parse asset for order %d: %w", i, err)
		}
		
		orderWire := OrderWire{
			Asset:      asset,
			IsBuy:      order.IsBuy,
			LimitPx:    priceWire,
			Size:       sizeWire,
			ReduceOnly: order.ReduceOnly,
			OrderType:  newOrderTypeWire(order),
		}

		// Normalize cloid to match Python SDK format (hex WITH 0x prefix)
		normalizedCloid, err := normalizeCloid(order.ClientOrderID)
		if err != nil {
			return OrderAction{}, fmt.Errorf("invalid cloid for order %d: %w", i, err)
		}
		orderWire.Cloid = normalizedCloid

		orderRequests[i] = orderWire
	}

	res := OrderAction{
		Type:     "order",
		Orders:   orderRequests,
		Grouping: string(GroupingNA),
		Builder:  info,
	}

	return res, nil
}