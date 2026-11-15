package core

import (
	"context"
	"errors"
	"regexp"
	"strconv"

	"gorm.io/gorm/clause"
)

const exchangeRateSettingKey = "exchange_rate_cny_per_usd"
const defaultExchangeRate = "7.2"

var exchangeRatePattern = regexp.MustCompile(`^[0-9]{1,5}(\.[0-9]{1,6})?$`)

func parseExchangeRate(value string) (float64, error) {
	rate, err := strconv.ParseFloat(value, 64)
	if err != nil || !exchangeRatePattern.MatchString(value) || rate <= 0 || rate > 10000 {
		return 0, errors.New("enter a number greater than 0 and at most 10000, with up to 6 decimal places")
	}
	return rate, nil
}

type exchangeRateContextKey struct{}

// ExchangeRate reads only the non-secret setting. A response-scoped value keeps
// list rows, history and chart data consistent even during a settings update.
// No process cache is used, so subsequent requests see saved changes immediately.
func (s *Store) ExchangeRate(ctx context.Context) (float64, error) {
	if rate, ok := ctx.Value(exchangeRateContextKey{}).(float64); ok {
		return rate, nil
	}
	var row Setting
	err := s.DB.WithContext(ctx).Select("value").Where(clause.Eq{Column: "key", Value: exchangeRateSettingKey}).First(&row).Error
	if errors.Is(err, ErrNotFound) {
		return parseExchangeRate(defaultExchangeRate)
	}
	if err != nil {
		return 0, err
	}
	return parseExchangeRate(row.Value)
}

func (s *Store) WithExchangeRate(ctx context.Context) (context.Context, error) {
	rate, err := s.ExchangeRate(ctx)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, exchangeRateContextKey{}, rate), nil
}
