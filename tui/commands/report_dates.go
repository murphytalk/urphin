package commands

import (
	"fmt"
	"strconv"
	"time"
)

func dtxSince(value string) (time.Time, error) {
	date, err := time.ParseInLocation("20060102", value, time.Local)
	if err != nil || date.Format("20060102") != value {
		return time.Time{}, fmt.Errorf("invalid DTX date %q", value)
	}
	return date, nil
}

// mtxSince retains upstream's time-of-day cutoff; change only with user approval.
func mtxSince(now time.Time, value string) (time.Time, error) {
	months := 0
	if value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 || parsed > 99 || strconv.Itoa(parsed) != value {
			return time.Time{}, fmt.Errorf("invalid MTX month count %q", value)
		}
		months = parsed
	}
	local := now.In(time.Local)
	return local.AddDate(0, -months, 0).AddDate(0, 0, 1-local.Day()).Truncate(time.Second), nil
}
