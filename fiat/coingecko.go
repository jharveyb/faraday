package fiat

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/shopspring/decimal"
)

const (
	coinGeckoURL = "https://api.coingecko.com/api/v3/coins/bitcoin/" +
		"market_chart"

	defaultCoinGeckoCurrency = "USD"
)

func newCoinGeckoAPI(g *Granularity) (*coinGeckoAPI, error) {
	api := &coinGeckoAPI{
		Granularity: GranularityHour,
	}

	if g != nil {
		api.Granularity = *g
	}

	if api.Granularity != GranularityHour {
		return nil, fmt.Errorf("coingecko only supports 1H " +
			"granularity")
	}

	return api, nil
}

// coinGeckoAPI implements the fiatBackend interface.
type coinGeckoAPI struct {
	Granularity
}

type coinGeckoResponse struct {
	Prices []coinGeckoPricePoint `json:"prices"`
}

type coinGeckoPricePoint []float64

// queryCoinGecko constructs and sends a request to coinGecko to query
// historical price information. The api is expressed as a lag in days relative
// to the current time, so we accept a lag value instead of a time range.
func queryCoinGecko(lag int) ([]byte, error) {
	queryURL := fmt.Sprintf("%v?vs_currency=usd&days=%v", coinGeckoURL,
		lag)

	log.Debugf("coingecko url: %v", queryURL)

	// Query the http endpoint with the url provided
	// #nosec G107
	response, err := http.Get(queryURL)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	return ioutil.ReadAll(response.Body)
}

// parseCoinGeckoData parses http response data from coingecko into Price
// structs.
func parseCoinGeckoGata(data []byte) ([]*Price, error) {
	var priceEntries coinGeckoResponse

	if err := json.Unmarshal(data, &priceEntries); err != nil {
		return nil, err
	}

	var usdRecords = make([]*Price, 0, len(priceEntries.Prices))

	for _, price := range priceEntries.Prices {
		if len(price) != 2 {
			return nil, fmt.Errorf("expected price and timestamp "+
				"got: %v entries", len(price))
		}
		ts := time.Millisecond * time.Duration(price[0])
		timestamp := time.Unix(0, ts.Nanoseconds())

		usdRecords = append(usdRecords, &Price{
			Timestamp: timestamp,
			Price:     decimal.NewFromFloat(price[1]),
			Currency:  defaultCoinGeckoCurrency,
		})
	}

	return usdRecords, nil
}

// rawPriceData retrieves price information from coingecko's api for the given
// time range.
func (c *coinGeckoAPI) rawPriceData(ctx context.Context, start,
	end time.Time) ([]*Price, error) {

	// The coingecko api only supports historical price points relative
	// to the present for the last 90 days (otherwise the granulaity of the
	// price data changes). We need at least one timestamp before our start
	// time so we limit the start point to 89 days, so that we can pad as
	// needed, and then post-filter the data after fetching it.
	now := time.Now()
	cutoff := now.Add(time.Hour * 24 * 89 * -1)

	if start.Before(cutoff) {
		return nil, fmt.Errorf("coingecko only supports 1 hour "+
			"granularity for the last 90 days, start time must be"+
			"> %v", cutoff)
	}

	// We now calculate the number of days we need to lag from the present
	// to cover our range. We add one day to our lag so that we always get
	// at least one timestamp that is before our start date.
	diff := now.Sub(start)
	lag := (int(diff.Hours()) / 24) + 1

	query := func() ([]byte, error) {
		return queryCoinGecko(lag)
	}

	// Query the api for this page of data. We allow retries at this
	// stage in case the api experiences a temporary limit.
	records, err := retryQuery(ctx, query, parseCoinGeckoGata)
	if err != nil {
		return nil, err
	}

	// Filter out all records that are after our end time. We don't filter
	// times before our start time because we queried from the correct
	// start time, it's just the range from end->now that we need to filter.
	// nolint: prealloc
	var inRangeRecords []*Price
	for _, record := range records {
		if record.Timestamp.After(end) {
			continue
		}

		inRangeRecords = append(inRangeRecords, record)
	}

	return inRangeRecords, nil
}
