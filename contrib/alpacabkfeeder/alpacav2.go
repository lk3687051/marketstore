package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/pkg/errors"

	"github.com/alpacahq/marketstore/v4/contrib/alpacabkfeeder/api"
	"github.com/alpacahq/marketstore/v4/contrib/alpacabkfeeder/configs"
	"github.com/alpacahq/marketstore/v4/contrib/alpacabkfeeder/feed"
	"github.com/alpacahq/marketstore/v4/contrib/alpacabkfeeder/symbols"
	"github.com/alpacahq/marketstore/v4/contrib/alpacabkfeeder/timer"
	"github.com/alpacahq/marketstore/v4/contrib/alpacabkfeeder/writer"
	"github.com/alpacahq/marketstore/v4/plugins/bgworker"
	"github.com/alpacahq/marketstore/v4/utils"
	"github.com/alpacahq/marketstore/v4/utils/log"
)

const getJSONFileTimeout = 10 * time.Second

// NewBgWorker returns the new instance of Alpaca Broker API Feeder.
// See configs.Config for the details of available configurations.
// nolint:deadcode // used as a plugin
func NewBgWorker(conf map[string]interface{}) (bgworker.BgWorker, error) {
	config, err := configs.NewConfig(conf)
	if err != nil {
		return nil, errors.Wrap(err, fmt.Sprintf("failed to load config file. %v", conf))
	}
	log.Info("loaded Alpaca Broker Feeder config...")

	apiCli := apiClient(config)

	// init Market Time Checker
	var timeChecker feed.MarketTimeChecker
	timeChecker = feed.NewDefaultMarketTimeChecker(
		config.ClosedDaysOfTheWeek,
		config.ClosedDays,
		config.OpenTime,
		config.CloseTime)
	if config.OffHoursSchedule != "" {
		scheduleMin, err := feed.ParseSchedule(config.OffHoursSchedule)
		if err != nil {
			return nil, fmt.Errorf("parse off_hours_schedule %s: %w", config.OffHoursSchedule, err)
		}
		log.Info(fmt.Sprintf("[Alpaca Broker Feeder] off_hours_schedule=%s[min] is set. "+
			"The data will be retrieved at %s [minute] even when the market is closed.",
			config.OffHoursSchedule, config.OffHoursSchedule),
		)
		timeChecker = feed.NewScheduledMarketTimeChecker(
			timeChecker,
			scheduleMin,
		)
	}

	ctx := context.Background()
	// init symbols Manager to update symbols in the target exchanges
	var sm symbols.Manager
	sm = symbols.NewManager(apiCli, config.Exchanges)
	if config.StocksJSONURL != "" {
		// use a remote JSON file instead of the config.Exchanges to list up the symbols
		sm = symbols.NewJSONFileManager(&http.Client{Timeout: getJSONFileTimeout},
			config.StocksJSONURL, config.StocksJSONBasicAuth,
		)
	}
	sm.UpdateSymbols()
	if config.SymbolsUpdateTime.IsZero() {
		config.SymbolsUpdateTime = config.UpdateTime
	}
	timer.RunEveryDayAt(ctx, config.SymbolsUpdateTime, sm.UpdateSymbols)
	log.Info("updated symbols using a remote json file.")

	// init SnapshotWriter
	var ssw writer.SnapshotWriter = writer.SnapshotWriterImpl{
		MarketStoreWriter: &writer.MarketStoreWriterImpl{},
		Timeframe:         config.Timeframe,
		Timezone:          utils.InstanceConfig.Timezone,
	}
	// init BarWriter
	var bw writer.BarWriter = writer.BarWriterImpl{
		MarketStoreWriter: &writer.MarketStoreWriterImpl{},
		Timeframe:         config.Backfill.Timeframe,
		Timezone:          utils.InstanceConfig.Timezone,
	}

	// init BarWriter to backfill daily chart data
	if config.Backfill.Enabled {
		const maxBarsPerRequest = 1000
		const maxSymbolsPerRequest = 100
		bf := feed.NewBackfill(sm, apiCli, bw, time.Time(config.Backfill.Since),
			maxBarsPerRequest, maxSymbolsPerRequest,
		)
		timer.RunEveryDayAt(ctx, config.UpdateTime, bf.UpdateSymbols)
	}

	return &feed.Worker{
		MarketTimeChecker: timeChecker,
		APIClient:         apiCli,
		SymbolManager:     sm,
		SnapshotWriter:    ssw,
		BarWriter:         bw,
		Interval:          config.Interval,
	}, nil
}

func apiClient(config *configs.DefaultConfig) *api.Client {
	// init Alpaca API client
	cred := &api.APIKey{
		ID:           config.APIKeyID,
		PolygonKeyID: config.APIKeyID,
		Secret:       config.APISecretKey,
		// OAuth:        os.Getenv(EnvApiOAuth),
	}
	if config.APIKeyID == "" || config.APISecretKey == "" {
		// if empty, get from env vars
		cred = api.Credentials()
	}
	return api.NewClient(cred)
}

func main() {}
