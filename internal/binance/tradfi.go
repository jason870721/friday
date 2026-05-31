package binance

import (
	"context"
	"fmt"
)

// SignTradFiPerpsAgreement signs the account-level TradFi-Perps agreement that
// Binance requires before it will accept orders on stock-linked perpetuals
// (contractType TRADIFI_PERPETUAL, e.g. NVDAUSDT). Without it, an opening order
// is rejected with code -4411 ("Please sign TradFi-Perps agreement contract").
//
// POST /fapi/v1/stock/contract (signed). Success returns {"code":200,"msg":
// "success"}; the call is idempotent, so re-signing an already-signed account
// also succeeds — safe to call on every startup. Market-data reads do NOT need
// the agreement, only order placement does.
func (c *Client) SignTradFiPerpsAgreement(ctx context.Context) error {
	if err := c.postSigned(ctx, "/fapi/v1/stock/contract", nil, nil); err != nil {
		return fmt.Errorf("signTradFiPerps: %w", err)
	}
	return nil
}
